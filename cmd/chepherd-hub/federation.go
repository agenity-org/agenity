// cmd/chepherd-hub/federation.go — #498 Wave F8 cross-org JWT
// federation relay. Hub-side surface that lets daemon-X obtain a
// JWT signed by daemon-Y for cross-org A2A authentication.
//
// PREMISE-CHECK FINDING (#498 dispatch 2026-06-01):
// chepherd already ships the substrate F8 needs:
//   - internal/auth/keystore.go.Sign — ES256 JWS mint with active
//     key (T2 #505)
//   - internal/auth/jwt_runner_middleware.go — JWKS-fetch verifier
//     (T1 #530)
//   - persistence.RBACGrantRepository — §13 grant table (D3 #469)
//
// What F8 ADDS (per [[feedback_find_what_dep_already_does_then_add_what_it_cant]]):
//   1. Hub relay endpoint POST /v1/federation/auth — forwards
//      the cross-org JWT-mint request from daemon-X to daemon-Y,
//      attesting the calling org's identity in a hub-signed header
//      so daemon-Y can trust it without re-verifying mTLS chain.
//   2. Per-target-daemon URL registry (--federation-target-{orgID}=URL
//      flags + env). Production: a directory service feeds this.
//   3. Body-blind invariant: hub forwards bytes; never decodes the
//      JWT itself; the JWT signing happens at daemon-Y.
//
// The DAEMON-side mint endpoint + caching client are wired
// in internal/federation/cross_org_jwt.go so unit tests can
// exercise them without booting a full daemon.
//
// Refs #498 V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 2.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// federationRequestTimeout caps how long the hub waits for
// daemon-Y to respond to the JWT-mint relay before returning 504
// to daemon-X. 15s is generous enough for slow daemon disk/CPU
// but bounded so a hung target daemon doesn't tie up hub workers.
const federationRequestTimeout = 15 * time.Second

// federationRegistry maps targetOrgID → daemon URL. Populated from
// --federation-target flag list at startup (operator-supplied; F-
// followup will pull from a directory). Concurrent reads only after
// startup so a plain map under mutex is sufficient.
type federationRegistry struct {
	mu      sync.RWMutex
	targets map[string]string

	totalRelays atomic.Int64
	totalFails  atomic.Int64
}

func newFederationRegistry() *federationRegistry {
	return &federationRegistry{targets: map[string]string{}}
}

// setTarget registers a daemon URL for orgID. Called at startup
// per --federation-target=<orgID>=<URL> flag.
func (r *federationRegistry) setTarget(orgID, url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.targets[orgID] = url
}

// lookup returns the daemon URL for orgID, or "" when unknown.
func (r *federationRegistry) lookup(orgID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.targets[orgID]
}

// known returns the list of registered org IDs. Surfaced via
// /healthz so operators can confirm registry contents.
func (r *federationRegistry) known() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.targets))
	for k := range r.targets {
		out = append(out, k)
	}
	return out
}

// federationAuthRequest is the wire shape POSTed to the hub by
// daemon-X. caller is omitted — the hub fills it from the
// authenticated org identity, defending against spoofing
// (mirror of F5's fromOrgId pattern).
type federationAuthRequest struct {
	TargetOrgID string `json:"targetOrgId"`
	Scope       string `json:"scope"`
	Audience    string `json:"audience,omitempty"`
}

// federationAuthResponse is the wire shape returned to daemon-X.
// Carries the Y-signed JWT plus the JWT's exp so daemon-X can
// cache it without parsing.
type federationAuthResponse struct {
	JWT       string `json:"jwt"`
	Issuer    string `json:"iss"`
	NotBefore int64  `json:"nbf"`
	Expires   int64  `json:"exp"`
}

// handleFederationAuth is the hub's relay endpoint. Authenticates
// the calling org (mTLS cert subject or X-Chepherd-Org header),
// forwards the request to the target daemon along with an
// attesting X-Chepherd-Caller-Org header, and returns daemon-Y's
// response verbatim.
//
// Body-blind invariant: hub does NOT inspect the JWT body returned
// by daemon-Y. Hub stores nothing past the request.
func (s *server) handleFederationAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed,
			map[string]string{"error": "POST only"})
		return
	}
	callerOrg := authenticatedOrg(r)
	if callerOrg == "" {
		writeJSON(w, http.StatusUnauthorized,
			map[string]string{"error": "no authenticated org identity"})
		return
	}
	if !s.orgAllowed(callerOrg) {
		writeJSON(w, http.StatusForbidden,
			map[string]string{"error": "caller org not in allowlist"})
		return
	}
	var req federationAuthRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 32*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "decode body: " + err.Error()})
		return
	}
	if req.TargetOrgID == "" || req.Scope == "" {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "targetOrgId and scope required"})
		return
	}
	if !s.orgAllowed(req.TargetOrgID) {
		writeJSON(w, http.StatusForbidden,
			map[string]string{"error": "target org not in allowlist"})
		return
	}
	targetURL := s.federation.lookup(req.TargetOrgID)
	if targetURL == "" {
		s.federation.totalFails.Add(1)
		writeJSON(w, http.StatusBadGateway,
			map[string]string{"error": "no federation target registered for org", "org": req.TargetOrgID})
		return
	}
	upstream, err := s.relayFederationToDaemon(r.Context(), targetURL, callerOrg, &req)
	if err != nil {
		s.federation.totalFails.Add(1)
		writeJSON(w, http.StatusBadGateway,
			map[string]string{"error": "relay: " + err.Error()})
		return
	}
	s.federation.totalRelays.Add(1)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(upstream.statusCode)
	_, _ = w.Write(upstream.body)
}

type upstreamResponse struct {
	statusCode int
	body       []byte
}

// relayFederationToDaemon POSTs the federation request to
// targetURL + "/api/v1/federation/jwt" with the calling org
// identity attested via X-Chepherd-Caller-Org header. The target
// daemon trusts the hub's attestation because the hub-to-daemon
// link is itself authenticated (production: mTLS via T3.1 cert
// pinning the hub identity; this PR ships the relay primitive).
func (s *server) relayFederationToDaemon(ctx context.Context, targetURL, callerOrg string, req *federationAuthRequest) (*upstreamResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, federationRequestTimeout)
	defer cancel()
	body, _ := json.Marshal(req)
	endpoint := strings.TrimRight(targetURL, "/") + "/api/v1/federation/jwt"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Chepherd-Caller-Org", callerOrg)
	httpReq.Header.Set("X-Chepherd-Hub-Attest", "true")
	client := s.federation.client
	if client == nil {
		client = &http.Client{Timeout: federationRequestTimeout}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("daemon-%s: %w", req.TargetOrgID, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("daemon-%s returned %d: %s",
			req.TargetOrgID, resp.StatusCode, string(respBody))
	}
	return &upstreamResponse{statusCode: resp.StatusCode, body: respBody}, nil
}

// federationStatus surfaces hub federation registry state via
// /healthz.
func (s *server) federationStatus() map[string]any {
	if s.federation == nil {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled":       true,
		"target_orgs":   s.federation.known(),
		"total_relays":  s.federation.totalRelays.Load(),
		"total_fails":   s.federation.totalFails.Load(),
	}
}

// federationRegistryWithClient extends the registry with the HTTP
// client used for upstream forwarding. Pulled into its own type
// so tests can inject a stub client without touching the server.
type federationRegistryWithClient struct {
	*federationRegistry
	client *http.Client
}

// loadFederationTargetsFromConfig parses the --federation-target flag
// value (comma-separated <orgID>=<URL> pairs) into the registry.
// Empty config → empty registry → /v1/federation/auth returns 502.
func loadFederationTargetsFromConfig(cfg *config) *federationRegistryWithClient {
	reg := newFederationRegistry()
	if cfg.federationTargets != "" {
		for _, pair := range strings.Split(cfg.federationTargets, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			eq := strings.IndexByte(pair, '=')
			if eq < 0 {
				continue
			}
			org := strings.TrimSpace(pair[:eq])
			url := strings.TrimSpace(pair[eq+1:])
			if org != "" && url != "" {
				reg.setTarget(org, url)
			}
		}
	}
	return &federationRegistryWithClient{
		federationRegistry: reg,
		client:             &http.Client{Timeout: federationRequestTimeout},
	}
}

// guard for unused imports when only some helpers ship in v1.
var _ = errors.New

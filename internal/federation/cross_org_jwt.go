// Package federation — internal/federation/cross_org_jwt.go
// implements the v0.9.4 §10 Pattern 2 Phase 2 cross-org JWT
// federation flow (#498 Wave F8). Two surfaces:
//
//  1. Daemon-side mint endpoint (CrossOrgJWTMinter) — receives the
//     hub-relayed federation request from another org's daemon,
//     validates the calling org has §13 grant for the requested
//     scope, mints a JWT signed with this daemon's ES256 key, and
//     returns it.
//
//  2. Daemon-side caching client (CrossOrgJWTClient) — outbound
//     side. daemon-X uses this to request Y-signed JWTs via the
//     hub. Caches per (caller, target, scope) tuple with TTL =
//     jwt.exp - safetyMargin so repeated A2A calls don't re-
//     federate every time.
//
// The hub-side relay handler lives at cmd/chepherd-hub/federation.go;
// this file is the daemon-side counterpart that the hub-relayed
// request lands at.
//
// PREMISE-CHECK FINDING (#498 dispatch 2026-06-01):
// chepherd already ships every primitive F8 needs (KeyStore.Sign for
// minting, jwt_runner_middleware for verifying, RBACGrantRepository
// for §13 authorization). F8's distinctive value-add is the
// FEDERATION wire (cross-org request shape + caching + cache-eviction
// on TTL).
//
// Refs #498 V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 2.
package federation

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
	"time"
)

// crossOrgJWTTTL is the issued JWT's lifetime. 5 minutes balances
// "callers need fresh tokens for sliding window" against "if Y's
// grant for X is revoked, the cached token shouldn't outlive the
// revocation by more than 5min". Conservative; production may tune.
const crossOrgJWTTTL = 5 * time.Minute

// jwtSafetyMargin is subtracted from jwt.exp when computing the
// client-side cache expiry, so cached entries are dropped before
// the JWT actually expires (avoids racing the validator on the
// receiving end).
const jwtSafetyMargin = 30 * time.Second

// CrossOrgJWTRequest is the wire shape POSTed by the hub to the
// daemon's federation mint endpoint. The hub attests the caller's
// org identity via X-Chepherd-Caller-Org header; this body
// carries the request parameters.
type CrossOrgJWTRequest struct {
	Scope    string `json:"scope"`
	Audience string `json:"audience,omitempty"`
}

// CrossOrgJWTResponse is the wire shape returned to the hub
// (which forwards to daemon-X).
type CrossOrgJWTResponse struct {
	JWT       string `json:"jwt"`
	Issuer    string `json:"iss"`
	NotBefore int64  `json:"nbf"`
	Expires   int64  `json:"exp"`
}

// JWTSigner abstracts the daemon's ES256 signing capability. The
// real impl in cmd/run.go's wiring is auth.KeyStore.Sign; tests
// inject a stub.
type JWTSigner interface {
	Sign(claims map[string]any) (string, error)
}

// GrantMeta carries the per-grant metadata the minter embeds in the
// JWT when the §13 grant check succeeds. Both fields are optional:
// callers that don't need audit/rate-limit signals may leave them
// zero and the corresponding JWT claims are omitted.
type GrantMeta struct {
	GrantID   string       // embedded as chepherd_grant_id claim
	RateWindow *RateWindow // embedded as chepherd_rate_window claim (nil → omitted)
}

// RateWindow carries the per-grant rate-limit configuration embedded
// as the chepherd_rate_window JWT claim per V0.9.2-ARCH §15.2.
type RateWindow struct {
	CallsPerMinute int `json:"calls_per_minute"`
	CallsPerDay    int `json:"calls_per_day"`
}

// CrossOrgGrantChecker abstracts the §13 grant lookup. Returns the
// matched GrantMeta (grant ID + rate window) and nil error when the
// callerOrg is authorized. GrantMeta may be nil when the checker
// allows without a specific grant record (e.g. dev/permissive mode).
// The real impl in cmd/run.go is the GrantStore's check; tests
// inject a stub.
type CrossOrgGrantChecker interface {
	Check(ctx context.Context, callerOrg, scope string) (*GrantMeta, error)
}

// CrossOrgJWTMinter is the daemon-side handler. Wire it onto the
// runtimehttp.Server at /api/v1/federation/jwt:
//
//	mux.Handle("/api/v1/federation/jwt", minter)
//
// The handler requires the hub-attesting X-Chepherd-Caller-Org +
// X-Chepherd-Hub-Attest:true headers; in production these are
// terminated against the hub's mTLS cert pinned at the daemon's
// federation listener (T3.1).
type CrossOrgJWTMinter struct {
	Issuer  string
	Signer  JWTSigner
	Grants  CrossOrgGrantChecker
	TTL     time.Duration
	NowFn   func() time.Time // injectable for tests; nil → time.Now
}

func (m *CrossOrgJWTMinter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	callerOrg := strings.TrimSpace(r.Header.Get("X-Chepherd-Caller-Org"))
	if callerOrg == "" {
		writeJSON(w, http.StatusUnauthorized,
			map[string]string{"error": "missing X-Chepherd-Caller-Org (hub didn't attest)"})
		return
	}
	if r.Header.Get("X-Chepherd-Hub-Attest") != "true" {
		writeJSON(w, http.StatusUnauthorized,
			map[string]string{"error": "missing X-Chepherd-Hub-Attest:true"})
		return
	}
	var req CrossOrgJWTRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "decode: " + err.Error()})
		return
	}
	if req.Scope == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "scope required"})
		return
	}
	var meta *GrantMeta
	if m.Grants != nil {
		var err error
		meta, err = m.Grants.Check(r.Context(), callerOrg, req.Scope)
		if err != nil {
			writeJSON(w, http.StatusForbidden,
				map[string]string{"error": "grant check: " + err.Error()})
			return
		}
	}
	if m.Signer == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"error": "signer not configured"})
		return
	}
	now := m.now()
	ttl := m.TTL
	if ttl <= 0 {
		ttl = crossOrgJWTTTL
	}
	nbf := now.Unix()
	exp := now.Add(ttl).Unix()
	claims := map[string]any{
		"iss":   m.Issuer,
		"sub":   callerOrg,
		"aud":   nonEmpty(req.Audience, m.Issuer),
		"scope": req.Scope,
		"nbf":   nbf,
		"exp":   exp,
		"iat":   nbf,
	}
	if meta != nil {
		if meta.GrantID != "" {
			claims["chepherd_grant_id"] = meta.GrantID
		}
		if meta.RateWindow != nil {
			claims["chepherd_rate_window"] = meta.RateWindow
		}
	}
	jws, err := m.Signer.Sign(claims)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError,
			map[string]string{"error": "sign: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, &CrossOrgJWTResponse{
		JWT:       jws,
		Issuer:    m.Issuer,
		NotBefore: nbf,
		Expires:   exp,
	})
}

func (m *CrossOrgJWTMinter) now() time.Time {
	if m.NowFn != nil {
		return m.NowFn()
	}
	return time.Now()
}

// CrossOrgJWTClient is the daemon-X side. Caches by (callerOrg,
// targetOrg, scope). Cache entry expires at jwt.exp - safetyMargin
// so the next call after expiry triggers a fresh federation.
type CrossOrgJWTClient struct {
	HubURL     string
	CallerOrg  string
	HTTPClient *http.Client

	NowFn func() time.Time // injectable for tests

	mu    sync.Mutex
	cache map[string]*cachedJWT
}

type cachedJWT struct {
	jwt    string
	exp    time.Time
}

// cacheKey is the tuple identity used as map key.
type cacheKey struct {
	target string
	scope  string
}

func keyOf(target, scope string) string { return target + "|" + scope }

// Get returns a fresh-or-cached JWT for (target, scope). On cache
// miss or expired entry, POSTs to hub /v1/federation/auth + caches
// the response.
func (c *CrossOrgJWTClient) Get(ctx context.Context, targetOrg, scope string) (string, error) {
	if c.HubURL == "" || targetOrg == "" || scope == "" {
		return "", errors.New("CrossOrgJWTClient.Get: empty HubURL/targetOrg/scope")
	}
	k := keyOf(targetOrg, scope)
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]*cachedJWT{}
	}
	if entry, ok := c.cache[k]; ok && c.nowFn().Before(entry.exp) {
		jws := entry.jwt
		c.mu.Unlock()
		return jws, nil
	}
	c.mu.Unlock()

	hc := c.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	body, _ := json.Marshal(map[string]string{
		"targetOrgId": targetOrg,
		"scope":       scope,
	})
	endpoint := strings.TrimRight(c.HubURL, "/") + "/v1/federation/auth"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.CallerOrg != "" {
		req.Header.Set("X-Chepherd-Org", c.CallerOrg)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("hub returned %d: %s", resp.StatusCode, respBody)
	}
	var out CrossOrgJWTResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.JWT == "" {
		return "", errors.New("hub response had empty JWT")
	}

	c.mu.Lock()
	c.cache[k] = &cachedJWT{
		jwt: out.JWT,
		exp: time.Unix(out.Expires, 0).Add(-jwtSafetyMargin),
	}
	c.mu.Unlock()
	return out.JWT, nil
}

// Invalidate drops the cache entry for (target, scope). Called when
// the caller observes an upstream 401 from the target — the cached
// token may have been revoked early on the target side.
func (c *CrossOrgJWTClient) Invalidate(targetOrg, scope string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, keyOf(targetOrg, scope))
}

// Len returns the number of cached entries. Used by tests + future
// /healthz exposure.
func (c *CrossOrgJWTClient) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cache)
}

func (c *CrossOrgJWTClient) nowFn() time.Time {
	if c.NowFn != nil {
		return c.NowFn()
	}
	return time.Now()
}

// ─── helpers ──────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func nonEmpty(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

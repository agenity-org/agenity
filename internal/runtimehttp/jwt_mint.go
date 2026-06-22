// internal/runtimehttp/jwt_mint.go implements the v0.9.4 §15.2 JWT
// mint endpoint on the chepherd-daemon HTTP surface (#468 Wave D2).
// A caller (an authenticated A2A client) requests a per-call JWT
// authorizing it to invoke a target agent. The daemon validates
// the RBAC grant, then mints + signs an ES256 token with the spec
// claim set: iss/sub/aud/exp/iat/jti/chepherd_grant_id/chepherd_rate_window.
//
// Refs #468 V0.9.2-ARCHITECTURE.md §15.2.
package runtimehttp

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/agenity-org/agenity/internal/auth"
)

// GrantCheckFn is the seam the RBAC store hooks into when Wave D3
// lands. For Wave D2, Server.GrantCheck is nil → mint defaults to
// allow-all so the JWT pipeline can be exercised end-to-end before
// the grant store is implemented. Returning allowed=false produces
// a 403 with the spec-shape body (#468 acceptance).
//
// The returned grantID + rateWindow are written into the JWT's
// chepherd_grant_id + chepherd_rate_window claims. Empty strings
// in the stub path map to empty claim values, which is acceptable
// per §15.2 — the claims exist for D3's accounting infrastructure,
// not for D2's mint-and-sign correctness.
type GrantCheckFn func(callerSID, targetSID string) (grantID, rateWindow string, allowed bool)

// jwtMintRequest is the POST body shape. The caller's SID could in
// principle be derived from the auth subject, but per §15.2 the
// claim is "calling agent SID" — i.e. the agent on whose behalf
// the call is being made, which can differ from the bearer-token
// subject (e.g. an operator dashboard minting on behalf of agent A).
// Accept it in the body for that flexibility; D3 will cross-check
// against the auth subject when wiring grants.
type jwtMintRequest struct {
	Sub string `json:"sub"`
	Aud string `json:"aud"`
}

// jwtMintResponse mirrors the canonical OAuth2-token-endpoint shape
// so dashboard/SDK code paths that already handle bearer tokens
// don't need a custom decoder. exp_in_seconds echoes the §15.2
// 60s default so callers don't have to parse the JWT to schedule
// renewal.
type jwtMintResponse struct {
	Token        string `json:"token"`
	ExpInSeconds int    `json:"exp_in_seconds"`
}

// jwtMintTTL is the §15.2 default expiry. "configurable per grant"
// arrives with Wave D3; for D2 the constant is the only path.
const jwtMintTTL = 60 * time.Second

// jwtMint handles POST /api/v1/jwt/mint. Refs #468.
func (s *Server) jwtMint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.KeyStore == nil && s.ES256Priv == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "es256 signing key unavailable",
		})
		return
	}
	var body jwtMintRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
		return
	}
	if body.Sub == "" || body.Aud == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "sub and aud are required",
		})
		return
	}
	grantID, rateWindow, allowed := s.checkGrant(body.Sub, body.Aud)
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":     "no grant authorizing " + body.Sub + " → " + body.Aud,
			"sub":       body.Sub,
			"aud":       body.Aud,
			"grant_id":  grantID,
		})
		return
	}
	now := time.Now()
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	claims := map[string]any{
		"iss":                   scheme + "://" + r.Host,
		"sub":                   body.Sub,
		"aud":                   body.Aud,
		"iat":                   now.Unix(),
		"exp":                   now.Add(jwtMintTTL).Unix(),
		"jti":                   uuid.NewString(),
		"chepherd_grant_id":     grantID,
		"chepherd_rate_window":  rateWindow,
	}
	// #505 Wave T2 — prefer the KeyStore.Sign path so the JWS header
	// carries the active key's per-key kid (enabling kid-aware
	// verification across rotations). Fall back to the legacy single-
	// key SignJWS when KeyStore is unwired (unit tests, smoke boots).
	var token string
	var err error
	if s.KeyStore != nil {
		token, err = s.KeyStore.Sign(claims)
	} else {
		token, err = auth.SignJWS(s.ES256Priv, claims)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "sign: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, jwtMintResponse{
		Token:        token,
		ExpInSeconds: int(jwtMintTTL / time.Second),
	})
}

// checkGrant is the dispatch table between the mint endpoint and
// the (future) RBAC store. When Server.GrantCheck is nil — the
// Wave D2 default — every mint is allowed with empty grant_id +
// rate_window. Wave D3 wires a real RBAC-store-backed function
// here that consults internal/persistence Grant records.
func (s *Server) checkGrant(callerSID, targetSID string) (grantID, rateWindow string, allowed bool) {
	if s.GrantCheck == nil {
		return "", "", true
	}
	return s.GrantCheck(callerSID, targetSID)
}

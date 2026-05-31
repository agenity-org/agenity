// internal/auth/jwt_runner_middleware.go — #486 Wave T1. HTTP
// middleware that gates a runner's /a2a/{sid}/* routes on a valid
// JWT issued by SOME chepherd-daemon (verified against that daemon's
// /.well-known/jwks.json per V0.9.2-ARCHITECTURE.md §15.1).
//
// Pre-T1 the runner accepted unsigned A2A requests — anyone with
// network reach to the endpoint could SendMessage. T1 closes that
// gap: each request carries Bearer <JWT>; middleware:
//
//   1. Extracts Bearer token
//   2. Decodes header (kid) + claims (iss, aud, exp, jti) WITHOUT
//      verifying signature
//   3. Derives JWKS URL from iss + fetches via JWKSClient (cached)
//   4. Verifies ES256 signature via auth.VerifyJWS
//   5. Verifies exp not past
//   6. Verifies aud equals runner's expected audience (sid OR
//      runner://<sid> URL form)
//   7. Verifies jti hasn't been seen in last 60s (replay prevention)
//   8. Passes the sub claim into the request context via
//      RunnerSubjectKey so downstream handlers can read it
//
// All failure modes return 401 with WWW-Authenticate: Bearer
// realm="chepherd-runner-a2a" — consistent semantics, no leakage
// of WHICH check failed (avoids oracle attacks on iss/aud/exp).
//
// Refs #486 #468 #505 V0.9.2-ARCHITECTURE.md §15.1.
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// RunnerSubjectKey is the context key under which the verified
// JWT sub claim is stored. Downstream handlers retrieve via
// SubjectFromRunnerContext.
type RunnerSubjectKey struct{}

// SubjectFromRunnerContext returns the authenticated subject
// (JWT sub claim) attached by JWTRunnerMiddleware. Empty when the
// middleware was not active OR when the route is exempt.
func SubjectFromRunnerContext(ctx context.Context) string {
	v, _ := ctx.Value(RunnerSubjectKey{}).(string)
	return v
}

// RunnerJWTMiddlewareConfig configures the middleware. RunnerSID is
// REQUIRED — the aud claim must match it.
type RunnerJWTMiddlewareConfig struct {
	RunnerSID  string
	JWKSClient *JWKSClient
	// JTICache TTL — 60s matches D2's mint TTL (#468 Wave D2). Tokens
	// minted with that TTL won't replay within their lifetime; longer
	// TTL just means the cache stores them longer.
	JTITTL time.Duration
	// Now is a clock seam for tests. nil → time.Now.
	Now func() time.Time
}

// JWTRunnerMiddleware wraps next with JWT verification per
// V0.9.2-ARCH §15.1. nil config or nil JWKSClient passes through
// (back-compat for tests / pre-T1 dev mode).
func JWTRunnerMiddleware(cfg *RunnerJWTMiddlewareConfig, next http.Handler) http.Handler {
	if cfg == nil || cfg.JWKSClient == nil || cfg.RunnerSID == "" {
		return next
	}
	if cfg.JTITTL == 0 {
		cfg.JTITTL = 60 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	jti := newJTICache(cfg.JTITTL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub, err := verifyRunnerJWT(r, cfg, jti)
		if err != nil {
			writeRunnerAuth401(w, err)
			return
		}
		ctx := context.WithValue(r.Context(), RunnerSubjectKey{}, sub)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// verifyRunnerJWT runs all the §15.1 checks + returns the sub claim
// on success, or a sanitised error on failure (errors are LOGGED to
// stderr with detail but the response only carries the generic
// 401 message — no oracle leakage about which check failed).
func verifyRunnerJWT(r *http.Request, cfg *RunnerJWTMiddlewareConfig, jti *jtiCache) (sub string, err error) {
	defer func() {
		if err != nil {
			fmt.Fprintf(os.Stderr, "[chepherd-runner-jwt] %s %s rejected: %v\n", r.Method, r.URL.Path, err)
		}
	}()
	token := extractBearerToken(r)
	if token == "" {
		return "", fmt.Errorf("missing Bearer token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("jwt: malformed (parts=%d, want 3)", len(parts))
	}

	// Decode header to extract kid (optional).
	var header struct {
		Kid string `json:"kid"`
		Alg string `json:"alg"`
	}
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("jwt: header decode: %w", err)
	}
	if err := json.Unmarshal(hdrBytes, &header); err != nil {
		return "", fmt.Errorf("jwt: header parse: %w", err)
	}

	// Decode claims to extract iss / aud / exp / jti / sub.
	var claims struct {
		Iss string `json:"iss"`
		Aud any    `json:"aud"` // can be string OR []string per RFC 7519
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
		JTI string `json:"jti"`
	}
	clmBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("jwt: claims decode: %w", err)
	}
	if err := json.Unmarshal(clmBytes, &claims); err != nil {
		return "", fmt.Errorf("jwt: claims parse: %w", err)
	}
	if claims.Iss == "" {
		return "", fmt.Errorf("jwt: missing iss claim")
	}

	// JWKS fetch + verify signature.
	jwksURL := DeriveJWKSURL(claims.Iss)
	pub, err := cfg.JWKSClient.PublicKey(jwksURL, header.Kid)
	if err != nil {
		return "", fmt.Errorf("jwt: jwks fetch: %w", err)
	}
	if _, err := VerifyJWS(pub, token); err != nil {
		return "", fmt.Errorf("jwt: %w", err)
	}

	// exp must not be past.
	now := cfg.Now()
	if claims.Exp > 0 && now.Unix() > claims.Exp {
		return "", fmt.Errorf("jwt: expired (exp=%d, now=%d)", claims.Exp, now.Unix())
	}

	// aud must match the runner's sid OR runner://<sid> URL form.
	if !audienceMatches(claims.Aud, cfg.RunnerSID) {
		return "", fmt.Errorf("jwt: aud %v doesn't match runner sid %q", claims.Aud, cfg.RunnerSID)
	}

	// jti replay prevention — short-term cache. jti is optional per
	// RFC 7519 but D2 #468 always mints it; absent → reject so we
	// don't accept tokens that bypass the replay window.
	if claims.JTI == "" {
		return "", fmt.Errorf("jwt: missing jti claim (D2 mint always sets it)")
	}
	if !jti.observeAndCheck(claims.JTI, now) {
		return "", fmt.Errorf("jwt: jti %q replay", claims.JTI)
	}

	return claims.Sub, nil
}

// audienceMatches checks claims.aud against the runner's expected
// values. RFC 7519 allows aud to be either a string OR a string
// array.
func audienceMatches(aud any, runnerSID string) bool {
	expected := map[string]bool{
		runnerSID:                true,
		"runner://" + runnerSID:  true,
	}
	switch v := aud.(type) {
	case string:
		return expected[v]
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && expected[s] {
				return true
			}
		}
	}
	return false
}

func extractBearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func writeRunnerAuth401(w http.ResponseWriter, _ error) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="chepherd-runner-a2a"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	// JSON-RPC -32001 ("authentication required") matches the
	// A2A v1.0 spec auth-failure shape (same code internal/a2a's
	// AuthMiddleware uses).
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    -32001,
			"message": "authentication required (Wave T1 #486)",
		},
	})
}

// ─── jtiCache ─────────────────────────────────────────────────────

// jtiCache is a short-term replay-prevention cache. Each jti
// observed is stored with its first-seen timestamp; observation
// after the TTL window is treated as fresh (token lifetimes match
// D2's 60s default so post-TTL replays are stale-token anyway —
// the exp check would have caught them).
type jtiCache struct {
	ttl time.Duration

	mu   sync.Mutex
	seen map[string]time.Time
	last time.Time
}

func newJTICache(ttl time.Duration) *jtiCache {
	return &jtiCache{ttl: ttl, seen: make(map[string]time.Time)}
}

// observeAndCheck records the jti's first-seen timestamp. Returns
// true if the jti is NEW (or its prior observation has aged out);
// false if seen within the TTL window (replay).
func (c *jtiCache) observeAndCheck(jti string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Garbage-collect expired entries opportunistically — cheap when
	// the map is small + amortised when it grows.
	if now.Sub(c.last) > c.ttl {
		for k, t := range c.seen {
			if now.Sub(t) > c.ttl {
				delete(c.seen, k)
			}
		}
		c.last = now
	}
	if t, ok := c.seen[jti]; ok && now.Sub(t) < c.ttl {
		return false
	}
	c.seen[jti] = now
	return true
}

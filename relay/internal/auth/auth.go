// Package auth implements OAuth2 PKCE token validation for the chepherd-rc
// relay. Tokens are issued by identity-svc (Keycloak / OpenOva identity)
// and presented as Bearer in the Authorization header. This package's
// Middleware verifies tokens on every protected endpoint.
//
// Per protocol v1 §6:
//   · OAuth2 PKCE flow (no client secret on public clients — web/iOS/Android)
//   · Refresh tokens rotate
//   · Token claims: sub, chepherd:user_id, chepherd:permitted_bastions, exp, iss
//   · On AUTH_REVOKED, client re-prompts the user
//
// Daemon-side tokens are different — those are long-lived registration
// tokens minted by `chepherd rc enable` and validated against a separate
// ledger in the Postgres registry.
package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Claims is the validated set of attributes derived from a token. Endpoints
// that need the caller's identity get this struct from the request context
// via ClaimsFromContext.
type Claims struct {
	Subject            string   `json:"sub"`
	UserID             string   `json:"chepherd:user_id"`
	PermittedBastions  []string `json:"chepherd:permitted_bastions"`
	Expiry             int64    `json:"exp"`
	IssuedAt           int64    `json:"iat"`
	Issuer             string   `json:"iss"`
	Audience           string   `json:"aud"`
	ClientID           string   `json:"client_id"`
	// IsDaemon is true for long-lived bastion-registration tokens (separate
	// trust domain from regular user tokens).
	IsDaemon bool `json:"chepherd:is_daemon,omitempty"`
}

// PermitsBastion reports whether this claim grants access to the given
// bastion_id. Daemon tokens implicitly only access their own bastion; user
// tokens carry an explicit allowlist.
func (c *Claims) PermitsBastion(bastionID string) bool {
	if c.IsDaemon {
		// Daemon tokens are scoped via the registration ledger, not the
		// claim itself — caller checks separately.
		return c.PermitsAnyBastion(bastionID)
	}
	for _, b := range c.PermittedBastions {
		if b == bastionID || b == "*" {
			return true
		}
	}
	return false
}

// PermitsAnyBastion is the daemon-claim variant — checks raw allowlist.
func (c *Claims) PermitsAnyBastion(bastionID string) bool {
	for _, b := range c.PermittedBastions {
		if b == bastionID {
			return true
		}
	}
	return false
}

// ClaimsKey is the context key for stashing validated Claims on a request.
type contextKey struct{}

var claimsKey = contextKey{}

// ClaimsFromContext returns the validated claims that Middleware attached
// to this request's context. Returns nil if no auth ran (programmer error
// — middleware should always run before any handler that calls this).
func ClaimsFromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(claimsKey).(*Claims)
	return c
}

// WithClaims stashes a Claims pointer on the request context. Normally the
// Verifier.Middleware does this; exposed here so integration tests can
// construct an authenticated context without minting a real JWT.
func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsKey, c)
}

// ─── verifier ──────────────────────────────────────────────────────────

// Verifier validates JWT tokens issued by identity-svc. Caches JWKS keys
// for the lifetime configured by RefreshInterval.
type Verifier struct {
	// JWKSEndpoint is the identity-svc URL exposing the public keyset
	// (typical: https://identity.openova.io/realms/openova/protocol/openid-connect/certs).
	JWKSEndpoint string

	// ExpectedIssuer rejects tokens not signed by this iss.
	ExpectedIssuer string

	// ExpectedAudience rejects tokens not addressed to this aud.
	ExpectedAudience string

	// RefreshInterval — how often to re-fetch JWKS. Default 1h.
	RefreshInterval time.Duration

	// HTTP — injected HTTP client (allows tests to mock).
	HTTP *http.Client

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	keysAt    time.Time
}

// New constructs a Verifier with sensible defaults.
func New(jwksEndpoint, issuer, audience string) *Verifier {
	return &Verifier{
		JWKSEndpoint:     jwksEndpoint,
		ExpectedIssuer:   issuer,
		ExpectedAudience: audience,
		RefreshInterval:  1 * time.Hour,
		HTTP:             &http.Client{Timeout: 10 * time.Second},
		keys:             map[string]*rsa.PublicKey{},
	}
}

// Verify parses + verifies a Bearer token. Returns the validated claims on
// success, or an error explaining why verification failed.
func (v *Verifier) Verify(ctx context.Context, token string) (*Claims, error) {
	if token == "" {
		return nil, errors.New("auth: empty token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("auth: malformed JWT (want 3 segments)")
	}

	// 1. Decode header → get kid.
	hb, err := base64URLDecode(parts[0])
	if err != nil {
		return nil, fmt.Errorf("auth: decode header: %w", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return nil, fmt.Errorf("auth: parse header: %w", err)
	}
	if hdr.Alg != "RS256" {
		return nil, fmt.Errorf("auth: unsupported alg %q (want RS256)", hdr.Alg)
	}

	// 2. Look up the signing key.
	key, err := v.keyFor(ctx, hdr.Kid)
	if err != nil {
		return nil, fmt.Errorf("auth: key lookup: %w", err)
	}

	// 3. Verify signature.
	signedData := []byte(parts[0] + "." + parts[1])
	sig, err := base64URLDecode(parts[2])
	if err != nil {
		return nil, fmt.Errorf("auth: decode signature: %w", err)
	}
	if err := verifyRSASignature(key, signedData, sig); err != nil {
		return nil, fmt.Errorf("auth: signature: %w", err)
	}

	// 4. Decode + check claims.
	pb, err := base64URLDecode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("auth: decode payload: %w", err)
	}
	var c Claims
	if err := json.Unmarshal(pb, &c); err != nil {
		return nil, fmt.Errorf("auth: parse claims: %w", err)
	}

	now := time.Now().Unix()
	if c.Expiry > 0 && now >= c.Expiry {
		return nil, errors.New("auth: token expired")
	}
	if v.ExpectedIssuer != "" && c.Issuer != v.ExpectedIssuer {
		return nil, fmt.Errorf("auth: bad issuer %q (want %q)", c.Issuer, v.ExpectedIssuer)
	}
	if v.ExpectedAudience != "" && c.Audience != v.ExpectedAudience {
		return nil, fmt.Errorf("auth: bad audience %q (want %q)", c.Audience, v.ExpectedAudience)
	}
	return &c, nil
}

func (v *Verifier) keyFor(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	if time.Since(v.keysAt) < v.RefreshInterval {
		if k, ok := v.keys[kid]; ok {
			v.mu.RUnlock()
			return k, nil
		}
	}
	v.mu.RUnlock()

	if err := v.refresh(ctx); err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("auth: kid %q not in JWKS", kid)
}

// refresh fetches the JWKS endpoint + repopulates the in-memory key map.
func (v *Verifier) refresh(ctx context.Context) error {
	if v.JWKSEndpoint == "" {
		return errors.New("auth: JWKSEndpoint not configured")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", v.JWKSEndpoint, nil)
	if err != nil {
		return err
	}
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks endpoint returned %d", resp.StatusCode)
	}
	var doc struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("parse jwks: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64URLDecode(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64URLDecode(k.E)
		if err != nil {
			continue
		}
		eInt := 0
		for _, b := range eBytes {
			eInt = eInt<<8 | int(b)
		}
		keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: eInt,
		}
	}
	v.mu.Lock()
	v.keys = keys
	v.keysAt = time.Now()
	v.mu.Unlock()
	return nil
}

// ─── middleware ─────────────────────────────────────────────────────────

// Middleware wraps an http.Handler with bearer-token verification. On
// success, the validated Claims are attached to the request context;
// handlers retrieve them via ClaimsFromContext.
//
// On failure: writes a structured 401 with the protocol §4.error
// AUTH_REVOKED code.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("Authorization")
		if !strings.HasPrefix(raw, "Bearer ") {
			authError(w, "AUTH_REVOKED", "Authorization: Bearer <token> required")
			return
		}
		token := strings.TrimPrefix(raw, "Bearer ")
		claims, err := v.Verify(r.Context(), token)
		if err != nil {
			authError(w, "AUTH_REVOKED", err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func authError(w http.ResponseWriter, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    code,
		"message": message,
	})
}

// ─── plumbing ──────────────────────────────────────────────────────────

func base64URLDecode(s string) ([]byte, error) {
	// Add padding to make Go's URL-base64 decoder happy.
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.URLEncoding.DecodeString(s)
}

// verifyRSASignature is a thin wrapper to keep imports tidy + allow future
// alg swaps. RS256 = RSASSA-PKCS1-v1_5 with SHA-256.
func verifyRSASignature(pub *rsa.PublicKey, data, sig []byte) error {
	return verifyRS256(pub, data, sig)
}

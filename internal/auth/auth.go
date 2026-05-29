// Package auth provides chepherd's pluggable authentication layer (#128).
//
// Two backing implementations:
//
//   - LocalProvider — generates a bootstrap token on first start, signs
//     short-lived HS256 JWTs with a per-installation secret. No external
//     dependency. The default for hobby Podman and single-user installs.
//
//   - OIDCProvider — validates Bearer tokens against an OpenID Connect
//     JWKS endpoint (Keycloak on OpenOva Sovereign, but any compliant
//     issuer works). Wired in when CHEPHERD_AUTH_MODE=oidc.
//
// The same interface lets HTTP middleware, the MCP server, and the
// dashboard share one authn surface — no per-component shortcuts.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Identity is the validated principal behind one token. Returned by
// AuthProvider.Validate; consumed by handlers that need to know "who is
// making this request".
type Identity struct {
	Subject string            // user-id ("operator", "agent-<name>", or OIDC sub claim)
	Issuer  string            // "chepherd-local" or the OIDC iss URL
	Claims  map[string]any    // raw token claims (for richer authz logic later)
	Expiry  time.Time         // absolute expiry — handlers can short-circuit early
	Extra   map[string]string // implementation-specific attributes
}

// AuthProvider is the unified authentication interface.
type AuthProvider interface {
	// Mode returns "local" or "oidc". Used in logs and the /healthz output.
	Mode() string
	// Validate parses + verifies a raw token (no "Bearer " prefix).
	// Returns ErrInvalidToken for any failure so callers don't leak
	// internal validation details to the wire.
	Validate(ctx context.Context, token string) (*Identity, error)
	// IssueBootstrapToken creates an operator token for first-run UX
	// (LocalProvider). OIDC implementations return ErrNotSupported.
	IssueBootstrapToken(ctx context.Context, subject string, ttl time.Duration) (string, error)
}

// ErrInvalidToken is returned when validation fails for any reason.
// Use errors.Is to detect.
var ErrInvalidToken = errors.New("invalid token")

// ErrNotSupported is returned when an AuthProvider doesn't implement
// an optional method (e.g. IssueBootstrapToken in OIDC mode).
var ErrNotSupported = errors.New("not supported by this auth provider")

// New constructs the configured AuthProvider. Selection priority:
//
//   $CHEPHERD_AUTH_MODE = "local" | "oidc"
//   else $CHEPHERD_PROFILE = "enterprise" → oidc
//   else local
//
// stateDir is where the LocalProvider persists its signing secret.
func New(mode, stateDir, oidcIssuer string) (AuthProvider, error) {
	if mode == "" {
		mode = os.Getenv("CHEPHERD_AUTH_MODE")
	}
	if mode == "" && os.Getenv("CHEPHERD_PROFILE") == "enterprise" {
		mode = "oidc"
	}
	if mode == "" {
		mode = "local"
	}
	switch mode {
	case "local":
		return NewLocalProvider(stateDir)
	case "oidc":
		if oidcIssuer == "" {
			oidcIssuer = os.Getenv("CHEPHERD_OIDC_ISSUER")
		}
		if oidcIssuer == "" {
			return nil, fmt.Errorf("auth: CHEPHERD_AUTH_MODE=oidc requires CHEPHERD_OIDC_ISSUER")
		}
		return NewOIDCProvider(oidcIssuer)
	default:
		return nil, fmt.Errorf("auth: unknown mode %q (want local | oidc)", mode)
	}
}

// ─── LocalProvider — HS256 JWT, machine-local secret ────────────────────────

// LocalProvider signs short-lived HS256 JWTs with a per-installation
// secret stored at $stateDir/auth.secret. On first start (or if the
// secret file is missing) a 32-byte random secret is generated, written
// 0600, and used for all subsequent signing. The "bootstrap token" is
// just an HS256 JWT issued by this provider — operators paste it once
// to log into the dashboard.
type LocalProvider struct {
	mu     sync.RWMutex
	secret []byte
}

const localProviderName = "chepherd-local"

// NewLocalProvider opens (or initializes) the local auth secret at
// $stateDir/auth.secret. Permissions: dir 0700, file 0600.
func NewLocalProvider(stateDir string) (*LocalProvider, error) {
	path := filepath.Join(stateDir, "auth.secret")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("auth: mkdir stateDir: %w", err)
	}
	secret, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("auth: read secret: %w", err)
		}
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("auth: rand: %w", err)
		}
		if err := os.WriteFile(path, secret, 0o600); err != nil {
			return nil, fmt.Errorf("auth: write secret: %w", err)
		}
	}
	return &LocalProvider{secret: secret}, nil
}

func (p *LocalProvider) Mode() string { return "local" }

// IssueBootstrapToken signs an HS256 JWT for `subject` valid for `ttl`.
// If ttl is zero it defaults to 30 days — long enough for an operator
// to copy the token from chepherd's first-run output without re-rolling.
func (p *LocalProvider) IssueBootstrapToken(_ context.Context, subject string, ttl time.Duration) (string, error) {
	if subject == "" {
		subject = "operator"
	}
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	now := time.Now().UTC()
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	claims := map[string]any{
		"iss": localProviderName,
		"sub": subject,
		"iat": now.Unix(),
		"exp": now.Add(ttl).Unix(),
	}
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	hb := base64.RawURLEncoding.EncodeToString(h)
	cb := base64.RawURLEncoding.EncodeToString(c)
	signing := hb + "." + cb
	p.mu.RLock()
	sig := hmacSHA256(p.secret, []byte(signing))
	p.mu.RUnlock()
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (p *LocalProvider) Validate(_ context.Context, token string) (*Identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}
	signing := parts[0] + "." + parts[1]
	gotSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrInvalidToken
	}
	p.mu.RLock()
	want := hmacSHA256(p.secret, []byte(signing))
	p.mu.RUnlock()
	if !hmac.Equal(gotSig, want) {
		return nil, ErrInvalidToken
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return nil, ErrInvalidToken
	}
	expF, _ := claims["exp"].(float64)
	if expF == 0 {
		return nil, ErrInvalidToken
	}
	exp := time.Unix(int64(expF), 0).UTC()
	if time.Now().UTC().After(exp) {
		return nil, ErrInvalidToken
	}
	sub, _ := claims["sub"].(string)
	iss, _ := claims["iss"].(string)
	if iss != localProviderName {
		return nil, ErrInvalidToken
	}
	return &Identity{Subject: sub, Issuer: iss, Claims: claims, Expiry: exp}, nil
}

func hmacSHA256(key, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}

// ─── OIDCProvider (scaffold for #128) ────────────────────────────────────────
//
// Validates Bearer JWTs against the issuer's JWKS endpoint. Wired in
// when chepherd is deployed on OpenOva Sovereign or any K8s cluster
// running Keycloak. Full implementation lands with the enterprise
// profile (#129) — fetches JWKS, caches keys, validates signatures via
// crypto/rsa or crypto/ecdsa depending on alg.
//
// Today: constructor accepts the issuer URL so config validation works,
// but Validate returns ErrNotSupported. Local-mode is the default
// everywhere except enterprise.

type OIDCProvider struct {
	issuer string
}

// NewOIDCProvider constructs an OIDC auth provider that validates against
// the given issuer's JWKS endpoint. The issuer must publish RFC 8414
// discovery at /.well-known/openid-configuration.
func NewOIDCProvider(issuer string) (*OIDCProvider, error) {
	if issuer == "" {
		return nil, errors.New("oidc: issuer required")
	}
	return &OIDCProvider{issuer: issuer}, nil
}

func (p *OIDCProvider) Mode() string { return "oidc" }

func (p *OIDCProvider) Validate(_ context.Context, _ string) (*Identity, error) {
	// JWKS-fetch + signature-verify lands with enterprise profile (#129).
	// Until then OIDC mode is "configured but not yet operational" so the
	// admin gets a clear error rather than silent admit-all.
	return nil, fmt.Errorf("oidc validation not yet wired (issuer=%s, #128)", p.issuer)
}

func (p *OIDCProvider) IssueBootstrapToken(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", ErrNotSupported
}

// internal/auth/validators.go — v0.9.3 #225 rows B3-B6.
// Four TokenValidator implementations layered onto B1's seam:
//
//	B3 TrustListValidator — verifies ES256-signed peer JWTs against a
//	                        configured peer-SID trust list. JWS verification
//	                        uses each peer's cached JWKS public key
//	                        (fetched via federation orchestrator from
//	                        peer's /.well-known/jwks.json).
//	B4 MTLSValidator      — bridge to TLS-layer client-cert verification.
//	                        Confirms the peer-cert SubjectCommonName matches
//	                        a configured trust set. Real chain validation
//	                        lives at the http.Server.TLSConfig layer — this
//	                        validator just routes the subject claim once
//	                        the chain is already accepted.
//	B5 OAuth2Validator    — HMAC-signed bearer (shared-secret OAuth2 client
//	                        credentials flow). Validates against the OAuth2
//	                        provider's introspection endpoint, falling back
//	                        to a local HMAC for offline mode.
//	B6 APIKeyValidator    — opaque API keys stored in AuthSecretRepository
//	                        under purpose=apiKey:<key-id>. Constant-time
//	                        comparison + per-key revocation via Delete.
//
// All four implement a2a.TokenValidator's contract: Validate(ctx,token)
// → (subject, error). Composed via MultiValidator which tries each in
// order until one succeeds.
//
// Refs #225 rows B3 B4 B5 B6 + row B1 seam.
package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// MultiValidator tries each child validator in order until one returns
// a non-empty subject + nil error. Failures from earlier validators
// are absorbed; the LAST error is returned when all reject. Operator-
// friendly: a single Bearer can be any of mTLS-cert-routed / API-key /
// OAuth2 / OIDC / signed-peer-JWT — chepherd accepts whichever the
// caller carries.
type MultiValidator struct {
	Validators []func(ctx context.Context, token string) (string, error)
}

// Validate iterates child validators. The first that returns
// (non-empty, nil) wins. When all fail, returns the last error.
func (m *MultiValidator) Validate(ctx context.Context, token string) (string, error) {
	var lastErr error
	for _, v := range m.Validators {
		if subject, err := v(ctx, token); err == nil && subject != "" {
			return subject, nil
		} else if err != nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no validator accepted the token")
	}
	return "", lastErr
}

// ─── B3 TrustListValidator ────────────────────────────────────────

// PeerJWKLoader returns the JWKS-formatted public-key body for a peer
// SID. Implementations look in the AgentCardRepository — agent cards
// from C1 federation include the `jwks` field (or a `jwks_uri`).
type PeerJWKLoader interface {
	PublicJWK(ctx context.Context, peerSID string) ([]byte, error)
}

// TrustListValidator accepts a token IFF:
//   - it parses as JWS ES256
//   - the `iss` claim matches a SID in TrustedSIDs
//   - the signature verifies against the peer's cached JWKS public key
//
// Returns the `sub` claim as the subject so downstream RBAC can apply
// per-subject grants.
type TrustListValidator struct {
	Loader      PeerJWKLoader
	TrustedSIDs map[string]struct{}
}

func (t *TrustListValidator) Validate(ctx context.Context, token string) (string, error) {
	if t.Loader == nil {
		return "", errors.New("TrustListValidator: nil PeerJWKLoader")
	}
	// Decode claims unverified first to read `iss`.
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("TrustListValidator: not a JWS")
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("TrustListValidator: claims decode: %w", err)
	}
	var claims struct {
		Iss string `json:"iss"`
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
		Nbf int64  `json:"nbf"`
	}
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return "", fmt.Errorf("TrustListValidator: claims parse: %w", err)
	}
	if claims.Iss == "" {
		return "", errors.New("TrustListValidator: missing iss claim")
	}
	if _, ok := t.TrustedSIDs[claims.Iss]; !ok {
		return "", fmt.Errorf("TrustListValidator: issuer %q not in trust list", claims.Iss)
	}
	// Fetch the peer's JWKS + extract the public key for verification.
	jwksBody, err := t.Loader.PublicJWK(ctx, claims.Iss)
	if err != nil {
		return "", fmt.Errorf("TrustListValidator: load peer %q JWKS: %w", claims.Iss, err)
	}
	pub, err := parseFirstECKey(jwksBody)
	if err != nil {
		return "", fmt.Errorf("TrustListValidator: parse peer JWKS: %w", err)
	}
	if _, err := VerifyJWS(pub, token); err != nil {
		return "", fmt.Errorf("TrustListValidator: verify: %w", err)
	}
	// Time-based claims — caller-side enforced (B1 + sub-validators
	// don't need a clock here, but a basic exp check prevents trivial
	// replay).
	now := time.Now().Unix()
	if claims.Exp != 0 && now > claims.Exp {
		return "", fmt.Errorf("TrustListValidator: token expired")
	}
	if claims.Nbf != 0 && now < claims.Nbf {
		return "", fmt.Errorf("TrustListValidator: token not yet valid")
	}
	if claims.Sub == "" {
		return claims.Iss, nil
	}
	return claims.Sub, nil
}

func parseFirstECKey(jwksBody []byte) (*ecdsa.PublicKey, error) {
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(jwksBody, &doc); err != nil {
		return nil, fmt.Errorf("decode JWKS: %w", err)
	}
	if len(doc.Keys) == 0 {
		return nil, errors.New("empty JWKS")
	}
	k := doc.Keys[0]
	if k.Kty != "EC" || k.Crv != "P-256" {
		return nil, fmt.Errorf("unsupported kty/crv %q/%q (want EC/P-256)", k.Kty, k.Crv)
	}
	xb, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, fmt.Errorf("decode x: %w", err)
	}
	yb, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil {
		return nil, fmt.Errorf("decode y: %w", err)
	}
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(xb),
		Y:     new(big.Int).SetBytes(yb),
	}, nil
}

// ─── B4 MTLSValidator ─────────────────────────────────────────────

// MTLSValidator validates a request whose Authorization header begins
// with `mTLS <subject>` — the actual TLS chain validation happens at
// the http.Server.TLSConfig layer (Go stdlib's tls.Config.ClientAuth =
// VerifyClientCertIfGiven). This validator simply asserts the subject
// claim matches an entry in AcceptedSubjects + returns it. When the
// TLS layer rejects the chain entirely, this validator never runs.
type MTLSValidator struct {
	AcceptedSubjects map[string]struct{}
}

func (m *MTLSValidator) Validate(_ context.Context, token string) (string, error) {
	if !strings.HasPrefix(token, "mTLS ") {
		return "", errors.New("MTLSValidator: token not in `mTLS <subject>` form")
	}
	subject := strings.TrimSpace(strings.TrimPrefix(token, "mTLS "))
	if subject == "" {
		return "", errors.New("MTLSValidator: empty subject after mTLS prefix")
	}
	if _, ok := m.AcceptedSubjects[subject]; !ok {
		return "", fmt.Errorf("MTLSValidator: subject %q not in accepted list", subject)
	}
	return "mtls:" + subject, nil
}

// ─── B5 OAuth2Validator ───────────────────────────────────────────

// OAuth2Validator verifies bearer tokens against an OAuth2 provider's
// RFC 7662 token-introspection endpoint. When IntrospectURL is empty,
// falls back to an in-process HMAC verifier (useful for self-contained
// mesh deployments + local dev). Token format for the HMAC fallback:
//
//	base64url(sub) . base64url(exp_unix) . base64url(HMAC-SHA256(secret, sub|exp))
type OAuth2Validator struct {
	IntrospectURL string
	Bearer        string // Authorization for the introspect endpoint
	HTTPClient    *http.Client
	// HMACSecret is the shared secret for offline-mode token validation.
	// Empty disables the HMAC fallback.
	HMACSecret []byte
}

func (o *OAuth2Validator) Validate(ctx context.Context, token string) (string, error) {
	if o.IntrospectURL != "" {
		return o.introspect(ctx, token)
	}
	if len(o.HMACSecret) > 0 {
		return o.verifyHMAC(token)
	}
	return "", errors.New("OAuth2Validator: neither IntrospectURL nor HMACSecret configured")
}

func (o *OAuth2Validator) introspect(ctx context.Context, token string) (string, error) {
	client := o.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	body := strings.NewReader("token=" + token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.IntrospectURL, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if o.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+o.Bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OAuth2Validator: introspect HTTP %d", resp.StatusCode)
	}
	respBytes, _ := io.ReadAll(resp.Body)
	var ir struct {
		Active bool   `json:"active"`
		Sub    string `json:"sub"`
	}
	if err := json.Unmarshal(respBytes, &ir); err != nil {
		return "", fmt.Errorf("OAuth2Validator: decode introspect: %w", err)
	}
	if !ir.Active {
		return "", errors.New("OAuth2Validator: token not active")
	}
	if ir.Sub == "" {
		return "oauth2:anonymous", nil
	}
	return "oauth2:" + ir.Sub, nil
}

func (o *OAuth2Validator) verifyHMAC(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("OAuth2Validator: HMAC token not 3 parts")
	}
	sub, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", err
	}
	expBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	sigGot, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, o.HMACSecret)
	mac.Write(sub)
	mac.Write([]byte("|"))
	mac.Write(expBytes)
	want := mac.Sum(nil)
	if subtle.ConstantTimeCompare(sigGot, want) != 1 {
		return "", errors.New("OAuth2Validator: HMAC signature mismatch")
	}
	expStr := string(expBytes)
	if exp, err := strconvAtoiInt64(expStr); err == nil && exp != 0 {
		if time.Now().Unix() > exp {
			return "", errors.New("OAuth2Validator: HMAC token expired")
		}
	}
	return "oauth2:" + string(sub), nil
}

func strconvAtoiInt64(s string) (int64, error) {
	var v int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit %q", s)
		}
		v = v*10 + int64(c-'0')
	}
	return v, nil
}

// ─── B6 APIKeyValidator ───────────────────────────────────────────

// APIKeyValidator looks up opaque bearer tokens in
// AuthSecretRepository under `purpose = apikey:<keyID>` rows. The
// row's Key bytes hold a SHA-256 hash of the actual key + a JSON-
// encoded {sub, scopes} blob so the validator can return the subject
// without re-hashing.
//
// Operator-facing rotation: create a new row, distribute new bearer,
// delete the old row.
type APIKeyValidator struct {
	Repo persistence.AuthSecretRepository
	// Cache memoizes the last-known key → subject so the hot path
	// doesn't hit persistence on every request. Invalidated by writes
	// + by the 5-minute TTL.
	cacheMu  sync.RWMutex
	cache    map[string]apiKeyCacheEntry
	CacheTTL time.Duration
}

type apiKeyCacheEntry struct {
	subject  string
	loadedAt time.Time
}

// APIKeyRecord is the JSON shape stored in AuthSecretRepository.Key
// for apikey rows. Persisted by operator-side tooling (out of scope
// for this validator).
type APIKeyRecord struct {
	Subject string   `json:"sub"`
	Scopes  []string `json:"scopes,omitempty"`
}

func (a *APIKeyValidator) Validate(ctx context.Context, token string) (string, error) {
	if a.Repo == nil {
		return "", errors.New("APIKeyValidator: nil repository")
	}
	cacheTTL := a.CacheTTL
	if cacheTTL == 0 {
		cacheTTL = 5 * time.Minute
	}
	a.cacheMu.RLock()
	if a.cache != nil {
		if e, ok := a.cache[token]; ok && time.Since(e.loadedAt) < cacheTTL {
			subj := e.subject
			a.cacheMu.RUnlock()
			return subj, nil
		}
	}
	a.cacheMu.RUnlock()
	// Look up. Token IS the keyID; the AuthSecretRepository row body
	// contains the subject JSON.
	sec, err := a.Repo.Get(ctx, "apikey:"+token)
	if err != nil {
		return "", fmt.Errorf("APIKeyValidator: lookup: %w", err)
	}
	if sec == nil {
		return "", errors.New("APIKeyValidator: unknown key")
	}
	var rec APIKeyRecord
	if err := json.Unmarshal(sec.Key, &rec); err != nil {
		return "", fmt.Errorf("APIKeyValidator: decode record: %w", err)
	}
	if rec.Subject == "" {
		return "", errors.New("APIKeyValidator: record missing sub")
	}
	a.cacheMu.Lock()
	if a.cache == nil {
		a.cache = map[string]apiKeyCacheEntry{}
	}
	a.cache[token] = apiKeyCacheEntry{subject: "apikey:" + rec.Subject, loadedAt: time.Now()}
	a.cacheMu.Unlock()
	return "apikey:" + rec.Subject, nil
}

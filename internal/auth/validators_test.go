// internal/auth/validators_test.go — v0.9.3 #225 rows B3-B6.
// Pins MultiValidator composition + TrustListValidator (JWS over peer
// JWKS) + MTLSValidator (subject-list gate) + OAuth2Validator (HMAC
// fallback + introspect URL) + APIKeyValidator (AuthSecretRepository
// lookup with cache).
//
// Refs #225 rows B3 B4 B5 B6.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/persistence/sqlite"
)

// ─── MultiValidator ─────────────────────────────────────────────

func TestMultiValidator_FirstAcceptingWins(t *testing.T) {
	mv := &MultiValidator{Validators: []func(ctx context.Context, token string) (string, error){
		func(_ context.Context, _ string) (string, error) { return "", errors.New("first rejects") },
		func(_ context.Context, _ string) (string, error) { return "second-accepted", nil },
		func(_ context.Context, _ string) (string, error) { return "third-never-runs", nil },
	}}
	got, err := mv.Validate(context.Background(), "tok")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got != "second-accepted" {
		t.Errorf("got %q, want second-accepted", got)
	}
}

func TestMultiValidator_AllRejectReturnsLastErr(t *testing.T) {
	mv := &MultiValidator{Validators: []func(ctx context.Context, token string) (string, error){
		func(_ context.Context, _ string) (string, error) { return "", errors.New("first") },
		func(_ context.Context, _ string) (string, error) { return "", errors.New("second") },
	}}
	if _, err := mv.Validate(context.Background(), "tok"); err == nil || err.Error() != "second" {
		t.Errorf("expected error 'second', got %v", err)
	}
}

// ─── B3 TrustListValidator ──────────────────────────────────────

type fakeJWKLoader struct {
	jwks map[string][]byte
	err  error
}

func (f *fakeJWKLoader) PublicJWK(_ context.Context, sid string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	b, ok := f.jwks[sid]
	if !ok {
		return nil, errors.New("unknown")
	}
	return b, nil
}

func TestTrustListValidator_AcceptsSignedFromTrustedPeer(t *testing.T) {
	store, _ := sqlite.NewStore(context.Background(), ":memory:")
	defer store.Close()
	priv, _ := LoadOrCreateES256(context.Background(), store.AuthSecrets())
	jwks, _ := PublicJWK(priv)

	tok, err := SignJWS(priv, map[string]any{
		"iss": "peer-A",
		"sub": "operator-on-peer-A",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("SignJWS: %v", err)
	}

	v := &TrustListValidator{
		Loader:      &fakeJWKLoader{jwks: map[string][]byte{"peer-A": jwks}},
		TrustedSIDs: map[string]struct{}{"peer-A": {}},
	}
	got, err := v.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got != "operator-on-peer-A" {
		t.Errorf("subject = %q, want operator-on-peer-A", got)
	}
}

func TestTrustListValidator_RejectsUntrustedIssuer(t *testing.T) {
	store, _ := sqlite.NewStore(context.Background(), ":memory:")
	defer store.Close()
	priv, _ := LoadOrCreateES256(context.Background(), store.AuthSecrets())
	jwks, _ := PublicJWK(priv)
	tok, _ := SignJWS(priv, map[string]any{"iss": "peer-EVIL", "sub": "x"})
	v := &TrustListValidator{
		Loader:      &fakeJWKLoader{jwks: map[string][]byte{"peer-EVIL": jwks}},
		TrustedSIDs: map[string]struct{}{"peer-A": {}},
	}
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Error("expected rejection of untrusted issuer")
	}
}

func TestTrustListValidator_RejectsExpired(t *testing.T) {
	store, _ := sqlite.NewStore(context.Background(), ":memory:")
	defer store.Close()
	priv, _ := LoadOrCreateES256(context.Background(), store.AuthSecrets())
	jwks, _ := PublicJWK(priv)
	tok, _ := SignJWS(priv, map[string]any{
		"iss": "peer-A", "sub": "x", "exp": time.Now().Add(-time.Minute).Unix(),
	})
	v := &TrustListValidator{
		Loader:      &fakeJWKLoader{jwks: map[string][]byte{"peer-A": jwks}},
		TrustedSIDs: map[string]struct{}{"peer-A": {}},
	}
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Error("expected rejection of expired token")
	}
}

// ─── B4 MTLSValidator ───────────────────────────────────────────

func TestMTLSValidator_AcceptsKnownSubject(t *testing.T) {
	v := &MTLSValidator{AcceptedSubjects: map[string]struct{}{"CN=peer-A": {}}}
	got, err := v.Validate(context.Background(), "mTLS CN=peer-A")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got != "mtls:CN=peer-A" {
		t.Errorf("subject = %q, want mtls:CN=peer-A", got)
	}
}

func TestMTLSValidator_RejectsUnknownSubject(t *testing.T) {
	v := &MTLSValidator{AcceptedSubjects: map[string]struct{}{"CN=peer-A": {}}}
	if _, err := v.Validate(context.Background(), "mTLS CN=peer-EVIL"); err == nil {
		t.Error("expected rejection of unknown subject")
	}
}

func TestMTLSValidator_RejectsWrongPrefix(t *testing.T) {
	v := &MTLSValidator{AcceptedSubjects: map[string]struct{}{"CN=peer-A": {}}}
	if _, err := v.Validate(context.Background(), "CN=peer-A"); err == nil {
		t.Error("expected rejection of token missing mTLS prefix")
	}
}

// ─── B5 OAuth2Validator ─────────────────────────────────────────

func TestOAuth2Validator_HMACFallback(t *testing.T) {
	secret := []byte("shared-walk-secret")
	v := &OAuth2Validator{HMACSecret: secret}
	sub := "alice"
	exp := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(sub))
	mac.Write([]byte("|"))
	mac.Write([]byte(exp))
	sig := mac.Sum(nil)
	tok := base64.RawURLEncoding.EncodeToString([]byte(sub)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(exp)) + "." +
		base64.RawURLEncoding.EncodeToString(sig)
	got, err := v.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got != "oauth2:alice" {
		t.Errorf("subject = %q, want oauth2:alice", got)
	}
}

func TestOAuth2Validator_HMACRejectsWrongSig(t *testing.T) {
	v := &OAuth2Validator{HMACSecret: []byte("correct")}
	exp := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	tok := base64.RawURLEncoding.EncodeToString([]byte("x")) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(exp)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte("wrong-sig-bytes"))
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Error("expected rejection of wrong signature")
	}
}

func TestOAuth2Validator_IntrospectEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":true,"sub":"introspected-user"}`))
	}))
	defer srv.Close()
	v := &OAuth2Validator{IntrospectURL: srv.URL}
	got, err := v.Validate(context.Background(), "token-xyz")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got != "oauth2:introspected-user" {
		t.Errorf("got %q, want oauth2:introspected-user", got)
	}
}

func TestOAuth2Validator_IntrospectInactiveRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"active":false}`))
	}))
	defer srv.Close()
	v := &OAuth2Validator{IntrospectURL: srv.URL}
	if _, err := v.Validate(context.Background(), "stale"); err == nil {
		t.Error("expected rejection of inactive token")
	}
}

// ─── B6 APIKeyValidator ─────────────────────────────────────────

func TestAPIKeyValidator_AcceptsRegisteredKey(t *testing.T) {
	store, _ := sqlite.NewStore(context.Background(), ":memory:")
	defer store.Close()
	rec := APIKeyRecord{Subject: "service-x"}
	body, _ := json.Marshal(rec)
	if err := store.AuthSecrets().Save(context.Background(), "apikey:secret-abc", body, "apikey"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	v := &APIKeyValidator{Repo: store.AuthSecrets()}
	got, err := v.Validate(context.Background(), "secret-abc")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got != "apikey:service-x" {
		t.Errorf("got %q, want apikey:service-x", got)
	}
}

func TestAPIKeyValidator_UnknownKeyRejected(t *testing.T) {
	store, _ := sqlite.NewStore(context.Background(), ":memory:")
	defer store.Close()
	v := &APIKeyValidator{Repo: store.AuthSecrets()}
	if _, err := v.Validate(context.Background(), "never-registered"); err == nil {
		t.Error("expected rejection of unknown key")
	}
}

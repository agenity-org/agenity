// internal/auth/jwt_runner_middleware_test.go — #486 Wave T1 unit
// assertions on the runner-side JWT verification middleware.
//
// Named assertions T1.U1-U8:
//
//	U1 — happy path: valid JWT signed by daemon's key passes
//	U2 — expired exp → 401
//	U3 — aud mismatch → 401 (neither sid nor runner://sid form)
//	U4 — replayed jti → 401 (second observation within TTL)
//	U5 — missing jti → 401 (D2 mint always sets it; absent = malformed)
//	U6 — JWKS-fetch-fail → 401 (don't fall open)
//	U7 — tampered signature → 401
//	U8 — sub claim propagated into request context via
//	     SubjectFromRunnerContext
//
// Refs #486.
package auth_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/auth"
)

// newTestIssuer stands up an httptest.Server serving a JWKS document
// for a newly-generated ES256 key. Returns the server URL + private
// key for signing tokens.
func newTestIssuer(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	jwksBody, err := auth.PublicJWK(priv)
	if err != nil {
		t.Fatalf("auth.PublicJWK: %v", err)
	}
	// PublicJWK already returns the full {keys: [...]} JWKS document.
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksBody)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, priv
}

// signWith mints a JWT with the given claims signed by priv.
func signWith(t *testing.T, priv *ecdsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	tok, err := auth.SignJWS(priv, claims)
	if err != nil {
		t.Fatalf("SignJWS: %v", err)
	}
	return tok
}

// newHandler returns a handler that responds 200 with the sub claim
// captured from context. Used to verify U8 (sub propagation).
func newHandler() (http.Handler, *string) {
	got := new(string)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = auth.SubjectFromRunnerContext(r.Context())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}), got
}

// TestT1_U1_HappyPath pins the canonical valid-token path + U8 (sub).
func TestT1_U1_HappyPath(t *testing.T) {
	iss, priv := newTestIssuer(t)
	const runnerSID = "test-runner-sid"

	tok := signWith(t, priv, map[string]any{
		"iss": iss,
		"sub": "test-caller",
		"aud": runnerSID,
		"exp": time.Now().Add(60 * time.Second).Unix(),
		"jti": "u1-jti",
	})
	cfg := &auth.RunnerJWTMiddlewareConfig{
		RunnerSID:  runnerSID,
		JWKSClient: auth.NewJWKSClient(nil, 0),
	}
	h, got := newHandler()
	srv := httptest.NewServer(auth.JWTRunnerMiddleware(cfg, h))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/a2a/"+runnerSID+"/jsonrpc",
		strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("U1 FAIL: POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("U1 FAIL: status = %d (body=%s)", resp.StatusCode, body)
	}
	if *got != "test-caller" {
		t.Errorf("U8 FAIL: SubjectFromRunnerContext = %q, want test-caller", *got)
	}
}

// TestT1_U2_ExpiredExp pins exp validation.
func TestT1_U2_ExpiredExp(t *testing.T) {
	iss, priv := newTestIssuer(t)
	tok := signWith(t, priv, map[string]any{
		"iss": iss,
		"sub": "caller",
		"aud": "sid",
		"exp": time.Now().Add(-1 * time.Second).Unix(), // expired
		"jti": "u2",
	})
	cfg := &auth.RunnerJWTMiddlewareConfig{
		RunnerSID:  "sid",
		JWKSClient: auth.NewJWKSClient(nil, 0),
	}
	expectStatus(t, cfg, tok, http.StatusUnauthorized, "U2")
}

// TestT1_U3_AudMismatch pins aud validation.
func TestT1_U3_AudMismatch(t *testing.T) {
	iss, priv := newTestIssuer(t)
	tok := signWith(t, priv, map[string]any{
		"iss": iss,
		"sub": "caller",
		"aud": "different-sid", // not the runner's sid
		"exp": time.Now().Add(60 * time.Second).Unix(),
		"jti": "u3",
	})
	cfg := &auth.RunnerJWTMiddlewareConfig{
		RunnerSID:  "expected-sid",
		JWKSClient: auth.NewJWKSClient(nil, 0),
	}
	expectStatus(t, cfg, tok, http.StatusUnauthorized, "U3")
}

// TestT1_U3_AudURLForm pins that aud can also be runner://<sid>.
func TestT1_U3_AudURLForm(t *testing.T) {
	iss, priv := newTestIssuer(t)
	const runnerSID = "url-form-sid"
	tok := signWith(t, priv, map[string]any{
		"iss": iss,
		"sub": "caller",
		"aud": "runner://" + runnerSID,
		"exp": time.Now().Add(60 * time.Second).Unix(),
		"jti": "u3url",
	})
	cfg := &auth.RunnerJWTMiddlewareConfig{
		RunnerSID:  runnerSID,
		JWKSClient: auth.NewJWKSClient(nil, 0),
	}
	expectStatus(t, cfg, tok, http.StatusOK, "U3-URL-form")
}

// TestT1_U4_ReplayedJTI pins the jti replay cache.
func TestT1_U4_ReplayedJTI(t *testing.T) {
	iss, priv := newTestIssuer(t)
	tok := signWith(t, priv, map[string]any{
		"iss": iss,
		"sub": "caller",
		"aud": "sid",
		"exp": time.Now().Add(60 * time.Second).Unix(),
		"jti": "u4-replay",
	})
	cfg := &auth.RunnerJWTMiddlewareConfig{
		RunnerSID:  "sid",
		JWKSClient: auth.NewJWKSClient(nil, 0),
	}
	h, _ := newHandler()
	srv := httptest.NewServer(auth.JWTRunnerMiddleware(cfg, h))
	defer srv.Close()

	// First use — accepted.
	if got := doRequest(t, srv.URL, tok); got != http.StatusOK {
		t.Fatalf("U4 setup FAIL: first use = %d, want 200", got)
	}
	// Second use within TTL — replay rejected.
	if got := doRequest(t, srv.URL, tok); got != http.StatusUnauthorized {
		t.Errorf("U4 FAIL: replay = %d, want 401", got)
	}
}

// TestT1_U5_MissingJTI pins that absent jti = 401.
func TestT1_U5_MissingJTI(t *testing.T) {
	iss, priv := newTestIssuer(t)
	tok := signWith(t, priv, map[string]any{
		"iss": iss,
		"sub": "caller",
		"aud": "sid",
		"exp": time.Now().Add(60 * time.Second).Unix(),
		// no jti
	})
	cfg := &auth.RunnerJWTMiddlewareConfig{
		RunnerSID:  "sid",
		JWKSClient: auth.NewJWKSClient(nil, 0),
	}
	expectStatus(t, cfg, tok, http.StatusUnauthorized, "U5")
}

// TestT1_U6_JWKSFetchFail pins fail-closed on JWKS unreachable.
func TestT1_U6_JWKSFetchFail(t *testing.T) {
	// iss points at a port nothing's listening on → fetch fails.
	_, priv := newTestIssuer(t)
	tok := signWith(t, priv, map[string]any{
		"iss": "http://127.0.0.1:1", // refused
		"sub": "caller",
		"aud": "sid",
		"exp": time.Now().Add(60 * time.Second).Unix(),
		"jti": "u6",
	})
	cfg := &auth.RunnerJWTMiddlewareConfig{
		RunnerSID: "sid",
		JWKSClient: auth.NewJWKSClient(&http.Client{Timeout: 500 * time.Millisecond}, 0),
	}
	expectStatus(t, cfg, tok, http.StatusUnauthorized, "U6")
}

// TestT1_U7_TamperedSig pins signature validation.
func TestT1_U7_TamperedSig(t *testing.T) {
	iss, priv := newTestIssuer(t)
	tok := signWith(t, priv, map[string]any{
		"iss": iss,
		"sub": "caller",
		"aud": "sid",
		"exp": time.Now().Add(60 * time.Second).Unix(),
		"jti": "u7",
	})
	// Flip a bit in the signature (last segment).
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("setup: tok parts = %d", len(parts))
	}
	parts[2] = parts[2][:len(parts[2])-2] + "AA" // overwrite last 2 chars
	tampered := strings.Join(parts, ".")
	cfg := &auth.RunnerJWTMiddlewareConfig{
		RunnerSID:  "sid",
		JWKSClient: auth.NewJWKSClient(nil, 0),
	}
	expectStatus(t, cfg, tampered, http.StatusUnauthorized, "U7")
}

// TestT1_MissingBearer pins absent header.
func TestT1_MissingBearer(t *testing.T) {
	cfg := &auth.RunnerJWTMiddlewareConfig{
		RunnerSID:  "sid",
		JWKSClient: auth.NewJWKSClient(nil, 0),
	}
	h, _ := newHandler()
	srv := httptest.NewServer(auth.JWTRunnerMiddleware(cfg, h))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/x", strings.NewReader("{}"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("WWW-Authenticate header missing Bearer; got %q", resp.Header.Get("WWW-Authenticate"))
	}
}

// ─── helpers ──────────────────────────────────────────────────────

func expectStatus(t *testing.T, cfg *auth.RunnerJWTMiddlewareConfig, tok string, want int, name string) {
	t.Helper()
	h, _ := newHandler()
	srv := httptest.NewServer(auth.JWTRunnerMiddleware(cfg, h))
	defer srv.Close()
	if got := doRequest(t, srv.URL, tok); got != want {
		t.Errorf("%s FAIL: status = %d, want %d", name, got, want)
	}
}

func doRequest(t *testing.T, base, tok string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/a2a/sid/jsonrpc",
		strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

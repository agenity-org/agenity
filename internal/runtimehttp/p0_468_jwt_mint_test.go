// internal/runtimehttp/p0_468_jwt_mint_test.go pins the v0.9.4
// §15.2 per-call JWT mint endpoint (#468 Wave D2). Asserts:
//
//   - POST /api/v1/jwt/mint returns 200 + ES256-signed token with
//     the full §15.2 claim set on a valid request.
//   - The token verifies against the daemon's public key (round-
//     trip integrity proof — caller can rely on the signature).
//   - The claims match the request: iss=scheme://host, sub/aud
//     from the body, exp=iat+60s, jti is a fresh UUID per mint.
//   - GrantCheck=nil defaults to allow-all (D2 stub behavior).
//   - GrantCheck returning allowed=false yields HTTP 403 with the
//     spec deny-body — the acceptance test from #468.
//   - Missing ES256 key returns 503 (the daemon is unhealthy, not
//     the caller's fault).
//   - Missing sub/aud returns 400.
//   - Non-POST returns 405.
//
// Refs #468 V0.9.2-ARCHITECTURE.md §15.2.
package runtimehttp

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/auth"
)

func newES256ForTest(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ES256: %v", err)
	}
	return priv
}

func TestWaveD2_JWTMint_HappyPath(t *testing.T) {
	t.Parallel()
	priv := newES256ForTest(t)
	srv := httptest.NewServer((&Server{ES256Priv: priv}).Handler())
	defer srv.Close()

	reqBody := bytes.NewBufferString(`{"sub":"agent-caller","aud":"agent-target"}`)
	resp, err := http.Post(srv.URL+"/api/v1/jwt/mint", "application/json", reqBody)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Token        string `json:"token"`
		ExpInSeconds int    `json:"exp_in_seconds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Token == "" {
		t.Fatal("token empty")
	}
	if body.ExpInSeconds != 60 {
		t.Errorf("exp_in_seconds = %d, want 60", body.ExpInSeconds)
	}

	claims, err := auth.VerifyJWS(&priv.PublicKey, body.Token)
	if err != nil {
		t.Fatalf("VerifyJWS: %v", err)
	}
	if claims["sub"] != "agent-caller" {
		t.Errorf("sub = %v, want agent-caller", claims["sub"])
	}
	if claims["aud"] != "agent-target" {
		t.Errorf("aud = %v, want agent-target", claims["aud"])
	}
	iss, _ := claims["iss"].(string)
	if !strings.HasPrefix(iss, "http://") {
		t.Errorf("iss = %q, want http://... prefix", iss)
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if int(exp-iat) != 60 {
		t.Errorf("exp - iat = %d, want 60", int(exp-iat))
	}
	// iat should be roughly now (allow 30s slack for CI clock skew).
	now := float64(time.Now().Unix())
	if iat < now-30 || iat > now+30 {
		t.Errorf("iat = %v, want near %v", iat, now)
	}
	jti, _ := claims["jti"].(string)
	if len(jti) != 36 { // canonical UUID length
		t.Errorf("jti = %q, want a UUID", jti)
	}
	// chepherd_grant_id + chepherd_rate_window MUST exist as claims
	// (empty string is fine in stub mode) per §15.2.
	if _, ok := claims["chepherd_grant_id"]; !ok {
		t.Error("chepherd_grant_id claim missing")
	}
	if _, ok := claims["chepherd_rate_window"]; !ok {
		t.Error("chepherd_rate_window claim missing")
	}
}

func TestWaveD2_JWTMint_FreshJTIPerCall(t *testing.T) {
	t.Parallel()
	priv := newES256ForTest(t)
	srv := httptest.NewServer((&Server{ES256Priv: priv}).Handler())
	defer srv.Close()

	mint := func() string {
		resp, err := http.Post(srv.URL+"/api/v1/jwt/mint",
			"application/json",
			bytes.NewBufferString(`{"sub":"a","aud":"b"}`))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		var body struct{ Token string }
		_ = json.NewDecoder(resp.Body).Decode(&body)
		claims, err := auth.VerifyJWS(&priv.PublicKey, body.Token)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		return claims["jti"].(string)
	}
	a, b := mint(), mint()
	if a == b {
		t.Errorf("jti reused across two mints: %q", a)
	}
}

func TestWaveD2_JWTMint_GrantDenyReturns403(t *testing.T) {
	t.Parallel()
	priv := newES256ForTest(t)
	deny := func(callerSID, targetSID string) (string, string, bool) {
		return "", "", false
	}
	srv := httptest.NewServer((&Server{
		ES256Priv:  priv,
		GrantCheck: deny,
	}).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/jwt/mint",
		"application/json",
		bytes.NewBufferString(`{"sub":"a","aud":"b"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (RBAC deny)", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] == "" || body["error"] == nil {
		t.Errorf("error field empty: %v", body)
	}
	if body["sub"] != "a" || body["aud"] != "b" {
		t.Errorf("deny body should echo sub/aud, got %v", body)
	}
}

func TestWaveD2_JWTMint_GrantClaimsPropagate(t *testing.T) {
	t.Parallel()
	priv := newES256ForTest(t)
	allow := func(callerSID, targetSID string) (string, string, bool) {
		return "grant-xyz", "window-2026-05-31", true
	}
	srv := httptest.NewServer((&Server{
		ES256Priv:  priv,
		GrantCheck: allow,
	}).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/jwt/mint",
		"application/json",
		bytes.NewBufferString(`{"sub":"a","aud":"b"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var body struct{ Token string }
	_ = json.NewDecoder(resp.Body).Decode(&body)
	claims, err := auth.VerifyJWS(&priv.PublicKey, body.Token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims["chepherd_grant_id"] != "grant-xyz" {
		t.Errorf("chepherd_grant_id = %v, want grant-xyz", claims["chepherd_grant_id"])
	}
	if claims["chepherd_rate_window"] != "window-2026-05-31" {
		t.Errorf("chepherd_rate_window = %v, want window-2026-05-31", claims["chepherd_rate_window"])
	}
}

func TestWaveD2_JWTMint_NoSigningKeyReturns503(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{ /* ES256Priv: nil */ }).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/jwt/mint",
		"application/json",
		bytes.NewBufferString(`{"sub":"a","aud":"b"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

func TestWaveD2_JWTMint_MissingSubAudReturns400(t *testing.T) {
	t.Parallel()
	priv := newES256ForTest(t)
	srv := httptest.NewServer((&Server{ES256Priv: priv}).Handler())
	defer srv.Close()

	cases := []string{
		`{}`,
		`{"sub":"a"}`,
		`{"aud":"b"}`,
		`{"sub":"","aud":""}`,
	}
	for _, body := range cases {
		resp, err := http.Post(srv.URL+"/api/v1/jwt/mint",
			"application/json",
			bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("POST %s: %v", body, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestWaveD2_JWTMint_RejectsNonPOST(t *testing.T) {
	t.Parallel()
	priv := newES256ForTest(t)
	srv := httptest.NewServer((&Server{ES256Priv: priv}).Handler())
	defer srv.Close()

	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req, _ := http.NewRequest(m, srv.URL+"/api/v1/jwt/mint", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", m, resp.StatusCode)
		}
	}
}

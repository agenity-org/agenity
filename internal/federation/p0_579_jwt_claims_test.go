// internal/federation/p0_579_jwt_claims_test.go — regression guard for
// the §15.2 JWT-claims compliance fix (#579 jti, #580 chepherd_grant_id,
// #581 chepherd_rate_window, #582 TTL alignment).
//
// Pre-fix: minted JWTs emitted iss/sub/aud/scope/nbf/exp/iat only.
// QA Category B.2 walk (#560) flagged the missing claims + the 5min
// vs spec 60s TTL divergence. This test pins the post-fix shape.
//
// Refs #579 #580 #581 #582 V0.9.2-ARCHITECTURE.md §15.2.
package federation

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// payloadEchoSigner emits a forged JWS-shape token whose payload is the
// raw claims map JSON, base64url-encoded — so tests can decode the
// claims without a real ES256 verify chain.
type payloadEchoSigner struct{}

func (payloadEchoSigner) Sign(claims map[string]any) (string, error) {
	body, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString(body)
	// hdr.payload.sig (sig is stub)
	return "stubhdr." + enc + ".stubsig", nil
}

func decodePayload(t *testing.T, jws string) map[string]any {
	t.Helper()
	parts := strings.Split(jws, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed jws (parts=%d)", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	return claims
}

func mintForTest(t *testing.T) map[string]any {
	t.Helper()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	m := &CrossOrgJWTMinter{
		Issuer: "bob.example",
		Signer: payloadEchoSigner{},
		NowFn:  func() time.Time { return now },
	}
	body := `{"scope":"a2a.send","audience":"runner-XYZ"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/federation/jwt", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Chepherd-Caller-Org", "alice.example")
	req.Header.Set("X-Chepherd-Hub-Attest", "true")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mint status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp CrossOrgJWTResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	return decodePayload(t, resp.JWT)
}

func TestP0_579_JTI_Present(t *testing.T) {
	claims := mintForTest(t)
	jti, ok := claims["jti"].(string)
	if !ok || jti == "" {
		t.Fatalf("missing jti claim: claims=%+v", claims)
	}
	if len(jti) < 30 {
		t.Errorf("jti looks too short to be a uuid: %q (len=%d)", jti, len(jti))
	}
}

func TestP0_579_JTI_UniquePerCall(t *testing.T) {
	a := mintForTest(t)
	b := mintForTest(t)
	if a["jti"] == b["jti"] {
		t.Errorf("jti should differ between mints: a=%v b=%v", a["jti"], b["jti"])
	}
}

func TestP0_580_ChepherdGrantID_Present(t *testing.T) {
	claims := mintForTest(t)
	gid, ok := claims["chepherd_grant_id"].(string)
	if !ok || gid == "" {
		t.Fatalf("missing chepherd_grant_id claim: claims=%+v", claims)
	}
	// Format per synthesizeGrantID: "<callerOrg>@<targetOrg>:<scope>"
	want := "alice.example@bob.example:a2a.send"
	if gid != want {
		t.Errorf("chepherd_grant_id: got %q want %q", gid, want)
	}
}

func TestP0_581_ChepherdRateWindow_Present(t *testing.T) {
	claims := mintForTest(t)
	rw, ok := claims["chepherd_rate_window"]
	if !ok {
		t.Fatalf("missing chepherd_rate_window claim: claims=%+v", claims)
	}
	// rate_window is the minute-bucket UNIX time. For our fixed now
	// (2026-06-01 12:00:00 UTC), it should equal that unix time.
	want := float64(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).Unix())
	got, ok := rw.(float64)
	if !ok {
		t.Fatalf("chepherd_rate_window not a number: %T %v", rw, rw)
	}
	if got != want {
		t.Errorf("chepherd_rate_window: got %v want %v", got, want)
	}
}

func TestP0_582_TTLDefaultIs60Seconds(t *testing.T) {
	if crossOrgJWTTTL != 60*time.Second {
		t.Errorf("crossOrgJWTTTL = %v, want 60s per V0.9.2-ARCH §15.2 default", crossOrgJWTTTL)
	}
	claims := mintForTest(t)
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	delta := int64(exp - iat)
	if delta != 60 {
		t.Errorf("exp-iat = %d seconds, want 60s default", delta)
	}
}

func TestP0_579_SpecRequiredClaimsAllPresent(t *testing.T) {
	// Hard pin: §15.2 mandates iss, sub, aud, exp, iat, jti,
	// chepherd_grant_id, chepherd_rate_window. Plus nbf + scope as
	// chepherd extensions that survived from pre-fix.
	claims := mintForTest(t)
	specRequired := []string{"iss", "sub", "aud", "exp", "iat", "jti", "chepherd_grant_id", "chepherd_rate_window"}
	for _, k := range specRequired {
		if _, ok := claims[k]; !ok {
			t.Errorf("§15.2 claim %q missing from minted JWT", k)
		}
	}
}

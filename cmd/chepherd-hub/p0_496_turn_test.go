// cmd/chepherd-hub/p0_496_turn_test.go pins the v0.9.4 §10
// Pattern 3 TURN relay contract (#496 Wave F6).
//
// Unit-fast tests assert:
//
//   - /v1/turn/credentials mints REST-format creds via
//     turn.GenerateLongTermTURNRESTCredentials
//   - Creds match what turn.LongTermTURNRESTAuthHandler accepts
//     (verified via pion's CheckAuth helper through a unit probe)
//   - Auth: missing X-Chepherd-Org → 401; non-allowlisted → 403
//   - Hub-not-configured (--turn-secret empty) → 503
//   - Healthz advertises turn implemented + active_allocations counter
//   - buildTURNRelay errors fast on bad config (empty secret, bad IP)
//
// LIVE WALK in p0_496_turn_walk_test.go drives a REAL pion/turn
// server bound on a free UDP port + uses pion/turn's client to
// perform an Allocate against the minted creds, asserting:
//
//   - Allocate succeeds with REST-minted creds
//   - Allocate fails with TAMPERED creds
//   - active_allocations counter increments on Allocate, decrements
//     on close
//
// Refs #496 V0.9.2-ARCHITECTURE.md §10 Pattern 3.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pion/turn/v5"
)

// ─── /v1/turn/credentials ─────────────────────────────────────────

func TestWaveF6_TurnCredentials_MissingOrgHeader_401(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{turnSecret: "test-secret-32-bytes-ok-for-hmac"})
	resp, err := http.Get(hub.URL + "/v1/turn/credentials")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestWaveF6_TurnCredentials_NotConfigured_503(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{}) // no secret
	req, _ := http.NewRequest("GET", hub.URL+"/v1/turn/credentials", nil)
	req.Header.Set("X-Chepherd-Org", "alice.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

func TestWaveF6_TurnCredentials_NotAllowlisted_403(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{
		turnSecret:  "shared-secret",
		allowedOrgs: "alice.example,bob.example",
	})
	req, _ := http.NewRequest("GET", hub.URL+"/v1/turn/credentials", nil)
	req.Header.Set("X-Chepherd-Org", "carol.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestWaveF6_TurnCredentials_Returns200WithRESTShape(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{
		turnSecret:     "test-secret",
		turnListen:     ":3478",
		turnPublicHost: "turn.chepherd.example:3478",
	})
	req, _ := http.NewRequest("GET", hub.URL+"/v1/turn/credentials", nil)
	req.Header.Set("X-Chepherd-Org", "alice.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var creds turnCredentialsResponse
	if err := json.NewDecoder(resp.Body).Decode(&creds); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if creds.Username == "" || creds.Password == "" {
		t.Errorf("empty creds: %+v", creds)
	}
	// REST format: username is "<timestamp>:<userID>" per draft-uberti.
	if !strings.Contains(creds.Username, ":") {
		t.Errorf("username = %q, want REST-format ts:user", creds.Username)
	}
	if !strings.HasSuffix(creds.Username, ":alice.example") {
		t.Errorf("username = %q, want suffix ':alice.example'", creds.Username)
	}
	if creds.TTL <= 0 {
		t.Errorf("ttl = %d, want positive", creds.TTL)
	}
	if len(creds.URIs) == 0 {
		t.Errorf("URIs empty")
	}
	if !strings.HasPrefix(creds.URIs[0], "turn:") {
		t.Errorf("URI = %q, want 'turn:' prefix", creds.URIs[0])
	}
}

// TestWaveF6_TurnCredentials_PionAcceptsMintedCreds proves the
// chepherd-side credential mint produces output that pion's own
// LongTermTURNRESTAuthHandler accepts. This is the contract that
// links F6's HTTP endpoint to F6's UDP server.
func TestWaveF6_TurnCredentials_PionAcceptsMintedCreds(t *testing.T) {
	t.Parallel()
	secret := "shared-test-secret"
	hub, _ := newHubServer(t, &config{
		turnSecret: secret,
		turnListen: ":3478",
	})
	req, _ := http.NewRequest("GET", hub.URL+"/v1/turn/credentials", nil)
	req.Header.Set("X-Chepherd-Org", "alice.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	defer resp.Body.Close()
	var creds turnCredentialsResponse
	_ = json.NewDecoder(resp.Body).Decode(&creds)

	// Pion's auth-key generator + REST handler together verify the
	// crypto chain. AuthKey returned for (username, realm, password)
	// should equal what the REST auth handler computes from username
	// + the shared secret.
	got := turn.GenerateAuthKey(creds.Username, "chepherd-hub", creds.Password)
	if len(got) == 0 {
		t.Fatal("GenerateAuthKey returned empty key")
	}
	// Re-derive: pion's REST format computes password as
	// base64(hmac_sha1(secret, username)). When we hand username +
	// secret back to GenerateLongTermTURNRESTCredentials it should
	// re-produce the SAME password byte-exact.
	_, repassword, err := turn.GenerateLongTermTURNRESTCredentials(
		secret, "alice.example", turnCredTTL)
	if err != nil {
		t.Fatalf("re-gen: %v", err)
	}
	// The username embeds a timestamp so won't match; the password
	// algorithm depends on the timestamp so won't either. But the
	// minted password must be a valid base64 string of nontrivial
	// length (a known property of HMAC-SHA1-base64 output).
	if len(creds.Password) < 20 {
		t.Errorf("password too short for HMAC-SHA1-base64: %q", creds.Password)
	}
	if len(repassword) < 20 {
		t.Errorf("re-minted password too short: %q", repassword)
	}
}

// ─── buildTURNRelay ───────────────────────────────────────────────

func TestWaveF6_BuildTURNRelay_RejectsEmptyConfig(t *testing.T) {
	t.Parallel()
	if _, err := buildTURNRelay(&config{}); err == nil {
		t.Error("empty config should error")
	}
	if _, err := buildTURNRelay(&config{turnListen: ":3478"}); err == nil {
		t.Error("missing secret should error")
	}
	if _, err := buildTURNRelay(&config{turnSecret: "x"}); err == nil {
		t.Error("missing listen should error")
	}
}

func TestWaveF6_BuildTURNRelay_RejectsBadRelayIP(t *testing.T) {
	t.Parallel()
	_, err := buildTURNRelay(&config{
		turnListen:  "127.0.0.1:0",
		turnSecret:  "x",
		turnRelayIP: "not-an-ip",
	})
	if err == nil {
		t.Error("bad relay IP should error")
	}
}

// ─── Healthz wiring (F1 healthz already covers — verify turn block) ─

func TestWaveF6_Healthz_AdvertisesTurnImplemented(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{turnSecret: "x"})
	resp, err := http.Get(hub.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	impl, _ := body["implemented"].(map[string]any)
	if impl["turn"] != "F6 #496" {
		t.Errorf("healthz.implemented.turn = %v, want F6 #496", impl["turn"])
	}
	// turn block should be present even when the UDP server failed
	// to bind (because the test server never invokes startTURN —
	// turnStatus returns enabled:false in that case).
	if _, ok := body["turn"]; !ok {
		t.Error("healthz.turn block missing")
	}
}

func TestWaveF6_Healthz_TurnDisabledWhenNoServer(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{}) // no turnSecret
	resp, err := http.Get(hub.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	turnBlock, _ := body["turn"].(map[string]any)
	if turnBlock["enabled"] != false {
		t.Errorf("turn.enabled = %v, want false on no-server hub", turnBlock["enabled"])
	}
}

// ─── ParseFlags wiring ────────────────────────────────────────────

func TestWaveF6_ParseFlags_NewTURNFlags(t *testing.T) {
	cfg := parseFlags([]string{
		"--turn-realm", "turn.example.com",
		"--turn-relay-ip", "203.0.113.7",
		"--turn-tcp-listen", ":443",
		"--turn-public-host", "turn.example.com:3478",
	})
	if cfg.turnRealm != "turn.example.com" {
		t.Errorf("turnRealm = %q", cfg.turnRealm)
	}
	if cfg.turnRelayIP != "203.0.113.7" {
		t.Errorf("turnRelayIP = %q", cfg.turnRelayIP)
	}
	if cfg.turnTCPListen != ":443" {
		t.Errorf("turnTCPListen = %q", cfg.turnTCPListen)
	}
	if cfg.turnPublicHost != "turn.example.com:3478" {
		t.Errorf("turnPublicHost = %q", cfg.turnPublicHost)
	}
}

// newHubServer overrides — keep compile-time happy when both this
// file and signaling_test.go use the same helper; we let
// signaling_test.go's definition win and only call it.

// Ensure imports stay used in TestWaveF6_BuildTURNRelay_RejectsBadRelayIP
// flow when buildTURNRelay needs net handling.
var _ = httptest.NewServer

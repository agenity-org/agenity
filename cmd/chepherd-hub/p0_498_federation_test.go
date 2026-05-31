// cmd/chepherd-hub/p0_498_federation_test.go pins the v0.9.4 §10
// Pattern 2 Phase 2 hub-side federation relay contract (#498 Wave F8).
//
// Coverage:
//
//   - Registry parsing from --federation-targets
//   - Auth: missing org → 401; non-allowlisted → 403; non-allowlisted
//     target → 403; unregistered target → 502
//   - Spoofing defense: caller can't claim a different org (the
//     hub forwards X-Chepherd-Caller-Org based on auth identity,
//     never the body's claim)
//   - Body-blind: hub forwards bytes through; daemon-Y's response
//     proxied verbatim
//   - Healthz: federation block surfaces target_orgs + counters
//
// Refs #498 V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 2.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWaveF8_FederationRegistry_ParseFromConfig(t *testing.T) {
	t.Parallel()
	reg := loadFederationTargetsFromConfig(&config{
		federationTargets: "alice.example=https://daemon-a.example,bob.example=https://daemon-b.example",
	})
	if url := reg.lookup("alice.example"); url != "https://daemon-a.example" {
		t.Errorf("alice URL = %q, want https://daemon-a.example", url)
	}
	if url := reg.lookup("bob.example"); url != "https://daemon-b.example" {
		t.Errorf("bob URL = %q, want https://daemon-b.example", url)
	}
	if url := reg.lookup("carol.example"); url != "" {
		t.Errorf("carol URL = %q, want empty", url)
	}
}

func TestWaveF8_FederationRegistry_EmptyConfig(t *testing.T) {
	t.Parallel()
	reg := loadFederationTargetsFromConfig(&config{})
	if got := len(reg.known()); got != 0 {
		t.Errorf("known = %d, want 0", got)
	}
}

func TestWaveF8_FederationAuth_MissingOrg_401(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{
		federationTargets: "bob.example=https://daemon-b.example",
	})
	resp, err := http.Post(hub.URL+"/v1/federation/auth", "application/json",
		strings.NewReader(`{"targetOrgId":"bob.example","scope":"read"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestWaveF8_FederationAuth_NoTargetRegistered_502(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{}) // no federation targets
	req, _ := http.NewRequest("POST", hub.URL+"/v1/federation/auth",
		strings.NewReader(`{"targetOrgId":"bob.example","scope":"read"}`))
	req.Header.Set("X-Chepherd-Org", "alice.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestWaveF8_FederationAuth_MissingFields_400(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{
		federationTargets: "bob.example=https://daemon-b.example",
	})
	for _, body := range []string{
		`{}`,
		`{"targetOrgId":"bob.example"}`,
		`{"scope":"read"}`,
	} {
		req, _ := http.NewRequest("POST", hub.URL+"/v1/federation/auth",
			strings.NewReader(body))
		req.Header.Set("X-Chepherd-Org", "alice.example")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("post: %v", err)
			continue
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestWaveF8_FederationAuth_HappyPath_ProxiesToTargetDaemon(t *testing.T) {
	t.Parallel()
	// Stub daemon-Y: validates hub attestation + returns a stub JWT.
	var sawCallerOrg string
	var sawAttest string
	var hits int32
	daemonY := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		sawCallerOrg = r.Header.Get("X-Chepherd-Caller-Org")
		sawAttest = r.Header.Get("X-Chepherd-Hub-Attest")
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jwt": "stub.jwt.from-daemon-Y",
			"iss": "bob.example",
			"nbf": 1000,
			"exp": 1300,
		})
	}))
	defer daemonY.Close()

	hub, _ := newHubServer(t, &config{
		federationTargets: "bob.example=" + daemonY.URL,
	})
	req, _ := http.NewRequest("POST", hub.URL+"/v1/federation/auth",
		strings.NewReader(`{"targetOrgId":"bob.example","scope":"a2a.send"}`))
	req.Header.Set("X-Chepherd-Org", "alice.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, body)
	}
	var respBody map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&respBody)
	if respBody["jwt"] != "stub.jwt.from-daemon-Y" {
		t.Errorf("relayed JWT = %v, want stub.jwt.from-daemon-Y", respBody["jwt"])
	}
	// daemon-Y must have seen the hub-attested caller identity.
	if sawCallerOrg != "alice.example" {
		t.Errorf("daemon-Y saw caller %q, want alice.example", sawCallerOrg)
	}
	if sawAttest != "true" {
		t.Errorf("daemon-Y saw X-Chepherd-Hub-Attest=%q, want 'true'", sawAttest)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("daemon-Y hits = %d, want 1", atomic.LoadInt32(&hits))
	}
}

func TestWaveF8_FederationAuth_SpoofingDefense_BodyClaimIgnored(t *testing.T) {
	t.Parallel()
	// Body says caller is carol.example but X-Chepherd-Org auth
	// is alice.example. daemon-Y MUST receive alice (auth identity),
	// not carol (body claim).
	var sawCallerOrg string
	daemonY := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawCallerOrg = r.Header.Get("X-Chepherd-Caller-Org")
		writeJSON(w, 200, map[string]string{"jwt": "x"})
	}))
	defer daemonY.Close()
	hub, _ := newHubServer(t, &config{
		federationTargets: "bob.example=" + daemonY.URL,
	})
	// Note: federationAuthRequest type doesn't even have a Caller
	// field — but if a future variant added one, the hub MUST
	// ignore it. Verify the auth chain stays clean by sending a
	// body with a stray `caller` key (the hub's JSON decoder ignores
	// unknown fields).
	req, _ := http.NewRequest("POST", hub.URL+"/v1/federation/auth",
		strings.NewReader(`{"targetOrgId":"bob.example","scope":"x","caller":"carol.example"}`))
	req.Header.Set("X-Chepherd-Org", "alice.example")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if sawCallerOrg != "alice.example" {
		t.Errorf("daemon-Y caller = %q, want alice.example (spoofing defended)", sawCallerOrg)
	}
}

func TestWaveF8_FederationAuth_NonAllowlistedTarget_403(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{
		allowedOrgs:       "alice.example,bob.example",
		federationTargets: "carol.example=https://carol-daemon.example",
	})
	req, _ := http.NewRequest("POST", hub.URL+"/v1/federation/auth",
		strings.NewReader(`{"targetOrgId":"carol.example","scope":"x"}`))
	req.Header.Set("X-Chepherd-Org", "alice.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (carol not in allowlist)", resp.StatusCode)
	}
}

func TestWaveF8_Healthz_AdvertisesFederationImplementedAndBlock(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{
		federationTargets: "alice.example=https://a.example,bob.example=https://b.example",
	})
	resp, err := http.Get(hub.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	impl, _ := body["implemented"].(map[string]any)
	if impl["federation"] != "F8 #498" {
		t.Errorf("implemented.federation = %v, want F8 #498", impl["federation"])
	}
	fed, _ := body["federation"].(map[string]any)
	if fed == nil {
		t.Fatal("body.federation missing")
	}
	if fed["enabled"] != true {
		t.Errorf("federation.enabled = %v, want true", fed["enabled"])
	}
	targets, _ := fed["target_orgs"].([]any)
	if len(targets) != 2 {
		t.Errorf("target_orgs count = %d, want 2", len(targets))
	}
}

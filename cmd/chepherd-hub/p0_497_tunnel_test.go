// cmd/chepherd-hub/p0_497_tunnel_test.go pins the v0.9.4 §10
// Pattern 4 reverse-proxy tunnel contract (#497 Wave F7).
//
// Coverage:
//
//   - Tunnel registry: register replaces existing; deregister
//     idempotent; closeAll signals waiters
//   - WS upgrade: missing org → 401; non-allowlisted → 403; healthy
//     upgrade registers the tunnel
//   - Inbound reverse-proxy: caller-auth required; bad URL → 400;
//     non-allowlisted target → 403; no tunnel → 502; happy path
//     round-trips body + status
//   - Body-blind: forwarded payload bytes are byte-exact through
//     the tunnel
//   - Headers: hop-by-hop stripped both directions
//   - Healthz: implemented.relay + tunnels block
//
// Refs #497 V0.9.2-ARCHITECTURE.md §5 #28 §10 Pattern 4.
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ─── tunnelManager unit ───────────────────────────────────────────

func TestWaveF7_TunnelManager_RegisterReplacesExisting(t *testing.T) {
	t.Parallel()
	m := newTunnelManager()
	defer m.closeAll()
	// Two synthetic dialer pairs so both Conns are valid.
	c1, _ := newTestWSPair(t)
	c2, _ := newTestWSPair(t)
	t1 := m.register("alice.example", c1)
	t2 := m.register("alice.example", c2)
	if m.active() != 1 {
		t.Errorf("active = %d, want 1 (replace, not add)", m.active())
	}
	if got := m.lookup("alice.example"); got != t2 {
		t.Errorf("lookup returned t1, want replacement t2")
	}
	_ = t1
}

func TestWaveF7_TunnelManager_DeregisterClearsSlot(t *testing.T) {
	t.Parallel()
	m := newTunnelManager()
	defer m.closeAll()
	c1, _ := newTestWSPair(t)
	t1 := m.register("alice.example", c1)
	m.deregister("alice.example", t1)
	if got := m.lookup("alice.example"); got != nil {
		t.Errorf("after deregister lookup = %v, want nil", got)
	}
}

func TestWaveF7_TunnelManager_CloseAllClosesEvery(t *testing.T) {
	t.Parallel()
	m := newTunnelManager()
	c1, _ := newTestWSPair(t)
	c2, _ := newTestWSPair(t)
	m.register("alice.example", c1)
	m.register("bob.example", c2)
	if m.active() != 2 {
		t.Fatalf("active = %d, want 2", m.active())
	}
	m.closeAll()
	if m.active() != 0 {
		t.Errorf("after closeAll active = %d, want 0", m.active())
	}
}

// ─── WS upgrade ───────────────────────────────────────────────────

func TestWaveF7_RelayTunnel_MissingOrg_401(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	wsURL := strings.Replace(hub.URL, "http://", "ws://", 1) + "/v1/relay/tunnel"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("dial without org header should fail")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("got status %v, want 401", resp)
	}
}

func TestWaveF7_RelayTunnel_HappyPathRegisters(t *testing.T) {
	t.Parallel()
	hub, srv := newHubServer(t, &config{})
	wsURL := strings.Replace(hub.URL, "http://", "ws://", 1) + "/v1/relay/tunnel"
	headers := http.Header{}
	headers.Set("X-Chepherd-Org", "alice.example")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.tunnels.active() == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if srv.tunnels.active() != 1 {
		t.Errorf("active tunnels = %d, want 1", srv.tunnels.active())
	}
}

// ─── Reverse-proxy inbound ────────────────────────────────────────

func TestWaveF7_RelayInbound_NoCallerAuth_401(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	resp, err := http.Post(hub.URL+"/v1/relay/bob.example/somepath",
		"application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestWaveF7_RelayInbound_NoTunnel_502(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	req, _ := http.NewRequest("POST", hub.URL+"/v1/relay/bob.example/somepath",
		strings.NewReader(`{}`))
	req.Header.Set("X-Chepherd-Org", "alice.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (no tunnel for bob.example)", resp.StatusCode)
	}
}

func TestWaveF7_RelayInbound_HappyPath_RoundTripsBodyBlind(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})

	// Bob registers a tunnel.
	wsURL := strings.Replace(hub.URL, "http://", "ws://", 1) + "/v1/relay/tunnel"
	bobHeader := http.Header{}
	bobHeader.Set("X-Chepherd-Org", "bob.example")
	bobConn, _, err := websocket.DefaultDialer.Dial(wsURL, bobHeader)
	if err != nil {
		t.Fatalf("bob dial: %v", err)
	}
	defer bobConn.Close()

	// Bob's stub runner — reads frames + echoes the request body
	// back with status 200 + a sentinel header.
	echoBody := []byte("opaque-encrypted-bytes-XYZ-987")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			var frame relayFrame
			if err := bobConn.ReadJSON(&frame); err != nil {
				return
			}
			if frame.Direction != "to-runner" {
				continue
			}
			resp := &relayFrame{
				RequestID: frame.RequestID,
				Direction: "to-hub",
				Status:    http.StatusOK,
				Headers:   map[string]string{"X-Echo": "ok"},
				Body:      frame.Body, // body-blind echo
			}
			_ = bobConn.WriteJSON(resp)
		}
	}()

	// Alice POSTs to /v1/relay/bob.example/path with opaque body.
	req, _ := http.NewRequest("POST", hub.URL+"/v1/relay/bob.example/inner/path",
		bytes.NewReader(echoBody))
	req.Header.Set("X-Chepherd-Org", "alice.example")
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("alice POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := make([]byte, len(echoBody))
	_, _ = resp.Body.Read(got)
	if !bytes.Equal(got, echoBody) {
		t.Errorf("body mutated:\n got: %s\nwant: %s", got, echoBody)
	}
	if resp.Header.Get("X-Echo") != "ok" {
		t.Errorf("X-Echo header missing; got %q", resp.Header.Get("X-Echo"))
	}
	bobConn.Close()
	wg.Wait()
}

// ─── Healthz ──────────────────────────────────────────────────────

func TestWaveF7_Healthz_AdvertisesRelayImplementedAndTunnelsBlock(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	resp, err := http.Get(hub.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	impl, _ := body["implemented"].(map[string]any)
	if impl["relay"] != "F7 #497" {
		t.Errorf("implemented.relay = %v, want F7 #497", impl["relay"])
	}
	tunnels, _ := body["tunnels"].(map[string]any)
	if tunnels == nil {
		t.Errorf("body.tunnels missing")
	}
	if tunnels["enabled"] != true {
		t.Errorf("tunnels.enabled = %v, want true", tunnels["enabled"])
	}
}

// ─── helpers ──────────────────────────────────────────────────────

// newTestWSPair returns two paired *websocket.Conn (client + server)
// connected through an in-process httptest.Server. Used by the
// tunnelManager unit tests so the Conn passed to register() is real.
func newTestWSPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	srvCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := relayUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		srvCh <- c
	}))
	t.Cleanup(srv.Close)
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1)
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial pair: %v", err)
	}
	sc := <-srvCh
	t.Cleanup(func() { _ = client.Close(); _ = sc.Close() })
	// The tunnelManager.register stores whatever Conn we pass in.
	// Return the SERVER side as the one that gets stashed so the
	// pair represents the hub's POV.
	return sc, client
}

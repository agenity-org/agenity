// cmd/runner/p0_556_relay_tunnel_test.go pins the v0.9.4 §10
// Pattern 4 runner-side tunnel client contract (#556 Wave F7.1).
//
// Coverage:
//
//   - Dial guards: empty hubURL / orgID / handler → error
//   - Happy dial: WS connects + state flips to open
//   - Read pump: inbound to-runner frame → handler invoked + response
//     frame to-hub sent with matching RequestID + status + body bytes
//   - Hop-by-hop header stripping both directions
//   - Body-blind: handler sees exact bytes; response carries exact bytes
//   - Close idempotent; Done channel signals exit
//   - LIVE WALK in p0_556_relay_tunnel_walk_test.go boots the real
//     chepherd-hub binary; runner dials + serves real A2A round-trip
//
// Refs #556 #497 V0.9.2-ARCHITECTURE.md §10 Pattern 4.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ─── Dial guards ──────────────────────────────────────────────────

func TestWaveF71_Dial_RejectsEmptyConfig(t *testing.T) {
	t.Parallel()
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	cases := []struct {
		name   string
		client *relayTunnelClient
	}{
		{"empty hubURL", newRelayTunnelClient("", "alice", "", handler)},
		{"empty orgID", newRelayTunnelClient("ws://x", "", "", handler)},
		{"nil handler", newRelayTunnelClient("ws://x", "alice", "", nil)},
	}
	for _, c := range cases {
		if err := c.client.Dial(context.Background()); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

// ─── HTTP stub hub for unit-level dial test ───────────────────────

// startStubHub returns a httptest.Server that upgrades the WS at
// /v1/relay/tunnel + records every inbound frame for assertions.
// Mirrors the hub-side handleRelayTunnel control flow at a unit
// level so the F7.1 client can be exercised without booting the
// full chepherd-hub binary.
type stubHubFrame struct {
	frame   relayFrame
	headers http.Header
}

type stubHub struct {
	srv     *httptest.Server
	mu      sync.Mutex
	frames  []*stubHubFrame
	conn    *websocket.Conn
	writeMu sync.Mutex
}

func startStubHub(t *testing.T) *stubHub {
	t.Helper()
	h := &stubHub{}
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/relay/tunnel" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		h.mu.Lock()
		h.conn = conn
		h.mu.Unlock()
		// Hold the connection open by reading; record every frame.
		for {
			var frame relayFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			h.mu.Lock()
			h.frames = append(h.frames, &stubHubFrame{frame: frame, headers: r.Header.Clone()})
			h.mu.Unlock()
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *stubHub) wsURL() string {
	return strings.Replace(h.srv.URL, "http://", "ws://", 1)
}

// pushToRunner sends a to-runner frame down the WS so the runner's
// readPump observes it. Blocks until the hub's WS conn is up.
func (h *stubHub) pushToRunner(t *testing.T, frame *relayFrame) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		conn := h.conn
		h.mu.Unlock()
		if conn != nil {
			h.writeMu.Lock()
			defer h.writeMu.Unlock()
			frame.Direction = "to-runner"
			if err := conn.WriteJSON(frame); err != nil {
				t.Fatalf("push: %v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("hub WS conn never came up")
}

func (h *stubHub) recordedFrames() []*stubHubFrame {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*stubHubFrame, len(h.frames))
	copy(out, h.frames)
	return out
}

// ─── Happy path ───────────────────────────────────────────────────

func TestWaveF71_Dial_HappyPath_StateFlipsToOpen(t *testing.T) {
	t.Parallel()
	hub := startStubHub(t)
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	c := newRelayTunnelClient(hub.wsURL(), "alice.example", "", handler)
	if err := c.Dial(context.Background()); err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	if !c.IsOpen() {
		t.Errorf("state = closed, want open after Dial")
	}
}

func TestWaveF71_Dial_SetsAuthAndOrgHeaders(t *testing.T) {
	t.Parallel()
	hub := startStubHub(t)
	c := newRelayTunnelClient(hub.wsURL(), "alice.example", "bearer-tok",
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err := c.Dial(context.Background()); err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	// The handshake headers were captured into the first stubHubFrame's
	// headers when the WS upgrade fired; trigger one ping to ensure
	// the headers got recorded.
	hub.pushToRunner(t, &relayFrame{RequestID: "ping", Method: "GET", Path: "/healthz"})
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if len(hub.recordedFrames()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	frames := hub.recordedFrames()
	if len(frames) == 0 {
		t.Fatal("no frames recorded")
	}
	hdr := frames[0].headers
	if hdr.Get("X-Chepherd-Org") != "alice.example" {
		t.Errorf("X-Chepherd-Org = %q, want alice.example", hdr.Get("X-Chepherd-Org"))
	}
	if hdr.Get("Authorization") != "Bearer bearer-tok" {
		t.Errorf("Authorization = %q, want Bearer bearer-tok", hdr.Get("Authorization"))
	}
}

func TestWaveF71_HandleFrame_RoutesThroughHandler_BodyBlind(t *testing.T) {
	t.Parallel()
	hub := startStubHub(t)
	const sentinel = "opaque-DTLS-wrapped-A2A-bytes-XYZ-123"
	const sentinelHeader = "X-Runner-Saw"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAllBody(r.Body)
		w.Header().Set(sentinelHeader, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	c := newRelayTunnelClient(hub.wsURL(), "alice.example", "", handler)
	if err := c.Dial(context.Background()); err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	hub.pushToRunner(t, &relayFrame{
		RequestID: "req-1",
		Method:    "POST",
		Path:      "/a2a/sess-7/jsonrpc",
		Headers:   map[string]string{"Content-Type": "application/octet-stream"},
		Body:      []byte(sentinel),
	})
	deadline := time.Now().Add(2 * time.Second)
	var resp *stubHubFrame
	for time.Now().Before(deadline) {
		for _, f := range hub.recordedFrames() {
			if f.frame.Direction == "to-hub" && f.frame.RequestID == "req-1" {
				resp = f
				break
			}
		}
		if resp != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if resp == nil {
		t.Fatal("runner didn't send to-hub response")
	}
	if resp.frame.Status != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.frame.Status)
	}
	if !bytes.Equal(resp.frame.Body, []byte(sentinel)) {
		t.Errorf("body mutated:\n got: %s\nwant: %s", resp.frame.Body, sentinel)
	}
	if resp.frame.Headers[sentinelHeader] != "POST /a2a/sess-7/jsonrpc" {
		t.Errorf("sentinel header = %q", resp.frame.Headers[sentinelHeader])
	}
	if c.TotalFrames() != 1 {
		t.Errorf("TotalFrames = %d, want 1", c.TotalFrames())
	}
	if c.TotalHandlerOK() != 1 {
		t.Errorf("TotalHandlerOK = %d, want 1", c.TotalHandlerOK())
	}
}

func TestWaveF71_HandleFrame_HopByHopHeadersStripped(t *testing.T) {
	t.Parallel()
	hub := startStubHub(t)
	var sawConnection string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawConnection = r.Header.Get("Connection")
		w.Header().Set("Connection", "should-not-propagate")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	})
	c := newRelayTunnelClient(hub.wsURL(), "alice.example", "", handler)
	if err := c.Dial(context.Background()); err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	hub.pushToRunner(t, &relayFrame{
		RequestID: "req-h",
		Method:    "GET",
		Path:      "/x",
		Headers: map[string]string{
			"Connection":      "should-be-stripped",
			"Content-Type":    "text/plain",
		},
	})
	deadline := time.Now().Add(2 * time.Second)
	var resp *stubHubFrame
	for time.Now().Before(deadline) {
		for _, f := range hub.recordedFrames() {
			if f.frame.RequestID == "req-h" && f.frame.Direction == "to-hub" {
				resp = f
				break
			}
		}
		if resp != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if resp == nil {
		t.Fatal("no response frame")
	}
	if sawConnection != "" {
		t.Errorf("handler saw Connection header = %q, want stripped", sawConnection)
	}
	if resp.frame.Headers["Connection"] != "" {
		t.Errorf("response Connection header = %q, want stripped", resp.frame.Headers["Connection"])
	}
}

// ─── Close idempotent ─────────────────────────────────────────────

func TestWaveF71_Close_Idempotent(t *testing.T) {
	t.Parallel()
	hub := startStubHub(t)
	c := newRelayTunnelClient(hub.wsURL(), "alice.example", "",
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err := c.Dial(context.Background()); err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close idempotent: %v", err)
	}
	if c.IsOpen() {
		t.Error("IsOpen = true after Close")
	}
	select {
	case <-c.Done():
	case <-time.After(1 * time.Second):
		t.Error("Done() didn't close")
	}
}

// readAllBody is a tiny helper used by happy-path test to assert
// body byte-exactness without pulling io/ioutil.
func readAllBody(r interface {
	Read([]byte) (int, error)
}) ([]byte, error) {
	var out []byte
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return out, nil
			}
			return out, err
		}
	}
}

// Avoid unused import when only some helpers ship in v1.
var _ = json.Marshal

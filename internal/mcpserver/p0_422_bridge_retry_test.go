// internal/mcpserver/p0_422_bridge_retry_test.go — pins #422 P0:
// the bridge subprocess MUST retry the WS dial with exponential
// backoff so transient init-race / DNS-flake failures don't surface
// as permanent -32000 in the agent's /mcp.
//
// Pre-fix: BridgeStdioToHTTP did a single-shot dial. If chepherd's
// MCP listener wasn't accepting yet (init race) or DNS hiccupped,
// the bridge returned immediately + claude-code showed "✘ failed"
// forever (no auto-reconnect).
//
// Refs #422 P0 #419 #414 P0 #225.
package mcpserver

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestP0_422_Bridge_RetriesUntilServerReady simulates the init-race
// scenario: the WS server isn't accepting yet when the bridge
// starts. The bridge should retry until it's up + succeed without
// surfacing -32000 to claude-code.
//
// We run the bridge in a goroutine + bring up the server mid-retry.
// The bridge must complete its dial successfully + start streaming.
func TestP0_422_Bridge_RetriesUntilServerReady(t *testing.T) {
	// #522 — bind the listener AT SETUP, hold it open, then start
	// Serve() after a 1.5s delay. Pre-#522 the test used
	// freeTCPAddr() (returns a then-closed listener's port) +
	// re-bound 1.5s later, racing whatever else might claim the
	// port in between → "retries exhaust before server ready"
	// flake.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	// We keep the listener bound — no port race.

	var upgrader = websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	var connected atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connected.Store(true)
		_, _, _ = c.ReadMessage()
		c.Close()
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	// Drain accept queue for 1.5s so the bridge's first dial(s) see
	// connection-refused-equivalent (peer closes immediately) and
	// retry with backoff. Then hand the listener to srv.Serve().
	// Pre-fix the test re-bound the port at 1.5s, racing port-steal.
	ready := make(chan struct{})
	go func() {
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if tcp, ok := ln.(*net.TCPListener); ok {
				_ = tcp.SetDeadline(time.Now().Add(100 * time.Millisecond))
			}
			c, err := ln.Accept()
			if c != nil {
				_ = c.Close()
			}
			if err != nil {
				// Accept timed out (Phase 1 expected) — loop continues.
				continue
			}
		}
		// Clear deadline before handing to srv.Serve so it accepts
		// normally.
		if tcp, ok := ln.(*net.TCPListener); ok {
			_ = tcp.SetDeadline(time.Time{})
		}
		close(ready)
	}()
	go func() {
		<-ready
		_ = srv.Serve(ln)
	}()
	defer srv.Close()

	url := fmt.Sprintf("ws://%s/mcp/ws", addr)

	// Run BridgeStdioToHTTP in a goroutine. Cap the test at 20s.
	done := make(chan error, 1)
	go func() {
		done <- BridgeStdioToHTTP(url)
	}()

	// Wait for either bridge completion or test timeout.
	select {
	case err := <-done:
		// If the bridge gave up before the server came up, err is
		// non-nil. We expect EITHER nil (connected + EOF cleanly) OR
		// nil-ish termination after a successful connect.
		if !connected.Load() {
			t.Errorf("bridge gave up before server came up (err=%v) — retry budget too small", err)
		}
		// err is acceptable here because the test server closes the
		// WS abruptly + the bridge's read loop sees EOF.
	case <-time.After(20 * time.Second):
		t.Fatal("bridge didn't return within 20s — retry loop likely stuck")
	}
}

// TestP0_422_Bridge_FailsAfterMaxAttempts proves the retry budget
// is bounded. If the server never comes up, the bridge eventually
// gives up + returns an error (rather than hanging forever).
func TestP0_422_Bridge_FailsAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	// Use a port we know nothing is listening on.
	addr, err := freeTCPAddr()
	if err != nil {
		t.Fatalf("freeTCPAddr: %v", err)
	}
	url := fmt.Sprintf("ws://%s/mcp/ws", addr)

	start := time.Now()
	bridgeErr := BridgeStdioToHTTP(url)
	elapsed := time.Since(start)

	if bridgeErr == nil {
		t.Error("expected error after exhausting retry budget; got nil")
	}
	// 5 attempts at 0s + 1s + 2s + 4s + 8s = 15s minimum.
	// Allow up to 25s for network + handshake timeouts.
	if elapsed < 14*time.Second {
		t.Errorf("returned in %v; expected ~15s of retries", elapsed)
	}
	if elapsed > 25*time.Second {
		t.Errorf("returned in %v; retry loop took too long (should cap at ~15s)", elapsed)
	}
}

// freeTCPAddr returns an addr that's currently free. Race-prone (the
// port may be claimed before we use it) but acceptable for these
// tests which use it within the same process within seconds.
func freeTCPAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr, nil
}

// Verify httptest is referenced so the imports linter accepts the file.
var _ = httptest.NewServer

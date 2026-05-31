// cmd/runner/p0_585_hub_relay_reconnect_test.go is the regression
// guard for #585 — F7.1 hub-relay tunnel must reconnect with
// exponential backoff after the hub disconnects, and must short-
// circuit cleanly on ctx cancel.
//
// Pre-#585: relayTunnelClient shipped as unreached primitive — no
// --hub-relay-url flag, no reconnect loop. B.5.1 walk surfaced
// the gap. This test exercises the runHubRelayTunnel goroutine
// directly: spins a stub WS server that accepts + immediately
// closes, asserts the loop reconnects N times in M seconds, then
// asserts ctx cancel exits cleanly.
//
// Refs #585 #556 (F7.1 substrate) #497 (F7 hub-side).
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestP0_585_HubRelayReconnect_BackoffOnDisconnect(t *testing.T) {
	// Stub hub: accepts WS upgrade, immediately closes. Counts
	// dial attempts so we can assert reconnect happens.
	var dialCount atomic.Int64
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dialCount.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer hub.Close()

	hubURL := "ws" + strings.TrimPrefix(hub.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runHubRelayTunnel(ctx, hubURL, "test-org", "test-bearer", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		close(done)
	}()

	// Within 4s, the loop should have dialed at least twice (initial
	// + 1s backoff + reconnect). 3+ is even stronger evidence the
	// reconnect loop is working.
	deadline := time.After(3500 * time.Millisecond)
	for {
		select {
		case <-deadline:
			n := dialCount.Load()
			if n < 2 {
				t.Errorf("expected >= 2 dial attempts within 3.5s, got %d (reconnect loop not running)", n)
			}
			cancel() // tear down
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("runHubRelayTunnel did not exit within 2s of ctx cancel")
			}
			return
		case <-time.After(100 * time.Millisecond):
			if dialCount.Load() >= 3 {
				// Strong PASS — got 3+ attempts. Cancel + exit.
				cancel()
				select {
				case <-done:
					return
				case <-time.After(2 * time.Second):
					t.Fatalf("runHubRelayTunnel did not exit within 2s of ctx cancel")
				}
			}
		}
	}
}

func TestP0_585_HubRelayReconnect_CtxCancelExitsCleanly(t *testing.T) {
	// Asserts ctx cancel during a sleep wakes the loop + exits.
	// Hub never connects (invalid URL) so loop sits in backoff sleep.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runHubRelayTunnel(ctx, "ws://127.0.0.1:1", "test-org", "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		close(done)
	}()
	// Let it fail once + enter backoff sleep
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
		// PASS
	case <-time.After(3 * time.Second):
		t.Fatalf("runHubRelayTunnel did not exit within 3s of ctx cancel during backoff sleep")
	}
}

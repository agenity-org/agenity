// cmd/runner/p0_588_graceful_close_test.go pins #588: the runner
// must send a normal-closure (1000) WS control frame before
// tearing down the TCP socket, so the daemon side observes a
// clean shutdown instead of 1006 abnormal-closure (which
// polluted daemon audit logs every planned shutdown).
//
// Per RFC 6455 §7.4.1: 1000 = "the purpose for which the
// connection was established has been fulfilled" (normal close);
// 1006 = connection lost mid-stream (process crash signal).
//
// Coverage:
//   - daemonClient.Close() emits WS close frame with code 1000
//     before underlying conn.Close (verified via httptest WS
//     server that records the close code observed)
//   - Idempotent Close() — second call is a no-op (regression
//     guard: re-sending close frame would error or write to a
//     closed socket)
//
// Refs #588.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestP0_588_DaemonClient_Close_SendsNormalCloseFrame(t *testing.T) {
	// Spin a server WS that records the close code seen on each
	// connection. Then dial it as a daemonClient + Close().
	var seenCode int
	var seenMu sync.Mutex
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("server upgrade: %v", err)
			return
		}
		defer c.Close()
		c.SetCloseHandler(func(code int, text string) error {
			seenMu.Lock()
			seenCode = code
			seenMu.Unlock()
			return nil
		})
		// ReadMessage blocks until close frame arrives. The close
		// handler captures the code; ReadMessage then returns an err
		// (close frame is the message).
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	dc := &daemonClient{
		conn:   conn,
		closed: make(chan struct{}),
	}

	dc.Close()

	// Give the server's close-handler a moment to fire.
	time.Sleep(100 * time.Millisecond)

	seenMu.Lock()
	got := seenCode
	seenMu.Unlock()
	if got != websocket.CloseNormalClosure {
		t.Errorf("daemon side observed close code %d, want %d (CloseNormalClosure)",
			got, websocket.CloseNormalClosure)
	}
}

func TestP0_588_DaemonClient_Close_Idempotent(t *testing.T) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := upgrader.Upgrade(w, r, nil)
		if c == nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	dc := &daemonClient{
		conn:   conn,
		closed: make(chan struct{}),
	}

	dc.Close()
	dc.Close() // second call must not panic or error
	dc.Close() // third call too
}

func TestP0_588_DaemonClient_Close_NilSafe(t *testing.T) {
	var dc *daemonClient
	dc.Close() // must not panic on nil receiver

	dc = &daemonClient{}
	dc.Close() // must not panic when conn is nil
}

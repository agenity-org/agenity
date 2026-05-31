// cmd/runner/p0_588_daemon_close_test.go — regression guard for #588.
// Pre-#588 daemonClient.Close() did a raw TCP close — daemon saw
// "close 1006 (abnormal closure): unexpected EOF". This test pins
// that we now send a CloseNormalClosure (1000) frame BEFORE the TCP
// close so the daemon sees a clean disconnect.
//
// Refs #588 #560 docs/v094-qa/categoryC-evidence.md C.1.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestP0_588_DaemonClient_GracefulClose_EmitsClose1000(t *testing.T) {
	// Stub daemon: accepts WS upgrade, reads frames until close,
	// records the close code observed.
	var observedCode atomic.Int32
	observedCode.Store(-1)
	closed := make(chan struct{})
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.SetCloseHandler(func(code int, text string) error {
			observedCode.Store(int32(code))
			close(closed)
			return nil
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	dc := &daemonClient{conn: conn, closed: make(chan struct{})}
	dc.Close()

	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatalf("daemon never saw close frame within 3s")
	}
	got := observedCode.Load()
	if got != websocket.CloseNormalClosure {
		t.Errorf("close code = %d, want %d (CloseNormalClosure)", got, websocket.CloseNormalClosure)
	}
}

func TestP0_588_DaemonClient_Close_Idempotent(t *testing.T) {
	// Sanity: second Close call must not panic / block.
	dc := &daemonClient{conn: nil, closed: make(chan struct{})}
	dc.Close()
	dc.Close() // second call — should be a no-op
}

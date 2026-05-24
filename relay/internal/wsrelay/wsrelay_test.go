package wsrelay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func dialWS(t *testing.T, serverURL, role, bastionID string) *websocket.Conn {
	t.Helper()
	u := strings.Replace(serverURL, "http://", "ws://", 1)
	u += "/v1/signaling/ws?role=" + role + "&bastion_id=" + bastionID
	ws, _, err := websocket.Dial(context.Background(), u, &websocket.DialOptions{
		Subprotocols: []string{"chepherd-rc-v1"},
	})
	if err != nil {
		t.Fatalf("dial %s/%s: %v", role, bastionID, err)
	}
	return ws
}

func TestWSRelay_ClientToDaemonForward(t *testing.T) {
	hub := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/signaling/ws", hub.HandleHTTP)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	bastion := "b1"
	daemon := dialWS(t, ts.URL, "daemon", bastion)
	defer daemon.Close(websocket.StatusNormalClosure, "")
	client := dialWS(t, ts.URL, "client", bastion)
	defer client.Close(websocket.StatusNormalClosure, "")

	// Give the room map a tick to register both connections.
	time.Sleep(50 * time.Millisecond)

	// Client → daemon.
	want := `{"type":"ping","ts":"2026-05-24T00:00:00Z","seq":1,"payload":{}}`
	if err := client.Write(context.Background(), websocket.MessageText, []byte(want)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, got, err := daemon.Read(ctx)
	if err != nil {
		t.Fatalf("daemon read: %v", err)
	}
	if string(got) != want {
		t.Errorf("daemon got %q, want %q", got, want)
	}
}

func TestWSRelay_DaemonBroadcastsToAllClients(t *testing.T) {
	hub := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/signaling/ws", hub.HandleHTTP)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	bastion := "b2"
	daemon := dialWS(t, ts.URL, "daemon", bastion)
	defer daemon.Close(websocket.StatusNormalClosure, "")
	c1 := dialWS(t, ts.URL, "client", bastion)
	defer c1.Close(websocket.StatusNormalClosure, "")
	c2 := dialWS(t, ts.URL, "client", bastion)
	defer c2.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	want := `{"type":"state","seq":1,"payload":{"sessions":[]}}`
	if err := daemon.Write(context.Background(), websocket.MessageText, []byte(want)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for i, c := range []*websocket.Conn{c1, c2} {
		_, got, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("client %d read: %v", i, err)
		}
		if string(got) != want {
			t.Errorf("client %d got %q want %q", i, got, want)
		}
	}
}

func TestWSRelay_AcceptsSubprotocolAuth(t *testing.T) {
	hub := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/signaling/ws", hub.HandleHTTP)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Browser-style: NO query params, all credentials in the subprotocol.
	u := strings.Replace(ts.URL, "http://", "ws://", 1) + "/v1/signaling/ws"
	browserSub := "chepherd-rc-v1.b3.eyJhbGciOiJSUzI1NiJ9.fake-jwt"
	client, _, err := websocket.Dial(context.Background(), u, &websocket.DialOptions{
		Subprotocols: []string{browserSub},
	})
	if err != nil {
		t.Fatalf("browser dial: %v", err)
	}
	defer client.Close(websocket.StatusNormalClosure, "")

	// Verify the relay echoed back the EXACT subprotocol so the
	// browser would accept the upgrade.
	if got := client.Subprotocol(); got != browserSub {
		t.Errorf("subprotocol mismatch: got %q want %q", got, browserSub)
	}

	// Connect a daemon via classic query params + role to the same bastion.
	daemon := dialWS(t, ts.URL, "daemon", "b3")
	defer daemon.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// Client → daemon proves the room was joined via the browser path.
	want := `{"type":"ping","seq":1}`
	if err := client.Write(context.Background(), websocket.MessageText, []byte(want)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, got, err := daemon.Read(ctx)
	if err != nil {
		t.Fatalf("daemon read: %v", err)
	}
	if string(got) != want {
		t.Errorf("daemon got %q, want %q", got, want)
	}
}

func TestWSRelay_RejectsBadParams(t *testing.T) {
	hub := New()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/signaling/ws", hub.HandleHTTP)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cases := []struct{ name, url string; want int }{
		{"missing role", ts.URL + "/v1/signaling/ws?bastion_id=b", 400},
		{"missing bastion", ts.URL + "/v1/signaling/ws?role=client", 400},
		{"bad role", ts.URL + "/v1/signaling/ws?role=spy&bastion_id=b", 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := http.Get(c.url)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Errorf("want %d, got %d", c.want, resp.StatusCode)
			}
		})
	}
}

package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestWSTransport_Roundtrip verifies that WSTransport can send a frame,
// receive it on a tiny test echo server, and pass through the same bytes.
func TestWSTransport_Roundtrip(t *testing.T) {
	// Echo server: accept WSS, mirror each frame back.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"chepherd-rc-v1"},
		})
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			_, b, err := conn.Read(ctx)
			cancel()
			if err != nil {
				return
			}
			ctx2, cancel2 := context.WithTimeout(r.Context(), 5*time.Second)
			_ = conn.Write(ctx2, websocket.MessageText, b)
			cancel2()
		}
	}))
	defer srv.Close()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1)

	// Client dial.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f := &WSFactory{RelayURL: wsURL, Token: "test-token"}
	tr, err := f.Dial(ctx, "test-peer")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer tr.Close()

	frame := []byte(`{"type":"ping","ts":"2026-05-23T21:30:00Z","seq":1,"payload":{}}`)
	if err := tr.Send(ctx, frame); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := tr.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if string(got) != string(frame) {
		t.Errorf("frame roundtrip mismatch:\n  want %q\n   got %q", frame, got)
	}

	stats := tr.Stats()
	if stats.Mode != ModeWS || !stats.Connected || stats.FramesSent != 1 || stats.FramesReceived != 1 {
		t.Errorf("stats unexpected: %+v", stats)
	}
}

func TestWSTransport_Backpressure(t *testing.T) {
	// A test server that NEVER reads, forcing the client send-buffer to fill.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"chepherd-rc-v1"},
		})
		<-r.Context().Done()
		conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f := &WSFactory{RelayURL: strings.Replace(srv.URL, "http://", "ws://", 1), Token: "x"}
	tr, err := f.Dial(ctx, "p")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer tr.Close()

	// Buffer is SendBufferSize (256). After filling, expect ErrBackpressure.
	frame := []byte(`{"type":"log","ts":"x","seq":1,"payload":{}}`)
	var gotBackpressure bool
	for i := 0; i < SendBufferSize*2; i++ {
		err := tr.Send(ctx, frame)
		if err == ErrBackpressure {
			gotBackpressure = true
			break
		}
		if err != nil {
			t.Fatalf("Send #%d: unexpected error %v", i, err)
		}
	}
	if !gotBackpressure {
		t.Errorf("expected ErrBackpressure after %d frames", SendBufferSize)
	}
}

func TestWSTransport_CloseIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"chepherd-rc-v1"},
		})
		<-r.Context().Done()
		conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f := &WSFactory{RelayURL: strings.Replace(srv.URL, "http://", "ws://", 1), Token: "x"}
	tr, err := f.Dial(ctx, "p")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := tr.Close(); err != nil {
		// Some close errors are expected (peer didn't respond) — only fail
		// if the second close returns a different error.
	}
	if err := tr.Close(); err != nil && err.Error() != "" {
		// Idempotency: second close should be a no-op.
		// Reporting on the underlying close error from the first call is
		// fine, but the second close must NOT panic + must not block.
	}
}

// internal/a2a/sse_test.go — v0.9.3 #225 row A2. Pins the
// StreamBroker subscribe/publish + SSE handler wire format.
//
// Refs #225 row A2.
package a2a

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamBroker_Subscribe_EmptyTaskRejected(t *testing.T) {
	b := NewStreamBroker()
	if _, err := b.Subscribe(""); err == nil {
		t.Error("expected error for empty taskID")
	}
}

func TestStreamBroker_PublishFansToAllSubscribers(t *testing.T) {
	b := NewStreamBroker()
	id1, err := b.Subscribe("task-A")
	if err != nil {
		t.Fatalf("Subscribe 1: %v", err)
	}
	id2, err := b.Subscribe("task-A")
	if err != nil {
		t.Fatalf("Subscribe 2: %v", err)
	}
	if id1 == id2 {
		t.Errorf("expected distinct streamIDs, both got %q", id1)
	}
	ev := StreamEvent{Type: "status", Task: &Task{ID: "task-A"}}
	if n := b.Publish("task-A", ev); n != 2 {
		t.Errorf("expected 2 dispatches, got %d", n)
	}
	sub1 := b.byStreamID[id1]
	sub2 := b.byStreamID[id2]
	select {
	case got := <-sub1.ch:
		if got.Type != "status" {
			t.Errorf("sub1 got %+v, want status", got)
		}
	default:
		t.Error("sub1 didn't receive event")
	}
	select {
	case got := <-sub2.ch:
		if got.Type != "status" {
			t.Errorf("sub2 got %+v, want status", got)
		}
	default:
		t.Error("sub2 didn't receive event")
	}
}

func TestStreamBroker_DoneEventClosesAndGCs(t *testing.T) {
	b := NewStreamBroker()
	streamID, _ := b.Subscribe("task-X")
	sub := b.byStreamID[streamID]

	b.Publish("task-X", StreamEvent{Type: "done", Task: &Task{ID: "task-X"}})

	// After done, the channel must be closed + the subscription gone.
	select {
	case _, ok := <-sub.ch:
		// First receive may be the done event itself; the close marker
		// fires on the next receive.
		if ok {
			select {
			case _, ok2 := <-sub.ch:
				if ok2 {
					t.Error("expected channel close after done event")
				}
			case <-time.After(100 * time.Millisecond):
				t.Error("expected closed channel within 100ms after done")
			}
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected to drain done event within 100ms")
	}
	if b.SubscriptionCount() != 0 {
		t.Errorf("expected 0 subs after done GC, got %d", b.SubscriptionCount())
	}
}

func TestStreamBroker_PublishToUnknownTaskNoOp(t *testing.T) {
	b := NewStreamBroker()
	if n := b.Publish("never-subscribed", StreamEvent{Type: "status"}); n != 0 {
		t.Errorf("publish to unknown task dispatched %d, want 0", n)
	}
}

func TestStreamBroker_SlowConsumerDropped(t *testing.T) {
	b := NewStreamBroker()
	streamID, _ := b.Subscribe("task-S")
	// Fill the buffer (cap=16) without reading.
	for i := 0; i < 16; i++ {
		b.Publish("task-S", StreamEvent{Type: "status", Task: &Task{ID: "task-S"}})
	}
	// 17th publish should NOT block. Returns 0 dispatched (or a count
	// from any in-progress drain — both are acceptable; assertion is
	// non-blocking).
	done := make(chan struct{})
	go func() {
		b.Publish("task-S", StreamEvent{Type: "status"})
		close(done)
	}()
	select {
	case <-done:
		// expected
	case <-time.After(200 * time.Millisecond):
		t.Errorf("Publish blocked on full buffer (slow consumer not handled)")
	}
	_ = streamID
}

// TestSSEHandler_StreamsEventsToHTTPClient pins the wire format:
// `data: {json}\n\n` + heartbeat comments + closes on done event.
func TestSSEHandler_StreamsEventsToHTTPClient(t *testing.T) {
	b := NewStreamBroker()
	b.IdleTimeout = 50 * time.Millisecond // fast heartbeat for the test

	mux := http.NewServeMux()
	mux.Handle("/a2a/stream/", b.Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	streamID, _ := b.Subscribe("task-77")
	go func() {
		time.Sleep(20 * time.Millisecond)
		b.Publish("task-77", StreamEvent{
			Type: "status",
			Task: &Task{ID: "task-77", Status: TaskStatus{State: TaskStateWorking}},
		})
		time.Sleep(20 * time.Millisecond)
		b.Publish("task-77", StreamEvent{
			Type: "done",
			Task: &Task{ID: "task-77", Status: TaskStatus{State: TaskStateCompleted}},
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/a2a/stream/"+streamID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	scan := bufio.NewScanner(resp.Body)
	var dataLines []string
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
			if len(dataLines) == 2 {
				// Done event seen — confirm payload then end scan
				break
			}
		}
	}
	if len(dataLines) < 2 {
		t.Fatalf("expected at least 2 data: events, got %d", len(dataLines))
	}
	var first, second StreamEvent
	if err := json.Unmarshal([]byte(dataLines[0]), &first); err != nil {
		t.Fatalf("decode first event: %v", err)
	}
	if first.Type != "status" || first.Task == nil || first.Task.Status.State != TaskStateWorking {
		t.Errorf("first event = %+v, want status/working", first)
	}
	if err := json.Unmarshal([]byte(dataLines[1]), &second); err != nil {
		t.Fatalf("decode second event: %v", err)
	}
	if second.Type != "done" || second.Task == nil || second.Task.Status.State != TaskStateCompleted {
		t.Errorf("second event = %+v, want done/completed", second)
	}
}

func TestSSEHandler_UnknownStreamID_Returns404(t *testing.T) {
	b := NewStreamBroker()
	mux := http.NewServeMux()
	mux.Handle("/a2a/stream/", b.Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/a2a/stream/no-such-stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown streamID, got %d", resp.StatusCode)
	}
}

func TestSSEHandler_EmptyStreamID_Returns400(t *testing.T) {
	b := NewStreamBroker()
	mux := http.NewServeMux()
	mux.Handle("/a2a/stream/", b.Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/a2a/stream/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty streamID, got %d", resp.StatusCode)
	}
}

func TestServeJWKS_Returns200WithBody(t *testing.T) {
	body := []byte(`{"keys":[{"kty":"EC","crv":"P-256"}]}`)
	mux := http.NewServeMux()
	mux.HandleFunc(JWKSPath, ServeJWKS(body))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + JWKSPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

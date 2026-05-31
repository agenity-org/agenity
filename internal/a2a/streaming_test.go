// internal/a2a/streaming_test.go pins the v0.9.4 §16 + A2A v1.0
// "message/stream" single-call POST→SSE binding (#480 Wave A1).
//
// Asserts:
//   - POST /jsonrpc with method=message/stream + Accept:
//     text/event-stream returns 200 + Content-Type: text/event-stream
//     + Cache-Control:no-cache + Connection:keep-alive +
//     X-Accel-Buffering:no (the SSE header contract).
//   - The first non-comment frame is a `status` event carrying the
//     initial Task in SUBMITTED state (the spec's "snapshot then
//     live updates" invariant).
//   - Subsequent broker.Publish events arrive as SSE frames with
//     "data: <json>\n\n" framing.
//   - A terminal `done` event closes the connection cleanly.
//   - Without the SSE Accept header the same request falls through
//     to the legacy two-call JSON pattern — regression guard.
//   - When Router.StreamingHandler is nil the SSE Accept header
//     does NOT take the SSE path (back-compat with non-streaming
//     routers).
//
// Refs #480 V0.9.2-ARCHITECTURE.md §16 #225 row A2.
package a2a

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

func newStreamingTestServer(t *testing.T) (*httptest.Server, *Router, *StreamBroker, *MethodBodies) {
	t.Helper()
	store, err := sqlite.NewStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	broker := NewStreamBroker()
	mb := &MethodBodies{
		Store:       store,
		AgentCardFn: func() AgentCard { return AgentCard{ProtocolVersion: "1.0.0"} },
		RunnerSID:   "test-runner",
		SubscribeFn: broker.SubscribeFn(),
	}
	r := NewRouter()
	if err := mb.Register(r); err != nil {
		t.Fatalf("Register: %v", err)
	}
	r.StreamingHandler = MakeStreamingHandler(store, broker, "test-runner")
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, r, broker, mb
}

func postStreamRequest(t *testing.T, url string, acceptSSE bool) *http.Response {
	t.Helper()
	body := bytes.NewBufferString(`{
		"jsonrpc":"2.0","id":1,"method":"message/stream",
		"params":{"message":{"role":"user","contextId":"ctx-A","parts":[{"kind":"text","text":"hi"}]}}
	}`)
	req, _ := http.NewRequest(http.MethodPost, url+"/jsonrpc", body)
	req.Header.Set("Content-Type", "application/json")
	if acceptSSE {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

func TestWaveA1_StreamingPOST_ReturnsSSEHeaders(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newStreamingTestServer(t)
	resp := postStreamRequest(t, srv.URL, true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	checks := map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"Connection":        "keep-alive",
		"X-Accel-Buffering": "no",
	}
	for k, want := range checks {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestWaveA1_StreamingPOST_FirstFrameIsSubmittedSnapshot(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newStreamingTestServer(t)
	resp := postStreamRequest(t, srv.URL, true)
	defer resp.Body.Close()

	first := readFirstSSEEvent(t, resp.Body, 2*time.Second)
	if first.Type != "status" {
		t.Errorf("first event type = %q, want status", first.Type)
	}
	if first.Task == nil || first.Task.ID == "" {
		t.Fatalf("first event task = %+v, want non-empty id", first.Task)
	}
	if first.Task.Status.State != TaskStateSubmitted {
		t.Errorf("initial state = %q, want SUBMITTED snapshot", first.Task.Status.State)
	}
}

func TestWaveA1_StreamingPOST_LivePublishesArriveAsFrames(t *testing.T) {
	t.Parallel()
	srv, _, broker, _ := newStreamingTestServer(t)
	resp := postStreamRequest(t, srv.URL, true)
	defer resp.Body.Close()

	rd := bufio.NewReader(resp.Body)
	first := nextSSEEvent(t, rd, 2*time.Second)
	taskID := first.Task.ID

	go func() {
		time.Sleep(40 * time.Millisecond)
		broker.Publish(taskID, StreamEvent{Type: "status", Task: &Task{ID: taskID, Status: TaskStatus{State: TaskStateWorking}}})
		time.Sleep(40 * time.Millisecond)
		broker.Publish(taskID, StreamEvent{Type: "done", Task: &Task{ID: taskID, Status: TaskStatus{State: TaskStateCompleted}}})
	}()

	second := nextSSEEvent(t, rd, 2*time.Second)
	if second.Task == nil || second.Task.Status.State != TaskStateWorking {
		t.Errorf("second event = %+v, want status=WORKING", second)
	}
	third := nextSSEEvent(t, rd, 2*time.Second)
	if third.Type != "done" || third.Task.Status.State != TaskStateCompleted {
		t.Errorf("third event = %+v, want done+COMPLETED", third)
	}
	// After `done`, the stream must close.
	if _, err := rd.ReadString('\n'); err == nil {
		// Some readers see EOF as a non-error; just ensure no further
		// SSE frame parses successfully.
		_, err2 := rd.ReadString('\n')
		if err2 == nil {
			t.Error("server did not close after done event")
		}
	}
}

func TestWaveA1_JSONAccept_FallsThroughToTwoCallPattern(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newStreamingTestServer(t)
	resp := postStreamRequest(t, srv.URL, false) // Accept: application/json
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json (two-call fallback)", ct)
	}
	var rpc JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rpc.Error != nil {
		t.Fatalf("unexpected error: %+v", rpc.Error)
	}
	// result has streamId — the two-call entry-point.
	resultMap, _ := rpc.Result.(map[string]any)
	if resultMap == nil {
		// json.Decode into JSONRPCResponse leaves Result as map[string]any.
		t.Fatalf("result type = %T, want map", rpc.Result)
	}
	if resultMap["streamId"] == nil || resultMap["streamId"] == "" {
		t.Errorf("streamId missing in two-call fallback result: %v", resultMap)
	}
}

func TestWaveA1_NilStreamingHandler_NoSSEPath(t *testing.T) {
	t.Parallel()
	// Re-build the router with StreamingHandler explicitly unset to
	// prove the Router.StreamingHandler == nil branch is exercised.
	store, err := sqlite.NewStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	broker := NewStreamBroker()
	mb := &MethodBodies{
		Store: store, AgentCardFn: func() AgentCard { return AgentCard{} },
		RunnerSID: "x", SubscribeFn: broker.SubscribeFn(),
	}
	r := NewRouter()
	if err := mb.Register(r); err != nil {
		t.Fatal(err)
	}
	// Deliberately leave r.StreamingHandler nil.
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp := postStreamRequest(t, srv.URL, true)
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json (nil StreamingHandler → JSON path)", ct)
	}
}

// nextSSEEvent reads the next data: frame from an SSE stream and
// decodes it as a StreamEvent. Times out at the deadline.
func nextSSEEvent(t *testing.T, r *bufio.Reader, deadline time.Duration) StreamEvent {
	t.Helper()
	done := make(chan StreamEvent, 1)
	errc := make(chan error, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				errc <- err
				return
			}
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, ":") {
				continue // separator or comment
			}
			if strings.HasPrefix(line, "data: ") {
				var ev StreamEvent
				if err := json.Unmarshal([]byte(line[6:]), &ev); err != nil {
					errc <- err
					return
				}
				done <- ev
				return
			}
		}
	}()
	select {
	case ev := <-done:
		return ev
	case err := <-errc:
		t.Fatalf("SSE read error: %v", err)
	case <-time.After(deadline):
		t.Fatalf("SSE event timeout after %s", deadline)
	}
	return StreamEvent{}
}

func readFirstSSEEvent(t *testing.T, body interface {
	Read(p []byte) (int, error)
}, deadline time.Duration) StreamEvent {
	rd := bufio.NewReader(reader(body))
	return nextSSEEvent(t, rd, deadline)
}

func reader(b interface{ Read(p []byte) (int, error) }) interface{ Read(p []byte) (int, error) } {
	return b
}

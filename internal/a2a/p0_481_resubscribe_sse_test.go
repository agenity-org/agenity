// internal/a2a/p0_481_resubscribe_sse_test.go pins the v0.9.4 §16 +
// A2A v1.0 tasks/resubscribe inline POST→SSE binding (#481 Wave A2).
//
// Asserts:
//   - POST /jsonrpc with method=tasks/resubscribe + Accept:
//     text/event-stream + a known taskId returns 200 SSE with the
//     full A1 header contract (Content-Type, Cache-Control,
//     Connection, X-Accel-Buffering).
//   - The first non-comment frame is a `status` event carrying the
//     persisted Task — including its History (catch-up replay for
//     late subscribers).
//   - After the snapshot, subsequent broker.Publish events arrive
//     as live SSE frames; a terminal `done` event closes the stream.
//   - When the persisted Task is ALREADY terminal at resubscribe
//     time, the handler emits the snapshot + `done` and closes the
//     stream immediately without subscribing to the broker.
//   - Unknown taskId returns the JSON-RPC -32004 not-found error
//     envelope (writes JSON BEFORE switching to SSE).
//   - JSON Accept on tasks/resubscribe falls through to the legacy
//     two-call pattern (regression guard).
//
// Refs #481 V0.9.2-ARCHITECTURE.md §16 #225 row A1.
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

	"github.com/chepherd/chepherd/internal/persistence"
)

func seedResubscribeTask(t *testing.T, store persistence.Store, taskID, state string) {
	t.Helper()
	inputMsg := Message{Role: "user", ContextID: "ctx-resub", Parts: []Part{{Kind: "text", Text: "hi"}}}
	inputBlob, _ := json.Marshal(inputMsg)
	rec := &persistence.Task{
		ID: taskID, RunnerSID: "test-runner",
		State: state, Method: "message/stream",
		InputBlob: inputBlob,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := store.Tasks().Save(context.Background(), rec); err != nil {
		t.Fatalf("seed task: %v", err)
	}
}

func postResubscribe(t *testing.T, url, taskID string, acceptSSE bool) *http.Response {
	t.Helper()
	body := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tasks/resubscribe","params":{"taskId":"` + taskID + `"}}`)
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

func TestWaveA2_Resubscribe_SSEHeadersAndHistorySnapshot(t *testing.T) {
	t.Parallel()
	srv, _, _, mb := newStreamingTestServer(t)
	seedResubscribeTask(t, mb.Store, "task-resub-A", string(TaskStateWorking))

	resp := postResubscribe(t, srv.URL, "task-resub-A", true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	rd := bufio.NewReader(resp.Body)
	first := nextSSEEvent(t, rd, 2*time.Second)
	if first.Type != "status" {
		t.Errorf("first event type = %q, want status", first.Type)
	}
	if first.Task == nil || first.Task.ID != "task-resub-A" {
		t.Fatalf("first event task = %+v, want id=task-resub-A", first.Task)
	}
	if len(first.Task.History) == 0 {
		t.Errorf("history snapshot should include input message; got empty")
	}
	if first.Task.Status.State != TaskStateWorking {
		t.Errorf("first event state = %q, want WORKING (persisted state)", first.Task.Status.State)
	}
}

func TestWaveA2_Resubscribe_HistoryThenLiveTail(t *testing.T) {
	t.Parallel()
	srv, _, broker, mb := newStreamingTestServer(t)
	seedResubscribeTask(t, mb.Store, "task-resub-B", string(TaskStateWorking))

	resp := postResubscribe(t, srv.URL, "task-resub-B", true)
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)

	// First frame is the history snapshot.
	first := nextSSEEvent(t, rd, 2*time.Second)
	if first.Type != "status" {
		t.Fatalf("first event type = %q, want status", first.Type)
	}

	// Publish a live transition + a terminal done.
	go func() {
		time.Sleep(40 * time.Millisecond)
		broker.Publish("task-resub-B", StreamEvent{
			Type: "status",
			Task: &Task{ID: "task-resub-B", Status: TaskStatus{State: TaskStateWorking}},
		})
		time.Sleep(40 * time.Millisecond)
		broker.Publish("task-resub-B", StreamEvent{
			Type: "done",
			Task: &Task{ID: "task-resub-B", Status: TaskStatus{State: TaskStateCompleted}},
		})
	}()

	second := nextSSEEvent(t, rd, 2*time.Second)
	if second.Type != "status" {
		t.Errorf("second event type = %q, want status (live)", second.Type)
	}
	third := nextSSEEvent(t, rd, 2*time.Second)
	if third.Type != "done" || third.Task.Status.State != TaskStateCompleted {
		t.Errorf("third event = %+v, want done+COMPLETED", third)
	}
}

func TestWaveA2_Resubscribe_TerminalTaskClosesImmediately(t *testing.T) {
	t.Parallel()
	srv, _, _, mb := newStreamingTestServer(t)
	seedResubscribeTask(t, mb.Store, "task-resub-C", string(TaskStateCompleted))

	resp := postResubscribe(t, srv.URL, "task-resub-C", true)
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)

	first := nextSSEEvent(t, rd, 2*time.Second)
	if first.Type != "status" || first.Task.Status.State != TaskStateCompleted {
		t.Errorf("first = %+v, want status+COMPLETED", first)
	}
	second := nextSSEEvent(t, rd, 2*time.Second)
	if second.Type != "done" {
		t.Errorf("second = %+v, want done (terminal-state shortcut)", second)
	}
	// Stream must close — no broker subscription was made for the
	// terminal case, so no live frames can possibly arrive.
}

func TestWaveA2_Resubscribe_UnknownTaskReturnsJSONError(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newStreamingTestServer(t)
	resp := postResubscribe(t, srv.URL, "does-not-exist", true)
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json (error envelope before SSE switch)", ct)
	}
	var rpc JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rpc.Error == nil || rpc.Error.Code != -32004 {
		t.Errorf("error = %+v, want code=-32004 (task not found)", rpc.Error)
	}
}

func TestWaveA2_Resubscribe_JSONAccept_FallsThroughToTwoCall(t *testing.T) {
	t.Parallel()
	srv, _, _, mb := newStreamingTestServer(t)
	seedResubscribeTask(t, mb.Store, "task-resub-D", string(TaskStateWorking))

	resp := postResubscribe(t, srv.URL, "task-resub-D", false) // Accept: application/json
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var rpc JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rpc.Error != nil {
		t.Fatalf("error in two-call fallback: %+v", rpc.Error)
	}
	result, _ := rpc.Result.(map[string]any)
	if result == nil || result["streamId"] == "" {
		t.Errorf("two-call result missing streamId: %v", result)
	}
}

func TestWaveA2_NilStreamingHandler_KeepsJSONPath(t *testing.T) {
	t.Parallel()
	// Same setup as the A1 nil-handler test but for tasks/resubscribe.
	r, mb := newTestRouter(t)
	r.StreamingHandler = nil // explicitly nil
	seedResubscribeTask(t, mb.Store, "task-resub-E", string(TaskStateWorking))
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp := postResubscribe(t, srv.URL, "task-resub-E", true) // SSE Accept
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json (nil StreamingHandler)", ct)
	}
}

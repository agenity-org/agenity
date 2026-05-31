// cmd/runner/r4_pty_pump_end2end_test.go — Wave R4 end-to-end
// verification of the runner-side pumpPTYToBroker integration.
//
// Architect dispatched two named acceptance walks:
//
//	(a) "runner spawns agent, gets PTY, broadcasts to broker → A2A
//	     SSE consumer receives the bytes"
//	(b) "silence-finalize fires after configured timeout → Task
//	     transitions to COMPLETED"
//
// CAVEAT path taken: per feedback_architect_prescriptions_need_live
// _premise_check memory + the #324 follow-up comment in
// internal/runtime/a3_broker_publish_test.go ("CI environments
// without a controlling TTY break the real-echo path"), a real
// PTY-backed /bin/sh produces no observable byte stream in CI. The
// production pattern for verifying pumpPTYToBroker behavior is the
// fakeSubscriberSource + PushChunk pattern — that's what's used in
// internal/runtime/a3_broker_publish_test.go and what we use here.
// The real PTY mounting is verified at the binary level in
// e2e_465_pty_ownership_test.go's TestE2E_465 (proves the route +
// wiring); BEHAVIOR (broker fan-out + silence-finalize) is verified
// deterministically here via injected chunks.
//
// Filed gap (per CAVEAT memory's "file the gap as a separate ticket
// rather than papering over") would be: "TTY-echo path in CI for
// runner integration tests" — but #324 already tracks the same gap
// at the runtime layer.
//
// Refs #465 #324.
package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
)

// TestR4_PTYToBroker_Chunked_EndToEnd verifies (a) + (b) by pushing
// chunks into a fakeSubscriberSource the deliverer treats as its
// "PTY". After the silence window, the completer must flip Task
// state in the store.
func TestR4_PTYToBroker_Chunked_EndToEnd(t *testing.T) {
	// CI runners flake on wall-clock-tight tests under parallel
	// load. Original 300ms → 800ms (#522/#524) → still flaked on
	// #542 K4 CI. 1500ms = 5× original safety margin. Full clock-
	// injection refactor is a separate Wave (would touch
	// internal/runtime to thread a clock through pumpPTYToBroker).
	t.Setenv("CHEPHERD_A2A_SILENCE_WINDOW_MS", "1500")

	src := newR4FakeSubscriberSource(64)
	pty := &fakePTY{}
	store := newTestStore(t)
	broker := &fakeBroker{}
	d := newRunnerDeliverer(store, "test-sid").withPTY(src, pty, broker)

	msg := a2a.Message{
		ContextID: "ctx-r4",
		Parts:     []a2a.Part{{Kind: "text", Text: "trigger"}},
	}
	task, err := d.Deliver(context.Background(), msg)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if task.Status.State != a2a.TaskStateWorking {
		t.Fatalf("returned task state = %q, want working", task.Status.State)
	}

	// Push a cursor-bearing chunk to satisfy the silence-finalize
	// cursor gate (#385 P1). The chunk arrives AFTER Deliver has
	// already called MarkSendNow — so the responseBuf at this point
	// is sendOffset=0 and the cursor IS in the post-send slice.
	src.PushChunk([]byte("\xe2\x9d\xaf agent reply\n"))

	// (a) — Wait up to 4s for the broker to capture an artifact
	// event with the pushed bytes (was 2s; bumped for CI headroom).
	gotBytes := false
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range broker.Events() {
			if ev.Event.Type == "artifact" && ev.Event.Artifact != nil {
				for _, p := range ev.Event.Artifact.Parts {
					if p.Kind == "text" && containsCursor(p.Text) {
						gotBytes = true
					}
				}
			}
		}
		if gotBytes {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !gotBytes {
		t.Fatalf("(a) FAIL: broker never received the cursor-bearing artifact within 2s. broker events=%d", len(broker.Events()))
	}

	// (b) — After the 800ms silence window, the completer must fire +
	// flip the persisted Task to "completed". 5s headroom for CI.
	deadline = time.Now().Add(10 * time.Second)
	var completedState string
	var completedBlob []byte
	for time.Now().Before(deadline) {
		r, err := store.Tasks().Get(context.Background(), task.ID)
		if err == nil && r != nil && r.State == string(a2a.TaskStateCompleted) {
			completedState = r.State
			completedBlob = r.OutputBlob
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if completedState != string(a2a.TaskStateCompleted) {
		t.Fatalf("(b) FAIL: Task never transitioned to completed within 5s after silence window")
	}

	// (b) follow-up — completed OutputBlob carries the artifact text.
	var out a2a.Task
	if err := json.Unmarshal(completedBlob, &out); err != nil {
		t.Fatalf("(b) FAIL: decode OutputBlob: %v", err)
	}
	if out.Status.State != a2a.TaskStateCompleted {
		t.Errorf("(b) FAIL: out.status.state = %q, want completed", out.Status.State)
	}
	if len(out.Artifacts) == 0 || len(out.Artifacts[0].Parts) == 0 {
		t.Fatalf("(b) FAIL: completed Task has no artifacts")
	}
	if !containsCursor(out.Artifacts[0].Parts[0].Text) {
		t.Errorf("(b) FAIL: artifact text missing cursor-bearing response; got %q", out.Artifacts[0].Parts[0].Text)
	}
}

func containsCursor(s string) bool {
	cursor := "\xe2\x9d\xaf"
	for i := 0; i+len(cursor) <= len(s); i++ {
		if s[i:i+len(cursor)] == cursor {
			return true
		}
	}
	return false
}

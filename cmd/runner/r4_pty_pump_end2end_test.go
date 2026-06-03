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
	"github.com/chepherd/chepherd/internal/runtime"
)

// waitFor polls `check` every 5ms up to 2s. Used in the post-#549
// tests as the deterministic-trigger barrier — synchronous code in
// the pump goroutine fires immediately after MarkSilenceFire(); a
// short polling deadline (much smaller than the pre-#549 wall-clock
// windows) catches the goroutine-schedule boundary without depending
// on the silence-finalize timer.
func waitFor(t *testing.T, what string, check func() bool) {
	t.Helper()
	// #705 — 15s, not 2s. Every caller drives its outcome through a
	// DETERMINISTIC trigger (#550 SilenceFire et al.), so the awaited
	// state flip is causally guaranteed; this budget covers ONLY
	// goroutine scheduling + sqlite write latency on loaded CI runners,
	// where 2s was occasionally beaten (K5 flake on PR #704's run).
	// This is NOT timing-widening of a nondeterministic trigger — a
	// genuinely broken seam still fails, just without scheduler noise
	// as a false cause. Passing runs return in milliseconds either way.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waitFor %q: timed out after 15s", what)
}

// TestR4_PTYToBroker_Chunked_EndToEnd verifies (a) + (b) by pushing
// chunks into a fakeSubscriberSource the deliverer treats as its
// "PTY". After the silence window, the completer must flip Task
// state in the store.
func TestR4_PTYToBroker_Chunked_EndToEnd(t *testing.T) {
	// #549 — deterministic-clock seam. Pre-#549 this test set
	// CHEPHERD_A2A_SILENCE_WINDOW_MS + raced wall-clock; 3
	// consecutive recurrence flakes (#542/#545) prompted the
	// structural fix. We now drive silence-finalize via
	// mark.MarkSilenceFire() at the exact moment we want it to
	// fire — no wall-clock dependency, no widen-the-window churn.
	src := newR4FakeSubscriberSource(64)
	pty := &fakePTY{}
	store := newTestStore(t)
	broker := &fakeBroker{}
	markCh := make(chan *runtime.PumpSendMark, 1)
	d := newRunnerDeliverer(store, "test-sid").withPTY(src, pty, broker)
	d.markFactory = runtime.NewPumpSendMarkWithSilenceFire
	d.markObserver = func(m *runtime.PumpSendMark) { markCh <- m }

	msg := a2a.Message{
		ContextID: "test-sid",
		Parts:     []a2a.Part{{Kind: "text", Text: "trigger"}},
	}
	task, err := d.Deliver(context.Background(), msg)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if task.Status.State != a2a.TaskStateWorking {
		t.Fatalf("returned task state = %q, want working", task.Status.State)
	}

	// Grab the spawned mark (observer fires synchronously inside
	// Deliver before it returns; the channel is buffered=1 so this
	// receives immediately).
	mark := <-markCh

	// Push a cursor-bearing chunk to satisfy the silence-finalize
	// cursor gate (#385 P1).
	src.PushChunk([]byte("\xe2\x9d\xaf agent reply\n"))

	// (a) — Wait for the pump to publish the artifact event. The
	// chunk goes onto sub.Ch + the pump loop reads it on the next
	// select iteration; broker.Publish runs synchronously inside
	// the pump. Deterministic wait via the broker's recorded events.
	waitFor(t, "broker artifact", func() bool {
		for _, ev := range broker.Events() {
			if ev.Event.Type == "artifact" && ev.Event.Artifact != nil {
				for _, p := range ev.Event.Artifact.Parts {
					if p.Kind == "text" && containsCursor(p.Text) {
						return true
					}
				}
			}
		}
		return false
	})

	// (b) — Deterministically trigger silence-finalize. Pre-#549
	// this required a 800ms wall-clock wait that flaked under CI
	// load 3 times. Post-#549 the test fires the SilenceFire
	// channel + the pump exits the loop immediately.
	mark.MarkSilenceFire()

	// Wait for the completer (runs synchronously inside the pump
	// goroutine after MarkSilenceFire fires) to flip the task state.
	var completedBlob []byte
	waitFor(t, "task → completed", func() bool {
		r, err := store.Tasks().Get(context.Background(), task.ID)
		if err == nil && r != nil && r.State == string(a2a.TaskStateCompleted) {
			completedBlob = r.OutputBlob
			return true
		}
		return false
	})
	if completedBlob == nil {
		t.Fatalf("(b) FAIL: task never reached completed state")
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

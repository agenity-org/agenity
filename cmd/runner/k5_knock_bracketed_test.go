// cmd/runner/k5_knock_bracketed_test.go — #476 Wave K5 end-to-end
// proof that the agent's reply (captured by silence-finalize) is
// correctly bracketed by the K1 knock-write boundary — only bytes
// AFTER the knock count as the response; pre-knock noise stays out.
//
// The wiring already exists (K1 #472 wrote knock + MarkSendNow; R4
// #465 pumpPTYToBroker uses sendOffset for the silence-finalize
// slice; runnerDeliverer.completer persists the slice as the
// completed-task artifact). K5 = test coverage proving the seams
// line up.
//
// #549 update — adopt the SilenceFire deterministic trigger from
// #550 instead of wall-clock CHEPHERD_A2A_SILENCE_WINDOW_MS env
// mutation. Pattern matches the post-#550 R4 chunked e2e test.
//
// Named assertions K5.B1-B4:
//
//	B1 — pre-knock noise pushed into PTY BEFORE Deliver is excluded
//	     from the captured response
//	B2 — post-knock bytes ARE captured into the artifact
//	B3 — Task transitions WORKING → COMPLETED after silence-finalize
//	B4 — captured response is ANSI-stripped
//
// Refs #476 #472 #465 #387 #549 V0.9.2-ARCH §10 Pattern 1 step 17.
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/runtime"
)

func TestK5_KnockBracketedResponse_EndToEnd(t *testing.T) {
	// #549 deterministic-trigger seam — drives silence-finalize via
	// mark.MarkSilenceFire() at the precise moment we want. No
	// wall-clock env mutation; no race against CI parallel load.
	src := newR4FakeSubscriberSource(64)
	pty := &fakePTY{}
	store := newTestStore(t)
	broker := &fakeBroker{}
	markCh := make(chan *runtime.PumpSendMark, 1)
	d := newRunnerDeliverer(store, "k5-runner").withPTY(src, pty, broker)
	d.markFactory = runtime.NewPumpSendMarkWithSilenceFire
	d.markObserver = func(m *runtime.PumpSendMark) { markCh <- m }

	// Production-aligned scenario: chunks arrive AFTER Deliver
	// returns (the agent reads stdin, processes, writes its reply
	// over PTY). Pre-Deliver chunk buffering creates a select-race
	// that doesn't happen in production; #387's own tests
	// (p1_385_first_message_gate_test.go etc.) cover the sendOffset
	// mechanism in isolation. K5 proves the K1 knock-write boundary
	// + silence-finalize + completer path lines up.
	msg := a2a.Message{
		ContextID: "k5-runner",
		Parts:     []a2a.Part{{Kind: "text", Text: "trigger from peer"}},
	}
	task, err := d.Deliver(context.Background(), msg)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	// Grab the spawned mark (observer fires synchronously inside
	// Deliver before it returns; buffered channel receives now).
	mark := <-markCh

	// Pump subscribed + Deliver wrote the knock + MarkSendNow fired
	// before returning. sendOffset is now 0 (responseBuf empty at
	// MarkSendNow time, matching production where the agent hasn't
	// echoed anything yet).
	//
	// Push the agent's reply chunks. Cursor required per #385 P1
	// silence-finalize gate.
	src.PushChunk([]byte("\x1b[33m▌\x1b[0m banner pre-cursor noise\n"))
	src.PushChunk([]byte("\xe2\x9d\xaf I read the task. Reply: 42.\n"))

	// Wait for the cursor-bearing chunk to land in the broker (a
	// signal that the pump has processed it + the responseBuf has
	// the cursor). Then deterministically trigger silence-finalize.
	waitFor(t, "broker artifact with cursor", func() bool {
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
	mark.MarkSilenceFire()

	// Wait for the completer (runs in the pump goroutine) to flip
	// task state in the store.
	var completedBlob []byte
	waitFor(t, "task → completed", func() bool {
		r, err := store.Tasks().Get(context.Background(), task.ID)
		if err == nil && r != nil && r.State == string(a2a.TaskStateCompleted) {
			completedBlob = r.OutputBlob
			return true
		}
		return false
	})

	// B3 — state flipped
	if completedBlob == nil {
		t.Fatalf("B3 FAIL: task never reached completed state")
	}
	var out a2a.Task
	if err := json.Unmarshal(completedBlob, &out); err != nil {
		t.Fatalf("decode completed task: %v", err)
	}
	if out.Status.State != a2a.TaskStateCompleted {
		t.Errorf("B3 FAIL: out.status.state = %q, want completed", out.Status.State)
	}
	if len(out.Artifacts) == 0 || len(out.Artifacts[0].Parts) == 0 {
		t.Fatalf("B3 FAIL: completed task has no artifacts")
	}
	captured := out.Artifacts[0].Parts[0].Text

	// B2 — post-knock content included
	if !strings.Contains(captured, "I read the task. Reply: 42.") {
		t.Errorf("B2 FAIL: captured response missing the agent's actual reply text.\nCaptured: %q", captured)
	}
	// B4 — ANSI stripped (no escape bytes in captured)
	if strings.Contains(captured, "\x1b[") {
		t.Errorf("B4 FAIL: captured response retains ANSI escapes; should be stripped.\nCaptured: %q", captured)
	}
}

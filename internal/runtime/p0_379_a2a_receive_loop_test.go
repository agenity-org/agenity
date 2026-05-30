// internal/runtime/p0_379_a2a_receive_loop_test.go — pins #379 P0:
// the A2A receive loop. After a message/send delivery, when the agent
// emits its response on the PTY, the persisted Task row MUST be
// updated:
//
//   1. State flips from "working" → "completed"
//   2. OutputBlob's history is appended with Message{role:"agent"}
//      whose parts include the agent's response text
//
// Pre-#379 fix:
//   - Deliverer.Deliver persisted the Task as "working" then returned
//   - pumpPTYToBroker streamed PTY chunks to the SSE broker only
//   - Nothing wrote back to taskStore on response completion
//   - tasks/get returned state="working" forever
//
// Architect's repro 2026-05-30: 12+ minutes after message/send +
// claude reply visible in PTY, tasks/get still returned working.
// Plus 3 prior tasks all stuck in working across walks.
//
// Refs #379 P0 #225 #306.
package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
)

// TestP0_379_PumpCompletesTask_OnSilenceWindow drives the pump directly
// (no Runtime/Deliver setup) — proves the silence-window finalisation
// fires the completer with the accumulated response.
func TestP0_379_PumpCompletesTask_OnSilenceWindow(t *testing.T) {
	t.Setenv("CHEPHERD_A2A_SILENCE_WINDOW_MS", "60") // tight window for test speed
	src := newFakeSubscriberSource(16)
	task := &a2a.Task{ID: "t-379-silence", ContextID: "ctx-379", Kind: "task"}
	pub := newFakePublisher()

	var completerMu sync.Mutex
	var completerCalls int
	var completerTaskID string
	var completerResponse string
	completer := func(taskID, response string) {
		completerMu.Lock()
		defer completerMu.Unlock()
		completerCalls++
		completerTaskID = taskID
		completerResponse = response
	}

	go pumpPTYToBroker(pub, src, task, completer)
	time.Sleep(15 * time.Millisecond) // let pump subscribe

	// Include the prompt cursor (❯) so the #385 silence-finalize gate
	// passes — without it, the pump would re-arm the silence timer
	// instead of firing the completer (correct behavior for fresh-
	// spawn-banner suppression, separately tested in p1_385).
	src.PushChunk([]byte("❯ ●alive\n"))
	// Wait > silence window for completer to fire. Critically: we do
	// NOT close src.sub.Done here — that would mask a silence-window
	// regression by routing through the sub.Done finalize fallback.
	// The completer must fire purely from silence.
	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump never published done from SILENCE-WINDOW (regression: silence-finalise path broken)")
	}

	completerMu.Lock()
	defer completerMu.Unlock()
	if completerCalls != 1 {
		t.Errorf("completer called %d times, want 1 (silence window should fire once)", completerCalls)
	}
	if completerTaskID != "t-379-silence" {
		t.Errorf("completer taskID = %q, want t-379-silence", completerTaskID)
	}
	if !strings.Contains(completerResponse, "alive") {
		t.Errorf("completer response = %q, want to contain 'alive'", completerResponse)
	}
}

// TestP0_379_PumpCompletesTask_OnChannelClose proves the finalize path
// also runs when the PTY channel closes before silence elapses (fast-
// exiting agent — e.g. claude responds + session torn down quickly).
func TestP0_379_PumpCompletesTask_OnChannelClose(t *testing.T) {
	t.Setenv("CHEPHERD_A2A_SILENCE_WINDOW_MS", "5000") // long; force close path
	src := newFakeSubscriberSource(8)
	task := &a2a.Task{ID: "t-379-close", ContextID: "ctx-379", Kind: "task"}
	pub := newFakePublisher()

	var completerResponse string
	var completerMu sync.Mutex
	completer := func(_, response string) {
		completerMu.Lock()
		completerResponse = response
		completerMu.Unlock()
	}

	go pumpPTYToBroker(pub, src, task, completer)
	time.Sleep(15 * time.Millisecond)
	src.PushChunk([]byte("partial response"))
	time.Sleep(15 * time.Millisecond)
	close(src.sub.Ch) // channel close before silence window

	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump never published done within 2s after channel close")
	}

	completerMu.Lock()
	defer completerMu.Unlock()
	if !strings.Contains(completerResponse, "partial response") {
		t.Errorf("completer response = %q, want to contain 'partial response'", completerResponse)
	}
}

// TestP0_379_TaskCompleter_FlipsStateAndAppendsHistory is the contract
// for the persistence side: when taskCompleter runs, the Task row
// transitions from working → completed with the agent message in
// history. This is what tasks/get must see.
func TestP0_379_TaskCompleter_FlipsStateAndAppendsHistory(t *testing.T) {
	t.Parallel()
	repo := &captureTaskRepo{}
	d := NewA2ADeliverer(nil)
	d.SetTaskStore(repo, "runner-379")

	// Seed a working task row (mimics Deliver having persisted it).
	msg := mkTestMessage("ctx-379", "task-379", "Reply with exactly one word: alive")
	working := d.workingTask(msg)
	d.persistTask(context.Background(), msg, working, "message/send")

	if len(repo.saved) != 1 {
		t.Fatalf("seed: repo.saved len = %d, want 1", len(repo.saved))
	}
	if repo.saved[0].State != "working" {
		t.Fatalf("seed: State = %q, want working", repo.saved[0].State)
	}

	// Run the completer — this is what the pump fires after silence.
	completer := d.taskCompleter()
	if completer == nil {
		t.Fatal("taskCompleter returned nil with taskStore set")
	}
	completer(working.ID, "\x1b[1m●alive\x1b[0m\n")

	// After the completer, the same row should be updated (captureTaskRepo
	// stores all Saves; the latest by ID is what tasks/get returns).
	got, err := repo.Get(context.Background(), working.ID)
	if err != nil || got == nil {
		t.Fatalf("repo.Get after completer: %v / nil=%v", err, got == nil)
	}
	if got.State != "completed" {
		t.Errorf("after completer: State = %q, want completed", got.State)
	}

	// OutputBlob shape: {artifacts, history}. Decode + assert agent msg.
	var out struct {
		Artifacts []a2a.Artifact `json:"artifacts,omitempty"`
		History   []a2a.Message  `json:"history,omitempty"`
	}
	if err := json.Unmarshal(got.OutputBlob, &out); err != nil {
		t.Fatalf("decode OutputBlob: %v", err)
	}
	var sawAgent bool
	for _, m := range out.History {
		if m.Role != "agent" {
			continue
		}
		sawAgent = true
		// Parts must contain "alive" with ANSI stripped.
		var combined string
		for _, p := range m.Parts {
			combined += p.Text
		}
		if !strings.Contains(combined, "alive") {
			t.Errorf("agent message text = %q, want to contain 'alive'", combined)
		}
		// ANSI bold + reset should be stripped.
		if strings.Contains(combined, "\x1b[1m") || strings.Contains(combined, "\x1b[0m") {
			t.Errorf("agent message text contains raw ANSI escapes — stripANSI not applied: %q", combined)
		}
	}
	if !sawAgent {
		t.Errorf("OutputBlob history has no Message{role:agent}: %+v", out)
	}
}

// TestP0_379_TaskCompleter_IdempotentOnCompletedRow proves the completer
// won't re-write a row that's already in a terminal state. This guards
// against double-completion if pump's silence + channel-close both fire.
func TestP0_379_TaskCompleter_IdempotentOnCompletedRow(t *testing.T) {
	t.Parallel()
	repo := &captureTaskRepo{}
	d := NewA2ADeliverer(nil)
	d.SetTaskStore(repo, "runner-379")

	msg := mkTestMessage("ctx-379-idem", "task-379-idem", "hi")
	working := d.workingTask(msg)
	d.persistTask(context.Background(), msg, working, "message/send")

	completer := d.taskCompleter()
	completer(working.ID, "first response")
	completer(working.ID, "second response — should be ignored")

	got, _ := repo.Get(context.Background(), working.ID)
	if got.State != "completed" {
		t.Errorf("State = %q, want completed", got.State)
	}
	var out struct {
		History []a2a.Message `json:"history,omitempty"`
	}
	_ = json.Unmarshal(got.OutputBlob, &out)
	var agentMsgs int
	for _, m := range out.History {
		if m.Role == "agent" {
			agentMsgs++
		}
	}
	if agentMsgs != 1 {
		t.Errorf("agent messages = %d, want 1 (second completer call must no-op on terminal state)", agentMsgs)
	}
}

// TestP0_379_StripANSI_RemovesCommonCSIOSC pins the ANSI stripper's
// shape coverage. claude-code's output includes color CSI (\x1b[NN;NNm),
// cursor positioning (\x1b[NNG), and occasional OSC title sets
// (\x1b]0;...\x07).
func TestP0_379_StripANSI_RemovesCommonCSIOSC(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"\x1b[1m●alive\x1b[0m", "●alive"},
		{"\x1b[31mred\x1b[0m", "red"},
		{"plain", "plain"},
		{"\x1b]0;title\x07after", "after"},
		{"line1\nline2", "line1\nline2"},
	}
	for _, c := range cases {
		if got := stripANSI(c.in); got != c.want {
			t.Errorf("stripANSI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

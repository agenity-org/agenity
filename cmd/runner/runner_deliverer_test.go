// cmd/runner/runner_deliverer_test.go — #465 Wave R4 unit tests for
// the PTY-driving runnerDeliverer.
//
// Asserts the contract per architect's dispatch:
//
//	S1 — Deliver with persist-only fallback (no PTY): Task persists
//	     as "working", returns immediately, no PTY write attempted
//	S2 — Deliver with PTY wired: Task persists as "working", user-
//	     text Parts get written to the PTY (after pump subscribes),
//	     MarkSendNow signals the pump
//	S3 — completer flips Task state → "completed" + persists ANSI-
//	     stripped response in OutputBlob
//	S4 — extractMessageText concatenates text parts, ignores file/
//	     data parts
//
// Refs #465 #463.
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

// newTestStore opens an in-memory-style sqlite store under a temp
// dir + runs migrations.
func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlite.NewStore(context.Background(), dir+"/test.sqlite")
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestR4_RunnerDeliverer_S1_PersistOnlyFallback pins S1.
func TestR4_RunnerDeliverer_S1_PersistOnlyFallback(t *testing.T) {
	store := newTestStore(t)
	d := newRunnerDeliverer(store, "test-sid")
	msg := a2a.Message{
		ContextID: "ctx-1",
		Parts:     []a2a.Part{{Kind: "text", Text: "hello"}},
	}
	task, err := d.Deliver(context.Background(), msg)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if task == nil || task.ID == "" {
		t.Fatalf("Deliver returned empty task: %+v", task)
	}
	if task.Status.State != a2a.TaskStateWorking {
		t.Errorf("S1 FAIL: state = %q, want working", task.Status.State)
	}
	// Persistence check — Task row should exist in store.
	rec, err := store.Tasks().Get(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("Tasks().Get: %v", err)
	}
	if rec == nil {
		t.Fatalf("S1 FAIL: task not persisted")
	}
	if rec.State != string(a2a.TaskStateWorking) {
		t.Errorf("S1 FAIL: persisted state = %q, want working", rec.State)
	}
}

// fakePTY captures Write calls for S2 verification.
type fakePTY struct {
	writes [][]byte
}

func (f *fakePTY) Write(p []byte) (int, error) {
	b := make([]byte, len(p))
	copy(b, p)
	f.writes = append(f.writes, b)
	return len(p), nil
}

// TestR4_RunnerDeliverer_S2_PTYWritesPassThrough pins S2 — when
// pty is wired, Deliver writes the user-text Parts to the PTY (+\n
// for claude-TUI submit) AFTER the pump's Subscribed signal.
//
// We use a fakePTY for write-capture + a fakeSubscriberSource for
// the pump's Subscribe call. The pump never actually fires
// silence-finalize because the fake never closes — that's covered
// in TestR4_RunnerDeliverer_S3.
func TestR4_RunnerDeliverer_S2_PTYWritesPassThrough(t *testing.T) {
	store := newTestStore(t)
	pty := &fakePTY{}
	src := newR4FakeSubscriberSource(64)
	broker := &fakeBroker{}

	d := newRunnerDeliverer(store, "test-sid").withPTY(src, pty, broker)
	msg := a2a.Message{
		ContextID: "ctx-1",
		Parts: []a2a.Part{
			{Kind: "text", Text: "Hello, "},
			{Kind: "text", Text: "world!"},
		},
	}
	task, err := d.Deliver(context.Background(), msg)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if task.Status.State != a2a.TaskStateWorking {
		t.Errorf("S2 FAIL: state = %q, want working", task.Status.State)
	}
	if len(pty.writes) == 0 {
		t.Fatalf("S2 FAIL: no PTY writes captured")
	}
	// K1 #472 — PTY now receives the knock marker (NOT user text).
	// Per §10 step 12 the agent's pattern detector sees the marker +
	// calls chepherd.get_task to fetch the message body. Format:
	//   [chepherd-knock taskID=<uuid> from=<sub>]\n
	// No JWT in this test → from="anonymous".
	got := string(pty.writes[0])
	const prefix = "[chepherd-knock taskID="
	const suffix = " from=anonymous]\n"
	if !strings.HasPrefix(got, prefix) {
		t.Errorf("S2 FAIL: PTY write = %q, want prefix %q (K1 knock format)", got, prefix)
	}
	if !strings.HasSuffix(got, suffix) {
		t.Errorf("S2 FAIL: PTY write = %q, want suffix %q (K1 knock format)", got, suffix)
	}
	// Verify embedded taskID matches the returned task.
	gotTaskID := strings.TrimSuffix(strings.TrimPrefix(got, prefix), suffix)
	if gotTaskID != task.ID {
		t.Errorf("S2 FAIL: knock taskID = %q, want %q", gotTaskID, task.ID)
	}
}

// TestR4_RunnerDeliverer_S3_CompleterFlipsState pins S3 — calling
// the completer directly (simulating silence-finalize) flips Task
// state in the store to "completed" + stores the ANSI-stripped
// response in OutputBlob as an artifact.
func TestR4_RunnerDeliverer_S3_CompleterFlipsState(t *testing.T) {
	store := newTestStore(t)
	d := newRunnerDeliverer(store, "test-sid")

	// First persist a working task as the precondition.
	msg := a2a.Message{ContextID: "ctx-3", Parts: []a2a.Part{{Kind: "text", Text: "go"}}}
	task, err := d.Deliver(context.Background(), msg)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	// Fire the completer with an ANSI-laced response.
	completer := d.completer()
	completer(task.ID, "\x1b[1mbold\x1b[0m response\n")

	// Re-read the row + assert.
	rec, err := store.Tasks().Get(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if rec.State != string(a2a.TaskStateCompleted) {
		t.Errorf("S3 FAIL: state = %q, want completed", rec.State)
	}
	var out a2a.Task
	if err := json.Unmarshal(rec.OutputBlob, &out); err != nil {
		t.Fatalf("decode OutputBlob: %v", err)
	}
	if out.Status.State != a2a.TaskStateCompleted {
		t.Errorf("S3 FAIL: OutputBlob.status.state = %q, want completed", out.Status.State)
	}
	if len(out.Artifacts) == 0 || len(out.Artifacts[0].Parts) == 0 {
		t.Fatalf("S3 FAIL: no artifacts/parts in OutputBlob")
	}
	text := out.Artifacts[0].Parts[0].Text
	if strings.Contains(text, "\x1b") {
		t.Errorf("S3 FAIL: response still contains ANSI escapes: %q", text)
	}
	if !strings.Contains(text, "bold response") {
		t.Errorf("S3 FAIL: response missing expected content: %q", text)
	}
}

// TestR4_ExtractMessageText_S4 pins S4 — multiple text parts
// concatenate, non-text parts are skipped.
func TestR4_ExtractMessageText_S4(t *testing.T) {
	got := extractMessageText(a2a.Message{
		Parts: []a2a.Part{
			{Kind: "text", Text: "A"},
			{Kind: "file", File: &a2a.FilePayload{URI: "ignored.txt"}},
			{Kind: "text", Text: "B"},
		},
	})
	if got != "AB" {
		t.Errorf("S4 FAIL: got %q, want AB", got)
	}
}

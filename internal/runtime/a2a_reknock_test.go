// internal/runtime/a2a_reknock_test.go — #79 re-knock watchdog.
//
// Pins the observation-based recovery: when a non-claude agent gets a
// knock but never calls chepherd.get_task (opencode/Groq malformed
// tool-call → no retry; or stacked-knock stall), the daemon re-injects
// the knock — UNLESS get_task was seen (MarkTaskFetched) or the task
// already left "working". Drives reKnockWatch directly with a fake
// Inject sink + the captureTaskRepo, with the delay shrunk via
// CHEPHERD_REKNOCK_DELAY_MS so the test is fast + deterministic.
package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/persistence"
)

// injectSink records every Inject call so the test can count re-knocks
// and assert the marker bytes.
type injectSink struct {
	mu    sync.Mutex
	calls [][]byte
}

func (s *injectSink) Inject(p []byte) (int, error) {
	s.mu.Lock()
	cp := append([]byte(nil), p...)
	s.calls = append(s.calls, cp)
	s.mu.Unlock()
	return len(p), nil
}

// count returns the number of distinct knock RE-injections — i.e. how
// many times the marker line ("[chepherd-knock") was written. This is the
// reKnock-attempt count (the bare CR submit writes carry no marker).
func (s *injectSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if strings.Contains(string(c), "[chepherd-knock ") {
			n++
		}
	}
	return n
}

func (s *injectSink) joined() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, c := range s.calls {
		b.Write(c)
	}
	return b.String()
}

// fastReknock shrinks the watchdog delay so tests don't wait 30s. Returns
// a restore fn.
func fastReknock(t *testing.T, delayMS, max string) {
	t.Helper()
	t.Setenv("CHEPHERD_REKNOCK_DELAY_MS", delayMS)
	t.Setenv("CHEPHERD_REKNOCK_MAX", max)
}

func newReknockDeliverer(store persistence.TaskRepository) *A2ADeliverer {
	d := &A2ADeliverer{}
	if store != nil {
		d.SetTaskStore(store, "test")
	}
	return d
}

// workingTaskRow persists a "working" task row for taskID so the
// watchdog's taskTerminalOrGone check sees it as still in flight.
func workingTaskRow(t *testing.T, store persistence.TaskRepository, taskID string) {
	t.Helper()
	rec := &persistence.Task{
		ID:        taskID,
		State:     string(a2a.TaskStateWorking),
		Method:    "message/send",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.Save(context.Background(), rec); err != nil {
		t.Fatalf("seed working task: %v", err)
	}
}

// TestReKnock_FiresWhenGetTaskNeverCalled — the core recovery path: a
// "working" task whose get_task never fired must be re-knocked exactly
// reKnockMax times.
func TestReKnock_FiresWhenGetTaskNeverCalled(t *testing.T) {
	fastReknock(t, "20", "2")
	store := &captureTaskRepo{}
	d := newReknockDeliverer(store)
	workingTaskRow(t, store, "task-stuck")
	sink := &injectSink{}

	d.reKnockWatch(sink, "opencode", "task-stuck", "operator")

	if got := sink.count(); got != 2 {
		t.Fatalf("re-knock count = %d, want 2 (reKnockMax)", got)
	}
	// Each re-knock must carry the marker + the ACTION-REQUIRED directive
	// for a non-claude flavor.
	body := sink.joined()
	if !strings.Contains(body, "[chepherd-knock taskID=task-stuck") {
		t.Errorf("re-knock body missing marker: %q", body)
	}
	if !strings.Contains(body, "ACTION REQUIRED") {
		t.Errorf("re-knock body missing ACTION REQUIRED directive (non-claude): %q", body)
	}
}

// TestReKnock_SkipsWhenGetTaskSeen — MarkTaskFetched short-circuits the
// watchdog: a fetched task must NOT be re-knocked.
func TestReKnock_SkipsWhenGetTaskSeen(t *testing.T) {
	fastReknock(t, "20", "2")
	store := &captureTaskRepo{}
	d := newReknockDeliverer(store)
	workingTaskRow(t, store, "task-fetched")
	sink := &injectSink{}

	// Simulate the recipient calling get_task before the watchdog's first
	// check by marking it fetched up front.
	d.MarkTaskFetched("task-fetched")
	d.reKnockWatch(sink, "opencode", "task-fetched", "operator")

	if got := sink.count(); got != 0 {
		t.Fatalf("re-knock count = %d, want 0 (get_task was seen)", got)
	}
}

// TestReKnock_SkipsWhenTaskTerminal — a task that already left "working"
// (e.g. silence-finalize completed it) must NOT be re-knocked even if
// get_task wasn't recorded.
func TestReKnock_SkipsWhenTaskTerminal(t *testing.T) {
	fastReknock(t, "20", "2")
	store := &captureTaskRepo{}
	d := newReknockDeliverer(store)
	// Persist a COMPLETED row directly.
	rec := &persistence.Task{
		ID:        "task-done",
		State:     string(a2a.TaskStateCompleted),
		Method:    "message/send",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.Save(context.Background(), rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sink := &injectSink{}

	d.reKnockWatch(sink, "opencode", "task-done", "operator")

	if got := sink.count(); got != 0 {
		t.Fatalf("re-knock count = %d, want 0 (task already terminal)", got)
	}
}

// TestReKnock_DisabledByMaxZero — CHEPHERD_REKNOCK_MAX=0 opts out
// entirely.
func TestReKnock_DisabledByMaxZero(t *testing.T) {
	fastReknock(t, "20", "0")
	store := &captureTaskRepo{}
	d := newReknockDeliverer(store)
	workingTaskRow(t, store, "task-optout")
	sink := &injectSink{}

	d.reKnockWatch(sink, "opencode", "task-optout", "operator")

	if got := sink.count(); got != 0 {
		t.Fatalf("re-knock count = %d, want 0 (REKNOCK_MAX=0 disables)", got)
	}
}

// TestReKnock_StopsMidLoopWhenFetched — if get_task fires AFTER the first
// re-knock, the watchdog must stop (not exhaust reKnockMax).
func TestReKnock_StopsMidLoopWhenFetched(t *testing.T) {
	fastReknock(t, "40", "3")
	store := &captureTaskRepo{}
	d := newReknockDeliverer(store)
	workingTaskRow(t, store, "task-mid")
	sink := &injectSink{}

	done := make(chan struct{})
	go func() {
		d.reKnockWatch(sink, "opencode", "task-mid", "operator")
		close(done)
	}()

	// Let the first re-knock fire (after ~40ms), then mark fetched so the
	// second check short-circuits.
	time.Sleep(60 * time.Millisecond)
	d.MarkTaskFetched("task-mid")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not return after get_task seen")
	}

	if got := sink.count(); got == 0 || got >= 3 {
		t.Fatalf("re-knock count = %d, want 1 or 2 (stopped after fetch, not full max)", got)
	}
}

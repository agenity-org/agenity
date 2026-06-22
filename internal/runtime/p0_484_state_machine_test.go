// internal/runtime/p0_484_state_machine_test.go pins the v0.9.4
// §16 deliverer-side state-machine integration (#484 Wave A5):
//   - GrantCheck wired to a deny stub produces a REJECTED Task
//     instead of a WORKING one + emits a `done` event on the
//     broker so SSE / webhook consumers see the denial.
//   - GrantCheck nil leaves Deliver in pre-A5 behavior (back-
//     compat for intra-org deployments).
//   - makeDecideStateFn returns the right TaskState for an
//     agentpatterns-detected idle signal.
//
// Refs #484 V0.9.2-ARCHITECTURE.md §16 #485 #482.
package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/agenity-org/agenity/internal/a2a"
)

// captureBroker records Publish calls so tests can assert what
// the deliverer fanned out. Implements brokerPublisher.
type captureBroker struct {
	events []a2a.StreamEvent
}

func (c *captureBroker) Publish(taskID string, ev a2a.StreamEvent) int {
	c.events = append(c.events, ev)
	return 1
}

func TestWaveA5_GrantDeny_TaskIsRejectedNotFailed(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := NewA2ADeliverer(rt)
	broker := &captureBroker{}
	d.broker = broker
	d.SetGrantCheck(func(callerSID, targetSID string) (bool, string) {
		return false, "policy denies caller→nonexistent for this org"
	})

	task, err := d.Deliver(context.Background(), a2a.Message{
		ContextID: "any-session",
		Parts:     []a2a.Part{{Kind: "text", Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Deliver unexpected error: %v", err)
	}
	if task == nil || task.Status.State != a2a.TaskStateRejected {
		t.Fatalf("Task state = %v, want REJECTED", task)
	}
	if len(broker.events) != 1 {
		t.Fatalf("broker events = %d, want 1 (rejection fan-out)", len(broker.events))
	}
	ev := broker.events[0]
	if ev.Type != "done" || ev.Task.Status.State != a2a.TaskStateRejected {
		t.Errorf("event = %+v, want done+REJECTED", ev)
	}
	if task.Status.Message == nil ||
		len(task.Status.Message.Parts) == 0 ||
		!strings.Contains(task.Status.Message.Parts[0].Text, "policy denies") {
		t.Errorf("rejection reason not propagated: %+v", task.Status.Message)
	}
}

func TestWaveA5_GrantNil_NoChangeInDeliveryPath(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := NewA2ADeliverer(rt)
	// grantCheck stays nil — pre-A5 behavior.

	task, err := d.Deliver(context.Background(), a2a.Message{
		ContextID: "nonexistent-session",
		Parts:     []a2a.Part{{Kind: "text", Text: "hi"}},
	})
	if err == nil {
		t.Error("expected error for nonexistent session, got nil")
	}
	// Without grantCheck, we should reach session-lookup which fails,
	// producing FAILED (not REJECTED).
	if task != nil && task.Status.State == a2a.TaskStateRejected {
		t.Error("nil grantCheck should NOT produce REJECTED — that's grant-check-specific")
	}
}

func TestWaveA5_MakeDecideStateFn_ClaudeCompletes(t *testing.T) {
	t.Parallel()
	decide := makeDecideStateFn("claude-code")
	// Bytes that don't match any input/auth pattern → COMPLETED.
	got := decide([]byte("plain response without any signal"))
	if got != a2a.TaskStateCompleted {
		t.Errorf("decide(plain) = %q, want COMPLETED", got)
	}
}

func TestWaveA5_MakeDecideStateFn_ClaudeAuthRequired(t *testing.T) {
	t.Parallel()
	decide := makeDecideStateFn("claude-code")
	got := decide([]byte("To continue, Authorize at: https://github.com/login/oauth/authorize?client=abc"))
	if got != a2a.TaskStateAuthRequired {
		t.Errorf("decide(oauth) = %q, want AUTH_REQUIRED", got)
	}
}

func TestWaveA5_MakeDecideStateFn_ClaudeInputRequired(t *testing.T) {
	t.Parallel()
	decide := makeDecideStateFn("claude-code")
	got := decide([]byte("Could you clarify which file you meant?"))
	if got != a2a.TaskStateInputRequired {
		t.Errorf("decide(question) = %q, want INPUT_REQUIRED", got)
	}
}

func TestWaveA5_MakeDecideStateFn_UnknownSlugCompletes(t *testing.T) {
	t.Parallel()
	decide := makeDecideStateFn("does-not-exist")
	// Noop flavor → always COMPLETED regardless of bytes.
	got := decide([]byte("Could you clarify? Authorize at: https://x/oauth/y"))
	if got != a2a.TaskStateCompleted {
		t.Errorf("Noop flavor should default to COMPLETED, got %q", got)
	}
}

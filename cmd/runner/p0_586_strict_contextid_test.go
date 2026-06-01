// cmd/runner/p0_586_strict_contextid_test.go — regression guard for
// #586. Runner Deliver MUST reject SendMessage when params.contextId
// doesn't match this runner's --sid. Pre-#586 the runner auto-created
// a task with a NEW UUID for ANY contextId, diverging from daemon
// behavior (which returned -32603 InternalError) — QA C.3 caught.
//
// Refs #586 #561 #566 docs/v094-qa/categoryC-evidence.md C.3.
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/chepherd/chepherd/internal/a2a"
)

func TestP0_586_Deliver_RejectsUnknownContextID(t *testing.T) {
	store := newTestStore(t)
	d := newRunnerDeliverer(store, "actual-runner-sid")
	msg := a2a.Message{
		ContextID: "no-such-session",
		Parts:     []a2a.Part{{Kind: "text", Text: "hello"}},
	}
	task, err := d.Deliver(context.Background(), msg)
	if err == nil {
		t.Fatalf("expected error for unknown contextId, got task=%+v", task)
	}
	// Error message should be diagnostic — name both the claimed
	// contextId AND the runner's actual sid so operators debugging a
	// cross-runner mis-route can see exactly what was attempted.
	if !strings.Contains(err.Error(), "no-such-session") {
		t.Errorf("error should name the unknown contextId, got: %v", err)
	}
	if !strings.Contains(err.Error(), "actual-runner-sid") {
		t.Errorf("error should name the runner's actual sid, got: %v", err)
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("error should say 'does not match', got: %v", err)
	}
}

func TestP0_586_Deliver_AcceptsMatchingContextID(t *testing.T) {
	// Sanity: when contextId matches the runner's sid, Deliver
	// proceeds as before (no false-positive rejection).
	store := newTestStore(t)
	d := newRunnerDeliverer(store, "actual-runner-sid")
	msg := a2a.Message{
		ContextID: "actual-runner-sid",
		Parts:     []a2a.Part{{Kind: "text", Text: "hello"}},
	}
	task, err := d.Deliver(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error for matching contextId: %v", err)
	}
	if task == nil || task.ContextID != "actual-runner-sid" {
		t.Errorf("expected task with matching contextId, got: %+v", task)
	}
}

// (Empty-runnerSID edge case omitted: the persist layer rejects
// empty RunnerSID independently, so the strict-check bypass for
// empty sid is structurally unreachable. Production wiring always
// passes a non-empty --sid per the runner CLI required-flag
// validation.)

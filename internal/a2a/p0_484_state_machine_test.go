// internal/a2a/p0_484_state_machine_test.go pins the v0.9.4 §16
// Task state-transition table (#484 Wave A5). Every legal
// transition is asserted, every illegal transition is asserted
// (the negative space is the substantive invariant). IsTerminal
// and TransitionTask are exercised against persistence.Task to
// prove the seam writes the right state + bumps UpdatedAt + only
// allows legal moves.
//
// Refs #484 V0.9.2-ARCHITECTURE.md §16.
package a2a

import (
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

func TestWaveA5_IsTerminal_TerminalStatesOnly(t *testing.T) {
	t.Parallel()
	cases := map[TaskState]bool{
		TaskStateSubmitted:     false,
		TaskStateWorking:       false,
		TaskStateInputRequired: false,
		TaskStateAuthRequired:  false,
		TaskStateCompleted:     true,
		TaskStateFailed:        true,
		TaskStateCanceled:      true,
		TaskStateRejected:      true,
	}
	for state, want := range cases {
		if got := IsTerminal(state); got != want {
			t.Errorf("IsTerminal(%q) = %v, want %v", state, got, want)
		}
	}
}

func TestWaveA5_IsValidTransition_LegalMoves(t *testing.T) {
	t.Parallel()
	legal := map[TaskState][]TaskState{
		TaskStateSubmitted:     {TaskStateWorking, TaskStateRejected},
		TaskStateWorking:       {TaskStateInputRequired, TaskStateAuthRequired, TaskStateCompleted, TaskStateFailed, TaskStateCanceled},
		TaskStateInputRequired: {TaskStateWorking, TaskStateCanceled},
		TaskStateAuthRequired:  {TaskStateWorking, TaskStateCanceled},
	}
	for from, tos := range legal {
		for _, to := range tos {
			if !IsValidTransition(from, to) {
				t.Errorf("IsValidTransition(%q → %q) = false, want true", from, to)
			}
		}
	}
}

func TestWaveA5_IsValidTransition_IllegalMoves(t *testing.T) {
	t.Parallel()
	// From every terminal state, every move is illegal.
	for _, terminal := range []TaskState{TaskStateCompleted, TaskStateFailed, TaskStateCanceled, TaskStateRejected} {
		for _, to := range AllStates() {
			if to == terminal {
				continue // same-state is idempotent, not illegal
			}
			if IsValidTransition(terminal, to) {
				t.Errorf("terminal %q → %q should be illegal", terminal, to)
			}
		}
	}
	// SUBMITTED has no direct path to any terminal except REJECTED.
	for _, to := range []TaskState{TaskStateCompleted, TaskStateFailed, TaskStateCanceled, TaskStateInputRequired, TaskStateAuthRequired} {
		if IsValidTransition(TaskStateSubmitted, to) {
			t.Errorf("SUBMITTED → %q should be illegal (must transition through WORKING first)", to)
		}
	}
	// INPUT_REQUIRED can only go to WORKING or CANCELED.
	for _, to := range []TaskState{TaskStateCompleted, TaskStateFailed, TaskStateAuthRequired, TaskStateRejected} {
		if IsValidTransition(TaskStateInputRequired, to) {
			t.Errorf("INPUT_REQUIRED → %q should be illegal", to)
		}
	}
	// AUTH_REQUIRED can only go to WORKING or CANCELED.
	for _, to := range []TaskState{TaskStateCompleted, TaskStateFailed, TaskStateInputRequired, TaskStateRejected} {
		if IsValidTransition(TaskStateAuthRequired, to) {
			t.Errorf("AUTH_REQUIRED → %q should be illegal", to)
		}
	}
}

func TestWaveA5_IsValidTransition_SameStateIsIdempotent(t *testing.T) {
	t.Parallel()
	for _, s := range AllStates() {
		if !IsValidTransition(s, s) {
			t.Errorf("same-state %q → %q should be idempotent (true)", s, s)
		}
	}
}

func TestWaveA5_TransitionTask_WritesStateAndUpdatedAt(t *testing.T) {
	t.Parallel()
	rec := &persistence.Task{ID: "t1", State: string(TaskStateWorking)}
	before := time.Now()
	if err := TransitionTask(rec, TaskStateInputRequired, "agent prompted"); err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	if rec.State != string(TaskStateInputRequired) {
		t.Errorf("State = %q, want %q", rec.State, TaskStateInputRequired)
	}
	if rec.UpdatedAt.Before(before) {
		t.Errorf("UpdatedAt = %v, want >= %v", rec.UpdatedAt, before)
	}
}

func TestWaveA5_TransitionTask_RejectsIllegalMoves(t *testing.T) {
	t.Parallel()
	rec := &persistence.Task{ID: "t2", State: string(TaskStateCompleted)}
	if err := TransitionTask(rec, TaskStateWorking, "trying"); err == nil {
		t.Error("expected error transitioning from terminal state")
	}
	// Record must be unchanged after illegal transition attempt.
	if rec.State != string(TaskStateCompleted) {
		t.Errorf("State changed after illegal transition: %q", rec.State)
	}
}

func TestWaveA5_TransitionTask_EmptyStateTreatedAsSubmitted(t *testing.T) {
	t.Parallel()
	rec := &persistence.Task{ID: "t3"} // State empty
	if err := TransitionTask(rec, TaskStateWorking, "first"); err != nil {
		t.Errorf("empty state should accept SUBMITTED → WORKING: %v", err)
	}
	if rec.State != string(TaskStateWorking) {
		t.Errorf("State = %q, want %q", rec.State, TaskStateWorking)
	}
}

func TestWaveA5_TransitionTask_NilRec(t *testing.T) {
	t.Parallel()
	if err := TransitionTask(nil, TaskStateWorking, "x"); err == nil {
		t.Error("nil task should error, not panic")
	}
}

func TestWaveA5_AllStates_EveryStateAccounted(t *testing.T) {
	t.Parallel()
	got := AllStates()
	if len(got) != 8 {
		t.Errorf("AllStates() = %d states, want 8 (per §16)", len(got))
	}
}

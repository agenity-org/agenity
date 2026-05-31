// internal/a2a/state_machine.go enforces the V0.9.2-ARCHITECTURE.md
// §16 + A2A v1.0 Task state-transition table (#484 Wave A5).
//
// Per the §16 contract:
//
//   SUBMITTED      → WORKING | REJECTED
//   WORKING        → INPUT_REQUIRED | AUTH_REQUIRED | COMPLETED | FAILED | CANCELED
//   INPUT_REQUIRED → WORKING | CANCELED
//   AUTH_REQUIRED  → WORKING | CANCELED
//   COMPLETED      → ∅  (terminal)
//   FAILED         → ∅  (terminal)
//   CANCELED       → ∅  (terminal)
//   REJECTED       → ∅  (terminal)
//
// All Task writes that change state SHOULD go through TransitionTask so
// the invariants are enforced uniformly. Free-form `rec.State = ...`
// assignments leak past the table and can produce illegal states (e.g.
// an already-COMPLETED task being flipped back to WORKING) that break
// downstream observers.
//
// Refs #484 V0.9.2-ARCHITECTURE.md §16.
package a2a

import (
	"fmt"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// IsTerminal reports whether `s` is a terminal state — one for which
// no outgoing transition is legal per the §16 contract. SSE brokers
// close subscriptions on terminal-state events; webhook consumers
// can treat the call as the final notification for the task.
func IsTerminal(s TaskState) bool {
	switch s {
	case TaskStateCompleted, TaskStateFailed, TaskStateCanceled, TaskStateRejected:
		return true
	}
	return false
}

// allowedTransitions enumerates legal `from → to` moves. Used by
// IsValidTransition + TransitionTask. Kept as a static table (not a
// regex / not a slice scan) so additions are reviewable as a
// diff-of-set and so the transition cost stays O(1).
var allowedTransitions = map[TaskState]map[TaskState]bool{
	TaskStateSubmitted: {
		TaskStateWorking:  true,
		TaskStateRejected: true,
	},
	TaskStateWorking: {
		TaskStateInputRequired: true,
		TaskStateAuthRequired:  true,
		TaskStateCompleted:     true,
		TaskStateFailed:        true,
		TaskStateCanceled:      true,
	},
	TaskStateInputRequired: {
		TaskStateWorking:  true,
		TaskStateCanceled: true,
	},
	TaskStateAuthRequired: {
		TaskStateWorking:  true,
		TaskStateCanceled: true,
	},
}

// IsValidTransition reports whether moving from `from` to `to` is
// allowed by the §16 contract. Terminal `from` states return false
// for every `to`; same-state transitions return true (TransitionTask
// no-ops on those, but pure predicate callers may want the
// idempotent answer).
func IsValidTransition(from, to TaskState) bool {
	if from == to {
		return true
	}
	if IsTerminal(from) {
		return false
	}
	allowed, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

// TransitionTask updates rec.State to `to` if the move is legal under
// the §16 contract. Returns an error on illegal transitions; no-op on
// same-state writes (the timestamp is still bumped so observers see a
// fresh UpdatedAt). `reason` is currently emitted via the StreamEvent
// + recorded as the future-Wave reason field; today's persistence
// schema keeps only the state, so the reason is not durably stored.
func TransitionTask(rec *persistence.Task, to TaskState, reason string) error {
	if rec == nil {
		return fmt.Errorf("TransitionTask: nil task")
	}
	from := TaskState(rec.State)
	if from == "" {
		// Empty state on a newly-allocated record: treat as
		// SUBMITTED for the purposes of the first transition.
		from = TaskStateSubmitted
	}
	if !IsValidTransition(from, to) {
		return fmt.Errorf("illegal transition: %s → %s (reason: %s)", from, to, reason)
	}
	rec.State = string(to)
	rec.UpdatedAt = time.Now().UTC()
	return nil
}

// AllStates returns every defined TaskState in the §16 contract. Used
// by tests + the dashboard's state-filter dropdown so additions to
// the enum surface automatically.
func AllStates() []TaskState {
	return []TaskState{
		TaskStateSubmitted,
		TaskStateWorking,
		TaskStateInputRequired,
		TaskStateAuthRequired,
		TaskStateCompleted,
		TaskStateFailed,
		TaskStateCanceled,
		TaskStateRejected,
	}
}

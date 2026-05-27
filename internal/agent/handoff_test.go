package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newBound(t *testing.T, s *Store, op uuid.UUID) *Agent {
	t.Helper()
	a := New("claude-code", "test-bound", "")
	_ = s.Save(a)
	if err := s.Bind(a.ID, op); err != nil {
		t.Fatalf("initial Bind: %v", err)
	}
	got, _ := s.Get(a.ID)
	if got.State != StateActive || got.CurrentOperator == nil || *got.CurrentOperator != op {
		t.Fatalf("Bind didn't set ACTIVE(%s): %+v", op, got)
	}
	return got
}

// Covers acceptance: "Two operators (op1, op2) can hand off cleanly:
// op1 sees notification, releases or times-out, op2 binds".
func TestHandoffHappyPath(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	opA := uuid.New()
	opB := uuid.New()
	a := newBound(t, s, opA)

	// Request — opA hands off to opB.
	ev, err := s.RequestHandoff(a.ID, opA, opB, "shift change")
	if err != nil {
		t.Fatalf("RequestHandoff: %v", err)
	}
	if ev.From != opA || ev.To != opB || ev.Reason != "shift change" {
		t.Fatalf("event content wrong: %+v", ev)
	}

	got, _ := s.Get(a.ID)
	if got.State != StateHandoffPending {
		t.Fatalf("state=%s, expected handoff_pending", got.State)
	}
	if got.PendingHandoffTo == nil || *got.PendingHandoffTo != opB {
		t.Fatalf("PendingHandoffTo not set to opB")
	}

	// opA releases.
	if err := s.Release(a.ID, opA, nil); err != nil {
		t.Fatalf("Release: %v", err)
	}
	got, _ = s.Get(a.ID)
	if got.State != StateUnbound || got.CurrentOperator != nil {
		t.Fatalf("after Release expected UNBOUND + unbound: %+v", got)
	}

	// opB binds.
	if err := s.Bind(a.ID, opB); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	got, _ = s.Get(a.ID)
	if got.State != StateActive || got.CurrentOperator == nil || *got.CurrentOperator != opB {
		t.Fatalf("after Bind expected ACTIVE(opB): %+v", got)
	}

	// Audit log shows one completed event.
	events, _ := s.HandoffEvents(a.ID)
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(events))
	}
	if events[0].CompletedAt == nil || events[0].Outcome != "completed" {
		t.Fatalf("event not marked completed: %+v", events[0])
	}
}

// Covers acceptance: "60s timeout auto-releases if relinquishing
// operator is AFK". We shrink HandoffTimeout to keep the test fast.
func TestHandoffTimeoutAutoRelease(t *testing.T) {
	old := HandoffTimeout
	HandoffTimeout = 50 * time.Millisecond
	defer func() { HandoffTimeout = old }()

	s, _ := NewStore(t.TempDir())
	opA, opB := uuid.New(), uuid.New()
	a := newBound(t, s, opA)
	_, _ = s.RequestHandoff(a.ID, opA, opB, "AFK case")

	// Sweep with now=before-timeout → no-op.
	if n, _ := s.SweepTimeouts(time.Now().UTC()); n != 0 {
		t.Fatalf("premature timeout sweep: n=%d", n)
	}

	time.Sleep(80 * time.Millisecond)

	// Sweep again → timeout fires.
	if n, _ := s.SweepTimeouts(time.Now().UTC()); n != 1 {
		t.Fatalf("expected 1 timeout, got %d", n)
	}
	got, _ := s.Get(a.ID)
	if got.State != StateUnbound {
		t.Fatalf("after timeout sweep expected UNBOUND, got %s", got.State)
	}

	// opB should still be able to bind (PendingHandoffTo preserved).
	if err := s.Bind(a.ID, opB); err != nil {
		t.Fatalf("Bind after timeout: %v", err)
	}

	// Audit log shows timed-out.
	events, _ := s.HandoffEvents(a.ID)
	if events[0].Outcome != "timed-out" {
		t.Fatalf("expected timed-out outcome, got %q", events[0].Outcome)
	}
}

// Covers acceptance: "Admin force-release reaches the same final state;
// logged distinctly".
func TestHandoffForceRelease(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	opA, opB := uuid.New(), uuid.New()
	admin := uuid.New()
	a := newBound(t, s, opA)
	_, _ = s.RequestHandoff(a.ID, opA, opB, "stalled op")

	// Non-admin can't release as someone else.
	if err := s.Release(a.ID, opB, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("opB shouldn't release on opA's behalf: %v", err)
	}

	// Admin force-release.
	if err := s.Release(a.ID, opA, &admin); err != nil {
		t.Fatalf("force-release: %v", err)
	}
	got, _ := s.Get(a.ID)
	if got.State != StateUnbound {
		t.Fatalf("force-release didn't reach UNBOUND")
	}

	events, _ := s.HandoffEvents(a.ID)
	if events[0].Outcome != "forced" || events[0].ForcedBy == nil || *events[0].ForcedBy != admin {
		t.Fatalf("force-release audit metadata wrong: %+v", events[0])
	}
}

// Self-handoff rejection.
func TestHandoffSelfRejected(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	op := uuid.New()
	a := newBound(t, s, op)
	if _, err := s.RequestHandoff(a.ID, op, op, ""); !errors.Is(err, ErrSelfHandoff) {
		t.Fatalf("self-handoff should be rejected, got %v", err)
	}
}

// Non-active state rejects RequestHandoff.
func TestHandoffNotActiveRejected(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	op := uuid.New()
	a := New("claude-code", "n", "")
	_ = s.Save(a) // never bound → StateUnbound
	if _, err := s.RequestHandoff(a.ID, op, uuid.New(), ""); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState, got %v", err)
	}
}

// Non-current operator can't initiate handoff.
func TestHandoffWrongCurrentOperator(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	opA, opB := uuid.New(), uuid.New()
	stranger := uuid.New()
	a := newBound(t, s, opA)
	if _, err := s.RequestHandoff(a.ID, stranger, opB, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-current op should be forbidden, got %v", err)
	}
}

// Wrong binder for pending handoff is rejected.
func TestHandoffWrongBinder(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	opA, opB := uuid.New(), uuid.New()
	intruder := uuid.New()
	a := newBound(t, s, opA)
	_, _ = s.RequestHandoff(a.ID, opA, opB, "")
	_ = s.Release(a.ID, opA, nil)
	if err := s.Bind(a.ID, intruder); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-addressee binder should be forbidden, got %v", err)
	}
}

// Package agent — operator-to-operator agent handoff (#173).
//
// State machine:
//
//	ACTIVE(op=A) → HANDOFF_PENDING(from=A,to=B) → UNBOUND → ACTIVE(op=B)
//
// Two paths from HANDOFF_PENDING:
//  1. opA voluntarily Release() → state = UNBOUND, opB Bind()s
//  2. 60-second timeout → state = UNBOUND, opB Bind()s automatically
//  3. ForceRelease (admin) → same final state but ForcedBy attribution
//
// Audit log is append-only — one HandoffEvent per request, completed-or-
// not. Stored in a sibling file to the Agent JSON.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

// HandoffEvent is one entry in the per-agent audit log. Append-only.
type HandoffEvent struct {
	AgentID     uuid.UUID  `json:"agent_id"`
	From        uuid.UUID  `json:"from"`
	To          uuid.UUID  `json:"to"`
	Reason      string     `json:"reason,omitempty"`
	RequestedAt time.Time  `json:"requested_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	ForcedBy    *uuid.UUID `json:"forced_by,omitempty"`
	// Outcome: "completed" | "rejected" | "timed-out" | "forced".
	// Empty while still pending.
	Outcome string `json:"outcome,omitempty"`
}

// Default timeout — exposed as a var so tests can shrink it.
var HandoffTimeout = 60 * time.Second

// RequestHandoff initiates the handoff protocol. The caller (CurrentOperator)
// asks for ownership to be transferred to `to`. Returns ErrInvalidState if
// the agent isn't currently ACTIVE.
//
// Side effects:
//   - Agent.State → HANDOFF_PENDING
//   - Agent.PendingHandoffTo / Reason / At recorded
//   - One HandoffEvent appended to the audit log
//
// Errors:
//   - ErrSelfHandoff if requester == current operator
//   - ErrInvalidState if not currently ACTIVE
//   - ErrNotFound if agent doesn't exist
func (s *Store) RequestHandoff(agentID, requester, to uuid.UUID, reason string) (*HandoffEvent, error) {
	if requester == to {
		return nil, ErrSelfHandoff
	}
	a, err := s.Get(agentID)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, ErrNotFound
	}
	if a.State != StateActive {
		return nil, fmt.Errorf("%w: state=%s, expected active", ErrInvalidState, a.State)
	}
	if a.CurrentOperator == nil || *a.CurrentOperator != requester {
		return nil, fmt.Errorf("%w: only the current operator can hand off; requester is not the binder", ErrForbidden)
	}
	now := time.Now().UTC()
	a.State = StateHandoffPending
	a.PendingHandoffTo = &to
	a.PendingHandoffReason = reason
	a.PendingHandoffAt = &now
	if err := s.Save(a); err != nil {
		return nil, err
	}
	ev := &HandoffEvent{
		AgentID:     agentID,
		From:        requester,
		To:          to,
		Reason:      reason,
		RequestedAt: now,
	}
	if err := s.AppendHandoffEvent(ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// Release voluntarily relinquishes the agent from its current operator.
// Transitions HANDOFF_PENDING → UNBOUND OR ACTIVE → UNBOUND. The
// pending-handoff fields are cleared but PendingHandoffTo's intent is
// preserved in the latest open HandoffEvent so Bind() can complete it.
//
// `actor` is the operator performing the release. Must equal
// CurrentOperator unless `forcedBy` is non-nil (admin force-release).
func (s *Store) Release(agentID, actor uuid.UUID, forcedBy *uuid.UUID) error {
	a, err := s.Get(agentID)
	if err != nil {
		return err
	}
	if a == nil {
		return ErrNotFound
	}
	if forcedBy == nil {
		if a.CurrentOperator == nil || *a.CurrentOperator != actor {
			return fmt.Errorf("%w: only the current operator may release; use force-release for admin override", ErrForbidden)
		}
	}
	a.State = StateUnbound
	a.CurrentOperator = nil
	// PendingHandoffTo intentionally KEPT so Bind() knows whom to bind.
	// PendingHandoffReason/At kept for the audit trail until Bind() clears them.
	if err := s.Save(a); err != nil {
		return err
	}
	if forcedBy != nil {
		// Annotate the latest open HandoffEvent with the forced outcome.
		_ = s.completeLatestHandoff(agentID, "forced", forcedBy)
	}
	return nil
}

// Bind transitions UNBOUND → ACTIVE(op=binder). If there's a pending
// handoff intent, the binder MUST be the addressee; otherwise any
// operator can bind a truly-unbound agent (e.g. fresh spawn).
func (s *Store) Bind(agentID, binder uuid.UUID) error {
	a, err := s.Get(agentID)
	if err != nil {
		return err
	}
	if a == nil {
		return ErrNotFound
	}
	if a.State != StateUnbound {
		return fmt.Errorf("%w: state=%s, expected unbound", ErrInvalidState, a.State)
	}
	if a.PendingHandoffTo != nil && *a.PendingHandoffTo != binder {
		return fmt.Errorf("%w: agent has pending handoff to %s; binder %s is not the addressee",
			ErrForbidden, *a.PendingHandoffTo, binder)
	}
	a.State = StateActive
	a.CurrentOperator = &binder
	// Complete the audit log entry if a pending handoff drove this bind.
	hadPending := a.PendingHandoffTo != nil
	a.PendingHandoffTo = nil
	a.PendingHandoffReason = ""
	a.PendingHandoffAt = nil
	if err := s.Save(a); err != nil {
		return err
	}
	if hadPending {
		_ = s.completeLatestHandoff(agentID, "completed", nil)
	}
	return nil
}

// HandoffEvents returns the audit log for one agent, chronological.
func (s *Store) HandoffEvents(agentID uuid.UUID) ([]*HandoffEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := os.ReadFile(s.handoffPath(agentID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*HandoffEvent
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RequestedAt.Before(out[j].RequestedAt)
	})
	return out, nil
}

// AppendHandoffEvent appends to the per-agent audit log file.
// Exported for tests that pre-seed the log.
func (s *Store) AppendHandoffEvent(ev *HandoffEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, _ := os.ReadFile(s.handoffPath(ev.AgentID))
	var entries []*HandoffEvent
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &entries)
	}
	entries = append(entries, ev)
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.handoffPath(ev.AgentID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.handoffPath(ev.AgentID))
}

// completeLatestHandoff stamps the most-recent pending HandoffEvent with
// the given outcome. Idempotent — second call is a no-op.
func (s *Store) completeLatestHandoff(agentID uuid.UUID, outcome string, forcedBy *uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.handoffPath(agentID))
	if err != nil {
		return err
	}
	var entries []*HandoffEvent
	if err := json.Unmarshal(b, &entries); err != nil {
		return err
	}
	now := time.Now().UTC()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].CompletedAt == nil {
			entries[i].CompletedAt = &now
			entries[i].Outcome = outcome
			if forcedBy != nil {
				entries[i].ForcedBy = forcedBy
			}
			break
		}
	}
	out, _ := json.MarshalIndent(entries, "", "  ")
	tmp := s.handoffPath(agentID) + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.handoffPath(agentID))
}

// SweepTimeouts releases any HANDOFF_PENDING agents past the timeout.
// Caller should run this from a 1-second-tick goroutine. Returns the
// count of agents that timed out.
func (s *Store) SweepTimeouts(now time.Time) (int, error) {
	agents, err := s.List(ListOpts{})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, a := range agents {
		if a.State != StateHandoffPending || a.PendingHandoffAt == nil {
			continue
		}
		if now.Sub(*a.PendingHandoffAt) < HandoffTimeout {
			continue
		}
		// Timeout — release on behalf of the current operator. We
		// preserve PendingHandoffTo so Bind() can pick up.
		a.State = StateUnbound
		a.CurrentOperator = nil
		if err := s.Save(a); err != nil {
			continue
		}
		_ = s.completeLatestHandoff(a.ID, "timed-out", nil)
		count++
	}
	return count, nil
}

// handoffPath returns the audit-log file path for one agent.
func (s *Store) handoffPath(id uuid.UUID) string {
	return filepath.Join(s.dir, id.String()+".handoffs.json")
}

// Errors exposed to the HTTP layer for accurate status codes.
var (
	ErrNotFound     = errors.New("agent: not found")
	ErrInvalidState = errors.New("agent: invalid state for this transition")
	ErrSelfHandoff  = errors.New("agent: cannot hand off to self")
	ErrForbidden    = errors.New("agent: not authorised for this action")
)

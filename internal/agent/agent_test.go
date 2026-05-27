package agent

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Verifies the core lifecycle: new → save → get → list → soft-delete.
// Covers acceptance: "Agent objects persist across chepherd-daemon
// restarts" by re-opening a Store on the same dir.
func TestStoreLifecycle(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")

	s, err := NewStore(stateDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	a := New("claude-code", "alpha", "anthropic-personal")
	if a.ID == uuid.Nil {
		t.Fatal("UUID should be minted")
	}
	if a.PVCHandle != "chepherd-agent-"+a.ID.String() {
		t.Fatalf("PVC handle wrong: %s", a.PVCHandle)
	}
	if err := s.Save(a); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reopen Store — simulates daemon restart.
	s2, err := NewStore(stateDir)
	if err != nil {
		t.Fatalf("re-NewStore: %v", err)
	}
	got, err := s2.Get(a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil after reopen — persistence broken")
	}
	if got.Label != "alpha" || got.AgentType != "claude-code" {
		t.Fatalf("round-trip drift: %+v", got)
	}

	// List default — should include the saved agent.
	all, err := s2.List(ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(all))
	}

	// SoftDelete — should disappear from default list, reappear with IncludeDeleted.
	if err := s2.SoftDelete(a.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if all, _ := s2.List(ListOpts{}); len(all) != 0 {
		t.Fatalf("deleted agent should be hidden from default list")
	}
	if all, _ := s2.List(ListOpts{IncludeDeleted: true}); len(all) != 1 {
		t.Fatalf("deleted agent should reappear with IncludeDeleted")
	}
}

// Verifies attach/detach session bookkeeping.
func TestAttachDetachSession(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	a := New("claude-code", "beta", "")
	_ = s.Save(a)

	if err := s.AttachSession(a.ID, "sess-1"); err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	got, _ := s.Get(a.ID)
	if len(got.Sessions) != 1 || got.Sessions[0].SessionID != "sess-1" {
		t.Fatalf("Attach didn't append: %+v", got.Sessions)
	}
	if got.Sessions[0].DetachedAt != nil {
		t.Fatal("DetachedAt should be nil after attach")
	}

	time.Sleep(2 * time.Millisecond)
	if err := s.DetachSession(a.ID, "sess-1"); err != nil {
		t.Fatalf("DetachSession: %v", err)
	}
	got, _ = s.Get(a.ID)
	if got.Sessions[0].DetachedAt == nil {
		t.Fatal("Detach didn't set DetachedAt")
	}

	// Second detach is idempotent.
	if err := s.DetachSession(a.ID, "sess-1"); err != nil {
		t.Fatalf("re-DetachSession: %v", err)
	}
}

// Verifies Skills field can be set + persists.
func TestSetSkills(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	a := New("claude-code", "skilled", "")
	_ = s.Save(a)
	if err := s.SetSkills(a.ID, []string{"architect", "implementer"}); err != nil {
		t.Fatalf("SetSkills: %v", err)
	}
	got, _ := s.Get(a.ID)
	if len(got.Skills) != 2 || got.Skills[0] != "architect" || got.Skills[1] != "implementer" {
		t.Fatalf("Skills not persisted in order: %+v", got.Skills)
	}
	// Clear
	if err := s.SetSkills(a.ID, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = s.Get(a.ID)
	if len(got.Skills) != 0 {
		t.Fatalf("Skills not cleared: %+v", got.Skills)
	}
}

// Verifies SetLabel + SetOperator.
func TestSetters(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	a := New("claude-code", "gamma", "")
	_ = s.Save(a)

	if err := s.SetLabel(a.ID, "renamed"); err != nil {
		t.Fatalf("SetLabel: %v", err)
	}
	got, _ := s.Get(a.ID)
	if got.Label != "renamed" {
		t.Fatalf("label not updated")
	}

	opID := uuid.New()
	if err := s.SetOperator(a.ID, &opID); err != nil {
		t.Fatalf("SetOperator: %v", err)
	}
	got, _ = s.Get(a.ID)
	if got.CurrentOperator == nil || *got.CurrentOperator != opID {
		t.Fatalf("CurrentOperator not bound")
	}

	if err := s.SetOperator(a.ID, nil); err != nil {
		t.Fatalf("SetOperator(nil): %v", err)
	}
	got, _ = s.Get(a.ID)
	if got.CurrentOperator != nil {
		t.Fatalf("CurrentOperator not cleared")
	}
}

// Verifies List filters by Operator + AgentType.
func TestListFilters(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	opA := uuid.New()
	opB := uuid.New()

	a1 := New("claude-code", "a1", "")
	a1.CurrentOperator = &opA
	_ = s.Save(a1)
	a2 := New("codex", "a2", "")
	a2.CurrentOperator = &opB
	_ = s.Save(a2)
	a3 := New("claude-code", "a3", "")
	_ = s.Save(a3) // unbound

	if got, _ := s.List(ListOpts{Operator: &opA}); len(got) != 1 || got[0].Label != "a1" {
		t.Fatalf("Operator filter broken: got %+v", got)
	}
	if got, _ := s.List(ListOpts{AgentType: "codex"}); len(got) != 1 || got[0].Label != "a2" {
		t.Fatalf("AgentType filter broken: got %+v", got)
	}
}

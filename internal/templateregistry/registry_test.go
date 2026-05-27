package templateregistry

import (
	"errors"
	"strings"
	"testing"
)

func TestBuiltinsShipped(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("expected 5 builtins, got %d", len(all))
	}
	wantIDs := []string{"solo", "pair", "two-pizza", "stack-trio", "council"}
	for i, want := range wantIDs {
		if all[i].ID != want || !all[i].ReadOnly {
			t.Fatalf("builtin %d: got %+v, want %s ReadOnly=true", i, all[i].ID, want)
		}
	}
}

func TestBuiltinsRejectMutation(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if _, err := s.Update("solo", Template{Name: "hacked"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Update solo should be ReadOnly, got %v", err)
	}
	if err := s.Delete("two-pizza"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Delete two-pizza should be ReadOnly, got %v", err)
	}
}

func TestUserCreateUpdateDelete(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	created, err := s.Create(Template{
		Name: "MyTeam",
		Members: []MemberSpec{{Label: "a", Role: "worker", AgentType: "claude-code"}},
	}, "operator@example.com")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(created.ID, "user-") {
		t.Fatalf("user id should be user-<uuid>: %s", created.ID)
	}
	if created.ReadOnly {
		t.Fatal("user-created should not be ReadOnly")
	}

	// Update name + members
	updated, err := s.Update(created.ID, Template{Name: "MyTeam-v2"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "MyTeam-v2" {
		t.Fatalf("Update didn't apply: %+v", updated)
	}

	// Delete
	if err := s.Delete(created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := s.Get(created.ID); got != nil {
		t.Fatal("Delete didn't remove")
	}
}

func TestUserCreateRequiresMembers(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if _, err := s.Create(Template{Name: "Empty"}, ""); err == nil {
		t.Fatal("Create with no members should fail")
	}
}

func TestPersistAcrossStoreReopen(t *testing.T) {
	dir := t.TempDir()
	s1, _ := NewStore(dir)
	_, _ = s1.Create(Template{
		Name: "Persistent",
		Members: []MemberSpec{{Label: "x", Role: "worker", AgentType: "claude-code"}},
	}, "")

	s2, _ := NewStore(dir)
	all, _ := s2.List()
	if len(all) != 6 {
		t.Fatalf("expected 5 builtins + 1 user = 6, got %d", len(all))
	}
}

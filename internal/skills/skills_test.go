package skills

import (
	"errors"
	"strings"
	"testing"
)

func TestBuiltinSetCount(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	all, _ := s.List(ListOpts{})
	if len(all) != 12 {
		t.Fatalf("expected 12 builtins, got %d", len(all))
	}
}

func TestBuiltinSetIDs(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	all, _ := s.List(ListOpts{})
	want := []string{
		"scrum-master", "product-owner", "tech-lead", "architect",
		"implementer", "frontend-impl", "backend-impl",
		"code-reviewer", "security-reviewer", "qa-tester",
		"docs-writer", "shepherd",
	}
	for i, w := range want {
		if all[i].ID != w {
			t.Errorf("position %d: got %q, want %q", i, all[i].ID, w)
		}
		if !all[i].ReadOnly {
			t.Errorf("builtin %s must be ReadOnly", all[i].ID)
		}
		if all[i].PromptOverride == "" {
			t.Errorf("builtin %s missing PromptOverride", all[i].ID)
		}
	}
}

func TestNoStackTrioAnywhere(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	all, _ := s.List(ListOpts{})
	for _, sk := range all {
		body := strings.ToLower(strings.Join([]string{
			sk.ID, sk.Name, sk.Description, sk.PromptOverride,
			strings.Join(sk.Tags, " "), sk.Icon,
		}, " "))
		if strings.Contains(body, "stack trio") || strings.Contains(body, "stack-trio") || strings.Contains(body, "stack_trio") {
			t.Fatalf("forbidden 'stack trio' string in skill %s", sk.ID)
		}
	}
}

func TestBuiltinsRejectMutation(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if _, err := s.Update("architect", Skill{Name: "hacked"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Update builtin should be ErrReadOnly, got %v", err)
	}
	if err := s.Delete("scrum-master"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Delete builtin should be ErrReadOnly, got %v", err)
	}
}

func TestUserCreateUpdateDelete(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	created, err := s.Create(Skill{
		Name:           "DevOps",
		Description:    "Manages infra.",
		PromptOverride: "You are a DevOps engineer.",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(created.ID, "user-") {
		t.Fatalf("id should be user-{uuid}: %s", created.ID)
	}
	if created.ReadOnly {
		t.Fatal("user-created should not be ReadOnly")
	}

	upd, err := s.Update(created.ID, Skill{Name: "DevOps-v2"})
	if err != nil || upd.Name != "DevOps-v2" {
		t.Fatalf("Update: %v / %+v", err, upd)
	}
	if err := s.Delete(created.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(created.ID); got != nil {
		t.Fatal("Delete didn't remove")
	}
}

func TestCreateRequiresPrompt(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if _, err := s.Create(Skill{Name: "X"}); err == nil {
		t.Fatal("Create without prompt should fail")
	}
}

func TestPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, _ := NewStore(dir)
	_, _ = s1.Create(Skill{Name: "DevOps", PromptOverride: "..."})
	s2, _ := NewStore(dir)
	all, _ := s2.List(ListOpts{})
	if len(all) != 13 {
		t.Fatalf("expected 12 builtins + 1 user = 13, got %d", len(all))
	}
}

func TestListFilterByTag(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	rev, _ := s.List(ListOpts{Tag: "review"})
	if len(rev) != 2 {
		t.Fatalf("expected 2 review skills (code-reviewer, security-reviewer), got %d", len(rev))
	}
}

func TestListFilterByCompat(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	codex, _ := s.List(ListOpts{Compat: "codex"})
	// implementer + code-reviewer compat codex
	if len(codex) < 2 {
		t.Fatalf("expected ≥2 codex-compat skills, got %d", len(codex))
	}
}

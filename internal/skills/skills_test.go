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
	if len(all) != 10 {
		t.Fatalf("expected 10 LEAN builtins, got %d", len(all))
	}
}

func TestBuiltinSetIDs(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	all, _ := s.List(ListOpts{})
	want := []string{
		"tdd", "code-review", "debugging", "security-review",
		"planning", "spec-driven", "api-design", "e2e-testing",
		"team-orchestration", "process-coaching",
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
		if all[i].UpstreamSource == "" {
			t.Errorf("builtin %s missing UpstreamSource pin", all[i].ID)
		}
		if all[i].UpstreamPath == "" {
			t.Errorf("builtin %s missing UpstreamPath pin", all[i].ID)
		}
	}
}

// TestNoBannedVocab guards the #194 banned-vocab rule across IDs,
// names, descriptions, prompt bodies, tags, and icons. Banned strings:
// "shepherd", "Stack Trio", "RACI" (any case, with or without
// separators). Failing this test means a future contributor reintroduced
// the v0.8-era role-skill confusion. See architect's 2026-05-27 +
// 2026-05-28 briefs.
func TestNoBannedVocab(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	all, _ := s.List(ListOpts{})
	banned := []string{
		"shepherd", "stack trio", "stack-trio", "stack_trio",
		"raci",
	}
	for _, sk := range all {
		body := strings.ToLower(strings.Join([]string{
			sk.ID, sk.Name, sk.Description, sk.PromptOverride,
			strings.Join(sk.Tags, " "), sk.Icon, sk.UpstreamSource, sk.UpstreamPath,
		}, " "))
		for _, b := range banned {
			if strings.Contains(body, b) {
				t.Fatalf("forbidden vocab %q in skill %s: %q",
					b, sk.ID, body)
			}
		}
	}
}

func TestEffectiveBodyPrefersOrgOverride(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	all, _ := s.List(ListOpts{})
	upstream := all[0].EffectiveBody()
	if upstream == "" {
		t.Fatal("upstream body empty")
	}
	// Org override wins.
	updated, err := s.SetOverride(all[0].ID, "org override body")
	if err != nil {
		t.Fatal(err)
	}
	if updated.EffectiveBody() != "org override body" {
		t.Fatalf("EffectiveBody = %q, want org override body", updated.EffectiveBody())
	}
	// Clear restores upstream priority.
	cleared, err := s.ClearOverride(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.EffectiveBody() != upstream {
		t.Fatalf("after ClearOverride EffectiveBody = %q, want upstream %q",
			cleared.EffectiveBody(), upstream)
	}
}

func TestSetOverridePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, _ := NewStore(dir)
	_, err := s1.SetOverride("tdd", "team-specific tdd body")
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := NewStore(dir)
	sk, _ := s2.Get("tdd")
	if sk.OrgOverrideBody != "team-specific tdd body" {
		t.Fatalf("override didn't persist: %q", sk.OrgOverrideBody)
	}
	if sk.EffectiveBody() != "team-specific tdd body" {
		t.Fatal("EffectiveBody didn't reflect persisted override")
	}
}

func TestBuiltinsRejectMutation(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if _, err := s.Update("tdd", Skill{Name: "hacked"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Update builtin should be ErrReadOnly, got %v", err)
	}
	if err := s.Delete("code-review"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Delete builtin should be ErrReadOnly, got %v", err)
	}
}

func TestUserCreateUpdateDelete(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	created, err := s.Create(Skill{
		Name:           "FinOps",
		Description:    "Cost optimisation discipline.",
		PromptOverride: "You watch unit economics.",
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

	upd, err := s.Update(created.ID, Skill{Name: "FinOps-v2"})
	if err != nil || upd.Name != "FinOps-v2" {
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
	_, _ = s1.Create(Skill{Name: "FinOps", PromptOverride: "..."})
	s2, _ := NewStore(dir)
	all, _ := s2.List(ListOpts{})
	if len(all) != 11 {
		t.Fatalf("expected 10 builtins + 1 user = 11, got %d", len(all))
	}
}

func TestListFilterByTag(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	rev, _ := s.List(ListOpts{Tag: "quality"})
	// tdd, code-review, debugging, security-review, e2e-testing all carry "quality"
	if len(rev) < 4 {
		t.Fatalf("expected ≥4 quality skills, got %d", len(rev))
	}
}

func TestListFilterByCompat(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	codex, _ := s.List(ListOpts{Compat: "codex"})
	// tdd, code-review, debugging compat codex (ccCodex in builtins)
	if len(codex) < 3 {
		t.Fatalf("expected ≥3 codex-compat skills, got %d", len(codex))
	}
}

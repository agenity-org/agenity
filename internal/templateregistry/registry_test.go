package templateregistry

import (
	"errors"
	"strings"
	"testing"
)

func TestVisibleBuiltinsAre6(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	vis, _ := s.List(ListOpts{VisibleOnly: true})
	if len(vis) != 6 {
		t.Fatalf("expected 6 visible builtins, got %d", len(vis))
	}
	wantIDs := []string{"solo", "pair", "trio", "scrum", "review", "custom"}
	for i, want := range wantIDs {
		if vis[i].ID != want {
			t.Errorf("position %d: got %q, want %q", i, vis[i].ID, want)
		}
		if !vis[i].ReadOnly {
			t.Errorf("builtin %s must be ReadOnly", vis[i].ID)
		}
	}
}

func TestAllBuiltinsCountInclHidden(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	all, _ := s.List(ListOpts{})
	// 6 visible + 3 hidden = 9 builtins
	if len(all) != 9 {
		t.Fatalf("expected 9 total builtins (6 visible + 3 hidden), got %d", len(all))
	}
	hidden := []string{"solo-supervised", "council", "multi-team"}
	for _, h := range hidden {
		t0, _ := s.Get(h)
		if t0 == nil {
			t.Errorf("hidden builtin %s missing", h)
			continue
		}
		if t0.Visible {
			t.Errorf("hidden builtin %s should default to Visible=false", h)
		}
	}
}

func TestNoStackTrioAnywhere(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	all, _ := s.List(ListOpts{})
	for _, tmpl := range all {
		body := strings.ToLower(strings.Join([]string{
			tmpl.ID, tmpl.Name, tmpl.Description, tmpl.WhenToUse, tmpl.Icon,
		}, " "))
		if strings.Contains(body, "stack trio") || strings.Contains(body, "stack-trio") || strings.Contains(body, "stack_trio") {
			t.Fatalf("forbidden 'stack trio' string in template %s: %q", tmpl.ID, body)
		}
	}
}

func TestSlotsReferenceValidSkills(t *testing.T) {
	// Valid skill IDs from #194 — must match the builtin set there.
	validSkills := map[string]bool{
		"scrum-master": true, "product-owner": true, "tech-lead": true,
		"architect": true, "implementer": true, "frontend-impl": true,
		"backend-impl": true, "code-reviewer": true, "security-reviewer": true,
		"qa-tester": true, "docs-writer": true, "shepherd": true,
	}
	s, _ := NewStore(t.TempDir())
	all, _ := s.List(ListOpts{})
	for _, tmpl := range all {
		for _, slot := range tmpl.Slots {
			if slot.PrimarySkill != "" && !validSkills[slot.PrimarySkill] {
				t.Errorf("template %s slot %s references unknown PrimarySkill %q",
					tmpl.ID, slot.Label, slot.PrimarySkill)
			}
			for _, alt := range slot.AltSkills {
				if !validSkills[alt] {
					t.Errorf("template %s slot %s alt-skill %q is unknown",
						tmpl.ID, slot.Label, alt)
				}
			}
		}
	}
}

func TestBuiltinsRejectMutation(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if _, err := s.Update("solo", Template{Name: "hacked"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Update solo should be ReadOnly, got %v", err)
	}
	if err := s.Delete("trio"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Delete trio should be ReadOnly, got %v", err)
	}
}

func TestSetVisibilityOnBuiltin(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	// council is hidden by default
	if err := s.SetVisibility("council", true); err != nil {
		t.Fatalf("SetVisibility: %v", err)
	}
	all, _ := s.List(ListOpts{VisibleOnly: true})
	if len(all) != 7 {
		t.Fatalf("expected 7 visible (6 + council), got %d", len(all))
	}
}

func TestUserCreateUpdateDelete(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	created, err := s.Create(Template{
		Name: "MyTeam",
		Slots: []SkillSlot{{Label: "a", PrimarySkill: "implementer", AgentTypeDefault: "claude-code"}},
	}, "operator@example.com")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(created.ID, "user-") {
		t.Fatalf("user id should be user-<uuid>: %s", created.ID)
	}
	if !created.Visible {
		t.Fatal("user-created should default to visible")
	}
	if err := s.Delete(created.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(created.ID); got != nil {
		t.Fatal("Delete didn't remove")
	}
}

func TestPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, _ := NewStore(dir)
	_, _ = s1.Create(Template{
		Name: "Persistent",
		Slots: []SkillSlot{{Label: "x", PrimarySkill: "implementer"}},
	}, "")
	s2, _ := NewStore(dir)
	all, _ := s2.List(ListOpts{})
	if len(all) != 10 { // 9 builtins + 1 user
		t.Fatalf("expected 9 builtins + 1 user = 10, got %d", len(all))
	}
}

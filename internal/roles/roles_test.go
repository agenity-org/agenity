package roles

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
	all, _ := s.List()
	if len(all) != 12 {
		t.Fatalf("expected 12 builtin Roles, got %d", len(all))
	}
}

func TestBuiltinSetIDs(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	all, _ := s.List()
	want := []string{
		// Leadership
		"product-owner", "architect", "tech-lead",
		// Methodology
		"scrum-master",
		// Engineering
		"generalist", "full-stack-developer", "frontend-developer", "backend-developer",
		// Operations
		"devops-sre",
		// Quality
		"qa-engineer", "security-engineer", "code-reviewer",
	}
	for i, w := range want {
		if all[i].ID != w {
			t.Errorf("position %d: got %q, want %q", i, all[i].ID, w)
		}
		if !all[i].ReadOnly {
			t.Errorf("builtin %s must be ReadOnly", all[i].ID)
		}
		if all[i].PrimaryPrompt == "" {
			t.Errorf("builtin %s missing PrimaryPrompt", all[i].ID)
		}
		if all[i].Category == "" {
			t.Errorf("builtin %s missing Category", all[i].ID)
		}
	}
}

func TestNoBannedVocab(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	all, _ := s.List()
	banned := []string{
		"stack trio", "stack-trio", "stack_trio",
		" raci ", "raci ", " raci",
	}
	for _, r := range all {
		body := strings.ToLower(strings.Join([]string{
			r.ID, r.Name, r.Description, r.PrimaryPrompt,
			strings.Join(r.DefaultSkills, " "), r.Icon, r.Category,
		}, " "))
		for _, b := range banned {
			if strings.Contains(body, b) {
				t.Fatalf("forbidden vocab %q in role %s: %q", b, r.ID, body)
			}
		}
	}
}

// TestCodeReviewerPairConditional locks the architect's 2026-05-28
// amendment: in a 2-person Pair, the Code Reviewer also owns
// team-orchestration + process-coaching. In Trio+, defer to dedicated
// scrum-master / tech-lead. The clause must be present in the
// PrimaryPrompt AND those two skills must be in DefaultSkills.
func TestCodeReviewerPairConditional(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	r, _ := s.Get("code-reviewer")
	if r == nil {
		t.Fatal("code-reviewer role missing")
	}
	if !strings.Contains(strings.ToLower(r.PrimaryPrompt), "pair") {
		t.Errorf("code-reviewer PrimaryPrompt must mention Pair-conditional scope")
	}
	if !strings.Contains(r.PrimaryPrompt, "team-orchestration") ||
		!strings.Contains(r.PrimaryPrompt, "process-coaching") {
		t.Errorf("code-reviewer PrimaryPrompt must name team-orchestration + process-coaching")
	}
	want := map[string]bool{
		"code-review": false, "security-review": false,
		"team-orchestration": false, "process-coaching": false,
	}
	for _, sk := range r.DefaultSkills {
		want[sk] = true
	}
	for sk, ok := range want {
		if !ok {
			t.Errorf("code-reviewer DefaultSkills missing %q", sk)
		}
	}
}

func TestBuiltinsRejectMutation(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if _, err := s.Update("architect", Role{Name: "hacked"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Update builtin should be ErrReadOnly, got %v", err)
	}
	if err := s.Delete("scrum-master"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Delete builtin should be ErrReadOnly, got %v", err)
	}
}

func TestUserCreateUpdateDelete(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	r, err := s.Create(Role{
		Name:          "Cost Analyst",
		Description:   "Watches unit economics.",
		PrimaryPrompt: "You are a Cost Analyst...",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(r.ID, "user-") {
		t.Fatalf("user role ID should start with user-: %s", r.ID)
	}
	if r.ReadOnly {
		t.Fatal("user-created should not be ReadOnly")
	}
	upd, err := s.Update(r.ID, Role{Name: "Cost Analyst v2"})
	if err != nil || upd.Name != "Cost Analyst v2" {
		t.Fatalf("Update: %v / %+v", err, upd)
	}
	if err := s.Delete(r.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(r.ID); got != nil {
		t.Fatal("Delete didn't remove")
	}
}

func TestCreateRequiresPrompt(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if _, err := s.Create(Role{Name: "X"}); err == nil {
		t.Fatal("Create without PrimaryPrompt should fail")
	}
}

func TestDefaultSkillsResolveToKnownIDs(t *testing.T) {
	// Lock the contract: every role's DefaultSkills entry must be a
	// valid skill ID in the 10 LEAN set. This is the cross-package
	// invariant — if either side drifts, this test catches it.
	leanSkillIDs := map[string]bool{
		"tdd": true, "code-review": true, "debugging": true,
		"security-review": true, "planning": true, "spec-driven": true,
		"api-design": true, "e2e-testing": true,
		"team-orchestration": true, "process-coaching": true,
	}
	s, _ := NewStore(t.TempDir())
	all, _ := s.List()
	for _, r := range all {
		for _, sk := range r.DefaultSkills {
			if !leanSkillIDs[sk] {
				t.Errorf("role %s references unknown skill %q (not in 10 LEAN set)",
					r.ID, sk)
			}
		}
	}
}

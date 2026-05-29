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
	wantIDs := []string{"solo", "pair", "trio", "scrum", "squad", "custom"}
	for i, want := range wantIDs {
		if vis[i].ID != want {
			t.Errorf("position %d: got %q, want %q", i, vis[i].ID, want)
		}
		if !vis[i].ReadOnly {
			t.Errorf("builtin %s must be ReadOnly", vis[i].ID)
		}
	}
}

// TestFibonacciSizeLabels locks the architect's 2026-05-28 spec:
// solo=1, pair=2, trio=3, scrum=5, squad=8, custom=0. The SizeLabel
// is rendered in the Stage 1 grid as a small badge.
func TestFibonacciSizeLabels(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	want := map[string]string{
		"solo": "1", "pair": "2", "trio": "3",
		"scrum": "5", "squad": "8", "custom": "0",
	}
	for id, w := range want {
		tm, _ := s.Get(id)
		if tm == nil {
			t.Errorf("template %s missing", id)
			continue
		}
		if tm.SizeLabel != w {
			t.Errorf("template %s SizeLabel = %q, want %q", id, tm.SizeLabel, w)
		}
	}
}

// TestSlotCountsMatchSizeLabels verifies the actual Slots length
// matches the displayed Fibonacci size (except Custom which is
// operator-composed).
func TestSlotCountsMatchSizeLabels(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	wantSlots := map[string]int{
		"solo": 1, "pair": 2, "trio": 3,
		"scrum": 5, "squad": 8, "custom": 0,
	}
	for id, n := range wantSlots {
		tm, _ := s.Get(id)
		if tm == nil {
			t.Errorf("template %s missing", id)
			continue
		}
		if len(tm.Slots) != n {
			t.Errorf("template %s slot count = %d, want %d", id, len(tm.Slots), n)
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

// TestNoBannedVocab guards #194 banned-vocab rule across template IDs,
// names, descriptions, when-to-use copy, icons, slot labels, role IDs,
// and owned skill IDs.
func TestNoBannedVocab(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	all, _ := s.List(ListOpts{})
	banned := []string{
		"shepherd", "stack trio", "stack-trio", "stack_trio",
		"raci",
	}
	for _, tmpl := range all {
		parts := []string{tmpl.ID, tmpl.Name, tmpl.Description, tmpl.WhenToUse, tmpl.Icon}
		for _, slot := range tmpl.Slots {
			parts = append(parts, slot.Label, slot.RoleID)
			parts = append(parts, slot.OwnedSkills...)
		}
		body := strings.ToLower(strings.Join(parts, " "))
		for _, b := range banned {
			if strings.Contains(body, b) {
				t.Fatalf("forbidden vocab %q in template %s: %q", b, tmpl.ID, body)
			}
		}
	}
}

// TestSlotsReferenceValidRolesAndSkills locks the cross-package
// contract: every Slot.RoleID must be one of the 12 builtin roles;
// every OwnedSkills entry must be one of the 10 LEAN skills.
func TestSlotsReferenceValidRolesAndSkills(t *testing.T) {
	validRoles := map[string]bool{
		"product-owner": true, "architect": true, "tech-lead": true,
		"scrum-master": true, "generalist": true,
		"full-stack-developer": true, "frontend-developer": true, "backend-developer": true,
		"devops-sre": true,
		"qa-engineer": true, "security-engineer": true, "code-reviewer": true,
	}
	validSkills := map[string]bool{
		"tdd": true, "code-review": true, "debugging": true,
		"security-review": true, "planning": true, "spec-driven": true,
		"api-design": true, "e2e-testing": true,
		"team-orchestration": true, "process-coaching": true,
	}
	s, _ := NewStore(t.TempDir())
	all, _ := s.List(ListOpts{})
	for _, tmpl := range all {
		for _, slot := range tmpl.Slots {
			if slot.RoleID != "" && !validRoles[slot.RoleID] {
				t.Errorf("template %s slot %s references unknown RoleID %q",
					tmpl.ID, slot.Label, slot.RoleID)
			}
			for _, sk := range slot.OwnedSkills {
				if !validSkills[sk] {
					t.Errorf("template %s slot %s OwnedSkills contains unknown %q",
						tmpl.ID, slot.Label, sk)
				}
			}
			for sk := range slot.OwnedSkillsScope {
				if !validSkills[sk] {
					t.Errorf("template %s slot %s OwnedSkillsScope keys contains unknown skill %q",
						tmpl.ID, slot.Label, sk)
				}
			}
		}
	}
}

// TestPairAbsorbsLeadershipSkills locks the Pair-conditional rule
// (architect 2026-05-28): in the 2-person Pair template, the
// code-reviewer slot MUST own team-orchestration + process-coaching
// (no dedicated Scrum Master / Tech Lead present).
func TestPairAbsorbsLeadershipSkills(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	pair, _ := s.Get("pair")
	if pair == nil {
		t.Fatal("pair template missing")
	}
	if len(pair.Slots) != 2 {
		t.Fatalf("pair must have exactly 2 slots, got %d", len(pair.Slots))
	}
	var rev *Slot
	for i := range pair.Slots {
		if pair.Slots[i].RoleID == "code-reviewer" {
			rev = &pair.Slots[i]
			break
		}
	}
	if rev == nil {
		t.Fatal("pair must contain a code-reviewer slot")
	}
	want := map[string]bool{
		"code-review": false, "security-review": false,
		"team-orchestration": false, "process-coaching": false,
	}
	for _, sk := range rev.OwnedSkills {
		want[sk] = true
	}
	for sk, present := range want {
		if !present {
			t.Errorf("pair code-reviewer must own %q (Pair-conditional rule)", sk)
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
		Slots: []Slot{{
			Label: "a", RoleID: "full-stack-developer",
			OwnedSkills: []string{"tdd"}, AgentTypeDefault: "claude-code",
		}},
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
		Slots: []Slot{{
			Label: "x", RoleID: "generalist",
			OwnedSkills: []string{"tdd", "code-review"},
		}},
	}, "")
	s2, _ := NewStore(dir)
	all, _ := s2.List(ListOpts{})
	if len(all) != 10 { // 9 builtins + 1 user
		t.Fatalf("expected 9 builtins + 1 user = 10, got %d", len(all))
	}
}

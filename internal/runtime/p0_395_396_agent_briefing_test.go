// internal/runtime/p0_395_396_agent_briefing_test.go — pins #395 P0
// + #396 P0: spawned agents must receive a chepherd briefing
// (CLAUDE.md + skills/) at spawn time, materialized into
// agentHomeDir/.claude/, made visible inside the container via
// the existing agentHomeDir → /home/agent bind-mount.
//
// Pre-fix: spawned claude-code had vanilla .claude (no CLAUDE.md,
// no skills/). When operator asked "who are your siblings" the
// agent listed claude-code's local subagent types (Explore, Plan,
// statusline-setup) — total disconnect from the chepherd peer
// mesh that's the whole product premise.
//
// Architect quote 2026-05-31:
//
//	"they are not aware of each other and they dont know what is
//	chephered"
//
// Refs #395 P0 #396 P0 #225.
package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestP0_395_AgentClaudeMD_ContainsChepherdPreamble locks the
// canonical sentences that claude-code MUST see at startup so it
// orients to the chepherd-team context. These are the load-bearing
// strings — if a rewrite drops them, the briefing has regressed.
func TestP0_395_AgentClaudeMD_ContainsChepherdPreamble(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	spec := SpawnSpec{
		Name:      "test-worker-395",
		Role:      "worker",
		Team:      "test-team",
		AgentSlug: "claude-code",
	}
	peers := []PeerBrief{
		{Name: "architect-1", Role: "architect", AgentSlug: "claude-code", Team: "test-team"},
		{Name: "qa-1", Role: "qa", AgentSlug: "claude-code", Team: "test-team"},
	}
	materializeAgentBriefing(spec, tmp, peers)
	body, err := os.ReadFile(filepath.Join(tmp, ".claude", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	s := string(body)
	required := []string{
		"worker agent hosted by a Chepherd runtime",   // preamble
		"test-worker-395",                             // own name
		"architect-1",                                 // peer name
		"qa-1",                                        // peer name
		"chepherd.send_to_session",                    // peer messaging tool
		"chepherd.alert_human",                        // operator escalation
		"chepherd.list_sessions",                      // live peer query
		"DO NOT write `@<peer>: <message>`",           // anti-pattern callout
		"What good looks like for your role",          // role guidance section
	}
	for _, sub := range required {
		if !strings.Contains(s, sub) {
			t.Errorf("CLAUDE.md missing %q\n---BODY---\n%s", sub, s)
		}
	}
}

// TestP0_395_AgentClaudeMD_RoleGuidanceMatchesRole pins that the
// "What good looks like" section actually changes by role. The
// architect-role spawn must talk about dispatching to workers; the
// worker-role spawn must talk about shipping concrete changes.
// Without this, every agent gets the same generic blob.
func TestP0_395_AgentClaudeMD_RoleGuidanceMatchesRole(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"architect": "dispatch to workers",
		"worker":    "SHIP CONCRETE CHANGES",
		"qa":        "FIND DEFECTS",
		"shepherd":  "KEEP THE TEAM ALIGNED",
	}
	for role, want := range cases {
		role, want := role, want
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			spec := SpawnSpec{Name: "agent-" + role, Role: Role(role), Team: "x", AgentSlug: "claude-code"}
			materializeAgentBriefing(spec, tmp, nil)
			body, _ := os.ReadFile(filepath.Join(tmp, ".claude", "CLAUDE.md"))
			if !strings.Contains(string(body), want) {
				t.Errorf("role=%q CLAUDE.md missing role-guidance phrase %q", role, want)
			}
		})
	}
}

// TestP0_396_AgentSkills_DirectoryPopulated pins that
// .claude/skills/ exists + contains the three canonical chepherd
// skills + a role-specific skill.
func TestP0_396_AgentSkills_DirectoryPopulated(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	spec := SpawnSpec{Name: "test-agent-396", Role: "worker", Team: "t1", AgentSlug: "claude-code"}
	materializeAgentBriefing(spec, tmp, nil)

	skillsDir := filepath.Join(tmp, ".claude", "skills")
	required := []string{
		"team-orientation.md",
		"peer-message.md",
		"operator-escalation.md",
		"role-worker.md",
	}
	for _, name := range required {
		p := filepath.Join(skillsDir, name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing skill file %s: %v", name, err)
			continue
		}
		body, _ := os.ReadFile(p)
		s := string(body)
		// Every skill MUST have the YAML frontmatter (name + description)
		// so claude-code's /skills surfaces it.
		if !strings.HasPrefix(s, "---\n") {
			t.Errorf("skill %s missing YAML frontmatter (first 3 chars: %q)", name, s[:min3(s)])
		}
		if !strings.Contains(s, "description:") {
			t.Errorf("skill %s missing description: field", name)
		}
	}
}

func min3(s string) int {
	if len(s) < 3 {
		return len(s)
	}
	return 3
}

// TestP0_396_RoleSkill_VariesByRole pins per-role skill content.
// architect role gets architect-flavored checklist; worker gets
// worker-flavored. Without this, every role-skill is identical.
func TestP0_396_RoleSkill_VariesByRole(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"shepherd":  "Do NOT make code changes yourself",
		"architect": "Produce a concrete spec",
		"worker":    "Run tests + go vet",
		"qa":        "File defects with reproduction steps",
	}
	for role, want := range cases {
		role, want := role, want
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			spec := SpawnSpec{Name: "agent-" + role, Role: Role(role), Team: "t", AgentSlug: "claude-code"}
			materializeAgentBriefing(spec, tmp, nil)
			body, err := os.ReadFile(filepath.Join(tmp, ".claude", "skills", "role-"+role+".md"))
			if err != nil {
				t.Fatalf("read role skill: %v", err)
			}
			if !strings.Contains(string(body), want) {
				t.Errorf("role=%q role-skill missing checklist phrase %q", role, want)
			}
		})
	}
}

// TestP0_395_PeerList_StableSort proves the peer list in CLAUDE.md
// is rendered in stable sort order — re-running the briefing with
// the same inputs produces byte-identical output. Without stable
// sort, every restart shuffles the peers + makes the briefing
// non-reproducible for testing.
func TestP0_395_PeerList_StableSort(t *testing.T) {
	t.Parallel()
	spec := SpawnSpec{Name: "a", Role: "worker", Team: "t", AgentSlug: "claude-code"}
	peers := []PeerBrief{
		{Name: "z-peer", Role: "worker", AgentSlug: "claude-code", Team: "t"},
		{Name: "a-peer", Role: "architect", AgentSlug: "claude-code", Team: "t"},
		{Name: "m-peer", Role: "qa", AgentSlug: "claude-code", Team: "t"},
	}
	tmp1, tmp2 := t.TempDir(), t.TempDir()
	materializeAgentBriefing(spec, tmp1, peers)
	materializeAgentBriefing(spec, tmp2, peers)
	b1, _ := os.ReadFile(filepath.Join(tmp1, ".claude", "CLAUDE.md"))
	b2, _ := os.ReadFile(filepath.Join(tmp2, ".claude", "CLAUDE.md"))
	if string(b1) != string(b2) {
		t.Error("re-runs produced different bytes — peer sort not stable")
	}
	// Also verify the sort order in the rendered output.
	body := string(b1)
	iA := strings.Index(body, "`a-peer`")
	iM := strings.Index(body, "`m-peer`")
	iZ := strings.Index(body, "`z-peer`")
	if !(iA < iM && iM < iZ) {
		t.Errorf("peer order wrong: a@%d m@%d z@%d", iA, iM, iZ)
	}
}

// TestP0_395_NoPeers_HasGracefulCopy locks the empty-peer-list
// experience: a freshly-spawned first-member sees the "you're the
// first" copy + the pointer to chepherd.list_sessions for live data.
func TestP0_395_NoPeers_HasGracefulCopy(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	spec := SpawnSpec{Name: "lonely-agent", Role: "worker", Team: "empty-team", AgentSlug: "claude-code"}
	materializeAgentBriefing(spec, tmp, nil)
	body, _ := os.ReadFile(filepath.Join(tmp, ".claude", "CLAUDE.md"))
	s := string(body)
	if !strings.Contains(s, "you're the first") {
		t.Errorf("empty-peer briefing missing graceful empty copy: %q", s)
	}
	if !strings.Contains(s, "chepherd.list_sessions") {
		t.Errorf("empty-peer briefing missing live-query pointer to chepherd.list_sessions")
	}
}

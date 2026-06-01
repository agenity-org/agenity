// internal/runtime/p0_404_p03_team_events_test.go — pins #404 P0.3:
// team-membership events drive PTY notifications + debounced
// briefing regen + team-CLAUDE.md canon materialization.
//
// Architect's P0.3 design answers (#408 comment 2026-05-31):
//   - Event bus: in-process channel inside Runtime
//   - Regen cadence: debounce 1s on briefing rewrite
//   - PTY notification fires IMMEDIATELY (no debounce)
//   - Scope: membership-only (join/leave/role-change); scorecard
//     updates are a separate concern
//   - Team CLAUDE.md canon_path was a LIE before this PR; materialize
//     on every team event so it's always real
//
// Refs #404 P0.3 #404 P0.1 #404 P0.2 #395 #225.
package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestP0_404_P03_RenderTeamEventNotification_Shape pins the exact
// notification text peer agents see on PTY. Vocabulary change here
// would cascade through agent reactions to team-events.
func TestP0_404_P03_RenderTeamEventNotification_Shape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ev       teamEvent
		contains []string
	}{
		{
			ev: teamEvent{Kind: TeamEventJoin, Agent: "beta", Team: "dev", NewRole: "reviewer"},
			contains: []string{
				"[chepherd team-event]",
				"`beta` joined team `dev` as `reviewer`",
				"chepherd.get_peer_card(\"beta\")",
			},
		},
		{
			ev: teamEvent{Kind: TeamEventLeave, Agent: "gamma", Team: "dev", OldRole: "qa"},
			contains: []string{
				"[chepherd team-event]",
				"`gamma` left team `dev`",
				"(was `qa`)",
			},
		},
		{
			ev: teamEvent{Kind: TeamEventRoleChange, Agent: "alpha", Team: "dev", OldRole: "worker", NewRole: "lead"},
			contains: []string{
				"[chepherd team-event]",
				"`alpha` role in team `dev`",
				"`worker` → `lead`",
			},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(string(c.ev.Kind), func(t *testing.T) {
			t.Parallel()
			got := renderTeamEventNotification(c.ev)
			for _, sub := range c.contains {
				if !strings.Contains(got, sub) {
					t.Errorf("notification missing %q: full=%q", sub, got)
				}
			}
			// PTY stdin injection was removed (#615 multi-line textarea
			// fix). The newline padding is no longer required — the
			// rendered text is kept for logging/debugging purposes only.
		})
	}
}

// TestP0_404_P03_RenderTeamCanon_HasCurrentMembers locks the team
// canon's content. Operators + agents reading the canon expect the
// member list to be current as of the last team event.
func TestP0_404_P03_RenderTeamCanon_HasCurrentMembers(t *testing.T) {
	t.Parallel()
	members := []teamCanonMemberBrief{
		{Name: "alpha", Role: "worker", AgentSlug: "claude-code"},
		{Name: "beta", Role: "reviewer", AgentSlug: "claude-code"},
	}
	body := renderTeamCanon("dev", "hub", members)
	required := []string{
		"# team `dev` charter",
		"Topology: `hub`",
		"## Current members",
		"**`alpha`** — role `worker`, agent `claude-code`",
		"**`beta`** — role `reviewer`, agent `claude-code`",
		"chepherd.list_sessions",
		"chepherd.get_peer_card",
		"chepherd.peer_status",
		"chepherd.send_to_session",
	}
	for _, sub := range required {
		if !strings.Contains(body, sub) {
			t.Errorf("canon missing %q\n---BODY---\n%s", sub, body)
		}
	}
}

// TestP0_404_P03_RenderTeamCanon_EmptyMembersHasGracefulCopy — a
// freshly-created team with no members yet still gets a real canon
// file (not blank). Avoids the "canon_path returned but file doesn't
// exist" failure mode from #404 body.
func TestP0_404_P03_RenderTeamCanon_EmptyMembersHasGracefulCopy(t *testing.T) {
	t.Parallel()
	body := renderTeamCanon("fresh-team", "mesh", nil)
	if !strings.Contains(body, "_No members yet._") {
		t.Errorf("empty-members canon missing graceful copy: %q", body)
	}
}

// TestP0_404_P03_MaterializeTeamCanon_WritesToCanonPath proves the
// runtime actually creates the file at the canon path. Pre-#404 P0.3
// the canon_path was returned by /api/v1/teams but the file didn't
// exist — architect's "the canon_path field is a LIE" callout.
func TestP0_404_P03_MaterializeTeamCanon_WritesToCanonPath(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rt, err := New(stateDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt.CreateTeam("p03-test", "", TopologyHub)
	rt.materializeTeamCanon("p03-test")

	canonPath := filepath.Join(stateDir, "teams", "p03-test", "CLAUDE.md")
	body, err := os.ReadFile(canonPath)
	if err != nil {
		t.Fatalf("canon file not written: %v", err)
	}
	if !strings.Contains(string(body), "team `p03-test` charter") {
		t.Errorf("canon content unexpected: %q", string(body))
	}
}

// TestP0_404_P03_EmitTeamEvent_NilChannelSafe pins the nil-channel
// safety contract: a Runtime built without startTeamEventLoop()
// (e.g., a constructor variant) must still let JoinTeam/Stop emit
// without panicking. Without this guard, JoinTeam-on-a-bare-Runtime
// would crash.
func TestP0_404_P03_EmitTeamEvent_NilChannelSafe(t *testing.T) {
	t.Parallel()
	// Bare Runtime via composite literal — doesn't go through
	// startTeamEventLoop, so teamEvents stays nil. The emit path
	// MUST handle this without panicking.
	rt := &Runtime{}
	rt.emitTeamEvent(teamEvent{Kind: TeamEventJoin, Agent: "x", Team: "y"})
}

// TestP0_404_P03_JoinTeam_SameRole_NoEvent guards against PTY-noise
// from no-op JoinTeam-as-role-update calls. Pre-fix, every JoinTeam
// emitted a join event even when the membership already existed +
// the role was unchanged. With the fix, no event fires + the
// existing membership is returned.
func TestP0_404_P03_JoinTeam_SameRole_NoEvent(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rt, err := New(stateDir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt.byName["alpha"] = "test-id-alpha"
	rt.info["test-id-alpha"] = &SessionInfo{
		Name: "alpha", Team: "dev", Role: "worker",
	}

	// First join — emits an event.
	if _, err := rt.JoinTeam("alpha", "dev", "worker", ""); err != nil {
		t.Fatalf("first JoinTeam: %v", err)
	}
	// Drain whatever the channel got.
	time.Sleep(50 * time.Millisecond)
	for len(rt.teamEvents) > 0 {
		<-rt.teamEvents
	}

	// Second join with SAME role — should NOT emit.
	if _, err := rt.JoinTeam("alpha", "dev", "worker", ""); err != nil {
		t.Fatalf("second JoinTeam: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(rt.teamEvents) != 0 {
		t.Errorf("same-role JoinTeam emitted event(s): %d in channel", len(rt.teamEvents))
	}
}

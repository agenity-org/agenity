// internal/runtime/p0_404_peer_agent_card_test.go — pins #404 P0.1:
// every spawned session has a PeerAgentCard exposing role,
// capabilities, skills, current state, scorecard. Peers consume
// it via:
//   GET /api/v1/sessions/<name>/agent-card  (HTTP)
//   chepherd.get_peer_card(name)            (MCP tool)
//
// Without the card, peers know names+roles (chepherd.list_sessions)
// but not capabilities. Operator quote 2026-05-31: "they must see
// the cards of their sibling".
//
// Refs #404 P0.1 #395 P0 #396 P0 #225.
package runtime

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

// TestP0_404_BuildPeerAgentCard_ShapeMatchesContract pins the
// canonical fields. Tests each role variant produces distinct
// capabilities + skills.
func TestP0_404_BuildPeerAgentCard_ShapeMatchesContract(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		role             string
		wantCapabilities []string
		wantSkills       []string
	}{
		"worker": {
			role:             "worker",
			wantCapabilities: []string{"code-changes", "test-execution", "pr-shipping"},
			wantSkills:       []string{"team-orientation", "peer-message", "operator-escalation", "role-worker"},
		},
		"architect": {
			role:             "architect",
			wantCapabilities: []string{"spec-design", "work-dispatch", "output-verification"},
			wantSkills:       []string{"team-orientation", "peer-message", "operator-escalation", "role-architect"},
		},
		"qa": {
			role:             "qa",
			wantCapabilities: []string{"surface-walk", "defect-filing", "verdict-retraction"},
			wantSkills:       []string{"team-orientation", "peer-message", "operator-escalation", "role-qa"},
		},
		"shepherd": {
			role:             "shepherd",
			wantCapabilities: []string{"team-routing", "peer-pane-observation", "operator-escalation"},
			wantSkills:       []string{"team-orientation", "peer-message", "operator-escalation", "role-shepherd"},
		},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			info := &SessionInfo{
				Name:      "agent-" + c.role,
				Role:      Role(c.role),
				Team:      "test-team",
				AgentSlug: "claude-code",
				CreatedAt: time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC),
				PID:       12345,
			}
			card := BuildPeerAgentCard(info)
			if card == nil {
				t.Fatal("BuildPeerAgentCard returned nil for non-nil info")
			}
			if card.Name != info.Name {
				t.Errorf("Name = %q, want %q", card.Name, info.Name)
			}
			if card.Role != c.role {
				t.Errorf("Role = %q, want %q", card.Role, c.role)
			}
			if card.Team != "test-team" {
				t.Errorf("Team = %q, want test-team", card.Team)
			}
			if card.State != "alive" {
				t.Errorf("State = %q, want alive (non-paused, non-exited info)", card.State)
			}
			gotCaps := append([]string(nil), card.Capabilities...)
			sort.Strings(gotCaps)
			wantCaps := append([]string(nil), c.wantCapabilities...)
			sort.Strings(wantCaps)
			if !reflect.DeepEqual(gotCaps, wantCaps) {
				t.Errorf("Capabilities = %v, want %v", gotCaps, wantCaps)
			}
			if !reflect.DeepEqual(card.Skills, c.wantSkills) {
				t.Errorf("Skills = %v, want %v", card.Skills, c.wantSkills)
			}
		})
	}
}

// TestP0_404_BuildPeerAgentCard_StateMatchesLifecycle locks the
// state-label contract: paused → "paused", exited → "exited", else
// "alive". Operators + peer agents both read this field; vocabulary
// must stay stable.
func TestP0_404_BuildPeerAgentCard_StateMatchesLifecycle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		paused, exited bool
		want           string
	}{
		{false, false, "alive"},
		{true, false, "paused"},
		{false, true, "exited"},
		{true, true, "exited"}, // exit wins — already-stopped agents aren't "paused"
	}
	for _, c := range cases {
		c := c
		t.Run(c.want, func(t *testing.T) {
			t.Parallel()
			info := &SessionInfo{
				Name:      "x",
				Role:      Role("worker"),
				AgentSlug: "claude-code",
				CreatedAt: time.Now().UTC(),
				Paused:    c.paused,
				Exited:    c.exited,
			}
			got := BuildPeerAgentCard(info)
			if got.State != c.want {
				t.Errorf("paused=%v exited=%v: State = %q, want %q", c.paused, c.exited, got.State, c.want)
			}
		})
	}
}

// TestP0_404_BuildPeerAgentCard_ScorecardPropagated checks the
// optional scorecard surface. Peer agents reading another agent's
// card see the G/V/F/E/D values when shepherd has assessed them.
func TestP0_404_BuildPeerAgentCard_ScorecardPropagated(t *testing.T) {
	t.Parallel()
	info := &SessionInfo{
		Name:      "scored",
		Role:      Role("worker"),
		AgentSlug: "claude-code",
		CreatedAt: time.Now().UTC(),
		Scorecard: &Scorecard{Goal: 4.0, Velocity: 3.5, Focus: 4.2, EndState: 3.8, Discipline: 4.1},
	}
	card := BuildPeerAgentCard(info)
	if card.ScorecardGVFE == nil {
		t.Fatal("scorecard nil — peer agents can't read assessment")
	}
	if card.ScorecardGVFE["G"] != 4.0 || card.ScorecardGVFE["V"] != 3.5 ||
		card.ScorecardGVFE["F"] != 4.2 || card.ScorecardGVFE["E"] != 3.8 ||
		card.ScorecardGVFE["D"] != 4.1 {
		t.Errorf("scorecard mismatch: %+v", card.ScorecardGVFE)
	}
}

// TestP0_404_BuildPeerAgentCard_NilInfoSafe — guard against an
// nil-pointer panic when the runtime returns no SessionInfo (peer
// disappeared between list_sessions and get_peer_card).
func TestP0_404_BuildPeerAgentCard_NilInfoSafe(t *testing.T) {
	t.Parallel()
	if BuildPeerAgentCard(nil) != nil {
		t.Error("nil info should return nil card, not panic")
	}
}

// TestP0_404_PeerAgentCard_UnknownRoleHasGeneralCapability — guard
// for new roles introduced in the future before the card switch
// catches up. Default to "general-purpose" so the card still
// renders rather than emitting an empty list.
func TestP0_404_PeerAgentCard_UnknownRoleHasGeneralCapability(t *testing.T) {
	t.Parallel()
	info := &SessionInfo{
		Name:      "future-role",
		Role:      Role("compliance-auditor"),
		AgentSlug: "claude-code",
		CreatedAt: time.Now().UTC(),
	}
	card := BuildPeerAgentCard(info)
	if len(card.Capabilities) != 1 || card.Capabilities[0] != "general-purpose" {
		t.Errorf("unknown role: Capabilities = %v, want [general-purpose]", card.Capabilities)
	}
	if card.Skills[len(card.Skills)-1] != "role-compliance-auditor" {
		t.Errorf("unknown role: skill list last entry = %q, want role-compliance-auditor", card.Skills[len(card.Skills)-1])
	}
}

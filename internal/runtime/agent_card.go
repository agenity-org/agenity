package runtime

import (
	"strings"
)

// PeerAgentCard is the per-session shape exposed via
// GET /api/v1/sessions/<name>/agent-card and the
// chepherd.get_peer_card MCP tool (#404 P0.1).
//
// Distinct from the chepherd-instance-level a2a.AgentCard
// (/.well-known/agent-card.json) which advertises the chepherd
// runtime to peer instances. PeerAgentCard advertises ONE spawned
// session to its sibling agents inside the chepherd team — answers
// the agent's "who is this peer, what do they do, what are they
// doing now" question without polling rt.Get from every consumer.
//
// JSON tags are camelCase per the A2A convention.
type PeerAgentCard struct {
	Name          string            `json:"name"`
	Role          string            `json:"role"`
	Team          string            `json:"team"`
	AgentSlug     string            `json:"agentSlug"`
	Capabilities  []string          `json:"capabilities"`
	Skills        []string          `json:"skills"`
	State         string            `json:"state"` // alive | paused | exited
	Paused        bool              `json:"paused"`
	Shepherding   []string          `json:"shepherding,omitempty"`
	CreatedAt     string            `json:"createdAt"`
	PID           int               `json:"pid,omitempty"`
	ScorecardGVFE map[string]float64 `json:"scorecard,omitempty"` // {G, V, F, E, D}
}

// BuildPeerAgentCard constructs a per-session card from the live
// SessionInfo. Pure function — no I/O, safe to call from the HTTP
// handler + MCP handler with the same inputs.
//
// #404 P0.1.
func BuildPeerAgentCard(info *SessionInfo) *PeerAgentCard {
	if info == nil {
		return nil
	}
	card := &PeerAgentCard{
		Name:         info.Name,
		Role:         string(info.Role),
		Team:         info.Team,
		AgentSlug:    info.AgentSlug,
		Capabilities: capabilitiesForRole(string(info.Role)),
		Skills:       skillsForRole(string(info.Role)),
		State:        stateLabel(info),
		Paused:       info.Paused,
		Shepherding:  info.Shepherding,
		CreatedAt:    info.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		PID:          info.PID,
	}
	if info.Scorecard != nil {
		card.ScorecardGVFE = map[string]float64{
			"G": info.Scorecard.Goal,
			"V": info.Scorecard.Velocity,
			"F": info.Scorecard.Focus,
			"E": info.Scorecard.EndState,
			"D": info.Scorecard.Discipline,
		}
	}
	return card
}

// stateLabel returns a human-readable lifecycle state. Operators +
// peer agents both read this — keep the vocabulary stable.
func stateLabel(info *SessionInfo) string {
	if info.Exited {
		return "exited"
	}
	if info.Paused {
		return "paused"
	}
	return "alive"
}

// capabilitiesForRole returns the list of A2A-style capability
// strings advertised by a role. Matches the role-guidance from
// agent_briefing.go so peer agents reading the card know what to
// expect when interacting.
//
// #404 P0.1 follow-up — extended the role coverage so reviewer,
// scrum-master, product-owner, security, devops don't hit the
// general-purpose fallback (architect's #407 walk caught this).
// Operators add custom roles via the spawn wizard's free-text role
// field; the default arm still handles those gracefully.
func capabilitiesForRole(role string) []string {
	switch strings.ToLower(role) {
	case "shepherd":
		return []string{
			"team-routing",
			"peer-pane-observation",
			"operator-escalation",
		}
	case "architect", "lead":
		return []string{
			"spec-design",
			"work-dispatch",
			"output-verification",
		}
	case "worker":
		return []string{
			"code-changes",
			"test-execution",
			"pr-shipping",
		}
	case "qa", "tester":
		return []string{
			"surface-walk",
			"defect-filing",
			"verdict-retraction",
		}
	case "reviewer", "reviewer-discipline", "reviewer-architect", "reviewer-economics":
		return []string{
			"code-review",
			"gap-analysis",
			"verdict-render",
		}
	case "scrum-master", "scrummaster":
		return []string{
			"team-cadence",
			"verdict-attribution",
			"impediment-removal",
		}
	case "product-owner", "po":
		return []string{
			"backlog-prioritization",
			"acceptance-criteria",
			"stakeholder-translation",
		}
	case "security", "security-reviewer":
		return []string{
			"threat-modeling",
			"vuln-triage",
			"secret-hygiene",
		}
	case "devops", "sre":
		return []string{
			"deploy-pipeline",
			"observability",
			"incident-response",
		}
	default:
		return []string{"general-purpose"}
	}
}

// skillsForRole mirrors the per-role skill set that
// materializeAgentBriefing writes into /home/agent/.claude/skills/.
// Peer agents reading the card can see which skill files the target
// has access to without bind-mounting the dir.
func skillsForRole(role string) []string {
	out := []string{
		"team-orientation",
		"peer-message",
		"operator-escalation",
	}
	r := strings.ToLower(role)
	if r == "" {
		r = "worker"
	}
	out = append(out, "role-"+r)
	return out
}

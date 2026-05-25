// Package runtime — Membership: the many-to-many join between Agent
// and Team that carries the role + per-membership brief override.
// An Agent can hold N memberships (in N different teams or in different
// roles within the same team — though same-team-multi-role is unusual).
package runtime

import "time"

// MembershipRole is the agent's role within a specific team.
// Open enum — catalog YAMLs can introduce custom roles (e.g., security-auditor).
type MembershipRole string

const (
	RoleMemberWorker             MembershipRole = "worker"
	RoleMemberShepherd           MembershipRole = "shepherd"
	RoleMemberReviewer           MembershipRole = "reviewer"
	RoleMemberReviewerDiscipline MembershipRole = "reviewer-discipline"
	RoleMemberReviewerArchitect  MembershipRole = "reviewer-architect"
	RoleMemberReviewerEconomics  MembershipRole = "reviewer-economics"
	RoleMemberTester             MembershipRole = "tester"
	RoleMemberArchitect          MembershipRole = "architect"
)

// Membership joins one Agent + one Team with a role. Globally unique by
// (AgentName, TeamName) — an agent can only hold one role in a given team.
type Membership struct {
	AgentName     string         `json:"agent_name"`
	TeamName      string         `json:"team_name"`
	Role          MembershipRole `json:"role"`
	BriefOverride string         `json:"brief_override,omitempty"`
	JoinedAt      time.Time      `json:"joined_at"`
}

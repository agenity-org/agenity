// Package runtime — Agent: a PTY-backed agent process with a globally
// unique @-address. Membership in zero or more teams is expressed via
// Membership records (see membership.go) — NOT as a field on Agent.
// This is the v0.6 unified model. SessionInfo (v0.5) is retained for
// transitional API compatibility but new code should use Agent.
package runtime

import "time"

// Agent is a live PTY-backed process. Each Agent corresponds to one
// spawn — one Claude / qwen / shell child. Globally unique by Name.
type Agent struct {
	// Identity
	ID        string    `json:"id"`        // ptyhost session ID (stable across restart attempts)
	Name      string    `json:"name"`      // canonical @-address (globally unique)
	AgentSlug string    `json:"agent"`     // claude-code, qwen-code, ...
	Cwd       string    `json:"cwd"`       // working directory
	CreatedAt time.Time `json:"created_at"`
	PID       int       `json:"pid,omitempty"`

	// Git context (extracted at spawn from cwd's git config; branch refreshed each List)
	GitHubURL string `json:"github_url,omitempty"`
	Branch    string `json:"branch,omitempty"`

	// Lifecycle
	Paused   bool `json:"paused"`
	Exited   bool `json:"exited,omitempty"`
	ExitCode int  `json:"exit_code,omitempty"`

	// Activity counters (populated by the runtime's per-agent sniffer)
	TotalBytes  int64   `json:"total_bytes"`
	Bytes5m     int64   `json:"bytes_5m"`
	Chunks5m    int     `json:"chunks_5m"`
	IdleSeconds float64 `json:"idle_seconds"`

	// Stat sheet — operator-configurable per-agent settings (v0.6-B)
	StatSheet AgentStatSheet `json:"stat_sheet,omitempty"`

	// Latest scorecard from the agent's shepherd. Nil until first assessment.
	Scorecard *Scorecard `json:"scorecard,omitempty"`

	// Verdict history (latest only — full history lives in events log)
	InterventionCount int       `json:"intervention_count,omitempty"`
	LastVerdict       string    `json:"last_verdict,omitempty"`
	LastVerdictAt     time.Time `json:"last_verdict_at,omitempty"`
	LastVerdictMsg    string    `json:"last_verdict_msg,omitempty"`
}

// AgentStatSheet is the operator-set "character sheet" for an agent.
// All values have sensible defaults that ship per-profile in the catalog.
type AgentStatSheet struct {
	ContextBudget      int     `json:"context_budget,omitempty"`      // tokens before forced respawn
	ModelTier          string  `json:"model_tier,omitempty"`          // haiku | sonnet | opus | qwen | (per-agent CLI choice)
	DisciplineWeight   float64 `json:"discipline_weight,omitempty"`   // 0.5..2.0 multiplier on D-axis severity
	VelocityExpect     string  `json:"velocity_expect,omitempty"`     // low | medium | high (baseline for V)
	TokenBudgetUSD     float64 `json:"token_budget_usd,omitempty"`    // $-cap per session
	ToolAllowlist      []string `json:"tool_allowlist,omitempty"`     // MCP tools the agent may call (empty=all)
}

// DefaultStatSheet returns the per-role defaults.
func DefaultStatSheet(role string) AgentStatSheet {
	switch role {
	case "shepherd":
		return AgentStatSheet{
			ContextBudget:    100_000,
			ModelTier:        "haiku",
			DisciplineWeight: 1.0,
			VelocityExpect:   "low", // shepherds don't ship code; velocity is measured differently
		}
	case "reviewer", "reviewer-discipline", "reviewer-architect", "reviewer-economics":
		return AgentStatSheet{
			ContextBudget:    50_000,
			ModelTier:        "haiku",
			DisciplineWeight: 1.2,
			VelocityExpect:   "low",
		}
	case "tester":
		return AgentStatSheet{
			ContextBudget:    100_000,
			ModelTier:        "sonnet",
			DisciplineWeight: 1.0,
			VelocityExpect:   "medium",
		}
	default: // worker / implementer / etc.
		return AgentStatSheet{
			ContextBudget:    200_000,
			ModelTier:        "sonnet",
			DisciplineWeight: 1.0,
			VelocityExpect:   "medium",
		}
	}
}

// Package agentpatterns provides per-CLI-agent-flavor pattern-match
// libraries so the chepherd runtime can detect agent state
// transitions (idle, input-required, auth-required, completed, tool
// calls) from PTY byte streams without a one-size-fits-all heuristic.
//
// Per V0.9.2-ARCHITECTURE.md §16 + the A2A v1.0 task state machine,
// each supported CLI agent emits its own idle/prompt/completion
// signals. Today's chepherd silence-finalize uses a single hardcoded
// heuristic ("look for `❯` then wait 1500ms quiet") that works for
// claude-code's legacy TUI but mis-fires on qwen-code's prompt
// shape and aider's `>` prompt — and never matches stream-json
// turn-end events.
//
// The Flavor interface is the seam. Each flavor file implements:
//
//   - DetectIdle — has the agent stopped emitting and is awaiting
//     input? Inputs: recent PTY bytes + how long since the last
//     byte. Confidence 0.0–1.0 lets callers (silence-finalize)
//     tune their threshold per use case.
//   - IsCompleted — unambiguous turn-end marker (e.g., claude-code's
//     stream-json `"stop_reason":"end_turn"`).
//   - IsInputRequired — the agent is asking for clarification /
//     a choice and has stopped producing output (A2A state
//     INPUT_REQUIRED in §16).
//   - IsAuthRequired — the agent emitted an OAuth challenge URL
//     (A2A state AUTH_REQUIRED in §16; OAuth-tool integration path).
//   - ExtractToolCalls — for agents emitting structured tool-call
//     events (claude-code's stream-json), returns parsed calls;
//     for free-text agents, returns nil.
//
// Dispatch: ByAgentSlug returns the registered Flavor for an agent
// slug (e.g., "claude-code"), or a Noop flavor that returns
// no-match for everything. Callers that haven't been adapted to
// the per-flavor library yet keep working through the Noop's
// permissive defaults.
//
// Refs #485 V0.9.2-ARCHITECTURE.md §16.
package agentpatterns

import (
	"time"
)

// DetectionResult is the common return shape for every Flavor
// predicate. Match is the boolean answer; Confidence and Reason
// give callers visibility into the decision so they can tune
// thresholds or surface the reason in operator-facing diagnostics.
type DetectionResult struct {
	// Match is the boolean "did the predicate match?" answer.
	Match bool

	// Confidence is 0.0–1.0 (1.0 = unambiguous, e.g., a structured
	// JSON turn-end event; 0.5 = heuristic, e.g., prompt-glyph
	// detection in a long buffer). Callers can compare against a
	// threshold appropriate to their use case — silence-finalize
	// wants >0.7 to avoid false completions; a UI status badge
	// can show "probably idle" at >0.4.
	Confidence float64

	// Reason is a short human-readable explanation ("turn-end JSON
	// event observed", "❯ prompt + 800ms quiet", etc.) for
	// operator-facing diagnostics + bug-report attachments.
	Reason string
}

// ToolCall is one parsed tool invocation emitted by an agent
// during its turn. Returned by Flavor.ExtractToolCalls.
type ToolCall struct {
	Name      string
	Arguments map[string]any
}

// AuthChallenge is the structured detail extracted from an agent's
// auth-required signal. Returned by Flavor.ExtractAuthChallenge
// (#503 Wave H5 / §15.3).
//
// Empirical-driven contract (see H5 escalation 2026-05-31): the
// original §15.3 framing presumed every MCP tool returns an OAuth
// challenge URL on the wire. Real claude-code 2.1.148 against
// Anthropic-managed connectors (e.g. claude.ai Google Drive) emits
// `mcp_servers[*].status="needs-auth"` + tool_result `status:"unsupported"`
// WITHOUT an in-band auth_url — the user must run /mcp interactively
// to start the OAuth flow. So URL is OPTIONAL; Provider+Message are
// the always-populated fields that drive the operator-facing prompt.
type AuthChallenge struct {
	// Provider is the human-readable auth scope name. For
	// Anthropic-managed connectors this is the MCP server name
	// (e.g. "claude.ai Google Drive"); for third-party OAuth-emitting
	// tools it's the tool/service name.
	Provider string

	// Message is the operator-facing instruction. Empirically
	// captured for claude.ai connectors as the tool_result message
	// ("Ask the user to run /mcp and select \"<server>\" to
	// authenticate."). For URL-emitting MCPs it's the prose
	// preceding the URL ("Authorize at: ...").
	Message string

	// URL is the direct OAuth start URL when emitted by the MCP
	// server. Empty for Anthropic-managed connectors that route
	// through claude.ai's own /mcp UI.
	URL string
}

// Flavor is the per-CLI-agent-flavor pattern-match contract.
// Implementations live one-per-file (claude_code.go, qwen_code.go,
// aider.go, etc.) and are registered with the package-level
// dispatch via init().
//
// All methods MUST be safe for concurrent use across goroutines
// (silence-finalize, the SSE publisher, and the dashboard observer
// may all query the same Flavor at once). In practice the
// implementations are pure functions over their inputs — no
// per-Flavor mutable state.
type Flavor interface {
	// Slug returns the canonical agent identifier this flavor
	// matches against AgentSlug in the runtime's session metadata
	// (e.g. "claude-code", "qwen-code").
	Slug() string

	// DetectIdle reports whether the agent appears to have completed
	// its turn and is awaiting new input. `bytes` is the recent PTY
	// output (last ~4KB is typical); `since` is how long since the
	// last byte arrived on the PTY.
	DetectIdle(bytes []byte, since time.Duration) DetectionResult

	// IsCompleted reports whether `bytes` contains an unambiguous
	// turn-end marker. Compared to DetectIdle this is the strong
	// signal: agent explicitly told us the turn ended (e.g.,
	// claude-code's stream-json {"stop_reason":"end_turn"}).
	// Should only return Match=true with Confidence ≥ 0.9.
	IsCompleted(bytes []byte) DetectionResult

	// IsInputRequired reports whether the agent is awaiting user
	// input mid-turn (a clarifying question, a Y/N choice). The
	// A2A state machine maps this to TASK_STATE_INPUT_REQUIRED.
	IsInputRequired(bytes []byte) DetectionResult

	// IsAuthRequired reports whether the agent emitted an OAuth
	// challenge URL (e.g., a tool needs GitHub API access and
	// returned 401 with oauth_url). The A2A state machine maps
	// this to TASK_STATE_AUTH_REQUIRED. See §15.3.
	IsAuthRequired(bytes []byte) DetectionResult

	// ExtractAuthChallenge parses the auth-required signal from
	// `bytes` into a structured AuthChallenge. Returns nil when no
	// match (mirrors IsAuthRequired returning Match=false). When
	// IsAuthRequired returns Match=true, ExtractAuthChallenge MUST
	// return a non-nil result with Provider+Message populated;
	// URL is optional. The two methods stay in lockstep so callers
	// can use IsAuthRequired as the cheap predicate + only call
	// ExtractAuthChallenge when they need to populate Task
	// Status.Details (#503 Wave H5 / §15.3).
	ExtractAuthChallenge(bytes []byte) *AuthChallenge

	// ExtractToolCalls returns any tool invocations parsed from
	// `bytes`. For agents emitting structured tool-call events
	// (claude-code stream-json's content blocks of type
	// tool_use), returns the parsed calls; for free-text agents
	// that surface tool calls only via output formatting, returns
	// nil. Confidence-only-via-presence: returning a non-empty
	// slice IS the match signal.
	ExtractToolCalls(bytes []byte) []ToolCall
}

// registry maps AgentSlug → Flavor. Populated by each per-flavor
// file's init(). Package-private so callers go through ByAgentSlug.
var registry = map[string]Flavor{}

// register is the per-flavor init hook. Panics on duplicate slug
// registration — duplicates indicate a programming error (two
// files claiming the same flavor) and silent override would mask
// it.
func register(f Flavor) {
	if _, dup := registry[f.Slug()]; dup {
		panic("agentpatterns: duplicate flavor registration for slug " + f.Slug())
	}
	registry[f.Slug()] = f
}

// ByAgentSlug returns the registered Flavor for slug, or a Noop
// flavor that returns no-match for every predicate. Callers should
// NEVER receive a nil Flavor — silence-finalize that asks "is the
// agent idle?" should get a safe answer ("no, I don't know") rather
// than a nil panic when the slug is unknown.
func ByAgentSlug(slug string) Flavor {
	if f, ok := registry[slug]; ok {
		return f
	}
	return Noop{}
}

// All returns every registered Flavor in registration order
// (per init() ordering). Exposed for dashboard surfaces that
// enumerate "supported agent flavors" and for the test suite's
// invariant checks ("every registered flavor satisfies the
// interface and returns sane Confidence values").
func All() []Flavor {
	out := make([]Flavor, 0, len(registry))
	for _, f := range registry {
		out = append(out, f)
	}
	return out
}

// Noop is the safe-default Flavor returned by ByAgentSlug when no
// registered flavor matches the slug. Every predicate returns
// Match=false / Confidence=0, so callers fall back to their own
// baseline heuristic (silence-finalize's existing 1500ms quiet
// timer, the dashboard's "unknown agent" badge, etc.) without
// false positives.
type Noop struct{}

func (Noop) Slug() string                                          { return "" }
func (Noop) DetectIdle(_ []byte, _ time.Duration) DetectionResult  { return DetectionResult{} }
func (Noop) IsCompleted(_ []byte) DetectionResult                  { return DetectionResult{} }
func (Noop) IsInputRequired(_ []byte) DetectionResult              { return DetectionResult{} }
func (Noop) IsAuthRequired(_ []byte) DetectionResult               { return DetectionResult{} }
func (Noop) ExtractAuthChallenge(_ []byte) *AuthChallenge          { return nil }
func (Noop) ExtractToolCalls(_ []byte) []ToolCall                  { return nil }

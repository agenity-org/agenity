// Package agentpatterns / scaffolds.go — scaffold flavors for
// CLI agents we plan to support but haven't built detectors for
// yet (#485 Wave A6). Each returns Noop-equivalent results so
// callers that route by AgentSlug still find a registered
// flavor (no fallback to the global Noop) — useful for
// distinguishing "we know about this slug but haven't tuned a
// detector" from "this slug is unknown entirely".
//
// Each scaffold flavor MUST be filled in once chepherd-worker's
// R4 silence-finalize capture phase ships fresh PTY-bytes
// fixtures from the actual binary. See testdata/README.md for
// the contract; do NOT guess detector regexes without driving
// the binary first ([[feedback_architect_prescriptions_need_live_premise_check]]).
//
// Refs #485.
package agentpatterns

import "time"

func init() {
	register(Codex{})
	register(GeminiCLI{})
	register(OpenCode{})
}

// ─── codex ────────────────────────────────────────────────────────

// Codex is the OpenAI codex CLI flavor — scaffold only.
type Codex struct{}

func (Codex) Slug() string                                         { return "codex" }
func (Codex) DetectIdle(_ []byte, _ time.Duration) DetectionResult { return DetectionResult{} }
func (Codex) IsCompleted(_ []byte) DetectionResult                 { return DetectionResult{} }
func (Codex) IsInputRequired(_ []byte) DetectionResult             { return DetectionResult{} }
func (Codex) IsAuthRequired(_ []byte) DetectionResult              { return DetectionResult{} }
func (Codex) ExtractAuthChallenge(_ []byte) *AuthChallenge         { return nil }
func (Codex) ExtractToolCalls(_ []byte) []ToolCall                 { return nil }

// ─── gemini-cli ───────────────────────────────────────────────────

// GeminiCLI is Google's gemini-cli flavor — scaffold only.
type GeminiCLI struct{}

func (GeminiCLI) Slug() string                                         { return "gemini-cli" }
func (GeminiCLI) DetectIdle(_ []byte, _ time.Duration) DetectionResult { return DetectionResult{} }
func (GeminiCLI) IsCompleted(_ []byte) DetectionResult                 { return DetectionResult{} }
func (GeminiCLI) IsInputRequired(_ []byte) DetectionResult             { return DetectionResult{} }
func (GeminiCLI) IsAuthRequired(_ []byte) DetectionResult              { return DetectionResult{} }
func (GeminiCLI) ExtractAuthChallenge(_ []byte) *AuthChallenge         { return nil }
func (GeminiCLI) ExtractToolCalls(_ []byte) []ToolCall                 { return nil }

// ─── opencode ─────────────────────────────────────────────────────

// OpenCode is the opencode CLI flavor — scaffold only.
type OpenCode struct{}

func (OpenCode) Slug() string                                         { return "opencode" }
func (OpenCode) DetectIdle(_ []byte, _ time.Duration) DetectionResult { return DetectionResult{} }
func (OpenCode) IsCompleted(_ []byte) DetectionResult                 { return DetectionResult{} }
func (OpenCode) IsInputRequired(_ []byte) DetectionResult             { return DetectionResult{} }
func (OpenCode) IsAuthRequired(_ []byte) DetectionResult              { return DetectionResult{} }
func (OpenCode) ExtractAuthChallenge(_ []byte) *AuthChallenge         { return nil }
func (OpenCode) ExtractToolCalls(_ []byte) []ToolCall                 { return nil }

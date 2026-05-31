// Package agentpatterns / claude_code.go — flavor implementation
// for Anthropic's claude-code CLI (#485 Wave A6).
//
// Two operating modes chepherd encounters:
//
//   - INTERACTIVE TUI (default). User boots `claude`; the TUI
//     renders an input box with a `❯` prompt cursor. The agent is
//     idle when the cursor sits at the prompt position and no
//     bytes have arrived for several hundred milliseconds. ANSI
//     cursor-blink escape codes may still arrive but carry no
//     semantic content — DetectIdle must tolerate them.
//
//   - --print --output-format json (and stream-json variant). The
//     binary runs as a one-shot pipeline; the final byte stream
//     is a single JSON object with `"type":"result"` and
//     `"stop_reason":"end_turn"`. IsCompleted returns Match=true
//     with confidence 1.0 on this — it's the strongest possible
//     signal because the binary itself declared turn-end.
//
// Fixtures: testdata/claude_code_*.txt contains REAL bytes
// captured from this machine's claude binary (version 2.1.148).
// See testdata/README.md for capture commands; do NOT regenerate
// fixtures without re-running the live capture against the same
// flavor + recording the version, or the per-flavor heuristic
// will drift from the binary that ships to operators.
//
// Refs #485 V0.9.2-ARCHITECTURE.md §16.
package agentpatterns

import (
	"bytes"
	"encoding/json"
	"regexp"
	"time"
)

func init() { register(ClaudeCode{}) }

// ClaudeCode is the claude-code (Anthropic) CLI flavor.
type ClaudeCode struct{}

func (ClaudeCode) Slug() string { return "claude-code" }

// claudeIdleQuietWindow is the minimum quiet period before
// DetectIdle returns Match=true with high confidence. Shorter
// than the pre-A6 default 1500ms because the prompt-glyph
// pre-gate (presence of `❯` near end-of-buffer) is a stronger
// signal than raw silence — 800ms is well above natural
// inter-token latency on a reasoning model but short enough
// that operator-perceived latency stays low.
const claudeIdleQuietWindow = 800 * time.Millisecond

// claudePromptCursorUTF8 is the UTF-8 encoding of `❯` (U+276F).
// claude-code's TUI renders this as the leftmost character of
// the input box prompt line. Its presence near the end of the
// recent PTY buffer is the gate for silence-finalize — see
// internal/runtime/a2a_publisher.go's promptCursorUTF8 var which
// this constant supersedes.
var claudePromptCursorUTF8 = []byte{0xe2, 0x9d, 0xaf}

// claudeStreamResultRE matches the stream-json turn-end event.
// claude-code's --print --output-format json mode emits a single
// final JSON object with type=result and stop_reason=end_turn;
// the stream-json variant emits it as the last newline-delimited
// event of the stream.
var claudeStreamResultRE = regexp.MustCompile(`"type":"result"[^}]*"stop_reason":"end_turn"`)

// claudeAuthURLRE matches OAuth challenge URLs printed by tools.
// Anthropic-side tools (Notion, GitHub, etc.) format challenges
// as a banner with "Authorize at:" or "Sign in at:" followed by
// an https://accounts. or https://*.com/oauth/ URL. The regex is
// intentionally permissive — false positives are caught by the
// caller's state machine (already-authenticated tasks ignore
// AUTH_REQUIRED transitions when no oauth_url tool actually
// emitted one).
var claudeAuthURLRE = regexp.MustCompile(`(?i)(authorize at|sign in at|visit)[: ]+https://[^\s]+/oauth/`)

// claudeInputRequiredRE matches the common interactive prompts
// claude-code uses when it pauses for user input mid-turn:
// "Could you clarify", "Should I", "Would you like", etc. Each
// followed by a `?` and at least the cursor reaching a quiet
// idle state.
var claudeInputRequiredRE = regexp.MustCompile(`(?i)(could you clarify|should i (proceed|continue)|would you like me to|do you want me to)[^\n]*\?`)

func (ClaudeCode) DetectIdle(b []byte, since time.Duration) DetectionResult {
	// Stream-json turn-end is the strongest signal — it dominates
	// the heuristic. Confidence 1.0.
	if claudeStreamResultRE.Match(b) {
		return DetectionResult{
			Match: true, Confidence: 1.0,
			Reason: "stream-json turn-end event (type=result, stop_reason=end_turn)",
		}
	}
	// Interactive TUI heuristic: prompt cursor near end-of-buffer
	// + sufficient quiet period.
	if !hasPromptCursorNearEnd(b) {
		return DetectionResult{
			Match: false, Confidence: 0.0,
			Reason: "no `❯` prompt cursor near end of recent buffer",
		}
	}
	if since < claudeIdleQuietWindow {
		return DetectionResult{
			Match: false, Confidence: 0.4,
			Reason: "prompt cursor present but quiet period too short",
		}
	}
	return DetectionResult{
		Match: true, Confidence: 0.85,
		Reason: "interactive TUI: prompt cursor + quiet > 800ms",
	}
}

func (ClaudeCode) IsCompleted(b []byte) DetectionResult {
	if claudeStreamResultRE.Match(b) {
		return DetectionResult{
			Match: true, Confidence: 1.0,
			Reason: "stream-json result envelope with stop_reason=end_turn",
		}
	}
	// Parse the last newline-delimited line as JSON; some claude
	// invocations split the stream-json across multiple lines.
	for _, line := range lastLines(b, 4) {
		var probe map[string]any
		if err := json.Unmarshal(line, &probe); err != nil {
			continue
		}
		if probe["type"] == "result" && probe["stop_reason"] == "end_turn" {
			return DetectionResult{
				Match: true, Confidence: 1.0,
				Reason: "stream-json result line parses with stop_reason=end_turn",
			}
		}
	}
	return DetectionResult{}
}

func (ClaudeCode) IsInputRequired(b []byte) DetectionResult {
	if claudeInputRequiredRE.Match(b) {
		return DetectionResult{
			Match: true, Confidence: 0.75,
			Reason: "interactive clarifying-question pattern detected",
		}
	}
	return DetectionResult{}
}

func (ClaudeCode) IsAuthRequired(b []byte) DetectionResult {
	if claudeAuthURLRE.Match(b) {
		return DetectionResult{
			Match: true, Confidence: 0.9,
			Reason: "OAuth challenge URL emitted by tool",
		}
	}
	return DetectionResult{}
}

// ExtractToolCalls parses stream-json content blocks of type
// "tool_use" from the recent bytes. Each well-formed block
// produces one ToolCall entry. Non-stream-json output (the
// interactive TUI) returns nil because tool calls are surfaced
// only via formatted output without machine-readable structure.
func (ClaudeCode) ExtractToolCalls(b []byte) []ToolCall {
	var calls []ToolCall
	for _, line := range lastLines(b, 64) {
		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		// Stream-json content_block_start events carry the
		// tool_use envelope when the block's type is tool_use.
		if ev["type"] != "content_block_start" {
			continue
		}
		block, _ := ev["content_block"].(map[string]any)
		if block == nil || block["type"] != "tool_use" {
			continue
		}
		name, _ := block["name"].(string)
		args, _ := block["input"].(map[string]any)
		if name == "" {
			continue
		}
		calls = append(calls, ToolCall{Name: name, Arguments: args})
	}
	return calls
}

// hasPromptCursorNearEnd reports whether the prompt cursor byte
// sequence appears in the last ~256 bytes of the buffer — the
// "near end" qualifier prevents banners (which also contain ❯
// inside their input-box rendering, per #387) from incorrectly
// triggering the gate when there's a legitimate response after
// them.
func hasPromptCursorNearEnd(b []byte) bool {
	tail := b
	if len(b) > 256 {
		tail = b[len(b)-256:]
	}
	return bytes.Contains(tail, claudePromptCursorUTF8)
}

// lastLines returns up to n trailing newline-delimited lines
// from b. Each returned slice points into b (no copy); callers
// must not mutate. Used by JSON parsers that look at the most
// recent stream-json events without scanning the whole buffer.
func lastLines(b []byte, n int) [][]byte {
	if n <= 0 {
		return nil
	}
	lines := bytes.Split(b, []byte{'\n'})
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

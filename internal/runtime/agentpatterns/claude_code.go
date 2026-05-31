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
	"strings"
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

// claudeAuthURLRE is the FALLBACK detector for third-party MCPs
// that emit an OAuth challenge URL in prose (Sentry / GitHub-MCP-
// style "Authorize at: https://...").
//
// HISTORY + EMPIRICAL FINDING (#503 Wave H5 / 2026-05-31):
//
//   A6 #485 shipped this regex against a SYNTHESIZED minimal-repro
//   fixture only ("Authorize at: https://accounts.example.com/...").
//   The H5 premise check drove 20.8 kB of REAL claude-code 2.1.148
//   stream-json against an Anthropic-managed OAuth-required MCP
//   (claude.ai Google Drive). This regex matched ZERO times.
//
//   Real claude-code's auth-required signal is STRUCTURED, not prose:
//
//     1. system/init event's mcp_servers[*].status="needs-auth"
//     2. tool_use_result envelope with status="unsupported"
//
//   The primary detector for claude.ai connectors is the structured
//   JSON parse below (detectClaudeAuthFromJSON). This regex stays
//   as the FALLBACK path so third-party MCPs that DO emit a literal
//   "Authorize at: https://..." URL still trip AUTH_REQUIRED. See
//   memory feedback_synth_fixtures_hidden_in_main.
var claudeAuthURLRE = regexp.MustCompile(`(?i)(authorize at|sign in at|visit)[: ]+https://[^\s]+/oauth/`)

// claudeNeedsAuthJSON matches the structured needs-auth marker in
// system/init's mcp_servers list. Pre-gates the JSON parse so the
// expensive json.Unmarshal only runs on bytes that contain the
// literal marker — the bytes flowing into IsAuthRequired can be
// large (whole PTY buffer / whole stream-json output).
var claudeNeedsAuthJSON = []byte(`"status":"needs-auth"`)

// claudeUnsupportedToolResultJSON matches the structured marker
// for an unsupported tool result (claude.ai connector requiring
// /mcp OAuth). Same pre-gate role as claudeNeedsAuthJSON.
var claudeUnsupportedToolResultJSON = []byte(`"status":"unsupported"`)

// claudeHeadlessAuthProseRE matches the canonical prose claude-code
// emits in --print --output-format json mode when a tool needs OAuth
// and the model summarises the auth requirement to the operator.
// Empirically captured against claude 2.1.148 invoking the claude.ai
// Google Drive MCP connector (#503 Wave H5):
//
//	"The Google Drive MCP connector can't be authenticated from this
//	 side. Please run `/mcp` and select **claude.ai Google Drive** to
//	 complete the OAuth flow in your browser."
//
// Two anchors: `/mcp` literal + (authenticate|OAuth flow) within a
// ~300-byte window. Both must be present to suppress false positives
// (a generic mention of /mcp in a help response isn't auth-required).
// The window can contain escaped `\"` quotes from the JSON envelope
// surrounding the prose, so the regex allows any char except newline.
var claudeHeadlessAuthProseRE = regexp.MustCompile(`(?is)/mcp.{0,300}(authenticat|oauth flow)`)

// claudeMarkdownProviderRE pulls the **emphasized** provider name
// out of the headless-mode prose. claude's response template uses
// markdown emphasis around the connector name; the surrounding
// JSON envelope escapes inner quotes (`\\"...\\"` becomes `**"x"**`
// inside the result string in raw bytes). Match either bare
// `**Name**` or quoted `**"Name"**` so both shapes work. Provider
// names are letters/digits/spaces/periods.
var claudeMarkdownProviderRE = regexp.MustCompile(`\*\*\\?"?([\w. ]+?)\\?"?\*\*`)

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
	// Primary: structured needs-auth signal in system/init event
	// (stream-json mode, empirically verified against real claude-
	// code 2.1.148 OAuth-required MCP, #503 Wave H5).
	if ch := detectClaudeAuthFromJSON(b); ch != nil {
		return DetectionResult{
			Match: true, Confidence: 0.95,
			Reason: "structured needs-auth marker (" + ch.Provider + ")",
		}
	}
	// Secondary: headless --print --output-format json prose
	// signal — claude summarises the auth requirement to the
	// operator after the unsupported tool result. Empirically
	// captured against real claude 2.1.148 (#503 Wave H5).
	if claudeHeadlessAuthProseRE.Match(b) {
		return DetectionResult{
			Match: true, Confidence: 0.85,
			Reason: "headless prose auth-required pattern (/mcp + authenticate|OAuth flow)",
		}
	}
	// Fallback: prose OAuth URL emitted by third-party MCP tools.
	if claudeAuthURLRE.Match(b) {
		return DetectionResult{
			Match: true, Confidence: 0.8,
			Reason: "OAuth challenge URL emitted by tool (prose fallback)",
		}
	}
	return DetectionResult{}
}

// ExtractAuthChallenge returns the structured auth-challenge
// details when IsAuthRequired would match. Returns nil otherwise.
// Symmetric with IsAuthRequired: callers can call IsAuthRequired
// as the cheap predicate then ExtractAuthChallenge only when they
// need Status.Details. (#503 Wave H5 / §15.3)
func (ClaudeCode) ExtractAuthChallenge(b []byte) *AuthChallenge {
	// Primary: structured detection from stream-json events.
	if ch := detectClaudeAuthFromJSON(b); ch != nil {
		return ch
	}
	// Secondary: headless prose pattern. Pull the provider name
	// out of the markdown-emphasized connector name when present.
	if loc := claudeHeadlessAuthProseRE.FindIndex(b); loc != nil {
		provider := "claude.ai connector"
		// Search a window AROUND the match for the markdown-
		// emphasized provider name. The connector name appears
		// in `**Name**` or `**"Name"**` shape near the /mcp anchor.
		start := loc[0] - 300
		if start < 0 {
			start = 0
		}
		end := loc[1] + 200
		if end > len(b) {
			end = len(b)
		}
		if m := claudeMarkdownProviderRE.FindStringSubmatch(string(b[start:end])); len(m) > 1 {
			provider = strings.TrimSpace(m[1])
		}
		return &AuthChallenge{
			Provider: provider,
			Message:  "Run /mcp and select \"" + provider + "\" to complete the OAuth flow in your browser.",
		}
	}
	// Fallback: parse the URL out of the prose regex match.
	if loc := claudeAuthURLRE.FindIndex(b); loc != nil {
		match := b[loc[0]:loc[1]]
		// Extract the URL portion (skip "Authorize at: " prose).
		urlStart := bytes.Index(match, []byte("https://"))
		if urlStart < 0 {
			return nil
		}
		urlEnd := len(match)
		// Extend forward through the rest of the URL token (regex
		// stops at "/oauth/" but we want the whole URL until
		// whitespace).
		tail := b[loc[0]+urlStart:]
		urlEnd = bytes.IndexAny(tail, " \t\n\r")
		if urlEnd < 0 {
			urlEnd = len(tail)
		}
		url := string(tail[:urlEnd])
		// Provider = host portion, no scheme.
		provider := url
		if i := bytes.Index([]byte(url), []byte("://")); i >= 0 {
			rest := url[i+3:]
			if j := bytes.IndexAny([]byte(rest), "/?"); j >= 0 {
				provider = rest[:j]
			} else {
				provider = rest
			}
		}
		return &AuthChallenge{
			Provider: provider,
			Message:  "Visit the OAuth URL in a browser to complete authentication.",
			URL:      url,
		}
	}
	return nil
}

// detectClaudeAuthFromJSON parses claude-code stream-json bytes for
// the empirically-verified auth-required signals:
//
//  1. `mcp_servers[*].status == "needs-auth"` in a system/init event
//     → Provider = mcp_servers[i].name; Message = canonical
//     run-/mcp instruction
//  2. tool_use_result `status == "unsupported"` with claude.ai
//     connector message → Provider = parsed from message; Message
//     = the connector message verbatim
//
// Returns nil when no match. Cheap pre-gate via byte-search avoids
// json.Unmarshal on bytes that obviously don't carry the signal.
func detectClaudeAuthFromJSON(b []byte) *AuthChallenge {
	hasNeedsAuth := bytes.Contains(b, claudeNeedsAuthJSON)
	hasUnsupported := bytes.Contains(b, claudeUnsupportedToolResultJSON)
	if !hasNeedsAuth && !hasUnsupported {
		return nil
	}
	// Stream-json is newline-delimited JSON; parse each line as an
	// independent event so a single malformed line doesn't poison
	// the whole detection.
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		// Path 1: system/init event with mcp_servers list.
		if hasNeedsAuth && ev["type"] == "system" && ev["subtype"] == "init" {
			servers, _ := ev["mcp_servers"].([]any)
			for _, s := range servers {
				m, _ := s.(map[string]any)
				if m == nil {
					continue
				}
				if m["status"] == "needs-auth" {
					name, _ := m["name"].(string)
					if name == "" {
						name = "unknown-mcp-server"
					}
					return &AuthChallenge{
						Provider: name,
						Message:  "Run /mcp and select \"" + name + "\" to authenticate.",
					}
				}
			}
		}
		// Path 2: tool_use_result with status="unsupported".
		// stream-json wraps tool_result under {"tool_use_result":{...}}.
		if hasUnsupported {
			tur, _ := ev["tool_use_result"].(map[string]any)
			if tur != nil && tur["status"] == "unsupported" {
				msg, _ := tur["message"].(string)
				provider := providerFromUnsupportedMessage(msg)
				if provider == "" {
					provider = "claude.ai connector"
				}
				if msg == "" {
					msg = "MCP connector requires authentication via /mcp."
				}
				return &AuthChallenge{
					Provider: provider,
					Message:  msg,
				}
			}
		}
	}
	return nil
}

// providerFromUnsupportedMessage parses the connector name out of
// the canonical claude.ai unsupported-tool message:
//
//	"This is a claude.ai MCP connector. Ask the user to run /mcp and
//	 select \"claude.ai Google Drive\" to authenticate."
//
// Returns "" when no double-quoted provider name is found.
func providerFromUnsupportedMessage(msg string) string {
	const sel = `select "`
	i := bytes.Index([]byte(msg), []byte(sel))
	if i < 0 {
		return ""
	}
	rest := msg[i+len(sel):]
	j := bytes.IndexByte([]byte(rest), '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
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

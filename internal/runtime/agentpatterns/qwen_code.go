// Package agentpatterns / qwen_code.go — flavor implementation
// for Alibaba's qwen-code CLI (#485 Wave A6).
//
// qwen-code uses a `qwen>` or `qwen | <message> >` interactive
// prompt and emits free-text output without structured turn-end
// events. The idle signal is therefore prompt-glyph detection +
// quiet period.
//
// Fixtures: NO live qwen-code binary on this build host; the
// shapes here are derived from qwen-code's public TUI screenshots
// + the prompt regex documented in qwen-code's README. When R4's
// fresh-bytes capture phase ships fixtures from the actual binary
// (chepherd-worker is running R4 in parallel), the heuristic
// thresholds in this file should be re-tuned against those. See
// testdata/README.md for the contract.
//
// Refs #485 V0.9.2-ARCHITECTURE.md §16.
package agentpatterns

import (
	"bytes"
	"regexp"
	"time"
)

func init() { register(QwenCode{}) }

type QwenCode struct{}

func (QwenCode) Slug() string { return "qwen-code" }

const qwenIdleQuietWindow = 500 * time.Millisecond

// qwenPromptRE matches the canonical qwen-code prompt line that
// appears when the agent has yielded back to the user — either
// the bare `qwen>` form or the segmented `qwen | <hint> >` form.
// Anchored to end-of-buffer so mid-response mentions of "qwen>"
// inside agent text don't false-trigger.
var qwenPromptRE = regexp.MustCompile(`(?m)^qwen( \| [^>\n]*)?>\s*$`)

// qwenInputRequiredRE matches qwen-code's clarifying-question
// patterns. The qwen system prompt tends to surface these as
// short questions ending in `?` followed by a render of the
// input prompt.
var qwenInputRequiredRE = regexp.MustCompile(`(?i)(could you|please specify|which (one|option))[^\n]*\?\s*$`)

var qwenAuthURLRE = regexp.MustCompile(`(?i)(authorize|sign in|visit)[: ]+https://[^\s]+/oauth/`)

func (QwenCode) DetectIdle(b []byte, since time.Duration) DetectionResult {
	if !qwenPromptRE.Match(b) {
		return DetectionResult{
			Match: false, Confidence: 0.0,
			Reason: "no `qwen>` prompt line at end of buffer",
		}
	}
	if since < qwenIdleQuietWindow {
		return DetectionResult{
			Match: false, Confidence: 0.4,
			Reason: "prompt present but quiet period too short",
		}
	}
	return DetectionResult{
		Match: true, Confidence: 0.8,
		Reason: "qwen-code: prompt + quiet > 500ms",
	}
}

// IsCompleted — qwen-code does NOT emit a structured turn-end
// event; completion is inferred from the prompt being present
// plus a confident-enough quiet window. Returning the same
// result as DetectIdle when its confidence is ≥0.7 keeps the
// strong/weak signal asymmetry intact.
func (QwenCode) IsCompleted(b []byte) DetectionResult {
	r := QwenCode{}.DetectIdle(b, qwenIdleQuietWindow+time.Millisecond)
	if r.Match && r.Confidence >= 0.7 {
		r.Reason = "prompt-glyph derived: " + r.Reason
		return r
	}
	return DetectionResult{}
}

func (QwenCode) IsInputRequired(b []byte) DetectionResult {
	if qwenInputRequiredRE.Match(b) {
		return DetectionResult{
			Match: true, Confidence: 0.65,
			Reason: "qwen-code clarifying-question pattern",
		}
	}
	return DetectionResult{}
}

func (QwenCode) IsAuthRequired(b []byte) DetectionResult {
	if qwenAuthURLRE.Match(b) {
		return DetectionResult{
			Match: true, Confidence: 0.85,
			Reason: "OAuth challenge URL emitted by tool",
		}
	}
	return DetectionResult{}
}

// ExtractAuthChallenge parses the OAuth URL out of the prose
// fallback pattern. qwen-code has no live binary on this build
// host (see file-header note), so URL is the only signal we
// currently surface; Provider falls back to the URL host.
func (QwenCode) ExtractAuthChallenge(b []byte) *AuthChallenge {
	loc := qwenAuthURLRE.FindIndex(b)
	if loc == nil {
		return nil
	}
	urlStart := bytes.Index(b[loc[0]:loc[1]], []byte("https://"))
	if urlStart < 0 {
		return nil
	}
	tail := b[loc[0]+urlStart:]
	end := bytes.IndexAny(tail, " \t\n\r")
	if end < 0 {
		end = len(tail)
	}
	url := string(tail[:end])
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

// ExtractToolCalls — qwen-code surfaces tool calls only via
// formatted output without a stable machine-readable structure
// the chepherd runtime can rely on. Returns nil; downstream
// callers fall back to free-text rendering.
func (QwenCode) ExtractToolCalls(_ []byte) []ToolCall { return nil }

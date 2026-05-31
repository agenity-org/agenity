// Package agentpatterns / aider.go — flavor implementation
// for the aider CLI (#485 Wave A6).
//
// aider uses a `>` prompt at the start of a new line when it's
// waiting for the user. Earlier history lines also start with
// `>` (the user's typed lines are echoed) so the gate must
// require the prompt to be the FINAL non-whitespace token of
// the recent buffer, not just anywhere in it.
//
// Idle window is shorter than claude-code's because aider's
// model-output cadence is more bursty — the agent finishes a
// reply quickly and then waits; a 300ms quiet period rarely
// straddles a real mid-response pause.
//
// Fixtures: NO live aider binary on this build host; shapes
// here are from aider's documented terminal interaction model.
// See testdata/README.md.
//
// Refs #485 V0.9.2-ARCHITECTURE.md §16.
package agentpatterns

import (
	"bytes"
	"regexp"
	"time"
)

func init() { register(Aider{}) }

type Aider struct{}

func (Aider) Slug() string { return "aider" }

const aiderIdleQuietWindow = 300 * time.Millisecond

// aiderInputRequiredRE matches aider's confirm-changes prompts
// + clarification questions. aider commonly asks "Apply edits?",
// "Proceed?", "Run shell command?" etc.
var aiderInputRequiredRE = regexp.MustCompile(`(?i)(apply edits|proceed|run shell command|continue)\? *$`)

var aiderAuthURLRE = regexp.MustCompile(`(?i)(authorize|sign in|visit)[: ]+https://[^\s]+/oauth/`)

func (Aider) DetectIdle(b []byte, since time.Duration) DetectionResult {
	tail := trailingNonWhitespaceBytes(b, 8)
	if len(tail) == 0 || !bytes.HasSuffix(tail, []byte{'>'}) {
		return DetectionResult{
			Match: false, Confidence: 0.0,
			Reason: "no `>` prompt at end of recent buffer",
		}
	}
	if since < aiderIdleQuietWindow {
		return DetectionResult{
			Match: false, Confidence: 0.4,
			Reason: "prompt present but quiet period too short",
		}
	}
	return DetectionResult{
		Match: true, Confidence: 0.8,
		Reason: "aider: `>` prompt + quiet > 300ms",
	}
}

func (Aider) IsCompleted(b []byte) DetectionResult {
	r := Aider{}.DetectIdle(b, aiderIdleQuietWindow+time.Millisecond)
	if r.Match && r.Confidence >= 0.7 {
		r.Reason = "prompt-glyph derived: " + r.Reason
		return r
	}
	return DetectionResult{}
}

func (Aider) IsInputRequired(b []byte) DetectionResult {
	if aiderInputRequiredRE.Match(b) {
		return DetectionResult{
			Match: true, Confidence: 0.7,
			Reason: "aider confirm-changes / clarifying-question pattern",
		}
	}
	return DetectionResult{}
}

func (Aider) IsAuthRequired(b []byte) DetectionResult {
	if aiderAuthURLRE.Match(b) {
		return DetectionResult{
			Match: true, Confidence: 0.85,
			Reason: "OAuth challenge URL emitted",
		}
	}
	return DetectionResult{}
}

// ExtractAuthChallenge — same prose-URL fallback as qwen-code; aider
// has no live binary on this build host. URL is the only signal we
// currently surface; Provider falls back to the URL host.
func (Aider) ExtractAuthChallenge(b []byte) *AuthChallenge {
	loc := aiderAuthURLRE.FindIndex(b)
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

func (Aider) ExtractToolCalls(_ []byte) []ToolCall { return nil }

// trailingNonWhitespaceBytes returns up to maxLen bytes of the
// most recent non-whitespace content in b. Useful for prompt
// detection on agents that print a final prompt glyph possibly
// followed by an ANSI cursor-position escape sequence.
func trailingNonWhitespaceBytes(b []byte, maxLen int) []byte {
	end := len(b)
	for end > 0 && isAsciiSpace(b[end-1]) {
		end--
	}
	start := end - maxLen
	if start < 0 {
		start = 0
	}
	return b[start:end]
}

func isAsciiSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

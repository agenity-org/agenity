// internal/runtimehttp/p0_600_oauth_url_sanitize_test.go pins #600:
// claude-code prints TUI helper text "Paste code here if prompted"
// immediately after the OAuth URL using ANSI cursor positioning.
// After ansiAndCRStrip removes the cursor escape, the helper text
// concatenates onto the URL with no whitespace separator, corrupting
// the final query param (typically state=).
//
// PKCE flow: chepherd generates random state, stores it, renders URL
// with state=<random>. Operator clicks → Claude OAuth → redirects back
// with state=<echoed>. chepherd validates echoed == stored. If chepherd
// emitted corrupted state=<random>Paste..., validation fails → operator
// stuck.
//
// Coverage:
//   - Known helper-text suffix "Pastecodehereifprompted" trimmed cleanly
//   - Known helper-text suffix "Paste" (conservative catch-all) trimmed
//   - Length-cap fallback for unknown TUI text (>64 chars)
//   - Clean URL passthrough (no changes when no helper text)
//   - Invalid URL passthrough (parse failure → return input unchanged)
//   - End-to-end via scanForClaudeOAuthURL: feed bytes with the exact
//     reported corruption shape, assert clean URL emerges
//
// Refs #600 #560 #613.
package runtimehttp

import (
	"strings"
	"testing"
)

func TestP0_600_TrimHelperSuffix_KnownPasteString(t *testing.T) {
	// The exact corruption shape worker reported from operator's walk.
	in := "rMcQR_fqAT1Kc9ufwCcoNzzhuUIy3B50KLBBBad2a5kPastecodehereifprompted"
	want := "rMcQR_fqAT1Kc9ufwCcoNzzhuUIy3B50KLBBBad2a5k"
	got := trimTUIHelperSuffix(in)
	if got != want {
		t.Errorf("trimTUIHelperSuffix(%q) = %q, want %q", in, got, want)
	}
}

func TestP0_600_TrimHelperSuffix_GenericPasteCatchAll(t *testing.T) {
	in := "abc123PasteUnknownNewTUIHelper"
	want := "abc123"
	got := trimTUIHelperSuffix(in)
	if got != want {
		t.Errorf("conservative Paste catch-all: got %q, want %q", got, want)
	}
}

func TestP0_600_TrimHelperSuffix_CleanValuePassthrough(t *testing.T) {
	// Real PKCE base64url state (43 chars), no helper concat.
	clean := "rMcQR_fqAT1Kc9ufwCcoNzzhuUIy3B50KLBBBad2a5k"
	got := trimTUIHelperSuffix(clean)
	if got != clean {
		t.Errorf("clean value should pass through: got %q, want %q", got, clean)
	}
}

func TestP0_600_TrimHelperSuffix_LengthCapFallback_CamelCaseBoundary(t *testing.T) {
	// Unknown TUI text glued on; should truncate at first CamelCase
	// boundary after offset 32.
	in := "rMcQR_fqAT1Kc9ufwCcoNzzhuUIy3B50KLBBBad2a5kUnknownHelperTextHere"
	got := trimTUIHelperSuffix(in)
	if !strings.HasPrefix(in, got) {
		t.Errorf("truncation must be prefix of input: got %q, in %q", got, in)
	}
	if strings.Contains(got, "Unknown") {
		t.Errorf("CamelCase boundary should have truncated 'Unknown'; got %q", got)
	}
}

func TestP0_600_SanitizeOAuthCallbackURL_RemovesHelperFromState(t *testing.T) {
	// The full URL worker captured from operator's wizard walk.
	raw := "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&state=rMcQR_fqAT1Kc9ufwCcoNzzhuUIy3B50KLBBBad2a5kPastecodehereifprompted"
	got := sanitizeOAuthCallbackURL(raw)
	if strings.Contains(got, "Pastecodehereifprompted") {
		t.Errorf("sanitized URL still contains TUI helper text: %q", got)
	}
	if !strings.Contains(got, "rMcQR_fqAT1Kc9ufwCcoNzzhuUIy3B50KLBBBad2a5k") {
		t.Errorf("legitimate PKCE state was stripped from sanitized URL: %q", got)
	}
	if !strings.Contains(got, "client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e") {
		t.Errorf("other query params should be preserved: %q", got)
	}
}

// TestP0_613_SanitizeOAuthCallbackURL_ScopeNotTruncated pins #613:
// trimTUIHelperSuffix's 64-char length cap was applied to ALL query
// params. The OAuth scope string is 67 chars; it got capped to 64,
// stripping the last 3 chars ("ude" from "claude") → invalid scope
// "user:sessions:cla" → "Unknown scope" error from Claude OAuth server.
func TestP0_613_SanitizeOAuthCallbackURL_ScopeNotTruncated(t *testing.T) {
	scope := "org:create_api_key user:profile user:inference user:sessions:claude"
	raw := "https://claude.com/cai/oauth/authorize?client_id=9d1c250a&scope=" +
		strings.ReplaceAll(scope, " ", "+") +
		"&state=rMcQR_fqAT1Kc9ufwCcoNzzhuUIy3B50KLBBBad2a5k"
	got := sanitizeOAuthCallbackURL(raw)
	if !strings.Contains(got, "user:sessions:claude") {
		t.Errorf("scope was truncated: %q (full scope %q must survive)", got, scope)
	}
}

func TestP0_600_SanitizeOAuthCallbackURL_InvalidURLPassthrough(t *testing.T) {
	bad := "://broken//url\x00\x01"
	got := sanitizeOAuthCallbackURL(bad)
	if got != bad {
		t.Errorf("invalid URL should passthrough; got %q", got)
	}
}

func TestP0_600_ScanForClaudeOAuthURL_E2E_StripsHelperTextSuffix(t *testing.T) {
	// Simulate raw PTY bytes claude prints: URL + ANSI cursor escape +
	// helper text positioned on next line. ansiAndCRStrip removes the
	// cursor escape; pre-#600 the helper text glued onto the URL.
	// Post-#600 the sanitize step strips it.
	raw := []byte("Authorize at: https://claude.com/cai/oauth/authorize?code=true&state=rMcQR_fqAT1Kc9ufwCcoNzzhuUIy3B50KLBBBad2a5k\x1b[5;0HPaste code here if prompted")
	got := scanForClaudeOAuthURL(raw)
	if got == "" {
		t.Fatal("scanForClaudeOAuthURL returned empty; should have matched the URL")
	}
	// After ansiAndCRStrip + sanitize, the URL should NOT contain
	// 'Paste' from the helper. Note: ANSI strip removes the \x1b[5;0H
	// escape AND the spaces in "Paste code here if prompted" are
	// preserved AFTER strip — but the regex stops at space, so this
	// case actually catches at the space boundary. The TUI cursor
	// pattern that causes glue-no-space is what trimTUIHelperSuffix
	// defends against (covered in the trimTUIHelperSuffix_* tests).
	if strings.Contains(got, "Paste") {
		t.Errorf("scanForClaudeOAuthURL leaked helper text into URL: %q", got)
	}
}

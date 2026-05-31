// Package agentpatterns / p0_503_auth_required_test.go pins the
// v0.9.4 §15.3 + §16 AUTH_REQUIRED detection contract (#503 Wave H5).
//
// REAL-BYTE fixtures (NOT minimal-repro):
//
//   - testdata/claude_code_mcp_oauth_needs_auth.stream-json — 20.8 kB
//     of stream-json bytes captured from claude 2.1.148 invoking
//     mcp__claude_ai_Google_Drive__authenticate. Contains both
//     `mcp_servers[*].status=needs-auth` + `tool_use_result
//     status=unsupported`.
//
//   - testdata/claude_code_print_json_oauth_needed.json — single
//     result envelope from claude --print --output-format json
//     against the same connector. Contains the canonical headless
//     prose pattern.
//
// Both fixtures were captured during the H5 escalation 2026-05-31
// AFTER A6 #485's synthesized-fixture detector was empirically
// proven to zero-match real bytes (see memory
// [[feedback_synth_fixtures_hidden_in_main]]).
//
// Refs #503 V0.9.2-ARCHITECTURE.md §15.3 §16.
package agentpatterns

import (
	"strings"
	"testing"
)

// ─── Stream-JSON (primary, structured) ────────────────────────────

func TestWaveH5_ClaudeCode_StreamJSON_NeedsAuth_RealFixture(t *testing.T) {
	t.Parallel()
	fix := mustReadFixture(t, "claude_code_mcp_oauth_needs_auth.stream-json")
	cc := ClaudeCode{}

	got := cc.IsAuthRequired(fix)
	if !got.Match {
		t.Fatalf("IsAuthRequired returned no match on real claude 2.1.148 OAuth stream:\n  %+v", got)
	}
	if got.Confidence < 0.9 {
		t.Errorf("Confidence = %f, want ≥ 0.9 on structured needs-auth marker", got.Confidence)
	}
	if !strings.Contains(got.Reason, "needs-auth") {
		t.Errorf("Reason = %q, want to mention needs-auth", got.Reason)
	}

	ch := cc.ExtractAuthChallenge(fix)
	if ch == nil {
		t.Fatal("ExtractAuthChallenge returned nil on positive-match fixture")
	}
	if ch.Provider != "claude.ai Google Drive" {
		t.Errorf("Provider = %q, want %q (parsed from mcp_servers[*].name)",
			ch.Provider, "claude.ai Google Drive")
	}
	if !strings.Contains(ch.Message, "/mcp") {
		t.Errorf("Message = %q, want to instruct operator to run /mcp", ch.Message)
	}
	// Anthropic-managed connectors don't emit an in-band URL — see
	// the H5 escalation's structural ceiling note.
	if ch.URL != "" {
		t.Errorf("URL = %q, want empty (claude.ai connectors don't emit URL)", ch.URL)
	}
}

// TestWaveH5_ClaudeCode_PrintJSON_Prose_RealFixture pins the
// HEADLESS --print --output-format json detection path against the
// real captured single-envelope fixture. This is the path the H5
// iogrid integration actually exercises (headless runners use
// --output-format json, not stream-json).
func TestWaveH5_ClaudeCode_PrintJSON_Prose_RealFixture(t *testing.T) {
	t.Parallel()
	fix := mustReadFixture(t, "claude_code_print_json_oauth_needed.json")
	cc := ClaudeCode{}

	got := cc.IsAuthRequired(fix)
	if !got.Match {
		t.Fatalf("IsAuthRequired returned no match on real headless OAuth fixture:\n  %+v", got)
	}
	if got.Confidence < 0.8 {
		t.Errorf("Confidence = %f, want ≥ 0.8", got.Confidence)
	}

	ch := cc.ExtractAuthChallenge(fix)
	if ch == nil {
		t.Fatal("ExtractAuthChallenge returned nil on positive-match fixture")
	}
	if !strings.Contains(strings.ToLower(ch.Provider), "google drive") &&
		!strings.Contains(strings.ToLower(ch.Provider), "claude.ai") {
		t.Errorf("Provider = %q, want to mention Google Drive or claude.ai connector",
			ch.Provider)
	}
	if !strings.Contains(strings.ToLower(ch.Message), "/mcp") {
		t.Errorf("Message = %q, want to mention /mcp", ch.Message)
	}
}

// ─── Negative: synthesized minimal-repro (legacy fallback) ────────

// TestWaveH5_ClaudeCode_LegacyMinimalRepro_StillMatches keeps the
// pre-H5 minimal-repro fixture coverage so the prose regex
// fallback (third-party MCPs emitting "Authorize at: ..." URLs)
// continues to work. THIS IS NOT the primary path; see the
// stream-json test above.
func TestWaveH5_ClaudeCode_LegacyMinimalRepro_StillMatches(t *testing.T) {
	t.Parallel()
	body := []byte("Authorize at: https://accounts.example.com/oauth/authorize?client_id=foo\n")
	cc := ClaudeCode{}
	got := cc.IsAuthRequired(body)
	if !got.Match {
		t.Errorf("legacy synth fixture should still match via prose fallback: %+v", got)
	}
	ch := cc.ExtractAuthChallenge(body)
	if ch == nil {
		t.Fatal("legacy fallback ExtractAuthChallenge returned nil")
	}
	if ch.URL == "" {
		t.Errorf("URL = %q, want populated from prose match", ch.URL)
	}
	if !strings.Contains(ch.URL, "accounts.example.com") {
		t.Errorf("URL = %q, want to contain accounts.example.com", ch.URL)
	}
}

// TestWaveH5_ClaudeCode_NoAuthBytes_NoMatch ensures the detector
// doesn't fire on the COMPLETED fixture (a successful task with
// no auth pattern).
func TestWaveH5_ClaudeCode_NoAuthBytes_NoMatch(t *testing.T) {
	t.Parallel()
	fix := mustReadFixture(t, "claude_code_print_json_endturn.json")
	cc := ClaudeCode{}
	got := cc.IsAuthRequired(fix)
	if got.Match {
		t.Errorf("IsAuthRequired false-positive on completed fixture: %+v", got)
	}
	if ch := cc.ExtractAuthChallenge(fix); ch != nil {
		t.Errorf("ExtractAuthChallenge non-nil on completed fixture: %+v", ch)
	}
}

// ─── Invariants ───────────────────────────────────────────────────

// TestWaveH5_AllFlavors_HaveExtractAuthChallenge ensures every
// registered Flavor implements ExtractAuthChallenge with sane
// behavior (returns nil for empty / unrelated bytes, doesn't panic).
func TestWaveH5_AllFlavors_HaveExtractAuthChallenge(t *testing.T) {
	t.Parallel()
	for _, f := range All() {
		// nil-safe on empty bytes
		if got := f.ExtractAuthChallenge(nil); got != nil {
			t.Errorf("%s: ExtractAuthChallenge(nil) = %+v, want nil", f.Slug(), got)
		}
		// nil-safe on unrelated bytes
		if got := f.ExtractAuthChallenge([]byte("hello world")); got != nil {
			t.Errorf("%s: ExtractAuthChallenge(hello world) = %+v, want nil",
				f.Slug(), got)
		}
		// IsAuthRequired stays in lockstep — if it returns Match=true,
		// ExtractAuthChallenge MUST return non-nil.
		body := []byte("Authorize at: https://oauth.example.com/oauth/authorize")
		if f.IsAuthRequired(body).Match {
			if f.ExtractAuthChallenge(body) == nil {
				t.Errorf("%s: IsAuthRequired matched but ExtractAuthChallenge returned nil",
					f.Slug())
			}
		}
	}
}

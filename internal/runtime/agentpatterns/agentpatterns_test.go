// Package agentpatterns / agentpatterns_test.go pins the v0.9.4
// §16 per-flavor pattern-match library contract (#485 Wave A6).
//
// Test structure:
//
//   - Per-flavor predicate tests that exercise the detector
//     against captured/documented PTY-bytes fixtures from
//     testdata/. Fixtures are real bytes wherever the binary is
//     available on the build host (claude-code today); shapes
//     for binaries not present are synthesized from public docs
//     and flagged in testdata/README.md.
//   - Invariant tests that walk All() and verify every
//     registered Flavor honors the basic contract (non-empty
//     Slug, Confidence in [0,1] for every predicate, Noop
//     dispatch fallback safe for unknown slugs).
//
// Refs #485 V0.9.2-ARCHITECTURE.md §16.
package agentpatterns

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustReadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// ─── Dispatch + invariants ────────────────────────────────────────

func TestWaveA6_ByAgentSlug_KnownSlugsResolveToConcreteFlavor(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"claude-code": "claude-code",
		"qwen-code":   "qwen-code",
		"aider":       "aider",
		"codex":       "codex",
		"gemini-cli":  "gemini-cli",
		"opencode":    "opencode",
	}
	for slug, wantSlug := range cases {
		f := ByAgentSlug(slug)
		if f.Slug() != wantSlug {
			t.Errorf("ByAgentSlug(%q).Slug() = %q, want %q", slug, f.Slug(), wantSlug)
		}
	}
}

func TestWaveA6_ByAgentSlug_UnknownSlugReturnsNoopNotNil(t *testing.T) {
	t.Parallel()
	f := ByAgentSlug("some-unknown-flavor-2026")
	if f == nil {
		t.Fatal("ByAgentSlug returned nil for unknown slug — callers must never get nil")
	}
	if _, ok := f.(Noop); !ok {
		t.Errorf("unknown slug should resolve to Noop, got %T", f)
	}
	// Every predicate on Noop must return non-matching, zero-
	// confidence results so callers fall back to their baseline.
	if got := f.DetectIdle([]byte("anything"), time.Hour); got.Match || got.Confidence != 0 {
		t.Errorf("Noop.DetectIdle = %+v, want zero", got)
	}
}

func TestWaveA6_AllFlavors_HonorConfidenceBounds(t *testing.T) {
	t.Parallel()
	for _, f := range All() {
		if f.Slug() == "" {
			t.Errorf("registered flavor with empty slug: %T", f)
		}
		// Every predicate output's Confidence MUST be in [0, 1].
		results := []DetectionResult{
			f.DetectIdle([]byte("xx"), time.Second),
			f.IsCompleted([]byte("xx")),
			f.IsInputRequired([]byte("xx")),
			f.IsAuthRequired([]byte("xx")),
		}
		for i, r := range results {
			if r.Confidence < 0 || r.Confidence > 1 {
				t.Errorf("%s predicate #%d confidence = %f, want in [0,1]",
					f.Slug(), i, r.Confidence)
			}
		}
	}
}

// ─── claude-code ──────────────────────────────────────────────────

func TestWaveA6_ClaudeCode_PrintJSONEndTurn_HighConfidenceCompleted(t *testing.T) {
	t.Parallel()
	fix := mustReadFixture(t, "claude_code_print_json_endturn.json")
	r := ClaudeCode{}.IsCompleted(fix)
	if !r.Match {
		t.Fatalf("IsCompleted on real print-json fixture returned no match: %+v", r)
	}
	if r.Confidence < 0.9 {
		t.Errorf("Confidence = %f, want >= 0.9 on structured turn-end event", r.Confidence)
	}
	idle := ClaudeCode{}.DetectIdle(fix, time.Second)
	if !idle.Match || idle.Confidence < 0.9 {
		t.Errorf("DetectIdle on print-json fixture = %+v, want strong match", idle)
	}
}

func TestWaveA6_ClaudeCode_InteractivePromptCursor_GatedBySilence(t *testing.T) {
	t.Parallel()
	// Synthesized interactive-TUI tail with the `❯` cursor.
	body := []byte("> some output here\n❯ ")
	weak := ClaudeCode{}.DetectIdle(body, 100*time.Millisecond)
	if weak.Match {
		t.Errorf("expected NO match while still inside quiet window: %+v", weak)
	}
	strong := ClaudeCode{}.DetectIdle(body, claudeIdleQuietWindow+50*time.Millisecond)
	if !strong.Match {
		t.Errorf("expected match once quiet > 800ms: %+v", strong)
	}
	if strong.Confidence < 0.7 {
		t.Errorf("interactive-cursor confidence = %f, want >= 0.7", strong.Confidence)
	}
}

func TestWaveA6_ClaudeCode_NoPromptCursor_NoMatch(t *testing.T) {
	t.Parallel()
	body := []byte("running tool: file_read(path=/etc/hosts)\n")
	r := ClaudeCode{}.DetectIdle(body, time.Minute)
	if r.Match {
		t.Errorf("expected no match without prompt cursor: %+v", r)
	}
}

func TestWaveA6_ClaudeCode_AuthChallengeURL(t *testing.T) {
	t.Parallel()
	body := []byte("Authorize at: https://accounts.example.com/oauth/authorize?client_id=foo\n")
	r := ClaudeCode{}.IsAuthRequired(body)
	if !r.Match || r.Confidence < 0.8 {
		t.Errorf("auth-challenge URL not detected: %+v", r)
	}
}

func TestWaveA6_ClaudeCode_InputRequiredQuestion(t *testing.T) {
	t.Parallel()
	body := []byte("Could you clarify which file you meant?")
	r := ClaudeCode{}.IsInputRequired(body)
	if !r.Match {
		t.Errorf("clarifying question not detected: %+v", r)
	}
}

func TestWaveA6_ClaudeCode_ExtractToolCalls_StreamJSON(t *testing.T) {
	t.Parallel()
	body := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"/tmp/x"}}}` + "\n")
	calls := ClaudeCode{}.ExtractToolCalls(body)
	if len(calls) != 1 || calls[0].Name != "read_file" {
		t.Fatalf("ExtractToolCalls = %+v, want one read_file call", calls)
	}
	if calls[0].Arguments["path"] != "/tmp/x" {
		t.Errorf("Arguments = %v, want path=/tmp/x", calls[0].Arguments)
	}
}

// ─── qwen-code ────────────────────────────────────────────────────

func TestWaveA6_QwenCode_PromptIdleFixture(t *testing.T) {
	t.Parallel()
	fix := mustReadFixture(t, "qwen_code_prompt_idle.txt")
	weak := QwenCode{}.DetectIdle(fix, 100*time.Millisecond)
	if weak.Match {
		t.Errorf("qwen-code matched while quiet window too short: %+v", weak)
	}
	strong := QwenCode{}.DetectIdle(fix, qwenIdleQuietWindow+50*time.Millisecond)
	if !strong.Match {
		t.Errorf("qwen-code prompt should match with quiet > 500ms: %+v", strong)
	}
}

// ─── aider ────────────────────────────────────────────────────────

func TestWaveA6_Aider_PromptIdleFixture(t *testing.T) {
	t.Parallel()
	fix := mustReadFixture(t, "aider_prompt_idle.txt")
	weak := Aider{}.DetectIdle(fix, 50*time.Millisecond)
	if weak.Match {
		t.Errorf("aider matched while quiet window too short: %+v", weak)
	}
	strong := Aider{}.DetectIdle(fix, aiderIdleQuietWindow+50*time.Millisecond)
	if !strong.Match {
		t.Errorf("aider `>` prompt should match with quiet > 300ms: %+v", strong)
	}
}

func TestWaveA6_Aider_NoPromptNoMatch(t *testing.T) {
	t.Parallel()
	body := []byte("aider> Loading...\nProcessing files")
	r := Aider{}.DetectIdle(body, time.Minute)
	if r.Match {
		t.Errorf("aider matched without trailing `>` prompt: %+v", r)
	}
}

// ─── scaffolds ────────────────────────────────────────────────────

func TestWaveA6_Scaffolds_ReturnNoMatchUntilImplemented(t *testing.T) {
	t.Parallel()
	for _, slug := range []string{"codex", "gemini-cli", "opencode"} {
		f := ByAgentSlug(slug)
		if r := f.DetectIdle([]byte("anything"), time.Hour); r.Match {
			t.Errorf("scaffold %s returned Match=true; expected no-match until detector ships", slug)
		}
	}
}

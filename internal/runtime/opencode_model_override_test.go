// internal/runtime/opencode_model_override_test.go — regression for the
// opencode hardcoded-provider bug (operator-reported 2026-06-20: "I select
// groq/cerebras for opencode but it's hardcoded to gemini").
//
// opencode reads its model from OPENCODE_MODEL (provider/model string), not a
// --model flag, so the model_tier→--model switch (claude-code/qwen-code/
// lean-coder) never reached it. The global vault OPENCODE_MODEL overlay then
// pinned EVERY opencode agent to one model regardless of the wizard's per-agent
// pick. Fix: opencodeModelOverrideEnv emits a per-agent OPENCODE_MODEL appended
// AFTER the vault overlay, so envSliceToMap's last-wins makes the pick win.
package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOpencodeModelOverride_PerAgentPickBeatsVaultDefault(t *testing.T) {
	// Mirror the Spawn env assembly order: global vault overlay first, then the
	// per-agent override appended last (exactly how Spawn builds envWithMCP).
	vaultDefault := "OPENCODE_MODEL=google/gemini-2.5-flash"
	pick := "groq/llama-3.3-70b-versatile"

	env := []string{vaultDefault, "CEREBRAS_API_KEY=x", "GROQ_API_KEY=y"}
	env = append(env, opencodeModelOverrideEnv("opencode", pick)...)

	got := envSliceToMap(env)["OPENCODE_MODEL"]
	if got != pick {
		t.Fatalf("opencode OPENCODE_MODEL = %q, want %q (per-agent wizard pick must override the vault default)", got, pick)
	}
}

func TestOpencodeModelOverride_NoPick_FallsBackToVault(t *testing.T) {
	// No per-agent pick → override is nil → the vault default stands.
	env := []string{"OPENCODE_MODEL=google/gemini-2.5-flash"}
	env = append(env, opencodeModelOverrideEnv("opencode", "")...)
	if got := envSliceToMap(env)["OPENCODE_MODEL"]; got != "google/gemini-2.5-flash" {
		t.Fatalf("no-pick OPENCODE_MODEL = %q, want the vault default", got)
	}
}

func TestOpencodeModelOverride_OnlyOpencode(t *testing.T) {
	// The override must NOT fire for other flavors (they use model_tier→--model).
	for _, slug := range []string{"claude-code", "gemini-cli", "lean-coder", "qwen-code", "copilot"} {
		if out := opencodeModelOverrideEnv(slug, "cerebras/gpt-oss-120b"); out != nil {
			t.Errorf("%s: expected no OPENCODE_MODEL override, got %v", slug, out)
		}
	}
}

// opencode.json's model (written via opencodeModelFromEnv) is what opencode
// actually obeys — it overrides the OPENCODE_MODEL env var. So the per-agent
// pick must win HERE, or the wizard selection is silently dropped even though
// the container env is correct (the exact 2026-06-20 symptom: env=groq but
// opencode.log showed providerID=google).
func TestOpencodeModelFromEnv_PerAgentPickWins(t *testing.T) {
	spec := SpawnSpec{AgentSlug: "opencode"}
	spec.StatSheet.ModelTier = "groq/llama-3.3-70b-versatile"
	// Even with a conflicting per-spawn OPENCODE_MODEL, the pick wins.
	spec.Env = []string{"OPENCODE_MODEL=google/gemini-2.5-flash"}
	if got := opencodeModelFromEnv(spec); got != "groq/llama-3.3-70b-versatile" {
		t.Fatalf("opencode.json model = %q, want the per-agent pick groq/llama-3.3-70b-versatile", got)
	}
}

func TestOpencodeModelFromEnv_NoPick_UsesEnv(t *testing.T) {
	spec := SpawnSpec{AgentSlug: "opencode"}
	spec.Env = []string{"OPENCODE_MODEL=cerebras/gpt-oss-120b"}
	if got := opencodeModelFromEnv(spec); got != "cerebras/gpt-oss-120b" {
		t.Fatalf("no-pick: opencode.json model = %q, want the env OPENCODE_MODEL", got)
	}
}

// A non-opencode slug must NEVER get OPENCODE_MODEL even when ModelTier carries
// an opencode-style "provider/model" string. (Complements the table-driven
// TestOpencodeModelOverride_OnlyOpencode with an explicit single-slug assertion
// against the empty-slug edge — `""` (claude-code default) must also be inert.)
func TestOpencodeModelOverride_NonOpencodeSlug_NoOverride(t *testing.T) {
	for _, slug := range []string{"", "claude-code", "gemini-cli"} {
		if out := opencodeModelOverrideEnv(slug, "groq/llama-3.3-70b-versatile"); out != nil {
			t.Errorf("slug %q: expected nil (no OPENCODE_MODEL), got %v", slug, out)
		}
	}
}

// FINDING (documented, NOT fixed here): opencodeModelOverrideEnv /
// opencodeModelFromEnv gate on `modelTier != ""`, NOT on a TrimSpace. A
// whitespace-only ModelTier (e.g. a client POSTing "model_tier": "  ") is
// therefore treated as a REAL pick: the override emits OPENCODE_MODEL="  " and
// opencodeModelFromEnv returns "  " instead of falling back to the vault
// default. The ticket's stated intent is "whitespace/empty model_tier falls
// back to vault" — the empty case does, the whitespace case does NOT. ModelTier
// is set verbatim from JSON (catalog.go) with no trimming on the path, so this
// is reachable from a malformed client request (not the normal wizard, which
// sends real model ids). These tests PIN CURRENT BEHAVIOR so the gap is visible
// and a future trim-fix flips them deliberately rather than silently. See the
// agent report for the recommended fix (TrimSpace at the gate).
func TestOpencodeModelOverride_WhitespaceModelTier_CurrentBehavior_NotTrimmed(t *testing.T) {
	out := opencodeModelOverrideEnv("opencode", "   ")
	// Current behavior: whitespace is NOT empty, so an override IS emitted.
	if len(out) != 1 || out[0] != "OPENCODE_MODEL=   " {
		t.Fatalf("documenting current (untrimmed) behavior: got %v, want [OPENCODE_MODEL=   ]"+
			" — if this now returns nil, the code started trimming; update this test + the finding", out)
	}
}

func TestOpencodeModelFromEnv_WhitespaceModelTier_CurrentBehavior_NotTrimmed(t *testing.T) {
	spec := SpawnSpec{AgentSlug: "opencode"}
	spec.StatSheet.ModelTier = "  "
	spec.Env = []string{"OPENCODE_MODEL=cerebras/gpt-oss-120b"} // a valid vault default present
	got := opencodeModelFromEnv(spec)
	// Current behavior: whitespace ModelTier wins over the valid env vault default.
	if got != "  " {
		t.Fatalf("documenting current (untrimmed) behavior: opencode.json model = %q, want \"  \" "+
			"(whitespace ModelTier currently beats the vault default; a trim-fix would make this fall back)", got)
	}
}

// The WRITTEN BYTES are what opencode obeys: assert the actual opencode.json the
// daemon writes (via writeFlavorMCPConfig) carries model == the per-agent pick.
// This goes one level deeper than opencodeModelFromEnv (which returns the
// string) — it proves the value reaches the file's "model" key after
// json.Marshal, the layer that produced the 2026-06-20 symptom (env=groq but
// the written config said google). Filesystem only — no daemon, no container.
func TestWriteFlavorMCPConfig_Opencode_WritesPerAgentPickAsModel(t *testing.T) {
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	home := t.TempDir()
	spec := SpawnSpec{Name: "oc1", AgentSlug: "opencode"}
	spec.StatSheet.ModelTier = "groq/llama-3.3-70b-versatile"
	// A conflicting vault default in the env must NOT win over the wizard pick.
	spec.Env = []string{"OPENCODE_MODEL=google/gemini-2.5-flash"}

	rt.writeFlavorMCPConfig(spec, home)

	dest := filepath.Join(home, ".config", "opencode", "opencode.json")
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("opencode.json not written: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("opencode.json is not valid JSON: %v\n%s", err, b)
	}
	if got, _ := cfg["model"].(string); got != "groq/llama-3.3-70b-versatile" {
		t.Fatalf("written opencode.json model = %q, want the per-agent pick groq/llama-3.3-70b-versatile\nfull config:\n%s", got, b)
	}
}

// And the no-pick path: with no ModelTier, the written opencode.json model
// follows the OPENCODE_MODEL env vault default — never a hardcoded constant.
func TestWriteFlavorMCPConfig_Opencode_NoPick_FollowsEnvVault(t *testing.T) {
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	home := t.TempDir()
	spec := SpawnSpec{Name: "oc2", AgentSlug: "opencode"}
	spec.Env = []string{"OPENCODE_MODEL=cerebras/gpt-oss-120b"}

	rt.writeFlavorMCPConfig(spec, home)

	b, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("opencode.json not written: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("opencode.json invalid JSON: %v", err)
	}
	if got, _ := cfg["model"].(string); got != "cerebras/gpt-oss-120b" {
		t.Fatalf("no-pick: written opencode.json model = %q, want the env vault cerebras/gpt-oss-120b", got)
	}
}

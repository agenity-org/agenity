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

import "testing"

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

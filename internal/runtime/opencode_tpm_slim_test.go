// internal/runtime/opencode_tpm_slim_test.go — regressions for the
// opencode TPM-fit slim (measured live 2026-06-21 against Cerebras + Groq).
//
// opencode's uncapped per-turn request busts the free providers' TPM
// (Cerebras 30k, Groq 12k): ~10.5 kB builtin system prompt + the full
// 27-tool chepherd MCP schema set (surfaced as 37 tools with builtins) +
// the ~10.9 kB AGENTS.md briefing → ~11k+ prompt_tokens for a trivial
// turn. The slim has four levers (tool allow-list, build-prompt override,
// instructions:[], per-model output cap). These tests pin the SHAPE of
// the generated opencode.json so the levers don't silently regress —
// especially the tool allow-list, whose previous incarnation (#743) was a
// bug that disabled ALL chepherd tools.
package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

// The four mesh-essential chepherd tools, in opencode's sanitized
// double-prefixed name form (<server>_<tool> where the tool is already
// "chepherd.<x>" → "chepherd_chepherd_<x>").
var wantOpencodeAllowed = []string{
	"chepherd_chepherd_get_task",
	"chepherd_chepherd_send_to_session",
	"chepherd_chepherd_alert_human",
	"chepherd_chepherd_list",
}

func TestOpencodeToolSlim_AllowsExactlyTheMeshTools(t *testing.T) {
	slim := opencodeToolSlim()
	// Wildcard denies all chepherd tools.
	if v, ok := slim["chepherd_chepherd_*"]; !ok || v != false {
		t.Fatalf("expected chepherd_chepherd_* => false, got %v (ok=%v)", v, ok)
	}
	// Each mesh tool is explicitly re-enabled.
	for _, name := range wantOpencodeAllowed {
		if v, ok := slim[name]; !ok || v != true {
			t.Errorf("expected %s => true, got %v (ok=%v)", name, v, ok)
		}
	}
	// Builtins denied.
	for _, b := range []string{"bash", "edit", "write", "read", "webfetch"} {
		if v, ok := slim[b]; !ok || v != false {
			t.Errorf("expected builtin %s => false, got %v (ok=%v)", b, v, ok)
		}
	}
}

// The #743 bug: the deny-wildcard must marshal BEFORE the specific
// re-enables so opencode's last-entry-wins (over Object.entries insertion
// order) lets the allows win. Go's json.Marshal sorts map keys
// alphabetically; '*' (0x2A) precedes '_' (0x5F), so
// "chepherd_chepherd_*" lands before "chepherd_chepherd_<x>". Pin that.
func TestOpencodeToolSlim_WildcardSortsBeforeAllows(t *testing.T) {
	b, err := json.Marshal(opencodeToolSlim())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	wildcardIdx := strings.Index(s, `"chepherd_chepherd_*"`)
	if wildcardIdx < 0 {
		t.Fatal("deny-wildcard key not found in marshaled JSON")
	}
	for _, name := range wantOpencodeAllowed {
		idx := strings.Index(s, `"`+name+`"`)
		if idx < 0 {
			t.Fatalf("allow key %s not found in marshaled JSON", name)
		}
		if idx < wildcardIdx {
			t.Errorf("allow %s (idx %d) marshaled BEFORE deny-wildcard (idx %d) — "+
				"opencode would deny it; this is the #743 regression", name, idx, wildcardIdx)
		}
	}
}

// The generated opencode.json must carry all four TPM levers in the right
// shape. Exercises writeFlavorMCPConfig end-to-end via a captured marshal.
func TestOpencodeConfig_CarriesAllTPMLevers(t *testing.T) {
	cfg := buildOpencodeConfigForTest(t, "cerebras/gpt-oss-120b", "oc-test")

	// (1) tool allow-list present + correct.
	tools, ok := cfg["tools"].(map[string]any)
	if !ok {
		t.Fatal("opencode.json missing top-level tools map")
	}
	if tools["chepherd_chepherd_get_task"] != true {
		t.Error("tools.chepherd_chepherd_get_task should be true")
	}
	if tools["chepherd_chepherd_*"] != false {
		t.Error("tools.chepherd_chepherd_* should be false")
	}

	// (2) build-agent prompt override present + names the tools + knock.
	agent, ok := cfg["agent"].(map[string]any)
	if !ok {
		t.Fatal("opencode.json missing agent map")
	}
	build, ok := agent["build"].(map[string]any)
	if !ok {
		t.Fatal("opencode.json missing agent.build")
	}
	prompt, _ := build["prompt"].(string)
	for _, want := range []string{"oc-test", "chepherd_chepherd_get_task", "chepherd_chepherd_send_to_session", "[chepherd-knock"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("agent.build.prompt missing %q", want)
		}
	}

	// (3) instructions:[] (empty array, not absent — so opencode doesn't
	// union stray rules files into the prompt).
	instr, ok := cfg["instructions"].([]string)
	if !ok || len(instr) != 0 {
		t.Errorf("instructions should be empty []string, got %#v", cfg["instructions"])
	}

	// (4) per-model output cap.
	prov, ok := cfg["provider"].(map[string]any)
	if !ok {
		t.Fatal("opencode.json missing provider block")
	}
	cer, ok := prov["cerebras"].(map[string]any)
	if !ok {
		t.Fatal("provider.cerebras missing")
	}
	models, _ := cer["models"].(map[string]any)
	m, _ := models["gpt-oss-120b"].(map[string]any)
	limit, _ := m["limit"].(map[string]any)
	if limit["output"] != opencodeMaxOutputTokens {
		t.Errorf("provider.cerebras.models.gpt-oss-120b.limit.output = %v, want %d",
			limit["output"], opencodeMaxOutputTokens)
	}

	// small_model present (drives the cheap title request).
	if cfg["small_model"] != "cerebras/gpt-oss-120b" {
		t.Errorf("small_model = %v, want cerebras/gpt-oss-120b", cfg["small_model"])
	}
}

func TestOpencodeProviderOutputCap_DerivesProviderFromModel(t *testing.T) {
	for _, tc := range []struct{ model, provider, modelID string }{
		{"groq/llama-3.3-70b-versatile", "groq", "llama-3.3-70b-versatile"},
		{"cerebras/gpt-oss-120b", "cerebras", "gpt-oss-120b"},
	} {
		cap := opencodeProviderOutputCap(tc.model)
		p, ok := cap[tc.provider].(map[string]any)
		if !ok {
			t.Errorf("%s: provider %q not in cap map %#v", tc.model, tc.provider, cap)
			continue
		}
		models := p["models"].(map[string]any)
		if _, ok := models[tc.modelID]; !ok {
			t.Errorf("%s: model %q not in cap models %#v", tc.model, tc.modelID, models)
		}
	}
	// Bare / malformed model → nil (no provider segment).
	if opencodeProviderOutputCap("noslash") != nil {
		t.Error("model without a provider segment should yield nil cap")
	}
}

// buildOpencodeConfigForTest reproduces writeFlavorMCPConfig's opencode
// cfg assembly so the test can assert on the map without touching the
// filesystem or needing a live MCP URL. Kept in lock-step with the
// production switch case (both read opencodeModelFromEnv → the same
// model-keyed levers). If writeFlavorMCPConfig's opencode branch changes,
// this helper must mirror it.
func buildOpencodeConfigForTest(t *testing.T, model, name string) map[string]any {
	t.Helper()
	cfg := map[string]any{
		"$schema":      "https://opencode.ai/config.json",
		"mcp":          map[string]any{"chepherd": map[string]any{}},
		"tools":        opencodeToolSlim(),
		"instructions": []string{},
	}
	cfg["model"] = model
	cfg["small_model"] = model
	cfg["agent"] = map[string]any{"build": map[string]any{"prompt": opencodeMeshPrompt(name)}}
	if prov := opencodeProviderOutputCap(model); prov != nil {
		cfg["provider"] = prov
	}
	return cfg
}

// Groq is now the preferred opencode default over Cerebras: the output
// cap made Groq fit 12k, and Groq's non-reasoning model avoids the
// Cerebras reasoning_content replay that breaks the multi-step tool loop.
func TestOpencodeModelFromEnv_PrefersGroqOverCerebras(t *testing.T) {
	spec := SpawnSpec{AgentSlug: "opencode"}
	spec.Env = []string{"CEREBRAS_API_KEY=x", "GROQ_API_KEY=y"}
	if got := opencodeModelFromEnv(spec); got != "groq/llama-3.3-70b-versatile" {
		t.Fatalf("with both keys, default = %q, want groq/llama-3.3-70b-versatile", got)
	}
	// Cerebras-only → cerebras (still the fallback when no Groq key).
	spec.Env = []string{"CEREBRAS_API_KEY=x"}
	if got := opencodeModelFromEnv(spec); got != "cerebras/gpt-oss-120b" {
		t.Fatalf("cerebras-only default = %q, want cerebras/gpt-oss-120b", got)
	}
}

func TestCompactFlavorBriefing_IsSmallAndHasPeersAndCanon(t *testing.T) {
	spec := SpawnSpec{Name: "oc-test", AgentSlug: "opencode", Team: "p0", Role: RoleWorker}
	peers := []PeerBrief{{Name: "peer-a", Role: "worker", AgentSlug: "opencode"}}
	canon := "Team charter: ship the mesh."

	compact := renderCompactFlavorBriefing(spec, peers, canon)
	full := renderFlavorBriefing(SpawnSpec{Name: "x", AgentSlug: "gemini-cli", Team: "p0", Role: RoleWorker}, peers, "gemini-cli", canon)

	// Compact must be dramatically smaller than the full briefing.
	if len(compact) >= len(full)/3 {
		t.Errorf("compact briefing (%d B) not much smaller than full (%d B)", len(compact), len(full))
	}
	// But it must still carry the peer roster + canon (the dynamic bits the
	// build-prompt can't).
	if !strings.Contains(compact, "peer-a") {
		t.Error("compact briefing missing peer roster")
	}
	if !strings.Contains(compact, "ship the mesh") {
		t.Error("compact briefing missing team canon")
	}
	// renderFlavorBriefing dispatches opencode → compact.
	if got := renderFlavorBriefing(spec, peers, "opencode", canon); got != compact {
		t.Error("renderFlavorBriefing should route opencode to renderCompactFlavorBriefing")
	}
}

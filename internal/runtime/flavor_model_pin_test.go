// internal/runtime/flavor_model_pin_test.go — pins the #79 fix: the daemon
// writes settings.model.name = gemini-3.1-flash-lite for gemini-cli. The earlier
// gemini-2.5-flash pin (c9ff5d0/#743) does NOT stick — gemini-cli v0.46 under
// gemini-api-key auth force-remaps every 2.x/3.x "flash" model to gemini-3.5-flash
// inside resolveModel (isFlashModel && useGemini3_5Flash → DEFAULT_GEMINI_FLASH_MODEL
// = "gemini-3.5-flash"). Proven live 2026-06-21: --model gemini-2.5-flash hit
// "limit: 20, model: gemini-3.5-flash". gemini-3.1-flash-lite ends in "flash-lite"
// (isFlashModel false) so the pin survives the remap, verified live (agent replied
// on gemini-3.1-flash-lite). qwen-code is a gemini fork but runs qwen models, so it
// must NOT get the gemini pin. Format verified from the bundle (reads settings.model?.name).
package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFlavorMCPConfig_GeminiModelPin(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	readSettings := func(slug, rel string) map[string]any {
		home := t.TempDir()
		rt.writeFlavorMCPConfig(SpawnSpec{AgentSlug: slug, Name: "agent-" + slug}, home)
		b, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil {
			t.Fatalf("%s: read %s: %v", slug, rel, err)
		}
		var cfg map[string]any
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatalf("%s: unmarshal %s: %v", slug, rel, err)
		}
		return cfg
	}

	// gemini-cli MUST pin settings.model.name = gemini-3.1-flash-lite (a model the
	// 3.5-flash remap can't reach, since isFlashModel keys off the "flash" suffix
	// and "flash-lite" doesn't match).
	g := readSettings("gemini-cli", filepath.Join(".gemini", "settings.json"))
	model, ok := g["model"].(map[string]any)
	if !ok {
		t.Fatalf("gemini-cli: settings.model missing or not an object: %#v", g["model"])
	}
	if got := model["name"]; got != "gemini-3.1-flash-lite" {
		t.Errorf("gemini-cli: settings.model.name = %v, want gemini-3.1-flash-lite (#79)", got)
	}

	// qwen-code is a gemini fork but runs qwen models — it must NOT inherit the
	// gemini model pin.
	q := readSettings("qwen-code", filepath.Join(".qwen", "settings.json"))
	if _, exists := q["model"]; exists {
		t.Errorf("qwen-code: settings.model should be absent (gemini-only pin), got %#v", q["model"])
	}
}

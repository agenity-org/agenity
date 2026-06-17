// internal/runtime/flavor_model_pin_test.go — pins the c9ff5d0 fix: the daemon
// writes settings.model.name = gemini-2.5-flash for gemini-cli so agents do NOT
// default to gemini-3.5-flash, whose FREE tier is only ~20 req/day. Observed live
// 2026-06-17: a gemini-cli agent called chepherd.get_task -> OK then died
// "Usage limit reached for gemini-3.5-flash" before it could send_to_session.
// qwen-code is a gemini fork but runs qwen models, so it must NOT get the gemini
// pin. Format verified from the gemini-cli bundle (reads settings.model?.name).
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

	// gemini-cli MUST pin settings.model.name = gemini-2.5-flash (off the 20/day
	// gemini-3.5-flash default).
	g := readSettings("gemini-cli", filepath.Join(".gemini", "settings.json"))
	model, ok := g["model"].(map[string]any)
	if !ok {
		t.Fatalf("gemini-cli: settings.model missing or not an object: %#v", g["model"])
	}
	if got := model["name"]; got != "gemini-2.5-flash" {
		t.Errorf("gemini-cli: settings.model.name = %v, want gemini-2.5-flash (c9ff5d0)", got)
	}

	// qwen-code is a gemini fork but runs qwen models — it must NOT inherit the
	// gemini model pin.
	q := readSettings("qwen-code", filepath.Join(".qwen", "settings.json"))
	if _, exists := q["model"]; exists {
		t.Errorf("qwen-code: settings.model should be absent (gemini-only pin), got %#v", q["model"])
	}
}

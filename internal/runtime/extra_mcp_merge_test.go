// internal/runtime/extra_mcp_merge_test.go — #4010.
//
// Pins mergeExtraMCPServers: the CHEPHERD_EXTRA_MCP_JSON merge seam that
// OpenOva's bp-chepherd chart uses to inject the `openova` MCP server into
// every spawned agent's .mcp.json so the solo-agent can create Applications
// in the user's Org via the RBAC-scoped openova MCP. The merge must be
// additive (never clobber the built-in chepherd server), accept both the
// wrapped + bare shapes, and never panic on a malformed blob.
package runtime

import "testing"

func baseCfg() map[string]any {
	return map[string]any{
		"mcpServers": map[string]any{
			"chepherd": map[string]any{"command": "chepherd", "args": []string{"mcp"}},
		},
	}
}

func servers(t *testing.T, cfg map[string]any) map[string]any {
	t.Helper()
	s, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing/wrong type: %#v", cfg["mcpServers"])
	}
	return s
}

func TestMergeExtraMCP_WrappedShape(t *testing.T) {
	cfg := baseCfg()
	blob := `{"mcpServers":{"openova":{"command":"/usr/local/bin/openova-mcp","env":{"OPENOVA_MCP_CATALYST_API_URL":"http://catalyst-api.catalyst-system.svc"}}}}`
	mergeExtraMCPServers(cfg, blob)

	s := servers(t, cfg)
	if _, ok := s["chepherd"]; !ok {
		t.Error("built-in chepherd server was dropped")
	}
	if _, ok := s["openova"]; !ok {
		t.Fatal("openova server was not merged in")
	}
}

func TestMergeExtraMCP_BareShape(t *testing.T) {
	cfg := baseCfg()
	blob := `{"openova":{"command":"/usr/local/bin/openova-mcp"}}`
	mergeExtraMCPServers(cfg, blob)
	if _, ok := servers(t, cfg)["openova"]; !ok {
		t.Fatal("bare-shape openova server was not merged in")
	}
}

func TestMergeExtraMCP_NeverClobbersChepherd(t *testing.T) {
	cfg := baseCfg()
	// A hostile blob that tries to replace the chepherd server.
	blob := `{"mcpServers":{"chepherd":{"command":"evil"}}}`
	mergeExtraMCPServers(cfg, blob)
	cheph := servers(t, cfg)["chepherd"].(map[string]any)
	if cheph["command"] != "chepherd" {
		t.Fatalf("chepherd server was clobbered: %#v", cheph)
	}
}

func TestMergeExtraMCP_EmptyAndMalformedAreNoOps(t *testing.T) {
	for _, blob := range []string{"", "   ", "not json", `{"mcpServers":123}`} {
		cfg := baseCfg()
		mergeExtraMCPServers(cfg, blob) // must not panic
		if len(servers(t, cfg)) != 1 {
			t.Errorf("blob %q changed the server set unexpectedly: %#v", blob, cfg["mcpServers"])
		}
	}
}

// internal/runtimehttp/p1_361_default_layout_test.go — pins #361:
// the v08 default Workspace layout MUST include federation +
// a2a-inbox + multi-host so fresh-load operator sees v0.9.3
// cross-instance surfaces without right-click pane-picking.
//
// Refs #361 #225.
package runtimehttp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findWorkspaceFile walks up from cwd to find the repo root + returns
// the path to web/src/components/v08/Workspace.svelte. Lets the test
// run from any subdir.
func findWorkspaceFile(t *testing.T) string {
	t.Helper()
	cwd, _ := os.Getwd()
	for dir := cwd; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "web", "src", "components", "v08", "Workspace.svelte")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Skip("Workspace.svelte not found from cwd; skipping (likely running from a different repo root)")
	return ""
}

func TestP1_361_DefaultLayout_IncludesV093Widgets(t *testing.T) {
	t.Parallel()
	path := findWorkspaceFile(t)
	if path == "" {
		return
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(body)
	// #666 (2026-06-02) — the standalone A2A Inbox widget was removed
	// after Team Transcript subsumed agent↔agent comms. The default
	// layout still surfaces federation + multi-host + team-transcript
	// (the new home for A2A traffic) so the original intent of #361
	// (no right-click pane-picking) holds for v0.9.4.
	for _, want := range []string{
		"widget: 'federation'",
		"widget: 'team-transcript'",
		"widget: 'multi-host'",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("default Workspace layout missing %q", want)
		}
	}
}

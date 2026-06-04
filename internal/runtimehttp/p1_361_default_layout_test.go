// internal/runtimehttp/p1_361_default_layout_test.go — pins the v08
// workspace's structural surface.
//
// History: #361 required cross-instance widgets on fresh load; #666
// removed the a2a-inbox; #692 removed the federation/multi-host panes;
// #709.S1.1 shipped the REAL fixed shell — rail / agent-details /
// transcript became fixed chrome (Shell.svelte), the pane grid was
// demoted to the center region, and the default center is a single
// terminal pane. The #361 intent (everything important visible on
// fresh load with zero pane-picking) is now satisfied STRUCTURALLY:
// the chrome cannot even be closed.
//
// Refs #361 #225 #666 #692 #690 #709.
package runtimehttp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoWebFile(t *testing.T, rel string) string {
	t.Helper()
	cwd, _ := os.Getwd()
	for dir := cwd; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, rel)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Skipf("%s not found from cwd; skipping", rel)
	return ""
}

func TestP1_361_FixedShell_StructuralSurface(t *testing.T) {
	t.Parallel()
	wsPath := repoWebFile(t, "web/src/components/v08/Workspace.svelte")
	shellPath := repoWebFile(t, "web/src/components/v08/Shell.svelte")
	if wsPath == "" || shellPath == "" {
		return
	}
	ws, err := os.ReadFile(wsPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	shell, err := os.ReadFile(shellPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	wsSrc, shellSrc := string(ws), string(shell)

	// The canvas renders the fixed Shell with all four regions.
	if !strings.Contains(wsSrc, "<Shell>") {
		t.Errorf("Workspace canvas does not render <Shell> — the fixed chrome is gone (#709.S1.1)")
	}
	for _, snippet := range []string{"{#snippet rail()}", "{#snippet center()}", "{#snippet contextTop()}", "{#snippet contextBottom()}"} {
		if !strings.Contains(wsSrc, snippet) {
			t.Errorf("Workspace missing Shell region %q", snippet)
		}
	}
	// Chrome regions exist in Shell and carry no pane controls.
	for _, marker := range []string{"shell-rail", "shell-center", "shell-context-top", "shell-context-bottom"} {
		if !strings.Contains(shellSrc, marker) {
			t.Errorf("Shell missing region marker %q", marker)
		}
	}
	// The default CENTER is a terminal pane and references no chrome widget.
	if !strings.Contains(wsSrc, "widget: 'terminal'") {
		t.Errorf("default center layout missing the terminal pane")
	}
	for _, banned := range []string{"widget: 'federation'", "widget: 'multi-host'", "widget: 'session-list'", "widget: 'inspector'"} {
		if strings.Contains(wsSrc, banned) {
			t.Errorf("layout/preset code still emits chrome/deleted pane %q (#709 made it fixed chrome)", banned)
		}
	}
}

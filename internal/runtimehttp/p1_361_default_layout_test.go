// internal/runtimehttp/p1_361_default_layout_test.go — pins the default
// v08 Workspace layout shape.
//
// History: #361 required federation + a2a-inbox + multi-host on fresh
// load (no right-click pane-picking for cross-instance surfaces). #666
// removed the a2a-inbox (Team Transcript subsumed it). #692 (the #690
// UX redesign) removed the federation + multi-host PANES entirely —
// their content moved into the session rail (⇄ mesh rows) and the
// Inspector's mesh section — and the default layout became the #690
// target shell: rail | terminal + transcript | inspector + transcript.
//
// The #361 intent ("fresh load shows the cross-instance surfaces with
// zero pane-picking") still holds: the rail (session-list) carries the
// mesh peers and the inspector carries reachability/synced detail, both
// present in the default layout below.
//
// Refs #361 #225 #666 #692 #690.
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

func TestP1_361_DefaultLayout_TargetShellWidgets(t *testing.T) {
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
	// #692 target shell: rail + terminal + transcript + inspector all
	// present on fresh load with zero pane-picking.
	for _, want := range []string{
		"widget: 'session-list'",
		"widget: 'terminal'",
		"widget: 'team-transcript'",
		"widget: 'inspector'",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("default Workspace layout missing %q", want)
		}
	}
	// The deleted panes must NOT reappear in the default layout.
	for _, banned := range []string{
		"widget: 'federation'",
		"widget: 'multi-host'",
	} {
		if strings.Contains(src, banned) {
			t.Errorf("default Workspace layout still references deleted pane %q (#692 removed it)", banned)
		}
	}
}

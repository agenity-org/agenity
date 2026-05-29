package canon

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestBannedVocabProjectWide runs scripts/banned-vocab-grep.sh and
// fails if any banned vocabulary leaks into the catalog or wizard
// surfaces. Wired here (rather than in roles/skills/templateregistry)
// so the project-wide guard fires from a single test target.
//
// The grep script's own allowlist defines which paths are exempt
// (infrastructure-layer 'shepherd' role-name in internal/runtime +
// internal/mcpserver; marketing copy in web/src/pages/brand.astro
// etc.; _test.go fixture lists).
//
// Filed in response to architect's 2026-05-28 brief on #200 — the
// anti-regression test must grep-fail on any banned-vocab leak.
func TestBannedVocabProjectWide(t *testing.T) {
	// Walk up to the repo root from this file's location.
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	for i := 0; i < 6; i++ {
		if _, err := exec.LookPath(filepath.Join(dir, "scripts", "banned-vocab-grep.sh")); err == nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	script := filepath.Join(dir, "scripts", "banned-vocab-grep.sh")
	cmd := exec.Command("bash", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("banned-vocab-grep.sh failed:\n%s", strings.TrimSpace(string(out)))
	}
}

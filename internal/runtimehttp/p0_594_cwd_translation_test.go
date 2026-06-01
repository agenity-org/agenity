// internal/runtimehttp/p0_594_cwd_translation_test.go pins #594:
// host-direct chepherd must translate the v0.9 wizard's hardcoded
// '/home/chepherd/repos' cwd to the daemon's actual host home
// equivalent, AND pre-validate cwd existence with an actionable
// error (not the kernel-level ENOENT from os/exec that earlier
// masked as 'fork/exec /usr/bin/podman: no such file').
//
// Coverage:
//
//   - '/home/chepherd/...' on host where /home/chepherd doesn't exist
//     and operator's $HOME does → translates to $HOME/... when that
//     path exists (success case)
//   - Non-existent cwd → returns clear error citing wizard Stage 2
//     repo selection (replaces the misleading podman-exec error)
//   - Empty cwd + empty providerID → defaults to operator $HOME
//     (unchanged behaviour, regression guard)
//   - Existing absolute path that isn't /home/chepherd/... → passes
//     through unchanged
//
// Refs #594 V0.9.2-ARCHITECTURE.md.
package runtimehttp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP0_594_CwdTranslation_HomeChepherdToHostHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if home == "/home/chepherd" {
		t.Skip("daemon running as user chepherd; translation no-op (canonical container topology)")
	}
	// Pre-condition: /home/chepherd doesn't exist on this host (or
	// the wizard's hardcoded path isn't valid as-is) → translation
	// must kick in.
	if _, err := os.Stat("/home/chepherd/repos"); err == nil {
		t.Skip("/home/chepherd/repos exists (canonical or symlinked); translation already a no-op")
	}
	// Ensure $HOME/repos exists for the success case (the operator's
	// actual workspace root).
	dst := filepath.Join(home, "repos")
	if _, err := os.Stat(dst); err != nil {
		t.Skipf("operator $HOME/repos doesn't exist (%v); skipping translation success case", err)
	}
	s := &Server{}
	got, err := s.resolveProviderCwd("", "/home/chepherd/repos", "")
	if err != nil {
		t.Fatalf("resolveProviderCwd: %v", err)
	}
	if got != dst {
		t.Fatalf("translated cwd = %q, want %q", got, dst)
	}
}

func TestP0_594_CwdTranslation_NonExistentCwd_ActionableError(t *testing.T) {
	s := &Server{}
	bogus := "/definitely/does/not/exist/anywhere/12345"
	_, err := s.resolveProviderCwd("", bogus, "")
	if err == nil {
		t.Fatal("expected error for non-existent cwd, got nil")
	}
	if !strings.Contains(err.Error(), "spawn cwd does not exist") {
		t.Fatalf("error not actionable: %q (want substring 'spawn cwd does not exist')", err)
	}
	if !strings.Contains(err.Error(), bogus) {
		t.Fatalf("error doesn't cite the bogus path: %q", err)
	}
	if !strings.Contains(err.Error(), "Stage 2") {
		t.Fatalf("error doesn't hint at wizard Stage 2: %q", err)
	}
}

func TestP0_594_CwdTranslation_EmptyDefaultsToHome(t *testing.T) {
	s := &Server{}
	home, _ := os.UserHomeDir()
	got, err := s.resolveProviderCwd("", "", "")
	if err != nil {
		t.Fatalf("resolveProviderCwd: %v", err)  // empty fallbackCwd → UserHomeDir
	}
	if got != home {
		t.Fatalf("default cwd = %q, want $HOME = %q", got, home)
	}
}

func TestP0_594_CwdTranslation_AbsolutePathPassthrough(t *testing.T) {
	s := &Server{}
	// Pick an absolute path that exists + isn't /home/chepherd/...
	// /tmp is universally writable + exists.
	got, err := s.resolveProviderCwd("", "/tmp", "")
	if err != nil {
		t.Fatalf("resolveProviderCwd: %v", err)
	}
	if got != "/tmp" {
		t.Fatalf("passthrough cwd = %q, want /tmp", got)
	}
}

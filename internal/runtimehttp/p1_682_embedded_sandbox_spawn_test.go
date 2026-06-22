// internal/runtimehttp/p1_682_embedded_sandbox_spawn_test.go pins #682:
// the built-in / embedded sandbox is a VIRTUAL provider — the wizard
// sends provider_id "embedded" with no clone_url and never persists a
// git-providers.json record. Before the fix, resolveProviderCwd iterated
// the persisted providers, found nothing matching "embedded", and fell
// through to `provider "embedded" not registered`, so the default
// Solo + Built-in spawn could never produce a running agent.
//
// These tests pin the ROUTING decision without booting the Gitea
// sidecar (podman is unavailable in CI): provider_id "embedded" must
// route to the embedded ensure+clone path, NOT the "not registered"
// dead-end. The embedded path will still error in CI (no podman), but
// the error must be an "embedded gitea" error — proving the request
// reached the right branch.
//
// Refs #682.
package runtimehttp

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenity-org/agenity/internal/runtime"
)

func TestP1_682_EmbeddedProvider_RoutesToEmbeddedPath_NotNotRegistered(t *testing.T) {
	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	s := &Server{rt: rt}

	// Stub the embedded-Gitea boot so the test pins the ROUTING decision
	// without booting a real container. The stub records the repo name it
	// was asked for and returns a sentinel error — if the request reaches
	// the embedded path, we see the sentinel; if it hits the old dead-end,
	// we see "not registered" instead.
	var gotRepo string
	sentinel := errors.New("stub-embedded-boot-reached")
	orig := ensureEmbeddedGiteaFn
	ensureEmbeddedGiteaFn = func(stateDir, repoName string) (*runtime.EmbeddedGiteaInfo, error) {
		gotRepo = repoName
		return nil, sentinel
	}
	t.Cleanup(func() { ensureEmbeddedGiteaFn = orig })

	// This is exactly what the wizard sends for Solo + Built-in:
	// provider_id "embedded", empty clone_url, cwd under repos/<owner>/<repo>.
	_, err = s.resolveProviderCwd("embedded", "/home/chepherd/repos/chepherd-admin/uat-walk-demo", "")
	if err == nil {
		t.Fatal("expected the stub sentinel error, got nil — embedded path not reached")
	}
	if strings.Contains(err.Error(), "not registered") {
		t.Fatalf("embedded provider hit the 'not registered' dead-end (the #682 bug): %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error %q does not wrap the embedded-path sentinel — routing unclear", err)
	}
	// And the repo name must come from the cwd basename, not a default.
	if gotRepo != "uat-walk-demo" {
		t.Fatalf("embedded repo name = %q, want %q (derived from cwd basename)", gotRepo, "uat-walk-demo")
	}
}

func TestP1_682_RepoNameFromCwd(t *testing.T) {
	cases := []struct{ cwd, want string }{
		{"/home/chepherd/repos/chepherd-admin/uat-walk-demo", "uat-walk-demo"},
		{"/home/chepherd/repos/chepherd-admin/uat-walk-demo/", "uat-walk-demo"},
		{"/home/chepherd/repos/single", "single"},
		{"", ""},
	}
	for _, c := range cases {
		if got := repoNameFromCwd(c.cwd); got != c.want {
			t.Errorf("repoNameFromCwd(%q) = %q, want %q", c.cwd, got, c.want)
		}
	}
}

// A genuinely unknown provider id (not "embedded", no persisted record)
// must STILL return the clear "not registered" error — the fix must not
// swallow that diagnostic for real external providers.
func TestP1_682_UnknownProvider_StillNotRegistered(t *testing.T) {
	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	s := &Server{rt: rt}
	_, err = s.resolveProviderCwd("github:does-not-exist", filepath.Join(t.TempDir(), "x"), "")
	if err == nil {
		t.Fatal("expected 'not registered' error for unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unknown provider error = %q, want 'not registered'", err)
	}
}

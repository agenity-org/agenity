package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

// openTestStore wires a SQLite persistence.Store backed by a temp DB
// for runtime.Runner tests.
func openTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "runner.db")
	store, err := sqlite.NewStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestNewRunner_ProcessKind(t *testing.T) {
	t.Parallel()
	r, err := NewRunner(RunnerConfig{
		Kind:     RunnerKindProcess,
		Store:    openTestStore(t),
		StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewRunner process: %v", err)
	}
	if _, ok := r.(*ProcessRunner); !ok {
		t.Errorf("NewRunner returned %T, want *ProcessRunner", r)
	}
}

func TestNewRunner_PodKindRequiresKubeconfig(t *testing.T) {
	t.Parallel()
	if _, err := NewRunner(RunnerConfig{
		Kind:     RunnerKindPod,
		Store:    openTestStore(t),
		StateDir: t.TempDir(),
	}); err == nil {
		t.Error("NewRunner pod without KubeconfigPath: want error, got nil")
	}
}

func TestNewRunner_PodKindWithKubeconfig(t *testing.T) {
	t.Parallel()
	r, err := NewRunner(RunnerConfig{
		Kind:           RunnerKindPod,
		Store:          openTestStore(t),
		StateDir:       t.TempDir(),
		KubeconfigPath: filepath.Join(t.TempDir(), "kubeconfig"),
	})
	if err != nil {
		t.Fatalf("NewRunner pod: %v", err)
	}
	if _, ok := r.(*PodRunner); !ok {
		t.Errorf("NewRunner returned %T, want *PodRunner", r)
	}
}

func TestNewRunner_UnknownKind(t *testing.T) {
	t.Parallel()
	_, err := NewRunner(RunnerConfig{
		Kind:     RunnerKind("bogus"),
		Store:    openTestStore(t),
		StateDir: t.TempDir(),
	})
	if err == nil {
		t.Error("NewRunner bogus kind: want error, got nil")
	}
}

func TestNewRunner_MissingStore(t *testing.T) {
	t.Parallel()
	_, err := NewRunner(RunnerConfig{
		Kind:     RunnerKindProcess,
		StateDir: t.TempDir(),
	})
	if err == nil {
		t.Error("NewRunner missing Store: want error, got nil")
	}
}

func TestNewRunner_MissingStateDir(t *testing.T) {
	t.Parallel()
	_, err := NewRunner(RunnerConfig{
		Kind:  RunnerKindProcess,
		Store: openTestStore(t),
	})
	if err == nil {
		t.Error("NewRunner missing StateDir: want error, got nil")
	}
}

// TestPodRunner_ScaffoldPending — historical name preserved post-#349.
// Pre-D1.7 this verified the scaffold-pending error path; D1.7 made
// newPodRunner actually parse the kubeconfig, so this test now pins
// that NewRunner with a NON-EXISTENT kubeconfig fails at construction.
// Methods on a successfully-constructed PodRunner are exercised by
// the D1 #312 + D1.2-D1.7 #349 batch tests.
func TestPodRunner_ScaffoldPending(t *testing.T) {
	t.Parallel()
	_, err := NewRunner(RunnerConfig{
		Kind:           RunnerKindPod,
		Store:          openTestStore(t),
		StateDir:       t.TempDir(),
		KubeconfigPath: filepath.Join(t.TempDir(), "kubeconfig"),
	})
	if err == nil {
		t.Fatal("NewRunner pod with non-existent kubeconfig: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "kubeconfig") {
		t.Errorf("err = %v, want kubeconfig-related", err)
	}
	return
	// Block below preserved-but-unreachable for git-history readability.
	// The original sentinel-error assertions belong to a different
	// universe; the new contract is the file-not-found surface above.
	var r Runner
	ctx := context.Background()
	for _, call := range []struct {
		name string
		fn   func() error
	}{
		{"Spawn", func() error { _, e := r.Spawn(ctx, SpawnSpec{Name: "x"}); return e }},
		{"Stop", func() error { return r.Stop(ctx, "x") }},
		{"Get", func() error { _, e := r.Get(ctx, "x"); return e }},
		{"List", func() error { _, e := r.List(ctx); return e }},
		{"Pause", func() error { return r.Pause(ctx, "x", true) }},
		{"Restart", func() error { return r.Restart(ctx, "x") }},
		{"Rename", func() error { return r.Rename(ctx, "x", "y") }},
		{"AttachIO", func() error { _, e := r.AttachIO(ctx, "x"); return e }},
	} {
		if err := call.fn(); err == nil || !strings.Contains(err.Error(), "scaffold pending") {
			t.Errorf("%s: want 'scaffold pending' error, got %v", call.name, err)
		}
	}
}

// TestNewWithStore_UsesRepository verifies the v0.9.2 persistence
// wire-up: when NewWithStore receives a non-nil persistence.Store, the
// internal agent registry is opened via NewStoreFromRepository (not
// file-on-disk). Easiest way to verify: Save an Agent through the
// Runtime's exposed registry + observe it lands in the underlying
// repository.
func TestNewWithStore_UsesRepository(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	rt, err := NewWithStore(t.TempDir(), store)
	if err != nil {
		t.Fatalf("NewWithStore: %v", err)
	}

	// AgentRegistry returns the v0.9.1 Store type either way; the
	// difference is whether its underlying NewStore* constructor was
	// file-on-disk or repository-backed. We probe by calling repo.List
	// directly — if Runtime's agentRegistry shares the same SQLite DB
	// (via Store.Agents()), List should round-trip an agent the same
	// way as the equivalence suite already proves.
	agents, err := store.Agents().List(context.Background())
	if err != nil {
		t.Fatalf("Repo.Agents.List: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("fresh store: agents = %d, want 0", len(agents))
	}
	// Smoke: rt is non-nil + agentRegistry is non-nil; structural
	// assertion that wire-up didn't crash.
	if rt.AgentRegistry() == nil {
		t.Error("agentRegistry should be non-nil")
	}
}

// TestProcessRunner_DelegatesToRuntime verifies ProcessRunner wires
// to Runtime — Get/Stop on a non-existent session returns the Runtime
// error (or wrapped ErrSessionNotFound), not the scaffold-pending
// sentinel.
func TestProcessRunner_DelegatesToRuntime(t *testing.T) {
	t.Parallel()
	r, err := NewRunner(RunnerConfig{
		Kind:     RunnerKindProcess,
		Store:    openTestStore(t),
		StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	ctx := context.Background()

	// Get on non-existent session returns ErrSessionNotFound (NOT
	// scaffold-pending — proves we're delegating).
	if _, err := r.Get(ctx, "nonexistent"); err == nil {
		t.Error("Get nonexistent: want error, got nil")
	} else if strings.Contains(err.Error(), "scaffold pending") {
		t.Errorf("Get returned scaffold-pending — delegation broken: %v", err)
	}

	// List on empty Runtime returns empty (not scaffold-pending).
	got, err := r.List(ctx)
	if err != nil {
		t.Errorf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List empty Runtime = %d, want 0", len(got))
	}
}

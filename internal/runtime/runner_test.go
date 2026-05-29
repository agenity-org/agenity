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

// TestPodRunner_ScaffoldPending pins the sentinel-error contract on
// PodRunner. ProcessRunner is wired to the existing Runtime in this
// sub-branch's runtime-migration commit; PodRunner stays scaffold
// until the k8s-integration commit.
func TestPodRunner_ScaffoldPending(t *testing.T) {
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

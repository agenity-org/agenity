// internal/runtime/orphan_reaper_test.go — root-cause fix for the
// "61 orphan session rows" mess after repeated daemon bounces.
//
// Two regressions pinned:
//   1. Stop(name) must delete the sessionsRepo row (parallel to #646
//      claudeLoginCancel fix).
//   2. reapOnce sweeps rows whose id has no matching in-memory session
//      (catches crashes / external kills / daemon-restart leaks).
package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

func TestOrphanReaper_StopDeletesRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "reaper.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	r, err := NewWithStore(t.TempDir(), store)
	if err != nil {
		t.Fatalf("NewWithStore: %v", err)
	}

	// Simulate a Spawn-style row by writing directly + entering it in
	// the in-memory registry (avoid needing a real container runtime).
	id := "test-agent-1"
	name := "test-agent"
	if err := store.Sessions().Save(ctx, id, map[string]any{"name": name}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	r.mu.Lock()
	r.byName[name] = id
	r.sessions[id] = nil // sentinel — Stop tolerates nil session
	r.info[id] = &SessionInfo{ID: id, Name: name, CreatedAt: time.Now()}
	r.mu.Unlock()

	// Stop should delete the in-memory entry AND the persisted row.
	if err := r.Stop(name); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	ids, err := store.Sessions().List(ctx)
	if err != nil {
		t.Fatalf("List after Stop: %v", err)
	}
	for _, got := range ids {
		if got == id {
			t.Errorf("row %q still present after Stop — #646-pattern regression", id)
		}
	}
}

func TestOrphanReaper_ReapsAbandonedRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "reaper2.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	r, err := NewWithStore(t.TempDir(), store)
	if err != nil {
		t.Fatalf("NewWithStore: %v", err)
	}

	// Seed 5 orphan rows directly into the store WITHOUT entering them
	// in the in-memory registry — simulates daemon restart where agents
	// died but rows persisted.
	for i, n := range []string{"orphan-1", "orphan-2", "orphan-3", "orphan-4", "orphan-5"} {
		_ = i
		if err := store.Sessions().Save(ctx, n, map[string]any{"name": n}); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}
	// Add ONE row that IS in-memory — must survive the reap.
	keepID := "live-session-1"
	if err := store.Sessions().Save(ctx, keepID, map[string]any{"name": "live-session"}); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	r.mu.Lock()
	r.sessions[keepID] = nil
	r.mu.Unlock()

	deleted := r.reapOnce(ctx)
	if deleted != 5 {
		t.Errorf("reapOnce deleted %d, want 5", deleted)
	}

	ids, _ := store.Sessions().List(ctx)
	if len(ids) != 1 || ids[0] != keepID {
		t.Errorf("after reap: ids=%v, want [%q]", ids, keepID)
	}
}

// Ensure the reaper handles the "everything is live" case (no-op).
func TestOrphanReaper_NoOpWhenAllLive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "reaper3.db"))
	defer store.Close()
	r, _ := NewWithStore(t.TempDir(), store)

	id := "live-1"
	_ = store.Sessions().Save(ctx, id, map[string]any{"name": "live"})
	r.mu.Lock()
	r.sessions[id] = nil
	r.mu.Unlock()

	deleted := r.reapOnce(ctx)
	if deleted != 0 {
		t.Errorf("reapOnce on all-live = %d, want 0", deleted)
	}
}

// Silence unused-import vet on persistence package in some Go versions.
var _ = persistence.SessionRepository(nil)

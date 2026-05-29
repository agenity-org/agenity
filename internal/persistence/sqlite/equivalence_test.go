package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chepherd/chepherd/internal/persistence/equivalence"
)

// TestEquivalence_Sqlite runs the chepherd v0.9.2 persistence
// backend-equivalence suite (internal/persistence/equivalence) against
// the SQLite Store. The same suite is run against the PostgreSQL Store
// from internal/persistence/postgres/equivalence_test.go, so any
// behavioral drift between backends surfaces as a test failure on the
// non-conforming side.
//
// Refs #208.
func TestEquivalence_Sqlite(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "equiv.db")
	store, err := NewStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	equivalence.RunAll(t, store)
}

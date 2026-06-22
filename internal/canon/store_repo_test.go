package canon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/agenity-org/agenity/internal/persistence/sqlite"
)

// TestStore_RepoBacked verifies that NewStoreFromRepository wired against
// the SQLite CanonRepository preserves the same public-method behavior
// as the file-on-disk Store: Get→blank when empty, Put→assigns version
// monotonically, History/Rollback work, ErrVersionNotFound on missing.
//
// Refs #208.
func TestStore_RepoBacked(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "canon.db")
	store, err := sqlite.NewStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := NewStoreFromRepository(store.Canon())

	// Get on empty returns a blank canon, not nil.
	c, err := s.Get()
	if err != nil {
		t.Fatalf("Get empty: %v", err)
	}
	if c == nil {
		t.Fatal("Get empty returned nil, want blank canon")
	}
	if c.ID != "default" || c.Version != 0 || c.Body != "" {
		t.Errorf("Get empty = %+v, want default/v0/blank", c)
	}

	// Put v1.
	v1, err := s.Put("first body", "operator", "v1 title")
	if err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if v1.Body != "first body" || v1.UpdatedBy != "operator" || v1.Title != "v1 title" || v1.Version != 1 {
		t.Errorf("v1 = %+v", v1)
	}

	// Put v2 with empty title → inherits v1 title.
	v2, err := s.Put("second body", "operator", "")
	if err != nil {
		t.Fatalf("Put v2: %v", err)
	}
	if v2.Title != "v1 title" {
		t.Errorf("v2 title = %q, want sticky %q", v2.Title, "v1 title")
	}
	if v2.Version <= v1.Version {
		t.Errorf("v2.Version=%d not > v1.Version=%d", v2.Version, v1.Version)
	}

	// Get current → v2.
	c, _ = s.Get()
	if c.Version != v2.Version || c.Body != "second body" {
		t.Errorf("Get current = %+v", c)
	}

	// History → [v1].
	hist, err := s.History(10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 1 || hist[0].Version != v1.Version {
		t.Errorf("History = %v", hist)
	}

	// Rollback to v1.
	rolled, err := s.Rollback(v1.Version, "operator")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolled.Body != "first body" {
		t.Errorf("Rollback body = %q, want 'first body'", rolled.Body)
	}

	// Rollback to missing version → ErrVersionNotFound (preserves v0.9.1 error semantic).
	if _, err := s.Rollback(9999, "operator"); err != ErrVersionNotFound {
		t.Errorf("Rollback missing err = %v, want ErrVersionNotFound", err)
	}
}

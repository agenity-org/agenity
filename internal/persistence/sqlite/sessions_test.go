package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/agenity-org/agenity/internal/persistence/migrate"

	_ "modernc.org/sqlite"
)

// openTestDB returns a SQLite db with v0.9.2 schema applied.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := migrate.Run(context.Background(), db, "sqlite"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestSessionRepository_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewSessionRepository(openTestDB(t))

	// Get on missing → empty map, no error.
	got, err := r.Get(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Get missing: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Get missing = %v, want empty map", got)
	}

	// Save + Get round-trip.
	want := map[string]any{"trust_band": "trusted", "intervention_count": float64(3)}
	if err := r.Save(ctx, "sess-1", want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err = r.Get(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Get after Save: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Get after Save = %v, want %v", got, want)
	}

	// Save overwrites.
	want2 := map[string]any{"trust_band": "concerned"}
	if err := r.Save(ctx, "sess-1", want2); err != nil {
		t.Fatalf("Save overwrite: %v", err)
	}
	got, _ = r.Get(ctx, "sess-1")
	if !reflect.DeepEqual(got, want2) {
		t.Errorf("Get after overwrite = %v, want %v", got, want2)
	}
}

func TestSessionRepository_ListDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewSessionRepository(openTestDB(t))

	// Empty list.
	ids, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("List empty = %v, want []", ids)
	}

	// Seed + list.
	for _, id := range []string{"a", "c", "b"} {
		if err := r.Save(ctx, id, map[string]any{"id": id}); err != nil {
			t.Fatalf("Save %q: %v", id, err)
		}
	}
	ids, _ = r.List(ctx)
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("List = %v, want %v (lexicographic)", ids, want)
	}

	// Delete + verify.
	if err := r.Delete(ctx, "b"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	ids, _ = r.List(ctx)
	want = []string{"a", "c"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("List after delete = %v, want %v", ids, want)
	}

	// Delete missing is not an error.
	if err := r.Delete(ctx, "nonexistent"); err != nil {
		t.Errorf("Delete missing returned error: %v", err)
	}
}

func TestSessionRepository_EmptyID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewSessionRepository(openTestDB(t))

	if _, err := r.Get(ctx, ""); err == nil {
		t.Error("Get(empty) = nil, want error")
	}
	if err := r.Save(ctx, "", nil); err == nil {
		t.Error("Save(empty) = nil, want error")
	}
	if err := r.Delete(ctx, ""); err == nil {
		t.Error("Delete(empty) = nil, want error")
	}
}

package migrate

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestRun_SQLite_AppliesAndIdempotent applies the SQLite schema against
// an in-memory database and verifies (a) all 13 entity tables exist,
// (b) Run is idempotent (calling twice doesn't error or double-apply).
//
// Refs #208.
func TestRun_SQLite_AppliesAndIdempotent(t *testing.T) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// First Run — applies migrations.
	if err := Run(ctx, db, "sqlite"); err != nil {
		t.Fatalf("Run (first): %v", err)
	}

	// Verify all 13 entity tables exist.
	wantTables := []string{
		"sessions", "skills", "agents", "canon", "keychain",
		"templates", "auth_secrets", "events", "rbac_grants",
		"tasks", "push_notification_configs", "agent_cards",
		"accounts",
		// + the bookkeeping table
		"chepherd_schema_migrations",
	}
	for _, table := range wantTables {
		var name string
		err := db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='table' AND name=$1`,
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", table, err)
		}
	}

	// Second Run — idempotent.
	if err := Run(ctx, db, "sqlite"); err != nil {
		t.Fatalf("Run (second): %v", err)
	}

	// Bookkeeping should have exactly one row per migration file.
	var applied int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chepherd_schema_migrations`,
	).Scan(&applied); err != nil {
		t.Fatalf("count bookkeeping: %v", err)
	}
	if applied != 1 {
		t.Errorf("applied count = %d, want 1 (only 001_init.sqlite.sql at this point)", applied)
	}
}

func TestRun_UnsupportedDialect(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, _ := sql.Open("sqlite", "file:"+dbPath)
	defer db.Close()

	if err := Run(context.Background(), db, "mysql"); err == nil {
		t.Fatal("Run(mysql): want error, got nil")
	}
}

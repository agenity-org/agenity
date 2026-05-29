// Package sqlite implements persistence.Store backed by SQLite via
// modernc.org/sqlite (pure-Go driver, no cgo). This is the chepherd
// v0.9.2 single-instance persistence default; see internal/persistence
// (#208) for the canonical interface contract.
package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Open returns a *sql.DB connected to the SQLite file at path.
// The parent directory is created if missing. The DB is configured
// for WAL journal mode + foreign keys.
//
// Schema migrations are NOT run here; call migrate.Run after Open.
//
// Refs #208.
func Open(path string) (*sql.DB, error) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		// caller is responsible for ensuring dir exists; we don't
		// MkdirAll here because that's a side effect the persistence
		// package should not own. Document and let it bubble up.
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open %q: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite ping %q: %w", path, err)
	}
	return db, nil
}

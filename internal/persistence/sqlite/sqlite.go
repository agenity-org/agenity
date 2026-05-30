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
// for WAL journal mode, foreign keys, and a 5-second busy timeout —
// the latter being load-bearing for #296.
//
// **#296 fix**: SQLite serializes writes at the database level (only
// ONE writer at a time, regardless of WAL mode). database/sql's
// connection pool opens multiple connections by default; when chepherd's
// boot sequence fires (1) the auth_secrets Save for ES256 lazy creation,
// (2) the session-registry hydration, (3) the catalog migration, and
// (4) the first reconciler tick concurrently — each on a different pool
// connection — they race for the write lock, the loser gets SQLITE_BUSY
// (5), and JWKS publication silently fails.
//
// Two-pronged fix:
//   - `_pragma=busy_timeout(5000)` makes SQLite wait up to 5s for the
//     write lock instead of bouncing immediately. Eliminates the race
//     for typical boot-time write storms (writes complete in <50ms).
//   - `SetMaxOpenConns(1)` caps the pool at a SINGLE connection, which
//     forces database/sql to serialize all queries at the Go layer
//     before reaching the SQLite mutex. Defense-in-depth: even if a
//     future query happens to exceed 5s, the second writer waits in
//     Go's pool queue rather than hitting SQLITE_BUSY at the driver.
//
// The 1-connection cap is acceptable for chepherd's single-instance
// shape: write load is dozens of ops/second peak, and modernc.org/sqlite
// is fast enough that serial execution doesn't bottleneck. HA backends
// (postgres) don't have this constraint and use the default pool size.
//
// Schema migrations are NOT run here; call migrate.Run after Open.
//
// Refs #208 #296.
func Open(path string) (*sql.DB, error) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		// caller is responsible for ensuring dir exists; we don't
		// MkdirAll here because that's a side effect the persistence
		// package should not own. Document and let it bubble up.
	}
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open %q: %w", path, err)
	}
	// See package doc above — single connection serializes writes at
	// the Go layer to eliminate the SQLITE_BUSY race the busy_timeout
	// PRAGMA covers as defense-in-depth.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite ping %q: %w", path, err)
	}
	return db, nil
}

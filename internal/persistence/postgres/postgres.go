// Package postgres implements persistence.Store backed by PostgreSQL
// via jackc/pgx/v5 (used through the stdlib database/sql adapter so
// the same Repository implementations work on both drivers). This is
// the chepherd v0.9.2 HA persistence backend; see internal/persistence
// (#208) for the canonical interface contract.
package postgres

import (
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
)

// Open returns a *sql.DB connected to the PostgreSQL DSN. The DSN
// follows libpq syntax (postgres://user:pass@host:port/dbname?...).
//
// Schema migrations are NOT run here; call migrate.Run after Open.
//
// Refs #208.
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres open: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	return db, nil
}

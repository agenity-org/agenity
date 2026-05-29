// Package migrate runs the chepherd v0.9.2 schema migrations against an
// open *sql.DB (sqlite or postgres). Migration files live alongside this
// package as numbered .sql files (001_*.sql, 002_*.sql, ...) embedded
// via go:embed.
//
// Migrations are append-only — never edit a merged migration; add a new
// numbered file instead. Refs #208.
package migrate

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed *.sql
var schemaFS embed.FS

// Run applies all pending migrations in lexicographic order. Idempotent:
// migrations already recorded in the chepherd_schema_migrations table are
// skipped. Safe to call on every daemon start.
//
// dialect MUST be "sqlite" or "postgres" — selects the bookkeeping
// table's autoincrement syntax.
func Run(ctx context.Context, db *sql.DB, dialect string) error {
	if dialect != "sqlite" && dialect != "postgres" {
		return fmt.Errorf("migrate: unsupported dialect %q (want sqlite or postgres)", dialect)
	}

	if err := ensureBookkeepingTable(ctx, db, dialect); err != nil {
		return fmt.Errorf("migrate: ensure bookkeeping table: %w", err)
	}

	applied, err := listApplied(ctx, db)
	if err != nil {
		return fmt.Errorf("migrate: list applied: %w", err)
	}

	files, err := listMigrationFiles(dialect)
	if err != nil {
		return fmt.Errorf("migrate: list migration files: %w", err)
	}

	for _, f := range files {
		if _, done := applied[f]; done {
			continue
		}
		body, err := schemaFS.ReadFile(f)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", f, err)
		}
		if err := applyOne(ctx, db, f, string(body)); err != nil {
			return fmt.Errorf("migrate: apply %s: %w", f, err)
		}
	}
	return nil
}

func ensureBookkeepingTable(ctx context.Context, db *sql.DB, dialect string) error {
	var ddl string
	switch dialect {
	case "sqlite":
		ddl = `CREATE TABLE IF NOT EXISTS chepherd_schema_migrations (
			filename TEXT PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`
	case "postgres":
		ddl = `CREATE TABLE IF NOT EXISTS chepherd_schema_migrations (
			filename TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`
	}
	_, err := db.ExecContext(ctx, ddl)
	return err
}

func listApplied(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT filename FROM chepherd_schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		out[f] = struct{}{}
	}
	return out, rows.Err()
}

// listMigrationFiles returns *.<dialect>.sql files in lexicographic order.
// Convention: NNN_title.<dialect>.sql (e.g. 001_init.sqlite.sql,
// 001_init.postgres.sql). Files for the other dialect are skipped.
func listMigrationFiles(dialect string) ([]string, error) {
	suffix := "." + dialect + ".sql"
	var files []string
	err := fs.WalkDir(schemaFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, suffix) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func applyOne(ctx context.Context, db *sql.DB, filename, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("exec migration: %w", err)
	}
	// $1 placeholder works on both modernc.org/sqlite and pgx (sqlite
	// accepts $NNN as numbered placeholder per its bound-parameter spec).
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chepherd_schema_migrations (filename) VALUES ($1)`,
		filename,
	); err != nil {
		return fmt.Errorf("record applied: %w", err)
	}
	return tx.Commit()
}

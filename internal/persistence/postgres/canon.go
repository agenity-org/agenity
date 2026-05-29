package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// CanonRepository implements persistence.CanonRepository against
// PostgreSQL. Same single-table + is_current flag design as SQLite
// (see internal/persistence/sqlite/canon.go for design rationale).
// Differences from SQLite:
//   - Save uses RETURNING version (pgx doesn't support LastInsertId).
//   - boolean column uses real BOOLEAN type (no boolToInt shim needed).
//
// Refs #208.
type CanonRepository struct {
	db *sql.DB
}

func NewCanonRepository(db *sql.DB) *CanonRepository {
	return &CanonRepository{db: db}
}

func (r *CanonRepository) Get(ctx context.Context) (*persistence.Canon, error) {
	var c persistence.Canon
	err := r.db.QueryRowContext(ctx,
		`SELECT version, body, title, updated_by, updated_at
		 FROM canon WHERE is_current = TRUE`,
	).Scan(&c.Version, &c.Body, &c.Title, &c.UpdatedBy, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres canon Get: %w", err)
	}
	return &c, nil
}

func (r *CanonRepository) Save(ctx context.Context, body, updatedBy, title string) (*persistence.Canon, error) {
	if body == "" {
		return nil, errors.New("postgres CanonRepository: empty body")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("canon Save begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE canon SET is_current = FALSE WHERE is_current = TRUE`,
	); err != nil {
		return nil, fmt.Errorf("canon Save unflag: %w", err)
	}
	now := time.Now().UTC()
	var version int
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO canon (body, title, updated_by, updated_at, is_current)
		 VALUES ($1, $2, $3, $4, TRUE)
		 RETURNING version`,
		body, title, updatedBy, now,
	).Scan(&version); err != nil {
		return nil, fmt.Errorf("canon Save insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("canon Save commit: %w", err)
	}
	return &persistence.Canon{
		Version:   version,
		Body:      body,
		Title:     title,
		UpdatedBy: updatedBy,
		UpdatedAt: now,
	}, nil
}

func (r *CanonRepository) History(ctx context.Context, limit int) ([]*persistence.Canon, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT version, body, title, updated_by, updated_at
		 FROM canon WHERE is_current = FALSE
		 ORDER BY version DESC LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres canon History: %w", err)
	}
	defer rows.Close()
	var out []*persistence.Canon
	for rows.Next() {
		var c persistence.Canon
		if err := rows.Scan(&c.Version, &c.Body, &c.Title, &c.UpdatedBy, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (r *CanonRepository) Rollback(ctx context.Context, toVersion int, actor string) (*persistence.Canon, error) {
	if toVersion <= 0 {
		return nil, errors.New("postgres CanonRepository: toVersion must be > 0")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("canon Rollback begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM canon WHERE version = $1`, toVersion,
	).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("canon Rollback: version %d not found", toVersion)
	} else if err != nil {
		return nil, fmt.Errorf("canon Rollback verify: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE canon SET is_current = FALSE WHERE is_current = TRUE`,
	); err != nil {
		return nil, fmt.Errorf("canon Rollback unflag: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE canon SET is_current = TRUE, updated_by = $1, updated_at = $2 WHERE version = $3`,
		actor, time.Now().UTC(), toVersion,
	); err != nil {
		return nil, fmt.Errorf("canon Rollback flag: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("canon Rollback commit: %w", err)
	}
	return r.Get(ctx)
}

var _ persistence.CanonRepository = (*CanonRepository)(nil)

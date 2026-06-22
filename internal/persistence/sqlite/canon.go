package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// CanonRepository implements persistence.CanonRepository against SQLite.
// Migrates from $stateDir/canon/{current.json,history/*.json}.
//
// Schema design: single `canon` table holds every version ever written;
// the row with `is_current = 1` is the current version (partial index
// makes that lookup fast). Save appends a new row with auto-incremented
// version + flips is_current. Rollback flips is_current to a prior
// version (the rolled-from version becomes a historical row).
//
// Refs #208.
type CanonRepository struct {
	db *sql.DB
}

func NewCanonRepository(db *sql.DB) *CanonRepository {
	return &CanonRepository{db: db}
}

// Get returns the current canon. Returns (nil, nil) if no canon has
// ever been saved — matches the v0.9.1 Get-on-empty semantic.
func (r *CanonRepository) Get(ctx context.Context) (*persistence.Canon, error) {
	var c persistence.Canon
	err := r.db.QueryRowContext(ctx,
		`SELECT version, body, title, updated_by, updated_at
		 FROM canon WHERE is_current = 1`,
	).Scan(&c.Version, &c.Body, &c.Title, &c.UpdatedBy, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite canon Get: %w", err)
	}
	return &c, nil
}

// Save appends a new canon version atomically: un-flags any prior
// current row, inserts the new row with is_current = 1, returns the
// new Canon with auto-assigned version.
func (r *CanonRepository) Save(ctx context.Context, body, updatedBy, title string) (*persistence.Canon, error) {
	if body == "" {
		return nil, errors.New("sqlite CanonRepository: empty body")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("canon Save begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE canon SET is_current = 0 WHERE is_current = 1`,
	); err != nil {
		return nil, fmt.Errorf("canon Save unflag: %w", err)
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO canon (body, title, updated_by, updated_at, is_current)
		 VALUES ($1, $2, $3, $4, 1)`,
		body, title, updatedBy, now,
	)
	if err != nil {
		return nil, fmt.Errorf("canon Save insert: %w", err)
	}
	version, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("canon Save lastid: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("canon Save commit: %w", err)
	}
	return &persistence.Canon{
		Version:   int(version),
		Body:      body,
		Title:     title,
		UpdatedBy: updatedBy,
		UpdatedAt: now,
	}, nil
}

// History returns up to limit historical (non-current) versions in
// descending version order. limit <= 0 → up to 100.
func (r *CanonRepository) History(ctx context.Context, limit int) ([]*persistence.Canon, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT version, body, title, updated_by, updated_at
		 FROM canon WHERE is_current = 0
		 ORDER BY version DESC LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite canon History: %w", err)
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

// Rollback flips is_current to the row at toVersion. The previously-
// current row becomes a historical row. Returns the new current Canon.
// Errors if toVersion doesn't exist.
func (r *CanonRepository) Rollback(ctx context.Context, toVersion int, actor string) (*persistence.Canon, error) {
	if toVersion <= 0 {
		return nil, errors.New("sqlite CanonRepository: toVersion must be > 0")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("canon Rollback begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Verify target exists.
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM canon WHERE version = $1`, toVersion,
	).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("canon Rollback: version %d not found", toVersion)
	} else if err != nil {
		return nil, fmt.Errorf("canon Rollback verify: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE canon SET is_current = 0 WHERE is_current = 1`,
	); err != nil {
		return nil, fmt.Errorf("canon Rollback unflag: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE canon SET is_current = 1, updated_by = $1, updated_at = $2 WHERE version = $3`,
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

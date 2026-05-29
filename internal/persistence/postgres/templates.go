package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// TemplateRepository implements persistence.TemplateRepository against
// SQLite. Migrates from $stateDir/templates-registry/{id}.yaml.
//
// Refs #208.
type TemplateRepository struct {
	db *sql.DB
}

func NewTemplateRepository(db *sql.DB) *TemplateRepository {
	return &TemplateRepository{db: db}
}

func (r *TemplateRepository) Get(ctx context.Context, id string) (*persistence.Template, error) {
	if id == "" {
		return nil, errors.New("postgres TemplateRepository: empty id")
	}
	var t persistence.Template
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, description, body, updated_at FROM templates WHERE id = $1`,
		id,
	).Scan(&t.ID, &t.Name, &t.Description, &t.Body, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("postgres templates Get %q: not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres templates Get %q: %w", id, err)
	}
	return &t, nil
}

func (r *TemplateRepository) List(ctx context.Context) ([]*persistence.Template, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, description, body, updated_at FROM templates ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres templates List: %w", err)
	}
	defer rows.Close()
	var out []*persistence.Template
	for rows.Next() {
		var t persistence.Template
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Body, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (r *TemplateRepository) Save(ctx context.Context, t *persistence.Template) error {
	if t == nil {
		return errors.New("postgres TemplateRepository: nil template")
	}
	if t.ID == "" {
		return errors.New("postgres TemplateRepository: empty ID")
	}
	t.UpdatedAt = time.Now().UTC()
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO templates (id, name, description, body, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT(id) DO UPDATE SET
		     name = excluded.name,
		     description = excluded.description,
		     body = excluded.body,
		     updated_at = excluded.updated_at`,
		t.ID, t.Name, t.Description, t.Body, t.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres templates Save %q: %w", t.ID, err)
	}
	return nil
}

func (r *TemplateRepository) Delete(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("postgres TemplateRepository: empty id")
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM templates WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("postgres templates Delete %q: %w", id, err)
	}
	return nil
}

var _ persistence.TemplateRepository = (*TemplateRepository)(nil)

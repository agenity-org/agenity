package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// SkillRepository implements persistence.SkillRepository against SQLite.
// Migrates from $stateDir/skills-registry/{id}.override.json. Each row
// holds both the default_body (from upstream catalog) and the
// optional override_body (operator's edits).
//
// Refs #208.
type SkillRepository struct {
	db *sql.DB
}

func NewSkillRepository(db *sql.DB) *SkillRepository {
	return &SkillRepository{db: db}
}

func (r *SkillRepository) Get(ctx context.Context, id string) (*persistence.Skill, error) {
	if id == "" {
		return nil, errors.New("sqlite SkillRepository: empty id")
	}
	var (
		s        persistence.Skill
		readOnly int
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, default_body, override_body, read_only, source, path, sort_order, metadata_json, updated_at
		 FROM skills WHERE id = $1`,
		id,
	).Scan(&s.ID, &s.Name, &s.DefaultBody, &s.OverrideBody, &readOnly,
		&s.Source, &s.Path, &s.SortOrder, &s.Metadata, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sqlite skills Get %q: not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite skills Get %q: %w", id, err)
	}
	s.ReadOnly = readOnly == 1
	return &s, nil
}

func (r *SkillRepository) List(ctx context.Context, opts persistence.SkillListOpts) ([]persistence.Skill, error) {
	var (
		conds []string
		args  []any
	)
	idx := 1
	if opts.Source != "" {
		conds = append(conds, fmt.Sprintf("source = $%d", idx))
		args = append(args, opts.Source)
		idx++
	}
	if !opts.IncludeOverridden {
		// Default: include all. When the operator opts out, filter to
		// only rows that have a non-empty override_body (i.e., the
		// operator-customized subset).
	} else {
		conds = append(conds, "override_body <> ''")
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	q := fmt.Sprintf(
		`SELECT id, name, default_body, override_body, read_only, source, path, sort_order, metadata_json, updated_at
		 FROM skills %s ORDER BY sort_order ASC, id ASC`,
		where,
	)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite skills List: %w", err)
	}
	defer rows.Close()
	var out []persistence.Skill
	for rows.Next() {
		var (
			s        persistence.Skill
			readOnly int
		)
		if err := rows.Scan(&s.ID, &s.Name, &s.DefaultBody, &s.OverrideBody, &readOnly,
			&s.Source, &s.Path, &s.SortOrder, &s.Metadata, &s.UpdatedAt); err != nil {
			return nil, err
		}
		s.ReadOnly = readOnly == 1
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *SkillRepository) Save(ctx context.Context, s *persistence.Skill) error {
	if s == nil {
		return errors.New("sqlite SkillRepository: nil skill")
	}
	if s.ID == "" {
		return errors.New("sqlite SkillRepository: empty skill ID")
	}
	s.UpdatedAt = time.Now().UTC()
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO skills (id, name, default_body, override_body, read_only, source, path, sort_order, metadata_json, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT(id) DO UPDATE SET
		     name = excluded.name,
		     default_body = excluded.default_body,
		     override_body = excluded.override_body,
		     read_only = excluded.read_only,
		     source = excluded.source,
		     path = excluded.path,
		     sort_order = excluded.sort_order,
		     metadata_json = excluded.metadata_json,
		     updated_at = excluded.updated_at`,
		s.ID, s.Name, s.DefaultBody, s.OverrideBody, boolToInt(s.ReadOnly),
		s.Source, s.Path, s.SortOrder, jsonOrEmpty(s.Metadata), s.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("sqlite skills Save %q: %w", s.ID, err)
	}
	return nil
}

func (r *SkillRepository) Delete(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("sqlite SkillRepository: empty id")
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM skills WHERE id = $1`, id,
	)
	if err != nil {
		return fmt.Errorf("sqlite skills Delete %q: %w", id, err)
	}
	return nil
}

var _ persistence.SkillRepository = (*SkillRepository)(nil)

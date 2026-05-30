package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// ArtifactRepository implements persistence.ArtifactRepository against
// SQLite. NEW in v0.9.3 #225 row H3. Backed by the artifacts table
// (migration 003_artifacts.sqlite.sql).
type ArtifactRepository struct {
	db *sql.DB
}

func NewArtifactRepository(db *sql.DB) *ArtifactRepository {
	return &ArtifactRepository{db: db}
}

func (r *ArtifactRepository) Get(ctx context.Context, artifactID string) (*persistence.Artifact, error) {
	if artifactID == "" {
		return nil, errors.New("sqlite ArtifactRepository: empty artifactID")
	}
	var a persistence.Artifact
	err := r.db.QueryRowContext(ctx,
		`SELECT id, task_id, name, parts_json, metadata_json, created_at
		 FROM artifacts WHERE id = $1`,
		artifactID,
	).Scan(&a.ID, &a.TaskID, &a.Name, &a.Parts, &a.Metadata, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sqlite artifacts Get %q: not found", artifactID)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite artifacts Get %q: %w", artifactID, err)
	}
	return &a, nil
}

func (r *ArtifactRepository) List(ctx context.Context, taskID string) ([]*persistence.Artifact, error) {
	if taskID == "" {
		return nil, errors.New("sqlite ArtifactRepository: empty taskID")
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, task_id, name, parts_json, metadata_json, created_at
		 FROM artifacts WHERE task_id = $1 ORDER BY created_at, id`,
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite artifacts List %q: %w", taskID, err)
	}
	defer rows.Close()
	var out []*persistence.Artifact
	for rows.Next() {
		var a persistence.Artifact
		if err := rows.Scan(&a.ID, &a.TaskID, &a.Name, &a.Parts, &a.Metadata, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

func (r *ArtifactRepository) Save(ctx context.Context, a *persistence.Artifact) error {
	if a == nil {
		return errors.New("sqlite ArtifactRepository: nil artifact")
	}
	if a.ID == "" {
		return errors.New("sqlite ArtifactRepository: empty artifact ID")
	}
	if a.TaskID == "" {
		return errors.New("sqlite ArtifactRepository: empty TaskID (artifacts require FK)")
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	parts := a.Parts
	if len(parts) == 0 {
		parts = []byte("[]")
	}
	meta := a.Metadata
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO artifacts (id, task_id, name, parts_json, metadata_json, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT(id) DO UPDATE SET
		     task_id       = excluded.task_id,
		     name          = excluded.name,
		     parts_json    = excluded.parts_json,
		     metadata_json = excluded.metadata_json`,
		a.ID, a.TaskID, a.Name, parts, meta, a.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("sqlite artifacts Save %q: %w", a.ID, err)
	}
	return nil
}

func (r *ArtifactRepository) Delete(ctx context.Context, artifactID string) error {
	if artifactID == "" {
		return errors.New("sqlite ArtifactRepository: empty artifactID")
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM artifacts WHERE id = $1`, artifactID,
	)
	if err != nil {
		return fmt.Errorf("sqlite artifacts Delete %q: %w", artifactID, err)
	}
	return nil
}

var _ persistence.ArtifactRepository = (*ArtifactRepository)(nil)

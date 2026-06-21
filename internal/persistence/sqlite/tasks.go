package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// TaskRepository implements persistence.TaskRepository against SQLite.
// NEW in v0.9.2. Persists A2A task state machines (one row per task
// on this runner).
//
// Refs #208.
type TaskRepository struct {
	db *sql.DB
}

func NewTaskRepository(db *sql.DB) *TaskRepository {
	return &TaskRepository{db: db}
}

func (r *TaskRepository) Get(ctx context.Context, taskID string) (*persistence.Task, error) {
	if taskID == "" {
		return nil, errors.New("sqlite TaskRepository: empty taskID")
	}
	var t persistence.Task
	err := r.db.QueryRowContext(ctx,
		`SELECT id, runner_sid, state, method, input_blob, output_blob,
		        auth_challenge, created_at, updated_at
		 FROM tasks WHERE id = $1`,
		taskID,
	).Scan(&t.ID, &t.RunnerSID, &t.State, &t.Method, &t.InputBlob, &t.OutputBlob,
		&t.AuthChallenge, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sqlite tasks Get %q: not found", taskID)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite tasks Get %q: %w", taskID, err)
	}
	return &t, nil
}

func (r *TaskRepository) Save(ctx context.Context, t *persistence.Task) error {
	if t == nil {
		return errors.New("sqlite TaskRepository: nil task")
	}
	if t.ID == "" {
		return errors.New("sqlite TaskRepository: empty task ID")
	}
	if t.RunnerSID == "" {
		return errors.New("sqlite TaskRepository: empty RunnerSID")
	}
	if t.State == "" {
		return errors.New("sqlite TaskRepository: empty State")
	}
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO tasks
		   (id, runner_sid, state, method, input_blob, output_blob,
		    auth_challenge, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT(id) DO UPDATE SET
		     runner_sid = excluded.runner_sid,
		     state = excluded.state,
		     method = excluded.method,
		     input_blob = excluded.input_blob,
		     output_blob = excluded.output_blob,
		     auth_challenge = excluded.auth_challenge,
		     updated_at = excluded.updated_at`,
		t.ID, t.RunnerSID, t.State, t.Method, t.InputBlob, t.OutputBlob,
		t.AuthChallenge, t.CreatedAt, t.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("sqlite tasks Save %q: %w", t.ID, err)
	}
	return nil
}

func (r *TaskRepository) List(ctx context.Context, opts persistence.TaskListOpts) ([]*persistence.Task, error) {
	var (
		conds []string
		args  []any
	)
	idx := 1
	if opts.RunnerSID != "" {
		conds = append(conds, fmt.Sprintf("runner_sid = $%d", idx))
		args = append(args, opts.RunnerSID)
		idx++
	}
	if opts.State != "" {
		conds = append(conds, fmt.Sprintf("state = $%d", idx))
		args = append(args, opts.State)
		idx++
	}
	if opts.SinceID != "" {
		conds = append(conds, fmt.Sprintf("id > $%d", idx))
		args = append(args, opts.SinceID)
		idx++
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 1000
	}
	// Default order is ascending id (UUIDv7 == chronological) so SinceID
	// cursor pagination (`id > cursor`) stays consistent. Newest flips to
	// created_at DESC so a bounded Limit returns the MOST-RECENT N — the
	// team transcript needs this or recent messages drop once tasks > Limit.
	// id DESC is a deterministic tie-break: without it, rows that share a
	// created_at fall in/out of the LIMIT window non-deterministically.
	order := "ORDER BY id"
	if opts.Newest {
		order = "ORDER BY created_at DESC, id DESC"
	}
	q := fmt.Sprintf(
		`SELECT id, runner_sid, state, method, input_blob, output_blob,
		        auth_challenge, created_at, updated_at
		 FROM tasks %s %s LIMIT %d`,
		where, order, limit,
	)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite tasks List: %w", err)
	}
	defer rows.Close()
	var out []*persistence.Task
	for rows.Next() {
		var t persistence.Task
		if err := rows.Scan(&t.ID, &t.RunnerSID, &t.State, &t.Method, &t.InputBlob,
			&t.OutputBlob, &t.AuthChallenge, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (r *TaskRepository) Delete(ctx context.Context, taskID string) error {
	if taskID == "" {
		return errors.New("sqlite TaskRepository: empty taskID")
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM tasks WHERE id = $1`, taskID,
	)
	if err != nil {
		return fmt.Errorf("sqlite tasks Delete %q: %w", taskID, err)
	}
	return nil
}

var _ persistence.TaskRepository = (*TaskRepository)(nil)

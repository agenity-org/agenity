package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// EventRepository implements persistence.EventRepository against SQLite.
// Append-only audit log; v0.9.2 A2A fields (Method/CallerOrg/CallerSID)
// stored in dedicated columns for queryable cross-org audit summaries.
//
// Refs #208.
type EventRepository struct {
	db *sql.DB
}

func NewEventRepository(db *sql.DB) *EventRepository {
	return &EventRepository{db: db}
}

func (r *EventRepository) Append(ctx context.Context, e persistence.Event) error {
	if e.ID == "" {
		return errors.New("postgres EventRepository: empty event ID")
	}
	if e.Kind == "" {
		return errors.New("postgres EventRepository: empty event Kind")
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO events (id, kind, actor, body, timestamp, a2a_method, caller_org, caller_sid)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.ID, e.Kind, e.Actor, e.Body, e.Timestamp, e.A2AMethod, e.CallerOrg, e.CallerSID,
	)
	if err != nil {
		return fmt.Errorf("postgres events Append %q: %w", e.ID, err)
	}
	return nil
}

// List returns events filtered by opts. Ordered by timestamp ascending.
// Empty opts → returns up to 1000 most-recent events.
func (r *EventRepository) List(ctx context.Context, opts persistence.EventListOpts) ([]persistence.Event, error) {
	var (
		conds []string
		args  []any
	)
	idx := 1
	if !opts.Since.IsZero() {
		conds = append(conds, fmt.Sprintf("timestamp >= $%d", idx))
		args = append(args, opts.Since)
		idx++
	}
	if len(opts.Kinds) > 0 {
		placeholders := make([]string, 0, len(opts.Kinds))
		for _, k := range opts.Kinds {
			placeholders = append(placeholders, fmt.Sprintf("$%d", idx))
			args = append(args, k)
			idx++
		}
		conds = append(conds, "kind IN ("+strings.Join(placeholders, ",")+")")
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 1000
	}
	q := fmt.Sprintf(
		`SELECT id, kind, actor, body, timestamp, a2a_method, caller_org, caller_sid
		 FROM events %s ORDER BY timestamp ASC LIMIT %d`,
		where, limit,
	)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres events List: %w", err)
	}
	defer rows.Close()
	var out []persistence.Event
	for rows.Next() {
		var e persistence.Event
		if err := rows.Scan(&e.ID, &e.Kind, &e.Actor, &e.Body, &e.Timestamp, &e.A2AMethod, &e.CallerOrg, &e.CallerSID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

var _ persistence.EventRepository = (*EventRepository)(nil)

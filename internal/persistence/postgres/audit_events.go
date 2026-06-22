// internal/persistence/postgres/audit_events.go — #489 Wave AU2
// PostgreSQL twin of internal/persistence/sqlite/audit_events.go.
// Same wire shape, same query patterns; backend swap is transparent
// to the AU1 receiver + dashboard query API.
//
// Refs #489 #488.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// AuditEventRepository implements persistence.AuditEventRepository
// against PostgreSQL.
type AuditEventRepository struct {
	db *sql.DB
}

func NewAuditEventRepository(db *sql.DB) *AuditEventRepository {
	return &AuditEventRepository{db: db}
}

func (r *AuditEventRepository) Save(ctx context.Context, ev *persistence.AuditEventRecord) error {
	if ev == nil {
		return errors.New("postgres AuditEventRepository: nil event")
	}
	if ev.ID == "" {
		return errors.New("postgres AuditEventRepository: empty id")
	}
	if ev.OrgID == "" {
		return errors.New("postgres AuditEventRepository: empty org_id")
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	raw := ev.RawJSON
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO audit_events
		   (id, org_id, event_type, timestamp, caller, callee, method,
		    latency_ms, jti, status, error, task_id, raw_json)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::jsonb)
		 ON CONFLICT(id) DO UPDATE SET
		     org_id     = excluded.org_id,
		     event_type = excluded.event_type,
		     timestamp  = excluded.timestamp,
		     caller     = excluded.caller,
		     callee     = excluded.callee,
		     method     = excluded.method,
		     latency_ms = excluded.latency_ms,
		     jti        = excluded.jti,
		     status     = excluded.status,
		     error      = excluded.error,
		     task_id    = excluded.task_id,
		     raw_json   = excluded.raw_json`,
		ev.ID, ev.OrgID, ev.EventType, ev.Timestamp,
		ev.Caller, ev.Callee, ev.Method,
		ev.LatencyMS, ev.JTI, ev.Status, ev.Error, ev.TaskID,
		string(raw),
	)
	if err != nil {
		return fmt.Errorf("postgres audit_events Save %q: %w", ev.ID, err)
	}
	return nil
}

func (r *AuditEventRepository) List(ctx context.Context, opts persistence.AuditEventListOpts) ([]*persistence.AuditEventRecord, error) {
	if opts.OrgID == "" {
		return nil, errors.New("postgres AuditEventRepository: List requires OrgID (per-org partition guard)")
	}
	conds := []string{"org_id = $1"}
	args := []any{opts.OrgID}
	idx := 2
	if opts.Caller != "" {
		conds = append(conds, fmt.Sprintf("caller = $%d", idx))
		args = append(args, opts.Caller)
		idx++
	}
	if opts.Callee != "" {
		conds = append(conds, fmt.Sprintf("callee = $%d", idx))
		args = append(args, opts.Callee)
		idx++
	}
	if opts.Method != "" {
		conds = append(conds, fmt.Sprintf("method = $%d", idx))
		args = append(args, opts.Method)
		idx++
	}
	if opts.Since != nil {
		conds = append(conds, fmt.Sprintf("timestamp >= $%d", idx))
		args = append(args, *opts.Since)
		idx++
	}
	if opts.Until != nil {
		conds = append(conds, fmt.Sprintf("timestamp <= $%d", idx))
		args = append(args, *opts.Until)
		idx++
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	q := fmt.Sprintf(
		`SELECT id, org_id, event_type, timestamp, caller, callee, method,
		        latency_ms, jti, status, error, task_id, raw_json::text
		 FROM audit_events
		 WHERE %s
		 ORDER BY timestamp DESC
		 LIMIT %d`,
		strings.Join(conds, " AND "), limit,
	)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres audit_events List: %w", err)
	}
	defer rows.Close()
	out := make([]*persistence.AuditEventRecord, 0, 32)
	for rows.Next() {
		ev := &persistence.AuditEventRecord{}
		var rawJSON string
		if err := rows.Scan(
			&ev.ID, &ev.OrgID, &ev.EventType, &ev.Timestamp,
			&ev.Caller, &ev.Callee, &ev.Method,
			&ev.LatencyMS, &ev.JTI, &ev.Status, &ev.Error, &ev.TaskID,
			&rawJSON,
		); err != nil {
			return nil, fmt.Errorf("postgres audit_events List scan: %w", err)
		}
		ev.RawJSON = []byte(rawJSON)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

var _ persistence.AuditEventRepository = (*AuditEventRepository)(nil)

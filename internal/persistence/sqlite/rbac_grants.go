package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// RBACGrantRepository implements persistence.RBACGrantRepository
// against SQLite. NEW in v0.9.2 (#7). Persists cross-org peering
// grants with scope/permissions/rate_limit stored as JSON columns.
//
// Refs #208.
type RBACGrantRepository struct {
	db *sql.DB
}

func NewRBACGrantRepository(db *sql.DB) *RBACGrantRepository {
	return &RBACGrantRepository{db: db}
}

func (r *RBACGrantRepository) Get(ctx context.Context, id string) (*persistence.Grant, error) {
	if id == "" {
		return nil, errors.New("sqlite RBACGrantRepository: empty id")
	}
	var (
		g            persistence.Grant
		scopeJSON    string
		permsJSON    string
		rateJSON     string
		expiresAt    sql.NullTime
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT id, granter_org, grantee_org, scope_json, permissions_json,
		        rate_limit_json, expires_at, accepted, created_by, created_at
		 FROM rbac_grants WHERE id = $1`,
		id,
	).Scan(&g.ID, &g.GranterOrg, &g.GranteeOrg, &scopeJSON, &permsJSON,
		&rateJSON, &expiresAt, &g.Accepted, &g.CreatedBy, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sqlite rbac_grants Get %q: not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite rbac_grants Get %q: %w", id, err)
	}
	if err := json.Unmarshal([]byte(scopeJSON), &g.Scope); err != nil {
		return nil, fmt.Errorf("sqlite rbac_grants Get %q scope: %w", id, err)
	}
	if err := json.Unmarshal([]byte(permsJSON), &g.Permissions); err != nil {
		return nil, fmt.Errorf("sqlite rbac_grants Get %q perms: %w", id, err)
	}
	if rateJSON != "null" && rateJSON != "" {
		var rl persistence.GrantRateLimit
		if err := json.Unmarshal([]byte(rateJSON), &rl); err != nil {
			return nil, fmt.Errorf("sqlite rbac_grants Get %q rate: %w", id, err)
		}
		g.RateLimit = &rl
	}
	if expiresAt.Valid {
		t := expiresAt.Time
		g.ExpiresAt = &t
	}
	return &g, nil
}

func (r *RBACGrantRepository) List(ctx context.Context, opts persistence.GrantListOpts) ([]*persistence.Grant, error) {
	var (
		conds []string
		args  []any
	)
	idx := 1
	if opts.GranterOrg != "" {
		conds = append(conds, fmt.Sprintf("granter_org = $%d", idx))
		args = append(args, opts.GranterOrg)
		idx++
	}
	if opts.GranteeOrg != "" {
		conds = append(conds, fmt.Sprintf("grantee_org = $%d", idx))
		args = append(args, opts.GranteeOrg)
		idx++
	}
	if opts.OnlyActive {
		conds = append(conds, "accepted = 1")
		conds = append(conds, fmt.Sprintf("(expires_at IS NULL OR expires_at > $%d)", idx))
		args = append(args, time.Now().UTC())
		idx++
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	q := fmt.Sprintf(
		`SELECT id FROM rbac_grants %s ORDER BY created_at DESC`, where,
	)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite rbac_grants List: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Second-pass fetch full records via Get (re-uses JSON unmarshaling).
	out := make([]*persistence.Grant, 0, len(ids))
	for _, id := range ids {
		g, err := r.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func (r *RBACGrantRepository) Save(ctx context.Context, g *persistence.Grant) error {
	if g == nil {
		return errors.New("sqlite RBACGrantRepository: nil grant")
	}
	if g.ID == "" {
		return errors.New("sqlite RBACGrantRepository: empty grant ID")
	}
	if g.GranterOrg == "" || g.GranteeOrg == "" {
		return errors.New("sqlite RBACGrantRepository: empty granter/grantee org")
	}
	scope, err := json.Marshal(g.Scope)
	if err != nil {
		return fmt.Errorf("marshal scope: %w", err)
	}
	if g.Permissions == nil {
		g.Permissions = []string{}
	}
	perms, err := json.Marshal(g.Permissions)
	if err != nil {
		return fmt.Errorf("marshal permissions: %w", err)
	}
	rate := "null"
	if g.RateLimit != nil {
		b, err := json.Marshal(g.RateLimit)
		if err != nil {
			return fmt.Errorf("marshal rate_limit: %w", err)
		}
		rate = string(b)
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now().UTC()
	}
	var expires sql.NullTime
	if g.ExpiresAt != nil {
		expires = sql.NullTime{Time: *g.ExpiresAt, Valid: true}
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO rbac_grants
		   (id, granter_org, grantee_org, scope_json, permissions_json,
		    rate_limit_json, expires_at, accepted, created_by, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT(id) DO UPDATE SET
		     granter_org = excluded.granter_org,
		     grantee_org = excluded.grantee_org,
		     scope_json = excluded.scope_json,
		     permissions_json = excluded.permissions_json,
		     rate_limit_json = excluded.rate_limit_json,
		     expires_at = excluded.expires_at,
		     accepted = excluded.accepted`,
		g.ID, g.GranterOrg, g.GranteeOrg, string(scope), string(perms),
		rate, expires, boolToInt(g.Accepted), g.CreatedBy, g.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("sqlite rbac_grants Save %q: %w", g.ID, err)
	}
	return nil
}

func (r *RBACGrantRepository) Delete(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("sqlite RBACGrantRepository: empty id")
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM rbac_grants WHERE id = $1`, id,
	)
	if err != nil {
		return fmt.Errorf("sqlite rbac_grants Delete %q: %w", id, err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// jsonOrEmpty returns the input bytes as a string, or "{}" if empty.
// Used for nullable JSON columns so the SQL DEFAULT '{}' is preserved
// when the caller hasn't populated the metadata field.
func jsonOrEmpty(b []byte) string {
	if len(b) == 0 {
		return "{}"
	}
	return string(b)
}

var _ persistence.RBACGrantRepository = (*RBACGrantRepository)(nil)

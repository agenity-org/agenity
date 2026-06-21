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

// AgentCardRepository implements persistence.AgentCardRepository
// against SQLite. NEW in v0.9.2. Caches Agent Cards keyed by SID for
// fast directory lookups; canonical Cards are served by individual
// runners at their .well-known URI.
//
// Refs #208.
type AgentCardRepository struct {
	db *sql.DB
}

func NewAgentCardRepository(db *sql.DB) *AgentCardRepository {
	return &AgentCardRepository{db: db}
}

func (r *AgentCardRepository) Get(ctx context.Context, agentSID string) (*persistence.AgentCard, error) {
	if agentSID == "" {
		return nil, errors.New("sqlite AgentCardRepository: empty SID")
	}
	var (
		c        persistence.AgentCard
		public   int
		syncedAt sql.NullTime
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT sid, name, body, public_visibility, synced_at, updated_at
		 FROM agent_cards WHERE sid = $1`,
		agentSID,
	).Scan(&c.SID, &c.Name, &c.Body, &public, &syncedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sqlite agent_cards Get %q: not found", agentSID)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite agent_cards Get %q: %w", agentSID, err)
	}
	c.PublicVisibility = public == 1
	if syncedAt.Valid {
		c.SyncedAt = syncedAt.Time
	}
	return &c, nil
}

func (r *AgentCardRepository) Save(ctx context.Context, c *persistence.AgentCard) error {
	if c == nil {
		return errors.New("sqlite AgentCardRepository: nil card")
	}
	if c.SID == "" {
		return errors.New("sqlite AgentCardRepository: empty SID")
	}
	if c.Name == "" {
		return errors.New("sqlite AgentCardRepository: empty Name")
	}
	if len(c.Body) == 0 {
		return errors.New("sqlite AgentCardRepository: empty Body")
	}
	c.UpdatedAt = time.Now().UTC()
	var syncedAt sql.NullTime
	if !c.SyncedAt.IsZero() {
		syncedAt = sql.NullTime{Time: c.SyncedAt, Valid: true}
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO agent_cards
		   (sid, name, body, public_visibility, synced_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT(sid) DO UPDATE SET
		     name = excluded.name,
		     body = excluded.body,
		     public_visibility = excluded.public_visibility,
		     synced_at = excluded.synced_at,
		     updated_at = excluded.updated_at`,
		c.SID, c.Name, c.Body, boolToInt(c.PublicVisibility),
		syncedAt, c.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("sqlite agent_cards Save %q: %w", c.SID, err)
	}
	return nil
}

func (r *AgentCardRepository) List(ctx context.Context, opts persistence.AgentCardListOpts) ([]*persistence.AgentCard, error) {
	var (
		conds []string
		args  []any
	)
	idx := 1
	if opts.PublicOnly {
		conds = append(conds, "public_visibility = 1")
	}
	// Capability + Tag filtering requires JSON introspection of the
	// Card body. Implemented here as a substring match on the body
	// blob (cheap, no JSON1 dep); refine to proper JSONPath later if
	// query volume warrants.
	if opts.Capability != "" {
		conds = append(conds, fmt.Sprintf("body LIKE $%d", idx))
		args = append(args, "%"+opts.Capability+"%")
		idx++
	}
	if opts.Tag != "" {
		conds = append(conds, fmt.Sprintf("body LIKE $%d", idx))
		args = append(args, "%"+opts.Tag+"%")
		idx++
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	q := fmt.Sprintf(
		`SELECT sid FROM agent_cards %s ORDER BY sid`,
		where,
	)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite agent_cards List: %w", err)
	}
	defer rows.Close()
	var sids []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			return nil, err
		}
		sids = append(sids, sid)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*persistence.AgentCard, 0, len(sids))
	for _, sid := range sids {
		c, err := r.Get(ctx, sid)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *AgentCardRepository) Delete(ctx context.Context, agentSID string) error {
	if agentSID == "" {
		return errors.New("sqlite AgentCardRepository: empty SID")
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM agent_cards WHERE sid = $1`, agentSID,
	)
	if err != nil {
		return fmt.Errorf("sqlite agent_cards Delete %q: %w", agentSID, err)
	}
	return nil
}

var _ persistence.AgentCardRepository = (*AgentCardRepository)(nil)

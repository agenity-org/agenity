package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// SessionRepository implements persistence.SessionRepository against SQLite.
// State is stored as a JSON-encoded TEXT column; Get and Save round-trip
// through json.Marshal/Unmarshal.
//
// Refs #208.
type SessionRepository struct {
	db *sql.DB
}

// NewSessionRepository wraps an open *sql.DB. The caller MUST have
// already applied migrations via internal/persistence/migrate.Run.
func NewSessionRepository(db *sql.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

// Get returns the persisted state for sessionID. Returns an empty map
// (NOT nil, NOT an error) when the session has no recorded state —
// matches v0.9.1 LoadState semantics.
func (r *SessionRepository) Get(ctx context.Context, sessionID string) (map[string]any, error) {
	if sessionID == "" {
		return nil, errors.New("postgres SessionRepository: empty sessionID")
	}
	var raw string
	err := r.db.QueryRowContext(ctx,
		`SELECT state_json FROM sessions WHERE session_id = $1`,
		sessionID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres sessions Get %q: %w", sessionID, err)
	}
	out := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, fmt.Errorf("postgres sessions Get %q unmarshal: %w", sessionID, err)
		}
	}
	return out, nil
}

// Save UPSERTs the state for sessionID. Idempotent.
func (r *SessionRepository) Save(ctx context.Context, sessionID string, state map[string]any) error {
	if sessionID == "" {
		return errors.New("postgres SessionRepository: empty sessionID")
	}
	if state == nil {
		state = map[string]any{}
	}
	body, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("postgres sessions Save %q marshal: %w", sessionID, err)
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO sessions (session_id, state_json, updated_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT(session_id) DO UPDATE SET
		     state_json = excluded.state_json,
		     updated_at = excluded.updated_at`,
		sessionID, string(body), time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("postgres sessions Save %q: %w", sessionID, err)
	}
	return nil
}

// Delete removes the session row. Returns nil if the session didn't exist.
func (r *SessionRepository) Delete(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("postgres SessionRepository: empty sessionID")
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE session_id = $1`,
		sessionID,
	)
	if err != nil {
		return fmt.Errorf("postgres sessions Delete %q: %w", sessionID, err)
	}
	return nil
}

// List returns all known session IDs in lexicographic order.
func (r *SessionRepository) List(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT session_id FROM sessions ORDER BY session_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres sessions List: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Verify SessionRepository implements the interface.
var _ persistence.SessionRepository = (*SessionRepository)(nil)

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// AgentRepository implements persistence.AgentRepository against SQLite.
// Migrates from $stateDir/agents-registry/{id}.json. The OwnedSkills,
// OwnedSkillsScope, and Sessions slices/maps are stored as JSON columns.
//
// Refs #208.
type AgentRepository struct {
	db *sql.DB
}

func NewAgentRepository(db *sql.DB) *AgentRepository {
	return &AgentRepository{db: db}
}

func (r *AgentRepository) Get(ctx context.Context, id string) (*persistence.Agent, error) {
	if id == "" {
		return nil, errors.New("sqlite AgentRepository: empty id")
	}
	var (
		a             persistence.Agent
		skillsJSON    string
		scopeJSON     string
		sessionsJSON  string
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT id, type, label, role_id, creator_account,
		        owned_skills_json, owned_skills_scope_json, sessions_json,
		        metadata_json, created_at, updated_at
		 FROM agents WHERE id = $1`,
		id,
	).Scan(&a.ID, &a.Type, &a.Label, &a.RoleID, &a.CreatorAccount,
		&skillsJSON, &scopeJSON, &sessionsJSON,
		&a.Metadata, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sqlite agents Get %q: not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite agents Get %q: %w", id, err)
	}
	if err := json.Unmarshal([]byte(skillsJSON), &a.OwnedSkills); err != nil {
		return nil, fmt.Errorf("unmarshal owned_skills: %w", err)
	}
	if err := json.Unmarshal([]byte(scopeJSON), &a.OwnedSkillsScope); err != nil {
		return nil, fmt.Errorf("unmarshal owned_skills_scope: %w", err)
	}
	if err := json.Unmarshal([]byte(sessionsJSON), &a.Sessions); err != nil {
		return nil, fmt.Errorf("unmarshal sessions: %w", err)
	}
	return &a, nil
}

func (r *AgentRepository) List(ctx context.Context) ([]*persistence.Agent, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id FROM agents ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite agents List: %w", err)
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
	out := make([]*persistence.Agent, 0, len(ids))
	for _, id := range ids {
		a, err := r.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *AgentRepository) Save(ctx context.Context, a *persistence.Agent) error {
	if a == nil {
		return errors.New("sqlite AgentRepository: nil agent")
	}
	if a.ID == "" {
		return errors.New("sqlite AgentRepository: empty agent ID")
	}
	if a.Type == "" {
		return errors.New("sqlite AgentRepository: empty agent Type")
	}
	if a.OwnedSkills == nil {
		a.OwnedSkills = []string{}
	}
	if a.OwnedSkillsScope == nil {
		a.OwnedSkillsScope = map[string]string{}
	}
	if a.Sessions == nil {
		a.Sessions = []persistence.SessionRef{}
	}
	skillsJSON, err := json.Marshal(a.OwnedSkills)
	if err != nil {
		return fmt.Errorf("marshal owned_skills: %w", err)
	}
	scopeJSON, err := json.Marshal(a.OwnedSkillsScope)
	if err != nil {
		return fmt.Errorf("marshal owned_skills_scope: %w", err)
	}
	sessionsJSON, err := json.Marshal(a.Sessions)
	if err != nil {
		return fmt.Errorf("marshal sessions: %w", err)
	}
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO agents
		   (id, type, label, role_id, creator_account,
		    owned_skills_json, owned_skills_scope_json, sessions_json,
		    metadata_json, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT(id) DO UPDATE SET
		     type = excluded.type,
		     label = excluded.label,
		     role_id = excluded.role_id,
		     creator_account = excluded.creator_account,
		     owned_skills_json = excluded.owned_skills_json,
		     owned_skills_scope_json = excluded.owned_skills_scope_json,
		     sessions_json = excluded.sessions_json,
		     metadata_json = excluded.metadata_json,
		     updated_at = excluded.updated_at`,
		a.ID, a.Type, a.Label, a.RoleID, a.CreatorAccount,
		string(skillsJSON), string(scopeJSON), string(sessionsJSON),
		jsonOrEmpty(a.Metadata), a.CreatedAt, a.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("sqlite agents Save %q: %w", a.ID, err)
	}
	return nil
}

func (r *AgentRepository) Delete(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("sqlite AgentRepository: empty id")
	}
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM agents WHERE id = $1`, id,
	)
	if err != nil {
		return fmt.Errorf("sqlite agents Delete %q: %w", id, err)
	}
	return nil
}

var _ persistence.AgentRepository = (*AgentRepository)(nil)

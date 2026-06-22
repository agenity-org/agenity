package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/agenity-org/agenity/internal/persistence"
)

// NewStoreFromRepository returns a *Store backed by a
// persistence.AgentRepository. Rich Agent fields not modeled at the
// column level (PVCHandle, CurrentOperator, LastActiveAt, DeletedAt)
// are stored in the Repository row's Metadata []byte column.
//
// Refs #208.
func NewStoreFromRepository(repo persistence.AgentRepository) *Store {
	return &Store{repo: repo}
}

func (s *Store) repoSave(a *Agent) error {
	pa, err := toPersistence(a)
	if err != nil {
		return err
	}
	return s.repo.Save(context.Background(), pa)
}

func (s *Store) repoGet(id uuid.UUID) (*Agent, error) {
	r, err := s.repo.Get(context.Background(), id.String())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return fromPersistence(r)
}

func (s *Store) repoList(opts ListOpts) ([]*Agent, error) {
	rows, err := s.repo.List(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]*Agent, 0, len(rows))
	for _, r := range rows {
		a, err := fromPersistence(r)
		if err != nil {
			continue
		}
		if !opts.IncludeDeleted && a.DeletedAt != nil {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func (s *Store) repoSoftDelete(id uuid.UUID) error {
	a, err := s.repoGet(id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	a.DeletedAt = &now
	return s.repoSave(a)
}

func (s *Store) repoAttachSession(id uuid.UUID, sessionID string) error {
	a, err := s.repoGet(id)
	if err != nil {
		return err
	}
	a.Sessions = append(a.Sessions, SessionRef{SessionID: sessionID, AttachedAt: time.Now().UTC()})
	a.LastActiveAt = time.Now().UTC()
	return s.repoSave(a)
}

func (s *Store) repoDetachSession(id uuid.UUID, sessionID string) error {
	a, err := s.repoGet(id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for i := range a.Sessions {
		if a.Sessions[i].SessionID == sessionID && a.Sessions[i].DetachedAt == nil {
			a.Sessions[i].DetachedAt = &now
			break
		}
	}
	return s.repoSave(a)
}

// toPersistence converts a domain Agent to a persistence row. Skills
// rename → OwnedSkills; rich fields go into Metadata.
func toPersistence(a *Agent) (*persistence.Agent, error) {
	extras := agentExtras{
		PVCHandle:       a.PVCHandle,
		CurrentOperator: a.CurrentOperator,
		LastActiveAt:    a.LastActiveAt,
		DeletedAt:       a.DeletedAt,
	}
	meta, err := json.Marshal(extras)
	if err != nil {
		return nil, fmt.Errorf("marshal agent extras: %w", err)
	}
	sessions := make([]persistence.SessionRef, 0, len(a.Sessions))
	for _, s := range a.Sessions {
		sessions = append(sessions, persistence.SessionRef{
			SessionID: s.SessionID, AttachedAt: s.AttachedAt,
		})
	}
	return &persistence.Agent{
		ID: a.ID.String(), Type: a.AgentType, Label: a.Label,
		RoleID: a.RoleID, CreatorAccount: a.CreatorAccount,
		OwnedSkills:      a.Skills,
		OwnedSkillsScope: a.OwnedSkillsScope,
		Sessions:         sessions,
		Metadata:         meta,
		CreatedAt:        a.CreatedAt,
	}, nil
}

func fromPersistence(r *persistence.Agent) (*Agent, error) {
	id, err := uuid.Parse(r.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid agent uuid %q: %w", r.ID, err)
	}
	a := &Agent{
		ID: id, AgentType: r.Type, Label: r.Label,
		RoleID: r.RoleID, CreatorAccount: r.CreatorAccount,
		Skills:           r.OwnedSkills,
		OwnedSkillsScope: r.OwnedSkillsScope,
		CreatedAt:        r.CreatedAt,
	}
	for _, sess := range r.Sessions {
		a.Sessions = append(a.Sessions, SessionRef{
			SessionID: sess.SessionID, AttachedAt: sess.AttachedAt,
		})
	}
	if len(r.Metadata) > 0 {
		var extras agentExtras
		if err := json.Unmarshal(r.Metadata, &extras); err != nil {
			return nil, fmt.Errorf("unmarshal agent extras %q: %w", r.ID, err)
		}
		a.PVCHandle = extras.PVCHandle
		a.CurrentOperator = extras.CurrentOperator
		a.LastActiveAt = extras.LastActiveAt
		a.DeletedAt = extras.DeletedAt
	}
	return a, nil
}

// agentExtras is the JSON envelope for rich Agent fields stored in
// persistence.Agent.Metadata.
type agentExtras struct {
	PVCHandle       string     `json:"pvc_handle,omitempty"`
	CurrentOperator *uuid.UUID `json:"current_operator,omitempty"`
	LastActiveAt    time.Time  `json:"last_active_at,omitempty"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
}

// ErrNotFound is returned by Get when the agent doesn't exist (also
// emitted via the file-on-disk path; see legacy NewStore in agent.go).
var ErrNotFound = errors.New("agent: not found")

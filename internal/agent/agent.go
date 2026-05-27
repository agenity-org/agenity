// Package agent — first-class Agent entity for v0.9 (Refs #172).
//
// An Agent is a durable, UUID-keyed handle for one logical AI worker.
// Distinct from a Session (which is one live PTY-attached process) and
// from a Workspace (which is the repo+team context). One Agent may have
// many Sessions over its life (resume, handoff, reattach); one Workspace
// may contain many Agents (a team).
//
// This package holds the data model + a file-backed Store. Wiring into
// runtime.Spawn + the HTTP API live in their respective packages.
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Agent is the first-class entity. UUID is the stable PK; Label is the
// human-facing handle (mutable). Every operator-facing surface should
// resolve by UUID and DISPLAY Label.
type Agent struct {
	// Identity — immutable after creation.
	ID        uuid.UUID `json:"id"`
	AgentType string    `json:"agent_type"` // claude-code | codex | aider | qwen-code …

	// Provenance — immutable.
	CreatorAccount string    `json:"creator_account,omitempty"` // free-form ref to the account/token that created this agent
	CreatedAt      time.Time `json:"created_at"`

	// Operator-facing handle. The ONLY field mutable via the public API.
	Label string `json:"label"`

	// Storage. Per-agent podman volume / k8s PVC name. Mounted at
	// /workspace inside every session attached to this agent.
	PVCHandle string `json:"pvc_handle"`

	// #194 — Skill IDs loaded onto this agent. First entry is the
	// PRIMARY skill (drives system prompt + default tools + stat
	// sheet); subsequent entries augment for multi-hat solo work.
	// Order matters; empty slice = "raw agent, no skill assigned".
	Skills []string `json:"skills,omitempty"`

	// Lifecycle binding — tracks the operator that currently owns
	// this agent. #173 (Handoff Protocol) was closed as not_planned
	// 2026-05-27; always-resume identity match supersedes the
	// request/release/bind state machine. Field stays for per-agent
	// ownership tracking.
	CurrentOperator *uuid.UUID `json:"current_operator,omitempty"`

	// Append-only history of session attachments. Each entry is a
	// SessionRef = {SessionID, AttachedAt, DetachedAt}. The latest
	// non-detached entry is the live session, if any.
	Sessions []SessionRef `json:"sessions,omitempty"`

	// Updated whenever a session writes bytes / activity occurs. Used by
	// the resume picker to sort "most-recent-first".
	LastActiveAt time.Time `json:"last_active_at"`

	// Soft-delete tombstone. Set when DELETE /api/v1/agents/{id} is
	// invoked; PVC is retained for grace period then GC'd. Agents with
	// DeletedAt != nil are excluded from default List() results.
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// SessionRef points at one PTY session that has been attached to this
// agent. AttachedAt is recorded when runtime.Spawn creates the session;
// DetachedAt is set when the session exits or the agent is handed off.
type SessionRef struct {
	SessionID  string     `json:"session_id"`
	AttachedAt time.Time  `json:"attached_at"`
	DetachedAt *time.Time `json:"detached_at,omitempty"`
}

// PVCName returns the canonical podman-volume / k8s-PVC name for an
// agent. Centralised here so every consumer agrees on the format.
func PVCName(id uuid.UUID) string {
	return "chepherd-agent-" + id.String()
}

// New mints a brand-new Agent with a fresh UUID + computed PVC handle.
// Caller is responsible for persisting (Store.Save) + actually
// provisioning the volume (runtime side).
func New(agentType, label, creatorAccount string) *Agent {
	id := uuid.New()
	now := time.Now().UTC()
	if label == "" {
		// Operator can always rename later via PATCH. Default label is
		// the short form of the UUID so the agent is at least
		// distinguishable in lists before the operator names it.
		label = "agent-" + id.String()[:8]
	}
	return &Agent{
		ID:             id,
		AgentType:      agentType,
		CreatorAccount: creatorAccount,
		CreatedAt:      now,
		Label:          label,
		PVCHandle:      PVCName(id),
		LastActiveAt:   now,
	}
}

// Store is the persistence layer for Agents. File-backed JSON-per-record
// in $stateDir/agents-registry/. Concurrent-safe via internal mutex.
type Store struct {
	dir string
	mu  sync.RWMutex
}

// NewStore opens (or initialises) the registry directory under stateDir.
// Failure to create the dir is fatal — Agent persistence is required for
// v0.9.
func NewStore(stateDir string) (*Store, error) {
	dir := filepath.Join(stateDir, "agents-registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("agent.NewStore mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) pathFor(id uuid.UUID) string {
	return filepath.Join(s.dir, id.String()+".json")
}

// Save persists one Agent. Atomic via temp-file + rename so concurrent
// reads never see a half-written record.
func (s *Store) Save(a *Agent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("agent.Save marshal: %w", err)
	}
	dst := s.pathFor(a.ID)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("agent.Save write tmp: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("agent.Save rename: %w", err)
	}
	return nil
}

// Get fetches one Agent by UUID. Returns nil, nil for not-found so
// callers can distinguish "missing" from "error".
func (s *Store) Get(id uuid.UUID) (*Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := os.ReadFile(s.pathFor(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("agent.Get read: %w", err)
	}
	var a Agent
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("agent.Get unmarshal: %w", err)
	}
	return &a, nil
}

// ListOpts controls which agents List returns.
type ListOpts struct {
	IncludeDeleted bool       // soft-deleted agents excluded by default
	Operator       *uuid.UUID // only agents currently bound to this op
	AgentType      string     // exact match if non-empty
}

// List returns agents matching opts, sorted newest-first by LastActiveAt.
func (s *Store) List(opts ListOpts) ([]*Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("agent.List readdir: %w", err)
	}
	out := make([]*Agent, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) < 5 || name[len(name)-5:] != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		var a Agent
		if err := json.Unmarshal(b, &a); err != nil {
			continue
		}
		if a.DeletedAt != nil && !opts.IncludeDeleted {
			continue
		}
		if opts.Operator != nil && (a.CurrentOperator == nil || *a.CurrentOperator != *opts.Operator) {
			continue
		}
		if opts.AgentType != "" && a.AgentType != opts.AgentType {
			continue
		}
		out = append(out, &a)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastActiveAt.After(out[j].LastActiveAt)
	})
	return out, nil
}

// SoftDelete marks an agent deleted. Caller should schedule PVC GC for
// the configured grace period (7d default).
func (s *Store) SoftDelete(id uuid.UUID) error {
	a, err := s.Get(id)
	if err != nil {
		return err
	}
	if a == nil {
		return fmt.Errorf("agent.SoftDelete: %s not found", id)
	}
	now := time.Now().UTC()
	a.DeletedAt = &now
	return s.Save(a)
}

// AttachSession appends a SessionRef and bumps LastActiveAt. Called by
// runtime.Spawn after a new session starts for this agent.
func (s *Store) AttachSession(id uuid.UUID, sessionID string) error {
	a, err := s.Get(id)
	if err != nil {
		return err
	}
	if a == nil {
		return fmt.Errorf("agent.AttachSession: %s not found", id)
	}
	now := time.Now().UTC()
	a.Sessions = append(a.Sessions, SessionRef{
		SessionID:  sessionID,
		AttachedAt: now,
	})
	a.LastActiveAt = now
	return s.Save(a)
}

// DetachSession closes out the most-recent open SessionRef matching
// sessionID. Idempotent — multiple detaches are no-ops after the first.
func (s *Store) DetachSession(id uuid.UUID, sessionID string) error {
	a, err := s.Get(id)
	if err != nil {
		return err
	}
	if a == nil {
		return nil
	}
	now := time.Now().UTC()
	for i := len(a.Sessions) - 1; i >= 0; i-- {
		if a.Sessions[i].SessionID == sessionID && a.Sessions[i].DetachedAt == nil {
			a.Sessions[i].DetachedAt = &now
			return s.Save(a)
		}
	}
	return nil
}

// SetLabel updates the human-facing handle. The only PATCH-mutable field.
func (s *Store) SetLabel(id uuid.UUID, label string) error {
	a, err := s.Get(id)
	if err != nil {
		return err
	}
	if a == nil {
		return fmt.Errorf("agent.SetLabel: %s not found", id)
	}
	a.Label = label
	return s.Save(a)
}

// SetSkills replaces the agent's Skills list (#194). Order matters —
// first entry is the primary skill driving system prompt composition.
func (s *Store) SetSkills(id uuid.UUID, skillIDs []string) error {
	a, err := s.Get(id)
	if err != nil {
		return err
	}
	if a == nil {
		return fmt.Errorf("agent.SetSkills: %s not found", id)
	}
	a.Skills = skillIDs
	return s.Save(a)
}

// SetOperator binds (or unbinds with nil) the agent's CurrentOperator.
// #173 closed as not_planned 2026-05-27 (always-resume model
// supersedes); this setter remains as the direct binding mechanism.
func (s *Store) SetOperator(id uuid.UUID, op *uuid.UUID) error {
	a, err := s.Get(id)
	if err != nil {
		return err
	}
	if a == nil {
		return fmt.Errorf("agent.SetOperator: %s not found", id)
	}
	a.CurrentOperator = op
	return s.Save(a)
}

// Package templateregistry — first-class team-template entity for the
// v0.9 SpawnWizard Stage-1 (#175). Replaces the previous hard-coded
// template array with a registry that:
//
//   - Ships 5 builtin templates marked ReadOnly (solo, pair, two-pizza,
//     stack-trio, council)
//   - Supports user-defined templates created via /admin/templates
//   - Persists user-defined templates as JSON-per-id under
//     $stateDir/templates-registry/
//   - Refuses PATCH/DELETE on builtins (returns 403 at the HTTP layer)
package templateregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Template is one team-shape recipe.
type Template struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Icon        string       `json:"icon"`
	WhenToUse   string       `json:"when_to_use"`
	Members     []MemberSpec `json:"members"`
	ReadOnly    bool         `json:"read_only"`
	AuthorRef   string       `json:"author_ref"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// MemberSpec is one slot in a template. AgentType is the default; the
// operator can override at instantiation time.
type MemberSpec struct {
	Label        string `json:"label"`
	Role         string `json:"role"`
	AgentType    string `json:"agent_type"`
	AccountClass string `json:"account_class"`
}

// Store fronts the registry. Builtins are returned in-memory; user-
// defined templates persist as JSON-per-id files.
type Store struct {
	dir      string
	mu       sync.RWMutex
	builtins []Template // immutable after construction
}

// NewStore opens the registry directory under stateDir and seeds the 5
// industry-standard builtins.
func NewStore(stateDir string) (*Store, error) {
	dir := filepath.Join(stateDir, "templates-registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("templateregistry.NewStore: %w", err)
	}
	now := time.Now().UTC()
	return &Store{
		dir: dir,
		builtins: []Template{
			{
				ID: "solo", Name: "Solo", Icon: "User",
				Description: "One agent, no shepherd. Quick exploration.",
				WhenToUse:   "Daily defaults; experimenting with a new repo.",
				Members: []MemberSpec{
					{Label: "agent-1", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
				},
				ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: "pair", Name: "Pair", Icon: "Users",
				Description: "Driver + observer; XP-style pair programming.",
				WhenToUse:   "Two-set-of-eyes problems; mentoring.",
				Members: []MemberSpec{
					{Label: "driver", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
					{Label: "observer", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
				},
				ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: "two-pizza", Name: "2-Pizza Team", Icon: "Network",
				Description: "1 orchestrator + 2 implementers. Bezos's 2-pizza rule.",
				WhenToUse:   "Small focused feature work; product sprint.",
				Members: []MemberSpec{
					{Label: "orch-1", Role: "orchestrator", AgentType: "claude-code", AccountClass: "anthropic"},
					{Label: "impl-1", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
					{Label: "impl-2", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
				},
				ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: "stack-trio", Name: "Stack Trio", Icon: "Layers",
				Description: "Frontend + backend + infra. Full-stack feature work.",
				WhenToUse:   "End-to-end features spanning multiple layers.",
				Members: []MemberSpec{
					{Label: "frontend", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
					{Label: "backend", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
					{Label: "infra", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
				},
				ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: "council", Name: "Council", Icon: "Vote",
				Description: "7 peers debating; design-by-committee / RFC review.",
				WhenToUse:   "Large architecture decisions; multi-perspective review.",
				Members: []MemberSpec{
					{Label: "p1", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
					{Label: "p2", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
					{Label: "p3", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
					{Label: "p4", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
					{Label: "p5", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
					{Label: "p6", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
					{Label: "p7", Role: "worker", AgentType: "claude-code", AccountClass: "anthropic"},
				},
				ReadOnly: true, AuthorRef: "chepherd", CreatedAt: now, UpdatedAt: now,
			},
		},
	}, nil
}

// List returns all builtins + user-defined templates, sorted (builtins
// first in their canonical order, then user-defined newest-first).
func (s *Store) List() ([]Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Template{}, s.builtins...)
	user, err := s.listUser()
	if err != nil {
		return nil, err
	}
	sort.Slice(user, func(i, j int) bool { return user[i].UpdatedAt.After(user[j].UpdatedAt) })
	out = append(out, user...)
	return out, nil
}

// Get returns one template by ID. Returns nil, nil for not-found.
func (s *Store) Get(id string) (*Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.builtins {
		if s.builtins[i].ID == id {
			b := s.builtins[i]
			return &b, nil
		}
	}
	b, err := os.ReadFile(s.pathFor(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var t Template
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Create persists a new user-defined template. Mints a "user-{uuid}"
// ID, stamps CreatedAt/UpdatedAt, and writes the JSON record.
// Returns the persisted template (with all server-side fields filled).
func (s *Store) Create(t Template, authorRef string) (*Template, error) {
	if t.Name == "" {
		return nil, errors.New("name required")
	}
	if len(t.Members) == 0 {
		return nil, errors.New("at least one member required")
	}
	id := "user-" + uuid.New().String()
	now := time.Now().UTC()
	t.ID = id
	t.ReadOnly = false
	t.AuthorRef = authorRef
	t.CreatedAt = now
	t.UpdatedAt = now
	if err := s.save(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Update mutates a user-defined template's name/desc/icon/whenToUse/
// members. Returns ErrReadOnly for builtins.
func (s *Store) Update(id string, patch Template) (*Template, error) {
	existing, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrNotFound
	}
	if existing.ReadOnly {
		return nil, ErrReadOnly
	}
	if patch.Name != "" {
		existing.Name = patch.Name
	}
	if patch.Description != "" {
		existing.Description = patch.Description
	}
	if patch.Icon != "" {
		existing.Icon = patch.Icon
	}
	if patch.WhenToUse != "" {
		existing.WhenToUse = patch.WhenToUse
	}
	if len(patch.Members) > 0 {
		existing.Members = patch.Members
	}
	existing.UpdatedAt = time.Now().UTC()
	if err := s.save(existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// Delete removes a user-defined template. Returns ErrReadOnly for
// builtins. Idempotent — second delete is a no-op.
func (s *Store) Delete(id string) error {
	existing, err := s.Get(id)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrNotFound
	}
	if existing.ReadOnly {
		return ErrReadOnly
	}
	return os.Remove(s.pathFor(id))
}

// internals

func (s *Store) pathFor(id string) string {
	return filepath.Join(s.dir, id+".json")
}

func (s *Store) save(t *Template) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.pathFor(t.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.pathFor(t.ID))
}

func (s *Store) listUser() ([]Template, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := []Template{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var t Template
		if err := json.Unmarshal(b, &t); err != nil {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

var (
	ErrNotFound = errors.New("templateregistry: not found")
	ErrReadOnly = errors.New("templateregistry: builtin templates are read-only")
)

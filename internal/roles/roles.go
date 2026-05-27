// Package roles — first-class Role catalog for v0.9.1 (#194).
//
// A Role is an industry-standard job function (PO / Architect / Tech
// Lead / Scrum Master / Generalist / Full-Stack Dev / Frontend Dev /
// Backend Dev / DevOps / QA / Security / Code Reviewer). An Agent
// carries exactly one Role; the Role's PrimaryPrompt declares scope
// when the agent also carries overlapping Skills (e.g. BE-dev with
// `tdd` scopes to backend code).
//
// 12 builtins ship pre-seeded. User-defined Roles are persisted as
// JSON-per-id under $stateDir/roles-registry/.
//
// Banned vocabulary per #194 (operator decision 2026-05-28): no
// "shepherd" / "Stack Trio" / "RACI" anywhere — enforced by
// TestNoBannedVocabAnywhere.
package roles

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

// Role is one job-function entry.
type Role struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Icon          string    `json:"icon"`
	Category      string    `json:"category"`        // leadership | methodology | engineering | operations | quality
	Description   string    `json:"description"`
	PrimaryPrompt string    `json:"primary_prompt"`  // role identity prompt; declares scope
	DefaultSkills []string  `json:"default_skills"`  // skill IDs auto-attached at spawn
	ReadOnly      bool      `json:"read_only"`
	Source        string    `json:"source"`          // "chepherd" | "user-{uuid}"
	SortOrder     int       `json:"sort_order"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Store is the persistence layer.
type Store struct {
	dir      string
	mu       sync.RWMutex
	builtins []Role
}

// NewStore opens the registry dir + seeds the 12 builtins.
func NewStore(stateDir string) (*Store, error) {
	dir := filepath.Join(stateDir, "roles-registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("roles.NewStore: %w", err)
	}
	return &Store{dir: dir, builtins: builtinSet()}, nil
}

// List returns builtins (sort_order asc) then user-defined (updated_at desc).
func (s *Store) List() ([]Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Role{}, s.builtins...)
	user, err := s.listUser()
	if err != nil {
		return nil, err
	}
	sort.Slice(user, func(i, j int) bool { return user[i].UpdatedAt.After(user[j].UpdatedAt) })
	out = append(out, user...)
	return out, nil
}

// Get returns one role by ID.
func (s *Store) Get(id string) (*Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.builtins {
		if s.builtins[i].ID == id {
			r := s.builtins[i]
			return &r, nil
		}
	}
	b, err := os.ReadFile(s.pathFor(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var r Role
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Create persists a new user-defined Role.
func (s *Store) Create(r Role) (*Role, error) {
	if r.Name == "" {
		return nil, errors.New("name required")
	}
	if r.PrimaryPrompt == "" {
		return nil, errors.New("primary_prompt required")
	}
	id := "user-" + uuid.New().String()
	now := time.Now().UTC()
	r.ID = id
	r.ReadOnly = false
	r.Source = id
	r.CreatedAt = now
	r.UpdatedAt = now
	r.SortOrder = 1000
	if r.Icon == "" {
		r.Icon = "Sparkles"
	}
	if r.Category == "" {
		r.Category = "engineering"
	}
	if err := s.save(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Update mutates a user-defined Role. Builtins → ErrReadOnly.
func (s *Store) Update(id string, patch Role) (*Role, error) {
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
	if patch.PrimaryPrompt != "" {
		existing.PrimaryPrompt = patch.PrimaryPrompt
	}
	if patch.Icon != "" {
		existing.Icon = patch.Icon
	}
	if patch.Category != "" {
		existing.Category = patch.Category
	}
	if patch.DefaultSkills != nil {
		existing.DefaultSkills = patch.DefaultSkills
	}
	existing.UpdatedAt = time.Now().UTC()
	if err := s.save(existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// Delete removes a user-defined Role. Builtins → ErrReadOnly.
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

func (s *Store) pathFor(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Store) save(r *Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.pathFor(r.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.pathFor(r.ID))
}

func (s *Store) listUser() ([]Role, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := []Role{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var r Role
		if err := json.Unmarshal(b, &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

var (
	ErrNotFound = errors.New("role: not found")
	ErrReadOnly = errors.New("role: builtin roles are read-only")
)

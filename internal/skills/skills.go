// Package skills — Skill Library for v0.9 (#194).
//
// Skills are first-class composable units loaded onto Agents (#172).
// A Skill is configuration — system prompt + tool allowlist + default
// stat-sheet + icon — keyed by stable ID. Agents reference Skills
// by ID; spawn composes the per-agent system prompt from the union of
// the agent's loaded skills.
//
// This file ships 12 builtins curated from operator-supplied references
// (gstack, addyosmani/agent-skills, ECC) and exposes file-backed CRUD
// for user-defined skills.
//
// Architectural note (operator-architect discovery 2026-05-27):
// "Agents are nothing but LLMs loaded with skills." Skill IS the role;
// templates compose skills onto agent slots; the agent's system prompt
// is the union of its loaded skills' PromptOverride blocks.
package skills

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

// Skill is the persistent unit.
type Skill struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	Icon            string         `json:"icon"`
	PromptOverride  string         `json:"prompt_override"`
	DefaultTools    []string       `json:"default_tools,omitempty"`
	AgentTypeCompat []string       `json:"agent_type_compat,omitempty"` // ["any"] for unrestricted
	StatSheet       map[string]any `json:"stat_sheet,omitempty"`
	Source          string         `json:"source"` // "chepherd" | "user-{uuid}" | "import:gstack" | …
	Tags            []string       `json:"tags,omitempty"`
	ReadOnly        bool           `json:"read_only"`
	SortOrder       int            `json:"sort_order"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// Store is the persistence layer. File-backed JSON-per-skill under
// $stateDir/skills/. Builtins are seeded in-memory at construction
// (immutable); user-defined live on disk.
type Store struct {
	dir      string
	mu       sync.RWMutex
	builtins []Skill // immutable; seeded once in NewStore
}

// NewStore initialises the registry directory + seeds the 12 builtins.
func NewStore(stateDir string) (*Store, error) {
	dir := filepath.Join(stateDir, "skills-registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("skills.NewStore: %w", err)
	}
	return &Store{dir: dir, builtins: builtinSet()}, nil
}

// ListOpts filters List results.
type ListOpts struct {
	Tag    string // exact match
	Compat string // matches against AgentTypeCompat ("any" always matches)
}

// List returns builtins (sort_order asc) then user-defined (updated_at desc).
func (s *Store) List(opts ListOpts) ([]Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Skill, 0, len(s.builtins))
	for _, b := range s.builtins {
		if !matchOpts(b, opts) {
			continue
		}
		out = append(out, b)
	}
	user, err := s.listUser()
	if err != nil {
		return nil, err
	}
	sort.Slice(user, func(i, j int) bool { return user[i].UpdatedAt.After(user[j].UpdatedAt) })
	for _, u := range user {
		if !matchOpts(u, opts) {
			continue
		}
		out = append(out, u)
	}
	return out, nil
}

func matchOpts(s Skill, opts ListOpts) bool {
	if opts.Tag != "" {
		found := false
		for _, t := range s.Tags {
			if t == opts.Tag {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if opts.Compat != "" {
		found := false
		for _, c := range s.AgentTypeCompat {
			if c == opts.Compat || c == "any" {
				found = true
				break
			}
		}
		if !found && len(s.AgentTypeCompat) > 0 {
			return false
		}
	}
	return true
}

// Get returns one skill by ID. Returns nil, nil for not-found.
func (s *Store) Get(id string) (*Skill, error) {
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
	var sk Skill
	if err := json.Unmarshal(b, &sk); err != nil {
		return nil, err
	}
	return &sk, nil
}

// Create persists a new user-defined Skill.
func (s *Store) Create(sk Skill) (*Skill, error) {
	if sk.Name == "" {
		return nil, errors.New("name required")
	}
	if sk.PromptOverride == "" {
		return nil, errors.New("prompt_override required")
	}
	id := "user-" + uuid.New().String()
	now := time.Now().UTC()
	sk.ID = id
	sk.ReadOnly = false
	if sk.Source == "" {
		sk.Source = id
	}
	sk.CreatedAt = now
	sk.UpdatedAt = now
	sk.SortOrder = 1000
	if sk.Icon == "" {
		sk.Icon = "Sparkles"
	}
	if len(sk.AgentTypeCompat) == 0 {
		sk.AgentTypeCompat = []string{"any"}
	}
	if err := s.save(&sk); err != nil {
		return nil, err
	}
	return &sk, nil
}

// Update mutates a user-defined Skill. Builtins → ErrReadOnly.
func (s *Store) Update(id string, patch Skill) (*Skill, error) {
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
	if patch.PromptOverride != "" {
		existing.PromptOverride = patch.PromptOverride
	}
	if patch.Icon != "" {
		existing.Icon = patch.Icon
	}
	if patch.DefaultTools != nil {
		existing.DefaultTools = patch.DefaultTools
	}
	if patch.AgentTypeCompat != nil {
		existing.AgentTypeCompat = patch.AgentTypeCompat
	}
	if patch.StatSheet != nil {
		existing.StatSheet = patch.StatSheet
	}
	if patch.Tags != nil {
		existing.Tags = patch.Tags
	}
	existing.UpdatedAt = time.Now().UTC()
	if err := s.save(existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// Delete removes a user-defined Skill. Builtins → ErrReadOnly.
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

func (s *Store) save(sk *Skill) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(sk, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.pathFor(sk.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.pathFor(sk.ID))
}

func (s *Store) listUser() ([]Skill, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := []Skill{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var sk Skill
		if err := json.Unmarshal(b, &sk); err != nil {
			continue
		}
		out = append(out, sk)
	}
	return out, nil
}

var (
	ErrNotFound = errors.New("skill: not found")
	ErrReadOnly = errors.New("skill: builtin skills are read-only")
)

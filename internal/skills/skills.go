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

	// PromptOverride is the agent-system-prompt fragment for this skill
	// at Layer 2 of the v0.9 context model. Historically named
	// "PromptOverride" before the canon work added Layer 1 — kept for
	// JSON-on-disk compat. Conceptually = "PromptBody" per #194 spec.
	PromptOverride string `json:"prompt_override"`

	// OrgOverrideBody is the admin-edited Layer-2 override (#194 spec).
	// When non-empty, supersedes PromptOverride at spawn-time merge.
	OrgOverrideBody string `json:"org_override_body,omitempty"`

	// UpstreamSource pins where this skill body was sourced from.
	// "chepherd@v0.9" for native; "addyosmani/agent-skills@<ref>" or
	// "affaan-m/ECC@<ref>" for imports.
	UpstreamSource string `json:"upstream_source,omitempty"`

	// UpstreamPath is the SKILL.md path inside the upstream repo.
	UpstreamPath string `json:"upstream_path,omitempty"`

	// Frontmatter holds YAML frontmatter parsed from upstream SKILL.md.
	Frontmatter map[string]any `json:"frontmatter,omitempty"`

	DefaultTools    []string       `json:"default_tools,omitempty"`
	AgentTypeCompat []string       `json:"agent_type_compat,omitempty"` // ["any"] for unrestricted
	StatSheet       map[string]any `json:"stat_sheet,omitempty"`
	Source          string         `json:"source"`
	Tags            []string       `json:"tags,omitempty"`
	ReadOnly        bool           `json:"read_only"`
	SortOrder       int            `json:"sort_order"`

	// TeamOnly = true means this skill is only applicable when there's
	// more than one agent on the team (e.g. team-orchestration +
	// process-coaching make no sense for a Solo team of 1). Coverage
	// panel uses this to compute applicable-skill denominator per
	// team size — Solo coverage shows X/8, Pair+ shows X/10.
	// Architect's #200 Bug 3 spec: "Solo coverage = 8/8 ✓".
	TeamOnly bool `json:"team_only,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EffectiveBody returns the body the agent should actually see at
// spawn — Layer 2 override if present, else upstream PromptOverride.
func (s *Skill) EffectiveBody() string {
	if s.OrgOverrideBody != "" {
		return s.OrgOverrideBody
	}
	return s.PromptOverride
}

// Store is the persistence layer. File-backed JSON-per-skill under
// $stateDir/skills/. Builtins are seeded in-memory at construction
// (immutable); user-defined live on disk.
type Store struct {
	dir      string
	mu       sync.RWMutex
	builtins []Skill // immutable; seeded once in NewStore
}

// NewStore initialises the registry directory + seeds the 10 LEAN
// builtins (v0.9.1, #194). Layer-2 overrides for builtins live as
// {id}.override.json sidecars and are re-applied at boot.
func NewStore(stateDir string) (*Store, error) {
	dir := filepath.Join(stateDir, "skills-registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("skills.NewStore: %w", err)
	}
	s := &Store{dir: dir, builtins: builtinSet()}
	// Reapply any persisted Layer-2 overrides for builtins.
	for i := range s.builtins {
		path := filepath.Join(dir, s.builtins[i].ID+".override.json")
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rec map[string]string
		if json.Unmarshal(b, &rec) == nil {
			s.builtins[i].OrgOverrideBody = rec["override_body"]
		}
	}
	return s, nil
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

// SetOverride saves an admin-edited Layer-2 override body. Allowed on
// builtins (this is how operators customise upstream skills without
// editing the canonical body). Returns the updated skill.
//
// For builtins, the override lives in a sidecar file under
// $stateDir/skills-registry/{id}.override.json — the in-memory builtin
// stays read-only.
func (s *Store) SetOverride(id, body string) (*Skill, error) {
	existing, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrNotFound
	}
	existing.OrgOverrideBody = body
	existing.UpdatedAt = time.Now().UTC()
	if existing.ReadOnly {
		// Sidecar-write for builtins.
		s.mu.Lock()
		defer s.mu.Unlock()
		path := filepath.Join(s.dir, id+".override.json")
		data, _ := json.MarshalIndent(map[string]string{
			"override_body": body,
		}, "", "  ")
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, path); err != nil {
			return nil, err
		}
		// Mutate the in-memory builtin so future Get/List reflect it.
		for i := range s.builtins {
			if s.builtins[i].ID == id {
				s.builtins[i].OrgOverrideBody = body
				s.builtins[i].UpdatedAt = existing.UpdatedAt
				break
			}
		}
		return existing, nil
	}
	if err := s.save(existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// ClearOverride removes the Layer-2 override and reverts to upstream.
func (s *Store) ClearOverride(id string) (*Skill, error) {
	return s.SetOverride(id, "")
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

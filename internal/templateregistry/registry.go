// Package templateregistry — Team Template Registry for v0.9 (#175,
// architect re-spec 2026-05-27).
//
// Templates compose Skills (#194) into team shapes. They do NOT embed
// hard-coded role strings; each slot points to a Skill ID. On spawn,
// each slot becomes one Agent with the slot's primary skill loaded.
//
// Visible builtins in v0.9: exactly 6 — solo / pair / trio / scrum /
// review / custom. Hidden builtins (solo-supervised / council /
// multi-team) exist in catalog/ but don't render on the wizard grid;
// admins can re-enable them via /admin/templates.
//
// "Stack Trio" — BANNED. Not a template ID, not a name, not anywhere.
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

// Template is the persistent unit.
type Template struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Icon        string      `json:"icon"`
	WhenToUse   string      `json:"when_to_use"`
	SortOrder   int         `json:"sort_order"`
	Slots       []SkillSlot `json:"slots"`
	ReadOnly    bool        `json:"read_only"`
	Visible     bool        `json:"visible"` // false = hidden from wizard grid (admin only)
	AuthorRef   string      `json:"author_ref"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// SkillSlot is one team-position. Each slot becomes one Agent on spawn.
type SkillSlot struct {
	Label               string   `json:"label"`
	PrimarySkill        string   `json:"primary_skill"`
	AltSkills           []string `json:"alt_skills,omitempty"`
	AdditionalSkills    []string `json:"additional_skills,omitempty"`
	AgentTypeDefault    string   `json:"agent_type_default"`
	AccountClassDefault string   `json:"account_class_default"`
}

// Store is file-backed persistence + in-memory builtin seed.
type Store struct {
	dir      string
	mu       sync.RWMutex
	builtins []Template
}

// NewStore opens the registry dir + seeds the 6 visible + 3 hidden
// builtins.
func NewStore(stateDir string) (*Store, error) {
	dir := filepath.Join(stateDir, "templates-registry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("templateregistry.NewStore: %w", err)
	}
	return &Store{dir: dir, builtins: builtinSet()}, nil
}

// ListOpts filters List results.
type ListOpts struct {
	VisibleOnly bool // when true, only Visible=true templates returned (Stage 1 grid uses this)
}

// List returns builtins (sort_order asc) then user-defined (updated_at desc).
func (s *Store) List(opts ListOpts) ([]Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Template, 0, len(s.builtins))
	for _, b := range s.builtins {
		if opts.VisibleOnly && !b.Visible {
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
		if opts.VisibleOnly && !u.Visible {
			continue
		}
		out = append(out, u)
	}
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

// Create persists a new user-defined Template.
func (s *Store) Create(t Template, authorRef string) (*Template, error) {
	if t.Name == "" {
		return nil, errors.New("name required")
	}
	id := "user-" + uuid.New().String()
	now := time.Now().UTC()
	t.ID = id
	t.ReadOnly = false
	t.AuthorRef = authorRef
	t.CreatedAt = now
	t.UpdatedAt = now
	t.SortOrder = 1000
	if t.Icon == "" {
		t.Icon = "PlusCircle"
	}
	// User-created templates default to visible.
	t.Visible = true
	if err := s.save(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Update mutates a user-defined Template. Builtins → ErrReadOnly.
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
	if len(patch.Slots) > 0 {
		existing.Slots = patch.Slots
	}
	existing.UpdatedAt = time.Now().UTC()
	if err := s.save(existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// SetVisibility toggles a template's Visible flag. Builtins ARE
// allowed to be hidden/shown (admin operation); the field is not
// part of ReadOnly enforcement.
func (s *Store) SetVisibility(id string, visible bool) error {
	// Builtin? Mutate the in-memory copy.
	s.mu.Lock()
	for i := range s.builtins {
		if s.builtins[i].ID == id {
			s.builtins[i].Visible = visible
			s.mu.Unlock()
			return nil
		}
	}
	s.mu.Unlock()
	existing, err := s.Get(id)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrNotFound
	}
	existing.Visible = visible
	return s.save(existing)
}

// Delete removes a user-defined Template. Builtins → ErrReadOnly.
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

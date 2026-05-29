// Package templateregistry — Team Template Registry for v0.9.1 (#175
// re-re-do, architect 2026-05-28 FINAL+).
//
// Templates compose Roles (#194) + Skills (#194) into team shapes.
// Each slot pairs ONE Role (from internal/roles, 12 builtins) with
// the SKILLS that role owns on this team (from internal/skills, 10
// LEAN). The slot's RoleID determines identity; OwnedSkills determine
// composable disciplines; OwnedSkillsScope refines scope when a Role
// + Skill combination is ambiguous (e.g. tdd with full-stack-developer
// can scope to "frontend", "backend", or "both").
//
// On spawn, each slot becomes one Agent with:
//   - PrimaryPrompt = role.PrimaryPrompt (from internal/roles)
//   - Effective skill bodies = skill.EffectiveBody() per OwnedSkills
//   - Canon (Layer 1, from internal/canon) prepended once
//
// Visible builtins (Fibonacci sizing per architect 2026-05-28):
//   solo (1) · pair (2) · trio (3) · scrum (5) · squad (8) · custom (0)
//
// Hidden builtins (catalog only; admin can flip via /admin/templates):
//   solo-supervised · council · multi-team
//
// Banned vocabulary: NO "shepherd" / "Stack Trio" / "RACI" — enforced
// by TestNoBannedVocab in registry_test.go.
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
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Icon        string    `json:"icon"`
	WhenToUse   string    `json:"when_to_use"`
	SizeLabel   string    `json:"size_label,omitempty"` // Fibonacci sizing display: "1", "2", "3", "5", "8", "0"
	SortOrder   int       `json:"sort_order"`
	Slots       []Slot    `json:"slots"`
	ReadOnly    bool      `json:"read_only"`
	Visible     bool      `json:"visible"` // false = hidden from wizard grid (admin only)
	AuthorRef   string    `json:"author_ref"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Slot is one team-position. Each slot becomes one Agent on spawn.
//
// Per architect 2026-05-28 FINAL+: Agent identity = Role + N owned
// Skills. Slot.RoleID names the Role (one of 12 builtins from
// internal/roles); OwnedSkills lists the engineering practices this
// agent owns on this team (subset of the 10 LEAN skills from
// internal/skills); OwnedSkillsScope refines scope when ambiguous
// (e.g. {"tdd": "backend"} narrows TDD discipline to backend code
// for a full-stack-developer slot).
type Slot struct {
	Label               string            `json:"label"`
	RoleID              string            `json:"role_id"`
	OwnedSkills         []string          `json:"owned_skills"`
	OwnedSkillsScope    map[string]string `json:"owned_skills_scope,omitempty"`
	AgentTypeDefault    string            `json:"agent_type_default"`
	AccountClassDefault string            `json:"account_class_default"`
}

// SkillSlot is the v0.8/v0.9.0 legacy alias for Slot. Kept as a type
// alias so any out-of-tree consumer that imported the old name still
// compiles; new code MUST use Slot.
//
// Deprecated: use Slot.
type SkillSlot = Slot

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

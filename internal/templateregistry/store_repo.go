package templateregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// NewStoreFromRepository returns a *Store backed by a
// persistence.TemplateRepository rather than file-on-disk. Rich
// Template fields not modeled at the column level (Icon, WhenToUse,
// SizeLabel, SortOrder, Slots, ReadOnly, Visible, AuthorRef,
// CreatedAt) are stored in the Repository row's Metadata []byte
// column as a JSON envelope; queryable fields (ID, Name, Description)
// stay structured.
//
// Refs #208.
func NewStoreFromRepository(repo persistence.TemplateRepository) *Store {
	return &Store{repo: repo, builtins: builtinSet()}
}

func (s *Store) repoList(opts ListOpts) ([]Template, error) {
	out := make([]Template, 0, len(s.builtins))
	for _, b := range s.builtins {
		if opts.VisibleOnly && !b.Visible {
			continue
		}
		out = append(out, b)
	}
	rows, err := s.repo.List(context.Background())
	if err != nil {
		return nil, err
	}
	user := make([]Template, 0, len(rows))
	for _, r := range rows {
		t, err := decodeTemplate(r)
		if err != nil {
			continue
		}
		if opts.VisibleOnly && !t.Visible {
			continue
		}
		user = append(user, *t)
	}
	sort.Slice(user, func(i, j int) bool { return user[i].UpdatedAt.After(user[j].UpdatedAt) })
	out = append(out, user...)
	return out, nil
}

func (s *Store) repoGet(id string) (*Template, error) {
	for i := range s.builtins {
		if s.builtins[i].ID == id {
			b := s.builtins[i]
			return &b, nil
		}
	}
	r, err := s.repo.Get(context.Background(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, nil
		}
		return nil, err
	}
	return decodeTemplate(r)
}

func (s *Store) repoCreate(t Template, authorRef string) (*Template, error) {
	if t.Name == "" {
		return nil, errors.New("name required")
	}
	id := "user-" + newUUID()
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
	t.Visible = true
	if err := s.repoSave(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) repoUpdate(id string, patch Template) (*Template, error) {
	cur, err := s.repoGet(id)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, ErrNotFound
	}
	if cur.ReadOnly {
		return nil, ErrReadOnly
	}
	applyPatch(cur, patch)
	cur.UpdatedAt = time.Now().UTC()
	if err := s.repoSave(cur); err != nil {
		return nil, err
	}
	return cur, nil
}

func (s *Store) repoSetVisibility(id string, visible bool) error {
	// Builtin → mutate in-memory.
	s.mu.Lock()
	for i := range s.builtins {
		if s.builtins[i].ID == id {
			s.builtins[i].Visible = visible
			s.mu.Unlock()
			return nil
		}
	}
	s.mu.Unlock()
	cur, err := s.repoGet(id)
	if err != nil {
		return err
	}
	if cur == nil {
		return ErrNotFound
	}
	cur.Visible = visible
	return s.repoSave(cur)
}

func (s *Store) repoDelete(id string) error {
	cur, err := s.repoGet(id)
	if err != nil {
		return err
	}
	if cur == nil {
		return ErrNotFound
	}
	if cur.ReadOnly {
		return ErrReadOnly
	}
	return s.repo.Delete(context.Background(), id)
}

// repoSave encodes a Template into a persistence.Template row (Name +
// Description structured, rest in Metadata) and Saves it.
func (s *Store) repoSave(t *Template) error {
	extras := templateExtras{
		Icon: t.Icon, WhenToUse: t.WhenToUse, SizeLabel: t.SizeLabel,
		SortOrder: t.SortOrder, Slots: t.Slots,
		ReadOnly: t.ReadOnly, Visible: t.Visible,
		AuthorRef: t.AuthorRef, CreatedAt: t.CreatedAt,
	}
	meta, err := json.Marshal(extras)
	if err != nil {
		return fmt.Errorf("marshal template extras: %w", err)
	}
	return s.repo.Save(context.Background(), &persistence.Template{
		ID: t.ID, Name: t.Name, Description: t.Description,
		Body:     []byte{}, // canonical Body could carry rendered YAML; currently unused
		Metadata: meta,
	})
}

// decodeTemplate constructs a domain Template from a persistence row.
func decodeTemplate(r *persistence.Template) (*Template, error) {
	t := &Template{
		ID: r.ID, Name: r.Name, Description: r.Description,
		UpdatedAt: r.UpdatedAt,
	}
	if len(r.Metadata) > 0 {
		var extras templateExtras
		if err := json.Unmarshal(r.Metadata, &extras); err != nil {
			return nil, fmt.Errorf("unmarshal template extras %q: %w", r.ID, err)
		}
		t.Icon = extras.Icon
		t.WhenToUse = extras.WhenToUse
		t.SizeLabel = extras.SizeLabel
		t.SortOrder = extras.SortOrder
		t.Slots = extras.Slots
		t.ReadOnly = extras.ReadOnly
		t.Visible = extras.Visible
		t.AuthorRef = extras.AuthorRef
		t.CreatedAt = extras.CreatedAt
	}
	return t, nil
}

func applyPatch(cur *Template, patch Template) {
	if patch.Name != "" {
		cur.Name = patch.Name
	}
	if patch.Description != "" {
		cur.Description = patch.Description
	}
	if patch.Icon != "" {
		cur.Icon = patch.Icon
	}
	if patch.WhenToUse != "" {
		cur.WhenToUse = patch.WhenToUse
	}
	if len(patch.Slots) > 0 {
		cur.Slots = patch.Slots
	}
}

// templateExtras is the JSON envelope for rich Template fields stored
// in persistence.Template.Metadata.
type templateExtras struct {
	Icon      string    `json:"icon,omitempty"`
	WhenToUse string    `json:"when_to_use,omitempty"`
	SizeLabel string    `json:"size_label,omitempty"`
	SortOrder int       `json:"sort_order,omitempty"`
	Slots     []Slot    `json:"slots,omitempty"`
	ReadOnly  bool      `json:"read_only,omitempty"`
	Visible   bool      `json:"visible,omitempty"`
	AuthorRef string    `json:"author_ref,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

func newUUID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

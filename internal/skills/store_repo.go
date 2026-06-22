package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// NewStoreFromRepository returns a *Store backed by a
// persistence.SkillRepository. Rich Skill fields not modeled at the
// column level (Description, Icon, PromptOverride, OrgOverrideBody,
// Frontmatter, DefaultTools, AgentTypeCompat, StatSheet, Tags,
// TeamOnly, CreatedAt) are stored in the Repository row's Metadata
// []byte column as a JSON envelope.
//
// Refs #208.
func NewStoreFromRepository(repo persistence.SkillRepository) *Store {
	return &Store{repo: repo, builtins: builtinSet()}
}

func (s *Store) repoList(opts ListOpts) ([]Skill, error) {
	rows, err := s.repo.List(context.Background(), persistence.SkillListOpts{})
	if err != nil {
		return nil, err
	}
	out := []Skill{}
	for _, b := range s.builtins {
		out = append(out, b)
	}
	for _, r := range rows {
		sk, err := decodeSkill(&r)
		if err != nil {
			continue
		}
		out = append(out, *sk)
	}
	filtered := out[:0]
	for _, sk := range out {
		if matchOpts(sk, opts) {
			filtered = append(filtered, sk)
		}
	}
	return filtered, nil
}

func (s *Store) repoGet(id string) (*Skill, error) {
	for i := range s.builtins {
		if s.builtins[i].ID == id {
			b := s.builtins[i]
			return &b, nil
		}
	}
	r, err := s.repo.Get(context.Background(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return decodeSkill(r)
}

func (s *Store) repoCreate(sk Skill) (*Skill, error) {
	if sk.ID == "" {
		return nil, errors.New("skills.Create: ID required")
	}
	now := time.Now().UTC()
	if sk.CreatedAt.IsZero() {
		sk.CreatedAt = now
	}
	sk.UpdatedAt = now
	if err := s.repoSave(&sk); err != nil {
		return nil, err
	}
	return &sk, nil
}

func (s *Store) repoUpdate(id string, patch Skill) (*Skill, error) {
	cur, err := s.repoGet(id)
	if err != nil {
		return nil, err
	}
	if cur.ReadOnly {
		return nil, ErrReadOnly
	}
	if patch.Name != "" {
		cur.Name = patch.Name
	}
	if patch.Description != "" {
		cur.Description = patch.Description
	}
	if patch.PromptOverride != "" {
		cur.PromptOverride = patch.PromptOverride
	}
	if patch.OrgOverrideBody != "" {
		cur.OrgOverrideBody = patch.OrgOverrideBody
	}
	if patch.SortOrder != 0 {
		cur.SortOrder = patch.SortOrder
	}
	if patch.Tags != nil {
		cur.Tags = patch.Tags
	}
	cur.UpdatedAt = time.Now().UTC()
	if err := s.repoSave(cur); err != nil {
		return nil, err
	}
	return cur, nil
}

func (s *Store) repoSetOverride(id, body string) (*Skill, error) {
	cur, err := s.repoGet(id)
	if err != nil {
		return nil, err
	}
	cur.OrgOverrideBody = body
	cur.UpdatedAt = time.Now().UTC()
	if err := s.repoSave(cur); err != nil {
		return nil, err
	}
	return cur, nil
}

func (s *Store) repoClearOverride(id string) (*Skill, error) {
	cur, err := s.repoGet(id)
	if err != nil {
		return nil, err
	}
	cur.OrgOverrideBody = ""
	cur.UpdatedAt = time.Now().UTC()
	if err := s.repoSave(cur); err != nil {
		return nil, err
	}
	return cur, nil
}

func (s *Store) repoDelete(id string) error {
	cur, err := s.repoGet(id)
	if err != nil {
		return err
	}
	if cur.ReadOnly {
		return ErrReadOnly
	}
	return s.repo.Delete(context.Background(), id)
}

func (s *Store) repoSave(sk *Skill) error {
	extras := skillExtras{
		Description:     sk.Description,
		Icon:            sk.Icon,
		OrgOverrideBody: sk.OrgOverrideBody,
		Frontmatter:     sk.Frontmatter,
		DefaultTools:    sk.DefaultTools,
		AgentTypeCompat: sk.AgentTypeCompat,
		StatSheet:       sk.StatSheet,
		Tags:            sk.Tags,
		TeamOnly:        sk.TeamOnly,
		CreatedAt:       sk.CreatedAt,
	}
	meta, err := json.Marshal(extras)
	if err != nil {
		return fmt.Errorf("marshal skill extras: %w", err)
	}
	return s.repo.Save(context.Background(), &persistence.Skill{
		ID: sk.ID, Name: sk.Name,
		DefaultBody:  sk.PromptOverride,
		OverrideBody: sk.OrgOverrideBody,
		ReadOnly:     sk.ReadOnly,
		Source:       sk.UpstreamSource,
		Path:         sk.UpstreamPath,
		SortOrder:    sk.SortOrder,
		Metadata:     meta,
	})
}

func decodeSkill(r *persistence.Skill) (*Skill, error) {
	sk := &Skill{
		ID:             r.ID,
		Name:           r.Name,
		PromptOverride: r.DefaultBody,
		OrgOverrideBody: r.OverrideBody,
		ReadOnly:       r.ReadOnly,
		UpstreamSource: r.Source,
		UpstreamPath:   r.Path,
		SortOrder:      r.SortOrder,
		Source:         "user", // legacy field; builtins handled in cache, user-rows always carry "user"
		UpdatedAt:      r.UpdatedAt,
	}
	if len(r.Metadata) > 0 {
		var extras skillExtras
		if err := json.Unmarshal(r.Metadata, &extras); err != nil {
			return nil, fmt.Errorf("unmarshal skill extras %q: %w", r.ID, err)
		}
		sk.Description = extras.Description
		sk.Icon = extras.Icon
		sk.Frontmatter = extras.Frontmatter
		sk.DefaultTools = extras.DefaultTools
		sk.AgentTypeCompat = extras.AgentTypeCompat
		sk.StatSheet = extras.StatSheet
		sk.Tags = extras.Tags
		sk.TeamOnly = extras.TeamOnly
		sk.CreatedAt = extras.CreatedAt
	}
	return sk, nil
}

// skillExtras is the JSON envelope for rich Skill fields stored in
// persistence.Skill.Metadata.
type skillExtras struct {
	Description     string         `json:"description,omitempty"`
	Icon            string         `json:"icon,omitempty"`
	OrgOverrideBody string         `json:"org_override_body,omitempty"`
	Frontmatter     map[string]any `json:"frontmatter,omitempty"`
	DefaultTools    []string       `json:"default_tools,omitempty"`
	AgentTypeCompat []string       `json:"agent_type_compat,omitempty"`
	StatSheet       map[string]any `json:"stat_sheet,omitempty"`
	Tags            []string       `json:"tags,omitempty"`
	TeamOnly        bool           `json:"team_only,omitempty"`
	CreatedAt       time.Time      `json:"created_at,omitempty"`
}

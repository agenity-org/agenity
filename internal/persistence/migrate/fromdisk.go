package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

// Stats reports per-entity-type counts migrated by FromDisk. A row's
// count includes both newly-inserted and UPSERT-overwritten rows
// (migration is idempotent, so the second run reports the same counts
// even though no DB rows were newly created).
type Stats struct {
	Sessions    int
	Skills      int
	Agents      int
	CanonRows   int // total canon rows written (current + history)
	Templates   int
	AuthSecrets int
}

// FromDisk migrates v0.9.1 file-on-disk state under stateDir
// (~/.local/state/chepherd by default) into the given persistence.Store.
//
// Idempotent: re-running over already-migrated data is a no-op via
// the Repositories' UPSERT semantics. Missing-directory is not an error
// (treated as "nothing to migrate" for that entity).
//
// Per-entity layouts migrated:
//   - sessions/<uuid>.json       → SessionRepository
//   - skills-registry/<id>.override.json → SkillRepository (overrides only;
//     builtins are seeded by domain package init)
//   - agents-registry/<uuid>.json → AgentRepository
//   - canon/current.json         → CanonRepository (current version)
//   - canon/history/v<N>.json    → CanonRepository (historical versions)
//   - templates-registry/<id>.yaml → TemplateRepository (raw YAML body)
//   - auth.secret                → AuthSecretRepository (purpose=dashboard-hs256)
//
// Returns per-entity-type stats so callers can log progress.
// Refs #208.
func FromDisk(ctx context.Context, stateDir string, store persistence.Store) (Stats, error) {
	var stats Stats
	n, err := migrateSessions(ctx, stateDir, store.Sessions())
	if err != nil {
		return stats, fmt.Errorf("migrate sessions: %w", err)
	}
	stats.Sessions = n
	if n, err = migrateSkills(ctx, stateDir, store.Skills()); err != nil {
		return stats, fmt.Errorf("migrate skills: %w", err)
	}
	stats.Skills = n
	if n, err = migrateAgents(ctx, stateDir, store.Agents()); err != nil {
		return stats, fmt.Errorf("migrate agents: %w", err)
	}
	stats.Agents = n
	if n, err = migrateCanon(ctx, stateDir, store.Canon()); err != nil {
		return stats, fmt.Errorf("migrate canon: %w", err)
	}
	stats.CanonRows = n
	if n, err = migrateTemplates(ctx, stateDir, store.Templates()); err != nil {
		return stats, fmt.Errorf("migrate templates: %w", err)
	}
	stats.Templates = n
	if n, err = migrateAuthSecret(ctx, stateDir, store.AuthSecrets()); err != nil {
		return stats, fmt.Errorf("migrate auth secret: %w", err)
	}
	stats.AuthSecrets = n
	return stats, nil
}

func migrateSessions(ctx context.Context, stateDir string, r persistence.SessionRepository) (int, error) {
	var count int
	dir := filepath.Join(stateDir, "sessions")
	entries, err := readDirOptional(dir)
	if err != nil || entries == nil {
		return count, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return count, err
		}
		state := map[string]any{}
		if len(b) > 0 {
			if err := json.Unmarshal(b, &state); err != nil {
				return count, fmt.Errorf("unmarshal %s: %w", e.Name(), err)
			}
		}
		if err := r.Save(ctx, id, state); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func migrateSkills(ctx context.Context, stateDir string, r persistence.SkillRepository) (int, error) {
	var count int
	dir := filepath.Join(stateDir, "skills-registry")
	entries, err := readDirOptional(dir)
	if err != nil || entries == nil {
		return count, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".override.json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".override.json")
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return count, err
		}
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			return count, fmt.Errorf("unmarshal %s: %w", e.Name(), err)
		}
		s := &persistence.Skill{ID: id}
		s.Name = stringField(raw, "name")
		s.OverrideBody = stringField(raw, "body")
		s.Source = stringField(raw, "upstream_source")
		s.Path = stringField(raw, "upstream_path")
		if so, ok := raw["sort_order"].(float64); ok {
			s.SortOrder = int(so)
		}
		if err := r.Save(ctx, s); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func migrateAgents(ctx context.Context, stateDir string, r persistence.AgentRepository) (int, error) {
	var count int
	dir := filepath.Join(stateDir, "agents-registry")
	entries, err := readDirOptional(dir)
	if err != nil || entries == nil {
		return count, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return count, err
		}
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			return count, fmt.Errorf("unmarshal %s: %w", e.Name(), err)
		}
		a := &persistence.Agent{ID: id}
		a.Type = stringField(raw, "type")
		if a.Type == "" {
			a.Type = stringField(raw, "agent_type")
		}
		a.Label = stringField(raw, "label")
		a.RoleID = stringField(raw, "role_id")
		a.CreatorAccount = stringField(raw, "creator_account")
		if skills, ok := raw["owned_skills"].([]any); ok {
			for _, s := range skills {
				if str, ok := s.(string); ok {
					a.OwnedSkills = append(a.OwnedSkills, str)
				}
			}
		}
		if scope, ok := raw["owned_skills_scope"].(map[string]any); ok {
			a.OwnedSkillsScope = map[string]string{}
			for k, v := range scope {
				if str, ok := v.(string); ok {
					a.OwnedSkillsScope[k] = str
				}
			}
		}
		// Sessions array (best-effort decode; missing → empty slice)
		if sess, ok := raw["sessions"].([]any); ok {
			for _, s := range sess {
				if m, ok := s.(map[string]any); ok {
					ref := persistence.SessionRef{SessionID: stringField(m, "session_id")}
					if ts, ok := m["attached_at"].(string); ok {
						if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
							ref.AttachedAt = parsed
						}
					}
					a.Sessions = append(a.Sessions, ref)
				}
			}
		}
		if err := r.Save(ctx, a); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func migrateCanon(ctx context.Context, stateDir string, r persistence.CanonRepository) (int, error) {
	var count int
	curPath := filepath.Join(stateDir, "canon", "current.json")
	curBytes, err := os.ReadFile(curPath)
	if os.IsNotExist(err) {
		return count, nil // no canon on disk
	}
	if err != nil {
		return count, err
	}
	// History first (so versions are assigned in temporal order).
	histDir := filepath.Join(stateDir, "canon", "history")
	hist, _ := readDirOptional(histDir)
	for _, e := range hist {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(histDir, e.Name()))
		if err != nil {
			return count, err
		}
		body, updatedBy, title, err := parseCanonFile(b)
		if err != nil {
			return count, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		if _, err := r.Save(ctx, body, updatedBy, title); err != nil {
			return count, fmt.Errorf("save history %s: %w", e.Name(), err)
		}
		count++
	}
	// Current goes last so it ends up as the is_current row.
	body, updatedBy, title, err := parseCanonFile(curBytes)
	if err != nil {
		return count, err
	}
	if _, err := r.Save(ctx, body, updatedBy, title); err != nil {
		return count, err
	}
	count++
	return count, nil
}

func parseCanonFile(b []byte) (body, updatedBy, title string, err error) {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return "", "", "", err
	}
	return stringField(raw, "body"), stringField(raw, "updated_by"), stringField(raw, "title"), nil
}

func migrateTemplates(ctx context.Context, stateDir string, r persistence.TemplateRepository) (int, error) {
	var count int
	dir := filepath.Join(stateDir, "templates-registry")
	entries, err := readDirOptional(dir)
	if err != nil || entries == nil {
		return count, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".yaml")
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return count, err
		}
		t := &persistence.Template{ID: id, Name: id, Body: body}
		if err := r.Save(ctx, t); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func migrateAuthSecret(ctx context.Context, stateDir string, r persistence.AuthSecretRepository) (int, error) {
	var count int
	path := filepath.Join(stateDir, "auth.secret")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return count, nil
	}
	if err != nil {
		return count, err
	}
	if len(b) == 0 {
		return count, nil
	}
	if err := r.Save(ctx, "dashboard-hs256", b, "HS256"); err != nil {
		return count, err
	}
	count++
	return count, nil
}

// ─── helpers ────────────────────────────────────────────────────

func readDirOptional(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return entries, err
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

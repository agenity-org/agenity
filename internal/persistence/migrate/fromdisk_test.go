package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
	_ "modernc.org/sqlite"
)

// minimalStore wraps a *sql.DB with hand-rolled repositories for tests.
// Avoids importing internal/persistence/sqlite to keep migrate package
// independent of the backend implementations (tests run against any
// persistence.Store).
type minimalStore struct {
	sessions    *miniSessionRepo
	skills      *miniSkillRepo
	agents      *miniAgentRepo
	canon       *miniCanonRepo
	templates   *miniTemplateRepo
	authSecrets *miniAuthSecretRepo
}

// miniRepo definitions are stub implementations used only by the
// fromdisk test to verify FromDisk dispatches to each Repository's
// Save with the parsed JSON payload. They don't persist anything;
// they collect calls in-memory for inspection.

type miniSessionRepo struct{ saved map[string]map[string]any }

func (r *miniSessionRepo) Get(ctx context.Context, id string) (map[string]any, error) {
	return r.saved[id], nil
}
func (r *miniSessionRepo) Save(ctx context.Context, id string, state map[string]any) error {
	r.saved[id] = state
	return nil
}
func (r *miniSessionRepo) Delete(ctx context.Context, id string) error { return nil }
func (r *miniSessionRepo) List(ctx context.Context) ([]string, error)  { return nil, nil }
func (r *miniSessionRepo) ResumableSessions(_ context.Context) ([]persistence.ResumableSession, error) {
	return nil, nil
}

type miniSkillRepo struct{ saved map[string]*persistence.Skill }

func (r *miniSkillRepo) Get(ctx context.Context, id string) (*persistence.Skill, error) {
	return r.saved[id], nil
}
func (r *miniSkillRepo) List(ctx context.Context, _ persistence.SkillListOpts) ([]persistence.Skill, error) {
	return nil, nil
}
func (r *miniSkillRepo) Save(ctx context.Context, s *persistence.Skill) error {
	r.saved[s.ID] = s
	return nil
}
func (r *miniSkillRepo) Delete(ctx context.Context, id string) error { return nil }

type miniAgentRepo struct{ saved map[string]*persistence.Agent }

func (r *miniAgentRepo) Get(ctx context.Context, id string) (*persistence.Agent, error) {
	return r.saved[id], nil
}
func (r *miniAgentRepo) List(ctx context.Context) ([]*persistence.Agent, error) { return nil, nil }
func (r *miniAgentRepo) Save(ctx context.Context, a *persistence.Agent) error {
	r.saved[a.ID] = a
	return nil
}
func (r *miniAgentRepo) Delete(ctx context.Context, id string) error { return nil }

type miniCanonRepo struct {
	savedVersions []*persistence.Canon
	currentBody   string
}

func (r *miniCanonRepo) Get(ctx context.Context) (*persistence.Canon, error) {
	if r.currentBody == "" {
		return nil, nil
	}
	return &persistence.Canon{Body: r.currentBody, Version: len(r.savedVersions)}, nil
}
func (r *miniCanonRepo) Save(ctx context.Context, body, updatedBy, title string) (*persistence.Canon, error) {
	c := &persistence.Canon{
		Version: len(r.savedVersions) + 1, Body: body, UpdatedBy: updatedBy, Title: title,
	}
	r.savedVersions = append(r.savedVersions, c)
	r.currentBody = body
	return c, nil
}
func (r *miniCanonRepo) History(ctx context.Context, _ int) ([]*persistence.Canon, error) {
	return nil, nil
}
func (r *miniCanonRepo) Rollback(ctx context.Context, _ int, _ string) (*persistence.Canon, error) {
	return nil, nil
}

type miniTemplateRepo struct{ saved map[string]*persistence.Template }

func (r *miniTemplateRepo) Get(ctx context.Context, id string) (*persistence.Template, error) {
	return r.saved[id], nil
}
func (r *miniTemplateRepo) List(ctx context.Context) ([]*persistence.Template, error) { return nil, nil }
func (r *miniTemplateRepo) Save(ctx context.Context, t *persistence.Template) error {
	r.saved[t.ID] = t
	return nil
}
func (r *miniTemplateRepo) Delete(ctx context.Context, id string) error { return nil }

type miniAuthSecretRepo struct{ saved map[string]*persistence.AuthSecret }

func (r *miniAuthSecretRepo) Get(ctx context.Context, purpose string) (*persistence.AuthSecret, error) {
	return r.saved[purpose], nil
}
func (r *miniAuthSecretRepo) Save(ctx context.Context, purpose string, key []byte, alg string) error {
	r.saved[purpose] = &persistence.AuthSecret{Purpose: purpose, Key: key, Algorithm: alg}
	return nil
}

// Implement the remaining 7 Repos as stubs (returning nil/no-op) so
// minimalStore satisfies persistence.Store fully.
type stubKeychain struct{}

func (stubKeychain) Get(_ context.Context, _ string) (string, error) { return "", nil }
func (stubKeychain) Set(_ context.Context, _, _ string) error        { return nil }
func (stubKeychain) Delete(_ context.Context, _ string) error        { return nil }

type stubEvents struct{}

func (stubEvents) Append(_ context.Context, _ persistence.Event) error { return nil }
func (stubEvents) List(_ context.Context, _ persistence.EventListOpts) ([]persistence.Event, error) {
	return nil, nil
}

type stubGrants struct{}

func (stubGrants) Get(_ context.Context, _ string) (*persistence.Grant, error) { return nil, nil }
func (stubGrants) List(_ context.Context, _ persistence.GrantListOpts) ([]*persistence.Grant, error) {
	return nil, nil
}
func (stubGrants) Save(_ context.Context, _ *persistence.Grant) error { return nil }
func (stubGrants) Delete(_ context.Context, _ string) error            { return nil }

type stubTasks struct{}

func (stubTasks) Get(_ context.Context, _ string) (*persistence.Task, error) { return nil, nil }
func (stubTasks) Save(_ context.Context, _ *persistence.Task) error          { return nil }
func (stubTasks) List(_ context.Context, _ persistence.TaskListOpts) ([]*persistence.Task, error) {
	return nil, nil
}
func (stubTasks) Delete(_ context.Context, _ string) error { return nil }

type stubPushConfigs struct{}

func (stubPushConfigs) Get(_ context.Context, _ string) (*persistence.PushNotificationConfig, error) {
	return nil, nil
}
func (stubPushConfigs) List(_ context.Context, _ string) ([]*persistence.PushNotificationConfig, error) {
	return nil, nil
}
func (stubPushConfigs) Save(_ context.Context, _ *persistence.PushNotificationConfig) error {
	return nil
}
func (stubPushConfigs) Delete(_ context.Context, _ string) error { return nil }

type stubAgentCards struct{}

func (stubAgentCards) Get(_ context.Context, _ string) (*persistence.AgentCard, error) { return nil, nil }
func (stubAgentCards) Save(_ context.Context, _ *persistence.AgentCard) error          { return nil }
func (stubAgentCards) List(_ context.Context, _ persistence.AgentCardListOpts) ([]*persistence.AgentCard, error) {
	return nil, nil
}
func (stubAgentCards) Delete(_ context.Context, _ string) error { return nil }

type stubAccounts struct{}

func (stubAccounts) Get(_ context.Context, _ string) (*persistence.Account, error) { return nil, nil }
func (stubAccounts) List(_ context.Context) ([]*persistence.Account, error)        { return nil, nil }
func (stubAccounts) Save(_ context.Context, _ *persistence.Account) error          { return nil }
func (stubAccounts) Delete(_ context.Context, _ string) error                      { return nil }

func (s *minimalStore) Sessions() persistence.SessionRepository    { return s.sessions }
func (s *minimalStore) Skills() persistence.SkillRepository        { return s.skills }
func (s *minimalStore) Agents() persistence.AgentRepository        { return s.agents }
func (s *minimalStore) Canon() persistence.CanonRepository         { return s.canon }
func (s *minimalStore) Keychain() persistence.KeychainRepository   { return stubKeychain{} }
func (s *minimalStore) Templates() persistence.TemplateRepository  { return s.templates }
func (s *minimalStore) AuthSecrets() persistence.AuthSecretRepository {
	return s.authSecrets
}
func (s *minimalStore) Events() persistence.EventRepository { return stubEvents{} }
func (s *minimalStore) Grants() persistence.RBACGrantRepository { return stubGrants{} }
func (s *minimalStore) Tasks() persistence.TaskRepository       { return stubTasks{} }
func (s *minimalStore) PushConfigs() persistence.PushNotificationConfigRepository {
	return stubPushConfigs{}
}
func (s *minimalStore) AgentCards() persistence.AgentCardRepository { return stubAgentCards{} }
func (s *minimalStore) Accounts() persistence.AccountRepository     { return stubAccounts{} }
func (s *minimalStore) Artifacts() persistence.ArtifactRepository   { return stubArtifacts{} }
func (s *minimalStore) Close() error                                { return nil }

type stubArtifacts struct{}

func (stubArtifacts) Get(_ context.Context, _ string) (*persistence.Artifact, error) {
	return nil, nil
}
func (stubArtifacts) List(_ context.Context, _ string) ([]*persistence.Artifact, error) {
	return nil, nil
}
func (stubArtifacts) Save(_ context.Context, _ *persistence.Artifact) error { return nil }
func (stubArtifacts) Delete(_ context.Context, _ string) error              { return nil }

func newMinimalStore() *minimalStore {
	return &minimalStore{
		sessions:    &miniSessionRepo{saved: map[string]map[string]any{}},
		skills:      &miniSkillRepo{saved: map[string]*persistence.Skill{}},
		agents:      &miniAgentRepo{saved: map[string]*persistence.Agent{}},
		canon:       &miniCanonRepo{},
		templates:   &miniTemplateRepo{saved: map[string]*persistence.Template{}},
		authSecrets: &miniAuthSecretRepo{saved: map[string]*persistence.AuthSecret{}},
	}
}

func TestFromDisk_AllEntities(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Seed sessions/.
	sessDir := filepath.Join(dir, "sessions")
	mustMkdir(t, sessDir)
	mustWriteJSON(t, filepath.Join(sessDir, "sess-1.json"),
		map[string]any{"trust_band": "trusted"})
	mustWriteJSON(t, filepath.Join(sessDir, "sess-2.json"),
		map[string]any{"intervention_count": 3})

	// Seed skills-registry/<id>.override.json.
	skDir := filepath.Join(dir, "skills-registry")
	mustMkdir(t, skDir)
	mustWriteJSON(t, filepath.Join(skDir, "tdd.override.json"),
		map[string]any{
			"name":            "tdd",
			"body":            "Test first.",
			"upstream_source": "github.com/example/tdd",
			"upstream_path":   "skills/tdd.md",
			"sort_order":      10,
		})

	// Seed agents-registry/<uuid>.json.
	agDir := filepath.Join(dir, "agents-registry")
	mustMkdir(t, agDir)
	mustWriteJSON(t, filepath.Join(agDir, "agent-1.json"),
		map[string]any{
			"type":               "claude-code",
			"label":              "alice",
			"role_id":            "full-stack-developer",
			"creator_account":    "acc-1",
			"owned_skills":       []string{"tdd", "code-review"},
			"owned_skills_scope": map[string]string{"tdd": "all"},
			"sessions": []any{
				map[string]any{"session_id": "s-1", "attached_at": time.Now().UTC().Format(time.RFC3339)},
			},
		})

	// Seed canon/current.json (no history).
	canonDir := filepath.Join(dir, "canon")
	mustMkdir(t, canonDir)
	mustWriteJSON(t, filepath.Join(canonDir, "current.json"),
		map[string]any{"body": "current canon body", "updated_by": "operator", "title": "v1"})

	// Seed templates-registry/<id>.yaml.
	tmplDir := filepath.Join(dir, "templates-registry")
	mustMkdir(t, tmplDir)
	mustWriteFile(t, filepath.Join(tmplDir, "solo.yaml"), []byte("name: solo\nmembers: 1\n"))

	// Seed auth.secret.
	mustWriteFile(t, filepath.Join(dir, "auth.secret"), []byte("hs256-bytes"))

	// Run.
	store := newMinimalStore()
	stats, err := FromDisk(context.Background(), dir, store)
	if err != nil {
		t.Fatalf("FromDisk: %v", err)
	}

	// Verify per-entity-type stats.
	want := Stats{Sessions: 2, Skills: 1, Agents: 1, CanonRows: 1, Templates: 1, AuthSecrets: 1}
	if stats != want {
		t.Errorf("Stats = %+v, want %+v", stats, want)
	}

	// Verify each entity was migrated.
	if len(store.sessions.saved) != 2 {
		t.Errorf("sessions saved = %d, want 2", len(store.sessions.saved))
	}
	if got := store.sessions.saved["sess-1"]; got["trust_band"] != "trusted" {
		t.Errorf("sess-1 = %v", got)
	}
	if got := store.skills.saved["tdd"]; got == nil || got.OverrideBody != "Test first." || got.SortOrder != 10 {
		t.Errorf("tdd skill = %+v", got)
	}
	if got := store.agents.saved["agent-1"]; got == nil || got.Type != "claude-code" ||
		len(got.OwnedSkills) != 2 || got.OwnedSkillsScope["tdd"] != "all" ||
		len(got.Sessions) != 1 {
		t.Errorf("agent-1 = %+v", got)
	}
	if c, _ := store.canon.Get(context.Background()); c == nil || c.Body != "current canon body" {
		t.Errorf("canon current = %v", c)
	}
	if got := store.templates.saved["solo"]; got == nil || string(got.Body) != "name: solo\nmembers: 1\n" {
		t.Errorf("solo template = %v", got)
	}
	if got := store.authSecrets.saved["dashboard-hs256"]; got == nil ||
		string(got.Key) != "hs256-bytes" || got.Algorithm != "HS256" {
		t.Errorf("auth secret = %v", got)
	}
}

func TestFromDisk_EmptyStateDir(t *testing.T) {
	t.Parallel()
	store := newMinimalStore()
	if _, err := FromDisk(context.Background(), t.TempDir(), store); err != nil {
		t.Fatalf("FromDisk empty: %v", err)
	}
	// Nothing was migrated; no errors either.
}

func TestFromDisk_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "sessions"))
	mustWriteJSON(t, filepath.Join(dir, "sessions", "s.json"), map[string]any{"x": 1})

	store := newMinimalStore()
	ctx := context.Background()
	if _, err := FromDisk(ctx, dir, store); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := FromDisk(ctx, dir, store); err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(store.sessions.saved) != 1 {
		t.Errorf("sessions saved = %d, want 1 (idempotent)", len(store.sessions.saved))
	}
}

// ─── helpers ──────────────────────────────────────────────────────

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWriteFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mustWriteFile(t, path, b)
}

// Linter-required reference (avoid "imported but unused" error if sql
// isn't actually used by this test file). Removed if not needed.
var _ = sql.ErrNoRows

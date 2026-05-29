// Package equivalence runs the chepherd v0.9.2 persistence test suite
// against any persistence.Store implementation. The same assertions
// run against the SQLite + PostgreSQL backends, so a divergence in
// driver semantics surfaces immediately as a test failure on the
// non-conforming backend.
//
// Backends invoke this from their _test.go files via:
//
//	func TestEquivalence(t *testing.T) {
//	    store := openMyBackend(t)
//	    equivalence.RunAll(t, store)
//	}
//
// Refs #208.
package equivalence

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// RunAll executes the full equivalence suite against the given Store.
// Each Repository contract is exercised with the same assertions; a
// backend that diverges from SQLite's behavior fails here.
func RunAll(t *testing.T, store persistence.Store) {
	t.Helper()
	t.Run("Sessions", func(t *testing.T) { sessions(t, store.Sessions()) })
	t.Run("Keychain", func(t *testing.T) { keychain(t, store.Keychain()) })
	t.Run("AuthSecrets", func(t *testing.T) { authSecrets(t, store.AuthSecrets()) })
	t.Run("Events", func(t *testing.T) { events(t, store.Events()) })
	t.Run("Accounts", func(t *testing.T) { accounts(t, store.Accounts()) })
	t.Run("Templates", func(t *testing.T) { templates(t, store.Templates()) })
	t.Run("Grants", func(t *testing.T) { grants(t, store.Grants()) })
	t.Run("Tasks", func(t *testing.T) { tasks(t, store.Tasks()) })
	t.Run("PushConfigs", func(t *testing.T) { pushConfigs(t, store.PushConfigs()) })
	t.Run("AgentCards", func(t *testing.T) { agentCards(t, store.AgentCards()) })
	t.Run("Skills", func(t *testing.T) { skills(t, store.Skills()) })
	t.Run("Agents", func(t *testing.T) { agents(t, store.Agents()) })
	t.Run("Canon", func(t *testing.T) { canon(t, store.Canon()) })
}

func sessions(t *testing.T, r persistence.SessionRepository) {
	ctx := context.Background()
	got, _ := r.Get(ctx, "sess-eq")
	if len(got) != 0 {
		t.Errorf("Get missing = %v, want empty", got)
	}
	want := map[string]any{"band": "trusted", "count": float64(7)}
	if err := r.Save(ctx, "sess-eq", want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _ = r.Get(ctx, "sess-eq")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Get = %v, want %v", got, want)
	}
	ids, _ := r.List(ctx)
	if len(ids) != 1 || ids[0] != "sess-eq" {
		t.Errorf("List = %v, want [sess-eq]", ids)
	}
	_ = r.Delete(ctx, "sess-eq")
}

func keychain(t *testing.T, r persistence.KeychainRepository) {
	ctx := context.Background()
	if err := r.Set(ctx, "EQ_KEY", "val"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, _ := r.Get(ctx, "EQ_KEY")
	if v != "val" {
		t.Errorf("Get = %q, want val", v)
	}
	_ = r.Delete(ctx, "EQ_KEY")
}

func authSecrets(t *testing.T, r persistence.AuthSecretRepository) {
	ctx := context.Background()
	if err := r.Save(ctx, "eq-hs256", []byte("k"), "HS256"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s, err := r.Get(ctx, "eq-hs256")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Algorithm != "HS256" || string(s.Key) != "k" {
		t.Errorf("Get = %v, want HS256/k", s)
	}
}

func events(t *testing.T, r persistence.EventRepository) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if err := r.Append(ctx, persistence.Event{
		ID: "eq-e1", Kind: "spawn", Actor: "operator", Timestamp: now,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := r.Append(ctx, persistence.Event{
		ID: "eq-e2", Kind: "a2a", Timestamp: now.Add(time.Second),
		A2AMethod: "SendMessage", CallerOrg: "org-Z",
	}); err != nil {
		t.Fatalf("Append e2: %v", err)
	}
	es, _ := r.List(ctx, persistence.EventListOpts{Kinds: []string{"a2a"}})
	if len(es) != 1 || es[0].A2AMethod != "SendMessage" || es[0].CallerOrg != "org-Z" {
		t.Errorf("List kind=a2a = %v", es)
	}
}

func accounts(t *testing.T, r persistence.AccountRepository) {
	ctx := context.Background()
	a := &persistence.Account{ID: "eq-acc", Class: "anthropic", Label: "x"}
	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, "eq-acc")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Class != "anthropic" || got.Label != "x" {
		t.Errorf("Get = %+v", got)
	}
}

func templates(t *testing.T, r persistence.TemplateRepository) {
	ctx := context.Background()
	tmpl := &persistence.Template{
		ID: "eq-tmpl", Name: "n", Description: "d",
		Body:     []byte("yaml: data"),
		Metadata: []byte(`{"icon":"PlusCircle","slots":[]}`),
	}
	if err := r.Save(ctx, tmpl); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, "eq-tmpl")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "n" || string(got.Body) != "yaml: data" {
		t.Errorf("Get = %+v", got)
	}
	// Metadata round-trips intact (semantic-JSON compare so PostgreSQL
	// JSONB whitespace normalization doesn't false-fail).
	assertJSONEqual(t, got.Metadata, []byte(`{"icon":"PlusCircle","slots":[]}`),
		"Template.Metadata")
}

func grants(t *testing.T, r persistence.RBACGrantRepository) {
	ctx := context.Background()
	g := &persistence.Grant{
		ID:          "eq-grant",
		GranterOrg:  "org-X",
		GranteeOrg:  "org-Y",
		Scope:       persistence.GrantScope{Type: "workspace", WorkspaceID: "ws-1"},
		Permissions: []string{"call_agent", "read_agent_card"},
		RateLimit:   &persistence.GrantRateLimit{CallsPerMinute: 100, CallsPerDay: 10000},
		Accepted:    true,
		CreatedBy:   "operator",
	}
	if err := r.Save(ctx, g); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, "eq-grant")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.GranterOrg != "org-X" || got.Scope.Type != "workspace" ||
		got.RateLimit == nil || got.RateLimit.CallsPerMinute != 100 {
		t.Errorf("Get = %+v", got)
	}
	if !reflect.DeepEqual(got.Permissions, []string{"call_agent", "read_agent_card"}) {
		t.Errorf("permissions = %v", got.Permissions)
	}
}

func tasks(t *testing.T, r persistence.TaskRepository) {
	ctx := context.Background()
	tk := &persistence.Task{
		ID: "eq-task", RunnerSID: "runner-1", State: "WORKING", Method: "SendMessage",
		InputBlob: []byte(`{"msg":"hi"}`),
	}
	if err := r.Save(ctx, tk); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, "eq-task")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != "WORKING" || string(got.InputBlob) != `{"msg":"hi"}` {
		t.Errorf("Get = %+v", got)
	}
	listed, _ := r.List(ctx, persistence.TaskListOpts{RunnerSID: "runner-1"})
	if len(listed) != 1 {
		t.Errorf("List runner = %d, want 1", len(listed))
	}
}

func pushConfigs(t *testing.T, r persistence.PushNotificationConfigRepository) {
	ctx := context.Background()
	c := &persistence.PushNotificationConfig{
		ID: "eq-push", TaskID: "task-1", URL: "https://example/hook",
		SigningKey: []byte("hmac"),
		Filters:    []string{"state:COMPLETED"},
	}
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, "eq-push")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.URL != "https://example/hook" || string(got.SigningKey) != "hmac" ||
		len(got.Filters) != 1 || got.Filters[0] != "state:COMPLETED" {
		t.Errorf("Get = %+v", got)
	}
}

func agentCards(t *testing.T, r persistence.AgentCardRepository) {
	ctx := context.Background()
	body := []byte(`{"name":"eq-card","capabilities":{"streaming":true}}`)
	c := &persistence.AgentCard{
		SID: "eq-sid", Name: "eq-card", Body: body, PublicVisibility: true,
		SyncedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := r.Save(ctx, c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, "eq-sid")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.PublicVisibility || string(got.Body) != string(body) {
		t.Errorf("Get = %+v", got)
	}
	public, _ := r.List(ctx, persistence.AgentCardListOpts{PublicOnly: true})
	if len(public) != 1 {
		t.Errorf("List PublicOnly = %d, want 1", len(public))
	}
}

func skills(t *testing.T, r persistence.SkillRepository) {
	ctx := context.Background()
	s := &persistence.Skill{
		ID: "eq-tdd", Name: "tdd", DefaultBody: "Test first.",
		OverrideBody: "Test first and assert on behavior.",
		ReadOnly:     false, Source: "upstream", Path: "/skills/tdd", SortOrder: 10,
		Metadata: []byte(`{"icon":"Beaker","tags":["test"]}`),
	}
	if err := r.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, "eq-tdd")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.OverrideBody != "Test first and assert on behavior." || got.SortOrder != 10 {
		t.Errorf("Get = %+v", got)
	}
	assertJSONEqual(t, got.Metadata, []byte(`{"icon":"Beaker","tags":["test"]}`),
		"Skill.Metadata")
	all, _ := r.List(ctx, persistence.SkillListOpts{})
	if len(all) != 1 {
		t.Errorf("List = %d, want 1", len(all))
	}
}

func agents(t *testing.T, r persistence.AgentRepository) {
	ctx := context.Background()
	a := &persistence.Agent{
		ID: "eq-agent", Type: "claude-code", Label: "alice",
		RoleID:           "full-stack-developer",
		CreatorAccount:   "acc-1",
		OwnedSkills:      []string{"tdd", "code-review"},
		OwnedSkillsScope: map[string]string{"tdd": "all"},
		Sessions:         []persistence.SessionRef{{SessionID: "s-1", AttachedAt: time.Now().UTC().Truncate(time.Second)}},
		Metadata:         []byte(`{"pvc_handle":"pvc-eq"}`),
	}
	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := r.Get(ctx, "eq-agent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Type != "claude-code" || got.OwnedSkillsScope["tdd"] != "all" ||
		len(got.OwnedSkills) != 2 || len(got.Sessions) != 1 {
		t.Errorf("Get = %+v", got)
	}
	assertJSONEqual(t, got.Metadata, []byte(`{"pvc_handle":"pvc-eq"}`),
		"Agent.Metadata")
}

func canon(t *testing.T, r persistence.CanonRepository) {
	ctx := context.Background()
	// Empty.
	got, err := r.Get(ctx)
	if err != nil {
		t.Fatalf("Get empty: %v", err)
	}
	if got != nil {
		t.Errorf("Get empty = %v, want nil", got)
	}
	// Save v1.
	v1, err := r.Save(ctx, "first body", "operator", "v1")
	if err != nil {
		t.Fatalf("Save v1: %v", err)
	}
	if v1.Version != 1 || v1.Body != "first body" {
		t.Errorf("v1 = %+v", v1)
	}
	// Save v2 — replaces current.
	v2, err := r.Save(ctx, "second body", "operator", "v2")
	if err != nil {
		t.Fatalf("Save v2: %v", err)
	}
	if v2.Version <= v1.Version {
		t.Errorf("v2.Version=%d not > v1.Version=%d", v2.Version, v1.Version)
	}
	got, _ = r.Get(ctx)
	if got.Version != v2.Version || got.Body != "second body" {
		t.Errorf("Get current = %+v", got)
	}
	// History returns v1 only (v2 is current).
	hist, err := r.History(ctx, 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 1 || hist[0].Version != v1.Version {
		t.Errorf("History = %+v", hist)
	}
	// Rollback to v1.
	rolled, err := r.Rollback(ctx, v1.Version, "operator")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolled.Version != v1.Version || rolled.Body != "first body" {
		t.Errorf("Rollback = %+v", rolled)
	}
	// Get now returns v1.
	got, _ = r.Get(ctx)
	if got.Version != v1.Version {
		t.Errorf("Get after Rollback = v%d, want v%d", got.Version, v1.Version)
	}
	// Rollback to missing version → error.
	if _, err := r.Rollback(ctx, 9999, "operator"); err == nil {
		t.Error("Rollback to missing: want error, got nil")
	}
}

// assertJSONEqual asserts that two JSON byte slices are SEMANTICALLY
// equal — i.e. they parse to deeply-equal Go values. SQLite TEXT
// columns round-trip bytes verbatim; PostgreSQL JSONB normalizes
// whitespace on storage (`{"a":1}` may come back as `{"a": 1}`).
// Equivalence assertions on Metadata round-trip must compare DATA,
// not BYTES, so SQLite + PostgreSQL backends both satisfy the same
// contract.
//
// Refs #208.
func assertJSONEqual(t *testing.T, got, want []byte, label string) {
	t.Helper()
	var gotV, wantV any
	if err := json.Unmarshal(got, &gotV); err != nil {
		t.Errorf("%s: got is not valid JSON (%v): %q", label, err, string(got))
		return
	}
	if err := json.Unmarshal(want, &wantV); err != nil {
		t.Errorf("%s: want is not valid JSON (%v): %q", label, err, string(want))
		return
	}
	if !reflect.DeepEqual(gotV, wantV) {
		t.Errorf("%s: semantic JSON mismatch\n  got:  %s\n  want: %s",
			label, string(got), string(want))
	}
}

// internal/runtime/d4_resume_test.go — pins #350 D4 auto-resume.
// Verifies the SessionRepository.ResumableSessions contract +
// Runtime.ResumableSessions converts repo records to SpawnSpecs
// with --resume <uuid> appended.
//
// Refs #350 D4.
package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/agenity-org/agenity/internal/persistence/sqlite"
)

func TestD4_ResumableSessions_ReturnsPersistedClaudeUUIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "d4.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	defer store.Close()

	// Seed 3 sessions:
	//   - A: has claude_uuid + alive → SHOULD appear
	//   - B: has claude_uuid + exited → SHOULD NOT appear
	//   - C: no claude_uuid → SHOULD NOT appear
	cases := []struct {
		id    string
		state map[string]any
	}{
		{"sess-A", map[string]any{
			"name":        "agent-alpha",
			"agent":       "claude-code",
			"team":        "default",
			"cwd":         "/work",
			"claude_uuid": "uuid-alpha-12345",
			"exited":      false,
		}},
		{"sess-B", map[string]any{
			"name":        "agent-beta",
			"agent":       "claude-code",
			"claude_uuid": "uuid-beta-67890",
			"exited":      true,
		}},
		{"sess-C", map[string]any{
			"name":  "agent-gamma",
			"agent": "qwen-code",
			// no claude_uuid
		}},
	}
	for _, c := range cases {
		if err := store.Sessions().Save(ctx, c.id, c.state); err != nil {
			t.Fatalf("Save %q: %v", c.id, err)
		}
	}

	// Spin up a minimal Runtime with the store wired (we don't need a
	// real spawner / ContainerRuntime for this test — ResumableSessions
	// only reads the repository).
	r := &Runtime{sessionsRepo: store.Sessions()}

	specs, err := r.ResumableSessions(ctx)
	if err != nil {
		t.Fatalf("ResumableSessions: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("specs = %d, want 1 (only sess-A is alive + has uuid): %+v", len(specs), specs)
	}
	got := specs[0]
	if got.Name != "agent-alpha" {
		t.Errorf("Name = %q, want agent-alpha", got.Name)
	}
	if got.AgentSlug != "claude-code" {
		t.Errorf("AgentSlug = %q, want claude-code", got.AgentSlug)
	}
	if got.Team != "default" {
		t.Errorf("Team = %q, want default", got.Team)
	}
	if got.Cwd != "/work" {
		t.Errorf("Cwd = %q, want /work", got.Cwd)
	}
	if len(got.AgentArgs) != 2 || got.AgentArgs[0] != "--resume" || got.AgentArgs[1] != "uuid-alpha-12345" {
		t.Errorf("AgentArgs = %v, want [--resume uuid-alpha-12345]", got.AgentArgs)
	}
}

func TestD4_ResumableSessions_NoStoreReturnsNil(t *testing.T) {
	t.Parallel()
	r := &Runtime{} // no sessionsRepo
	specs, err := r.ResumableSessions(context.Background())
	if err != nil {
		t.Errorf("err = %v, want nil (back-compat for v0.9.1 callers)", err)
	}
	if specs != nil {
		t.Errorf("specs = %v, want nil", specs)
	}
}

func TestD4_Save_PersistsClaudeUUIDInColumn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "d4-save.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	defer store.Close()

	if err := store.Sessions().Save(ctx, "sess-x", map[string]any{
		"name":        "agent-x",
		"agent":       "claude-code",
		"claude_uuid": "uuid-x-saved",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// ResumableSessions reads from the indexed column directly; if
	// Save didn't write it, the result is empty.
	resumable, err := store.Sessions().ResumableSessions(ctx)
	if err != nil {
		t.Fatalf("ResumableSessions: %v", err)
	}
	if len(resumable) != 1 {
		t.Fatalf("resumable = %d, want 1", len(resumable))
	}
	if resumable[0].ClaudeSessionUUID != "uuid-x-saved" {
		t.Errorf("ClaudeSessionUUID = %q, want uuid-x-saved", resumable[0].ClaudeSessionUUID)
	}
}

func TestD4_Save_UpdateOverwritesClaudeUUID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "d4-update.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	defer store.Close()
	// First save with v1 UUID
	if err := store.Sessions().Save(ctx, "sess-u", map[string]any{
		"name":        "agent-u",
		"agent":       "claude-code",
		"claude_uuid": "uuid-v1",
	}); err != nil {
		t.Fatalf("Save v1: %v", err)
	}
	// Second save with v2 UUID
	if err := store.Sessions().Save(ctx, "sess-u", map[string]any{
		"name":        "agent-u",
		"agent":       "claude-code",
		"claude_uuid": "uuid-v2",
	}); err != nil {
		t.Fatalf("Save v2: %v", err)
	}
	resumable, _ := store.Sessions().ResumableSessions(ctx)
	if len(resumable) != 1 {
		t.Fatalf("expected 1, got %d", len(resumable))
	}
	if resumable[0].ClaudeSessionUUID != "uuid-v2" {
		t.Errorf("Got %q, want uuid-v2 (overwrite)", resumable[0].ClaudeSessionUUID)
	}
}

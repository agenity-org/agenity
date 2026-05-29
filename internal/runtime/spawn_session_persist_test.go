// internal/runtime/spawn_session_persist_test.go — pins the #216
// regression contract: Runtime.Spawn writes through to the
// SessionRepository so shepherd's discoverSessions tick sees the
// runtime-spawned session.
//
// Pre-#216 the only callers of store.Sessions().Save were:
//   - internal/shepherd/shepherd.go (after discovery — chicken-and-egg)
//   - test seed code
//
// PR #211 (runtime migration to persistence.Store) + PR #213 (daemon
// retire) stitched the SessionRepository contract into the runtime via
// NewWithStore + cmd/run.go but never updated Runtime.Spawn to write
// through it. The v0.9.2 e2e walk (PR #214 + the operator walk recorded
// on chepherd/chepherd#208) surfaced the defect at Step 6 — sessions
// table empty after 70s wait, shepherd ticking forever over zero rows.
//
// This test asserts the post-#216 invariant: NewWithStore + a helper
// invocation produce a row in store.Sessions() keyed by the session ID,
// with the agent-id + name + role + team + created_at fields populated.
//
// Refs #208.
package runtime

import (
	"context"
	"testing"
	"time"
)

// TestRuntime_SpawnPersistsSession_Helper exercises the
// persistInitialSessionState helper directly. The helper is extracted
// from the Spawn function precisely to make this assertion possible
// without needing a real PTY-backed session.New (which requires a
// runnable agent binary in PATH on the test host).
//
// Refs #208.
func TestRuntime_SpawnPersistsSession_Helper(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	rt, err := NewWithStore(t.TempDir(), store)
	if err != nil {
		t.Fatalf("NewWithStore: %v", err)
	}

	// Sanity: fresh store has zero session rows. If this assert ever
	// fires, the openTestStore helper changed shape — investigate.
	ctx := context.Background()
	rows, err := store.Sessions().List(ctx)
	if err != nil {
		t.Fatalf("Sessions.List pre-spawn: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("fresh store: sessions = %d, want 0", len(rows))
	}

	const (
		sessionID = "session-test-1780000000000000000"
		agentID   = "agent-test-uuid-7d9c-a69a"
	)
	spec := SpawnSpec{
		Name:      "shepherd",
		AgentSlug: "claude-code",
		Team:      "default",
		Role:      RoleShepherd,
		Cwd:       "/tmp",
	}
	info := &SessionInfo{
		ID:        sessionID,
		Name:      spec.Name,
		CreatedAt: time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC),
	}

	if err := rt.persistInitialSessionState(ctx, sessionID, spec, info, agentID); err != nil {
		t.Fatalf("persistInitialSessionState: %v", err)
	}

	// ─── Verify: store.Sessions().Get returns the row ───────────────
	state, err := store.Sessions().Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("Sessions.Get post-spawn: %v", err)
	}
	if state == nil {
		t.Fatal("Sessions.Get returned nil — Save did not persist")
	}

	// Schema assertions per the helper contract.
	if got := state["agent_id"]; got != agentID {
		t.Errorf("state[agent_id] = %v, want %q", got, agentID)
	}
	if got := state["name"]; got != "shepherd" {
		t.Errorf("state[name] = %v, want 'shepherd'", got)
	}
	if got := state["role"]; got != string(RoleShepherd) {
		t.Errorf("state[role] = %v, want %q", got, RoleShepherd)
	}
	if got := state["team"]; got != "default" {
		t.Errorf("state[team] = %v, want 'default'", got)
	}
	if got, ok := state["created_at"].(string); !ok || got == "" {
		t.Errorf("state[created_at] = %v, want non-empty RFC3339 string", state["created_at"])
	}

	// ─── Critical for the shepherd chicken-and-egg fix: state must
	// NOT carry next_tick_at, so shepherd.tickOnce treats this session
	// as "due now" and stamps last_tick_at + next_tick_at on the very
	// first tick after Spawn. If a future change starts seeding
	// next_tick_at here, the first observation lags by one TickInterval.
	if _, present := state["next_tick_at"]; present {
		t.Errorf("state[next_tick_at] set on initial spawn = %v, want absent so shepherd ticks immediately", state["next_tick_at"])
	}

	// ─── List discoverability — shepherd.discoverSessions queries this.
	ids, err := store.Sessions().List(ctx)
	if err != nil {
		t.Fatalf("Sessions.List post-spawn: %v", err)
	}
	if len(ids) != 1 || ids[0] != sessionID {
		t.Errorf("Sessions.List = %v, want [%q]", ids, sessionID)
	}
}

// TestRuntime_SpawnPersistsSession_NoStoreIsNoop confirms the v0.9.1
// file-on-disk fallback path stays safe: persistInitialSessionState
// silently no-ops when r.sessionsRepo is nil. Otherwise legacy callers
// of runtime.New (no Store provided) would crash on every Spawn.
//
// Refs #208.
func TestRuntime_SpawnPersistsSession_NoStoreIsNoop(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New (no store): %v", err)
	}
	if rt.sessionsRepo != nil {
		t.Fatal("rt.sessionsRepo should be nil for the v0.9.1 file-on-disk path")
	}
	// Helper must no-op without panicking; equivalent to legacy behavior.
	err = rt.persistInitialSessionState(
		context.Background(),
		"some-session",
		SpawnSpec{Name: "x"},
		&SessionInfo{ID: "some-session", CreatedAt: time.Now()},
		"some-agent",
	)
	if err != nil {
		t.Errorf("persistInitialSessionState with nil sessionsRepo: got err %v, want nil (silent no-op)", err)
	}
}

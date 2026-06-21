// internal/runtime/container_stop_test.go — pins the #258 invariants:
//
//   1. Runtime.Stop invokes containerRuntime.StopContainer with the
//      session's agent name, even when the underlying PTY close
//      succeeds. Pre-#258 the function only closed the PTY which left
//      `podman run --rm` cleanup unreliable (operator counted 19
//      zombies on `podman ps -a`).
//
//   2. Runtime.ReapOrphanContainers calls ListAgentContainers, then
//      StopContainer on every name that ISN'T in the live registry,
//      and SKIPS names that ARE in the registry (no spurious teardown
//      of live agents on chepherd boot).
//
// Refs #258.
package runtime

import (
	"strings"
	"sync"
	"testing"

	"github.com/agenity-org/agenity/internal/ptyhost/session"
	"github.com/google/uuid"
)

// newTestRuntime constructs a minimally-initialised *Runtime
// sufficient for Stop / ReapOrphanContainers tests — no spawner,
// no vault, no persistence. broadcast() requires r.cond which
// hangs off r.mu so we mint both here.
func newTestRuntime(t *testing.T, cr ContainerRuntime) *Runtime {
	t.Helper()
	rt := &Runtime{
		stateDir:         t.TempDir(),
		containerRuntime: cr,
		sessions:         map[string]*session.Session{},
		byName:           map[string]string{},
		info:             map[string]*SessionInfo{},
		sessionToAgent:   map[string]uuid.UUID{},
	}
	rt.cond = sync.NewCond(&rt.mu)
	return rt
}

// fakeContainerRuntime implements ContainerRuntime for tests. Records
// every StopContainer call + serves a controllable ListAgentContainers
// response.
type fakeContainerRuntime struct {
	mu          sync.Mutex
	stopped     []string
	listReturns []string
	listErr     error
}

func (f *fakeContainerRuntime) Name() string         { return "fake" }
func (f *fakeContainerRuntime) Available() error     { return nil }
func (f *fakeContainerRuntime) AgentHomeDir(agentName, stateDir string) (string, error) {
	return stateDir + "/" + agentName, nil
}
func (f *fakeContainerRuntime) SpawnArgs(agentName, agentHomeDir, agentSecretsDir, cwd string, argv []string, env []string) ([]string, []string) {
	return argv, env
}
// #270 — interface compliance; existing tests don't assert on UUID.
func (f *fakeContainerRuntime) SetInstanceUUID(string) {}
func (f *fakeContainerRuntime) StopContainer(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, name)
	return nil
}
func (f *fakeContainerRuntime) ListAgentContainers() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.listReturns...), f.listErr
}
func (f *fakeContainerRuntime) ProbeContainerRunning(string) (bool, string, error) {
	return true, "", nil
}
func (f *fakeContainerRuntime) stoppedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.stopped...)
}

// TestStopCallsContainerStop pins the post-#258 invariant: Runtime.Stop
// invokes containerRuntime.StopContainer with the session's agent
// name. Pre-#258 the function returned without touching the container
// layer at all (PTY-close-only).
func TestStopCallsContainerStop(t *testing.T) {
	fcr := &fakeContainerRuntime{}
	rt := newTestRuntime(t, fcr)
	// Register a fake session so Stop has something to look up. The
	// nil *session.Session is fine — Stop's `if s != nil` guards the
	// Close call.
	rt.byName["unit-test-agent"] = "sess-1"
	rt.sessions["sess-1"] = nil
	rt.info["sess-1"] = &SessionInfo{Name: "unit-test-agent"}

	if err := rt.Stop("unit-test-agent"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	names := fcr.stoppedNames()
	if len(names) != 1 || names[0] != "unit-test-agent" {
		t.Fatalf("expected StopContainer(\"unit-test-agent\") exactly once, got %v", names)
	}
}

// TestReapOrphanContainersSkipsLive pins the post-#258 startup helper:
// only containers whose agent-name is NOT in the live registry get
// torn down. Operators who restart chepherd with running agents must
// not lose them.
func TestReapOrphanContainersSkipsLive(t *testing.T) {
	fcr := &fakeContainerRuntime{
		// Both a live agent AND an orphan container are present.
		listReturns: []string{
			"chepherd-agent-alive-one",
			"chepherd-agent-orphan-one",
			"chepherd-agent-orphan-two",
		},
	}
	rt := newTestRuntime(t, fcr)
	rt.byName["alive-one"] = "sess-A"
	rt.info["sess-A"] = &SessionInfo{Name: "alive-one"}
	reaped := rt.ReapOrphanContainers()
	if reaped != 2 {
		t.Errorf("expected 2 reaped, got %d", reaped)
	}
	names := fcr.stoppedNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 StopContainer calls, got %d (%v)", len(names), names)
	}
	for _, n := range names {
		if n == "alive-one" {
			t.Errorf("orphan reap MUST NOT kill live agents — got %v", names)
		}
		if !strings.HasPrefix(n, "orphan-") {
			t.Errorf("unexpected reaped name %q (wanted orphan-*)", n)
		}
	}
}

// TestReapOrphanContainersNoCrashOnEmpty pins the "no agents to reap"
// case — startup helper must not crash when the operator has zero
// chepherd-agent-* containers (the happy path on a fresh install).
func TestReapOrphanContainersNoCrashOnEmpty(t *testing.T) {
	fcr := &fakeContainerRuntime{listReturns: nil}
	rt := newTestRuntime(t, fcr)
	if got := rt.ReapOrphanContainers(); got != 0 {
		t.Errorf("expected 0 reaped on empty, got %d", got)
	}
}

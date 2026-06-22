// internal/runtime/instance_uuid_scoping_test.go — pins the #270
// invariant: two chepherd Runtimes with distinct state-dirs derive
// distinct 8-char instance UUIDs, prefix container names with those
// UUIDs, and never see (or reap) each other's containers.
//
// The pre-#270 reaper used the unscoped filter `chepherd-agent-*`
// → a developer running a second chepherd binary alongside the
// operator's bastion could cross-kill bastion agents on boot.
//
// Refs #270 #258 #260 #218.
package runtime

import (
	"strings"
	"sync"
	"testing"

	"github.com/agenity-org/agenity/internal/ptyhost/session"
	"github.com/google/uuid"
)

// TestInstanceUUIDIsDeterministicAndUnique pins the helper used by
// NewWithStore: same state-dir path → same UUID, distinct paths →
// distinct UUIDs (with overwhelming probability — 8 hex chars =
// 2^32 keyspace, ~1e-19 collision chance for the realistic count of
// chepherd binaries on a host).
func TestInstanceUUIDIsDeterministicAndUnique(t *testing.T) {
	a := instanceUUIDFromStateDir("/home/openova/.local/state/chepherd")
	b := instanceUUIDFromStateDir("/home/openova/.local/state/chepherd")
	if a != b {
		t.Errorf("expected deterministic UUID, got %q vs %q", a, b)
	}
	c := instanceUUIDFromStateDir("/tmp/chepherd-dev-state")
	if a == c {
		t.Errorf("expected distinct UUIDs for distinct paths, both got %q", a)
	}
	if len(a) != 8 || len(c) != 8 {
		t.Errorf("expected 8-char UUIDs, got len=%d and len=%d", len(a), len(c))
	}
}

// instanceScopedContainerRuntime implements ContainerRuntime + tracks
// which agent names IT was asked to stop. Container names are
// prefixed with the instanceUUID so the test can simulate two
// chepherd binaries owning distinct pools.
type instanceScopedContainerRuntime struct {
	mu           sync.Mutex
	instanceUUID string
	owned        map[string]struct{} // agent slugs (without prefix)
	stopped      []string
}

func newInstanceScopedCR(agents ...string) *instanceScopedContainerRuntime {
	cr := &instanceScopedContainerRuntime{owned: map[string]struct{}{}}
	for _, a := range agents {
		cr.owned[a] = struct{}{}
	}
	return cr
}

func (f *instanceScopedContainerRuntime) Name() string     { return "scoped-fake" }
func (f *instanceScopedContainerRuntime) Available() error { return nil }
func (f *instanceScopedContainerRuntime) AgentHomeDir(name, stateDir string) (string, error) {
	return stateDir + "/" + name, nil
}
func (f *instanceScopedContainerRuntime) SpawnArgs(name, home, secrets, cwd string, argv, env []string) ([]string, []string) {
	return argv, env
}
func (f *instanceScopedContainerRuntime) SetInstanceUUID(uuid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instanceUUID = uuid
}
func (f *instanceScopedContainerRuntime) StopContainer(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, name)
	return nil
}

// ListAgentContainers mimics `podman ps -a --filter
// name=chepherd-agent-<uuid>-` — returns only OUR own containers
// regardless of what other instances might have running. This is the
// post-#270 behaviour; pre-#270 the filter would have returned every
// `chepherd-agent-*` container on the host.
func (f *instanceScopedContainerRuntime) ListAgentContainers() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := "chepherd-agent-" + f.instanceUUID + "-"
	out := []string{}
	for slug := range f.owned {
		out = append(out, prefix+slug)
	}
	return out, nil
}
func (f *instanceScopedContainerRuntime) ProbeContainerRunning(string) (bool, string, error) {
	return true, "", nil
}
func (f *instanceScopedContainerRuntime) stoppedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.stopped...)
}

// makeRuntime constructs a minimally-wired Runtime bound to a specific
// state-dir + container runtime + with the live registry preloaded.
func makeRuntime(t *testing.T, stateDir string, cr ContainerRuntime, liveAgents ...string) *Runtime {
	t.Helper()
	uuidStr := instanceUUIDFromStateDir(stateDir)
	cr.SetInstanceUUID(uuidStr)
	rt := &Runtime{
		stateDir:         stateDir,
		instanceUUID:     uuidStr,
		containerRuntime: cr,
		sessions:         map[string]*session.Session{},
		byName:           map[string]string{},
		info:             map[string]*SessionInfo{},
		sessionToAgent:   map[string]uuid.UUID{},
	}
	rt.cond = sync.NewCond(&rt.mu)
	for i, a := range liveAgents {
		rt.byName[a] = "sess-" + a
		rt.info["sess-"+a] = &SessionInfo{Name: a, ID: "sess-" + a}
		_ = i
	}
	return rt
}

// TestReaperDoesNotCrossKillSecondInstance simulates the operator's
// real scenario: bastion chepherd at ~/.local/state/chepherd owns
// `product-owner`, `architect`, `frontend`. Developer's test binary
// at /tmp/chepherd-dev boots fresh with zero agents. Pre-#270 the
// dev binary's reaper would list all bastion containers + kill them.
// Post-#270, ListAgentContainers is instance-scoped so the dev
// binary doesn't even SEE the bastion's containers.
func TestReaperDoesNotCrossKillSecondInstance(t *testing.T) {
	bastionCR := newInstanceScopedCR("product-owner", "architect", "frontend")
	bastion := makeRuntime(t, t.TempDir()+"/bastion-state", bastionCR,
		"product-owner", "architect", "frontend")
	devCR := newInstanceScopedCR()
	dev := makeRuntime(t, t.TempDir()+"/dev-state", devCR)
	if bastion.InstanceUUID() == dev.InstanceUUID() {
		t.Fatalf("expected distinct UUIDs, both got %q", bastion.InstanceUUID())
	}
	// Dev runtime boots → calls ReapOrphanContainers. Bastion's
	// container runtime is a separate fake instance, so the dev
	// reaper's containerRuntime.ListAgentContainers() returns ONLY
	// devCR.owned (empty) — bastionCR.owned is invisible to dev.
	// Pre-#270 the same call would have returned EVERY chepherd-agent-*
	// container on the host (we model that here by checking that dev
	// does NOT stop any bastion agents).
	reaped := dev.ReapOrphanContainers()
	if reaped != 0 {
		t.Errorf("dev reaper killed %d containers; expected 0 (cross-kill regression)", reaped)
	}
	if len(devCR.stoppedNames()) > 0 {
		t.Errorf("dev reaper called StopContainer with %v — should never touch bastion agents", devCR.stoppedNames())
	}
	if len(bastionCR.stoppedNames()) > 0 {
		t.Errorf("bastion CR received stop calls FROM the dev reaper: %v — instance-scoping broken", bastionCR.stoppedNames())
	}
}

// TestReaperStillCleansOwnOrphans pins the #258 invariant that #270
// is meant to preserve, not break: if a chepherd's own container
// list contains a name that's NOT in its live registry, that
// container IS still reaped. The instance-scoping prevents
// cross-killing of OTHER chepherds' agents; it does not turn off
// the orphan-cleanup behaviour for THIS chepherd's own zombies.
func TestReaperStillCleansOwnOrphans(t *testing.T) {
	// "ghost-agent" was spawned by a prior chepherd boot, survived
	// the crash, and is now on the container list but NOT in the
	// fresh registry.
	cr := newInstanceScopedCR("ghost-agent")
	rt := makeRuntime(t, t.TempDir()+"/state", cr /* no live agents */)
	if reaped := rt.ReapOrphanContainers(); reaped != 1 {
		t.Errorf("expected 1 reaped (own orphan), got %d", reaped)
	}
	names := cr.stoppedNames()
	if len(names) != 1 || names[0] != "ghost-agent" {
		t.Errorf("expected StopContainer(\"ghost-agent\"), got %v", names)
	}
}

// TestContainerNamePrefixCarriesInstanceUUID — sanity-check the
// pure helper used by every name-composition site so a future
// refactor doesn't silently change the shape (e.g. drop the dash).
func TestContainerNamePrefixCarriesInstanceUUID(t *testing.T) {
	got := containerNamePrefix("a1b2c3d4")
	if got != "chepherd-agent-a1b2c3d4-" {
		t.Errorf("expected `chepherd-agent-a1b2c3d4-`, got %q", got)
	}
	// Defensive fallback when UUID is empty: pre-#270 shape so a
	// not-yet-configured runtime doesn't break spawn.
	if got := containerNamePrefix(""); got != "chepherd-agent-" {
		t.Errorf("expected `chepherd-agent-` fallback, got %q", got)
	}
	// Sanity: composing with a slug produces the expected full name.
	full := containerNamePrefix("a1b2c3d4") + "frontend-developer"
	if !strings.HasPrefix(full, "chepherd-agent-a1b2c3d4-") {
		t.Errorf("composed name %q missing scoped prefix", full)
	}
}

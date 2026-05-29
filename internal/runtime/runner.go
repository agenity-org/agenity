package runtime

import (
	"context"
	"errors"
	"io"

	"github.com/chepherd/chepherd/internal/persistence"
)

// Runner is the spawn-and-manage abstraction backing the chepherd
// v0.9.2 runner-vs-pod parity per docs/V0.9.2-ARCHITECTURE.md §S1.
//
// Two implementations live in this package:
//
//   - ProcessRunner — rootless podman sibling containers (dev /
//     single-host) wrapping the existing 4001 LOC Runtime
//   - PodRunner — k8s CRI Pods (production / multi-host)
//
// Both implementations consume a persistence.Store from PR #209 —
// no JSON-on-disk inside runtime. The broader Runtime concerns
// (events / inbox / teams / scorecards / band / judge) stay on the
// composing Runtime struct; Runner is the focused process-management
// surface that swaps between dev and prod orchestrators.
//
// Refs #208.
type Runner interface {
	// Spawn launches a new agent process per spec. Returns its
	// session metadata; persists the session record via the bound
	// SessionRepository.
	Spawn(ctx context.Context, spec SpawnSpec) (*SessionInfo, error)

	// Stop terminates the session. Idempotent: stopping an
	// already-stopped session returns nil.
	Stop(ctx context.Context, sessionID string) error

	// Get returns session metadata or an error wrapping
	// ErrSessionNotFound.
	Get(ctx context.Context, sessionID string) (*SessionInfo, error)

	// List returns all sessions this runner manages.
	List(ctx context.Context) ([]*SessionInfo, error)

	// Pause toggles whether the session accepts new input.
	Pause(ctx context.Context, sessionID string, paused bool) error

	// Restart kills + respawns the session under the same ID,
	// preserving its on-disk working directory and identity.
	Restart(ctx context.Context, sessionID string) error

	// Rename changes the operator-facing handle without disturbing
	// the underlying process.
	Rename(ctx context.Context, sessionID, newName string) error

	// AttachIO returns a duplex stream wired to the session's PTY.
	// Closing the returned ReadWriter detaches without stopping
	// the session.
	AttachIO(ctx context.Context, sessionID string) (io.ReadWriteCloser, error)
}

// ErrSessionNotFound is returned by Runner.Get / Stop / Pause /
// Restart / Rename / AttachIO when the session doesn't exist.
var ErrSessionNotFound = errors.New("runtime: session not found")

// errScaffoldPending returns a sentinel error indicating the named
// Runner method's implementation arrives in a subsequent commit on
// this sub-branch. Used by PodRunner stubs until k8s integration lands.
func errScaffoldPending(method string) error {
	return errors.New("runtime: " + method + " scaffold pending — k8s integration commit on this sub-branch")
}

// RunnerKind enumerates the two implementations Runner ships in
// v0.9.2.
type RunnerKind string

const (
	RunnerKindProcess RunnerKind = "process" // rootless podman sibling
	RunnerKindPod     RunnerKind = "pod"     // k8s CRI Pod
)

// RunnerConfig carries the substrate dependencies common to both
// implementations. Each impl ignores fields it doesn't need
// (e.g., KubeconfigPath is process-mode-empty).
type RunnerConfig struct {
	// Kind selects ProcessRunner or PodRunner. Required.
	Kind RunnerKind

	// Store is the persistence layer the runner reads/writes session
	// metadata + agent registry + audit events through. Required for
	// both kinds.
	Store persistence.Store

	// StateDir is the host directory where per-session working trees,
	// secrets, PTY logs etc. live. Required for both kinds (process
	// mounts it into containers; pod uses it as a PVC source).
	StateDir string

	// PodmanRoot overrides the rootless podman storage location for
	// ProcessRunner only. Empty = host's default ($XDG_DATA_HOME).
	PodmanRoot string

	// KubeconfigPath is the path to the kubeconfig file PodRunner
	// uses. Required when Kind == RunnerKindPod.
	KubeconfigPath string
}

// NewRunner constructs a Runner per cfg.Kind. Returns an error if
// cfg.Kind is unrecognized or required fields are missing.
//
// Both ProcessRunner and PodRunner currently return a sentinel
// "scaffold pending" error from their methods until the implementation
// commits in this sub-branch wire them up. The interface + dispatch
// shape are stable.
func NewRunner(cfg RunnerConfig) (Runner, error) {
	if cfg.Store == nil {
		return nil, errors.New("runtime.NewRunner: Store is required")
	}
	if cfg.StateDir == "" {
		return nil, errors.New("runtime.NewRunner: StateDir is required")
	}
	switch cfg.Kind {
	case RunnerKindProcess:
		return newProcessRunner(cfg)
	case RunnerKindPod:
		return newPodRunner(cfg)
	default:
		return nil, errors.New("runtime.NewRunner: unknown Kind " + string(cfg.Kind))
	}
}

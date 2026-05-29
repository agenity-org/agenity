package runtime

import (
	"context"
	"errors"
	"io"
)

// ProcessRunner implements Runner for rootless podman sibling
// containers — chepherd's dev / single-host substrate. Wraps the
// existing 4001 LOC Runtime spawn logic (incrementally migrated in
// subsequent commits on this sub-branch).
//
// Refs #208.
type ProcessRunner struct {
	cfg RunnerConfig
}

func newProcessRunner(cfg RunnerConfig) (*ProcessRunner, error) {
	return &ProcessRunner{cfg: cfg}, nil
}

// Spawn — concrete implementation arrives in the runtime-migration
// commit on this sub-branch (wraps existing Runtime.Spawn after the
// persistence.Store wiring lands).
func (r *ProcessRunner) Spawn(ctx context.Context, spec SpawnSpec) (*SessionInfo, error) {
	return nil, errScaffoldPending("ProcessRunner.Spawn")
}

func (r *ProcessRunner) Stop(ctx context.Context, sessionID string) error {
	return errScaffoldPending("ProcessRunner.Stop")
}

func (r *ProcessRunner) Get(ctx context.Context, sessionID string) (*SessionInfo, error) {
	return nil, errScaffoldPending("ProcessRunner.Get")
}

func (r *ProcessRunner) List(ctx context.Context) ([]*SessionInfo, error) {
	return nil, errScaffoldPending("ProcessRunner.List")
}

func (r *ProcessRunner) Pause(ctx context.Context, sessionID string, paused bool) error {
	return errScaffoldPending("ProcessRunner.Pause")
}

func (r *ProcessRunner) Restart(ctx context.Context, sessionID string) error {
	return errScaffoldPending("ProcessRunner.Restart")
}

func (r *ProcessRunner) Rename(ctx context.Context, sessionID, newName string) error {
	return errScaffoldPending("ProcessRunner.Rename")
}

func (r *ProcessRunner) AttachIO(ctx context.Context, sessionID string) (io.ReadWriteCloser, error) {
	return nil, errScaffoldPending("ProcessRunner.AttachIO")
}

// errScaffoldPending returns a sentinel error indicating the named
// Runner method's implementation arrives in a subsequent commit on
// this sub-branch. Stops at compile-time refactor; behavior-time
// integration happens in a follow-up.
func errScaffoldPending(method string) error {
	return errors.New("runtime: " + method + " scaffold pending — implementation arrives in this sub-branch's runtime-migration commit")
}

var _ Runner = (*ProcessRunner)(nil)

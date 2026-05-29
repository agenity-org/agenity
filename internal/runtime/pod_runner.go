package runtime

import (
	"context"
	"errors"
	"io"
)

// PodRunner implements Runner for k8s CRI Pods — chepherd's
// production / multi-host substrate. Implementation arrives in this
// sub-branch's k8s-integration commit; the type + interface
// conformance land here so the dispatcher in NewRunner compiles.
//
// Refs #208.
type PodRunner struct {
	cfg RunnerConfig
}

func newPodRunner(cfg RunnerConfig) (*PodRunner, error) {
	if cfg.KubeconfigPath == "" {
		return nil, errors.New("runtime.NewRunner: PodRunner requires cfg.KubeconfigPath")
	}
	return &PodRunner{cfg: cfg}, nil
}

func (r *PodRunner) Spawn(ctx context.Context, spec SpawnSpec) (*SessionInfo, error) {
	return nil, errScaffoldPending("PodRunner.Spawn")
}

func (r *PodRunner) Stop(ctx context.Context, sessionID string) error {
	return errScaffoldPending("PodRunner.Stop")
}

func (r *PodRunner) Get(ctx context.Context, sessionID string) (*SessionInfo, error) {
	return nil, errScaffoldPending("PodRunner.Get")
}

func (r *PodRunner) List(ctx context.Context) ([]*SessionInfo, error) {
	return nil, errScaffoldPending("PodRunner.List")
}

func (r *PodRunner) Pause(ctx context.Context, sessionID string, paused bool) error {
	return errScaffoldPending("PodRunner.Pause")
}

func (r *PodRunner) Restart(ctx context.Context, sessionID string) error {
	return errScaffoldPending("PodRunner.Restart")
}

func (r *PodRunner) Rename(ctx context.Context, sessionID, newName string) error {
	return errScaffoldPending("PodRunner.Rename")
}

func (r *PodRunner) AttachIO(ctx context.Context, sessionID string) (io.ReadWriteCloser, error) {
	return nil, errScaffoldPending("PodRunner.AttachIO")
}

var _ Runner = (*PodRunner)(nil)

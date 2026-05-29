package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/chepherd/chepherd/internal/ptyhost/session"
)

// ProcessRunner implements Runner for rootless podman sibling
// containers — chepherd's dev / single-host substrate. Wraps the
// existing 4001 LOC Runtime spawn/manage logic by delegating each
// Runner method to its Runtime equivalent. The persistence.Store
// from cfg is held for forthcoming wire-up of session/agent/event
// persistence reads in this sub-branch's later commits; the current
// commit delegates verbatim to Runtime which still owns its
// internal state machinery.
//
// Refs #208.
type ProcessRunner struct {
	cfg     RunnerConfig
	rt      *Runtime
	rtOwned bool // true when ProcessRunner constructed Runtime itself
}

func newProcessRunner(cfg RunnerConfig) (*ProcessRunner, error) {
	rt, err := NewWithStore(cfg.StateDir, cfg.Store)
	if err != nil {
		return nil, fmt.Errorf("ProcessRunner: bootstrap Runtime: %w", err)
	}
	return &ProcessRunner{cfg: cfg, rt: rt, rtOwned: true}, nil
}

// NewProcessRunnerFromRuntime wires ProcessRunner around an existing
// *Runtime (e.g. one created by cmd/run.go that holds operator hooks,
// vault provider, etc.). The persistence.Store from cfg is held for
// future wiring; Runner.* still delegates to the Runtime's existing
// methods.
func NewProcessRunnerFromRuntime(cfg RunnerConfig, rt *Runtime) *ProcessRunner {
	return &ProcessRunner{cfg: cfg, rt: rt, rtOwned: false}
}

// Runtime exposes the wrapped *Runtime so callers (cmd/, runtimehttp/,
// etc.) that still depend on the broader API surface (events / inbox /
// teams / scorecards / canon-bootstrap) can reach it during the
// runner-split migration window. Slated to shrink as those concerns
// move to dedicated abstractions in later sub-branches.
func (r *ProcessRunner) Runtime() *Runtime { return r.rt }

// Spawn launches a new agent session via Runtime.Spawn and discards
// the *session.Session handle (callers reach it via AttachIO when
// they need the PTY).
func (r *ProcessRunner) Spawn(ctx context.Context, spec SpawnSpec) (*SessionInfo, error) {
	info, _, err := r.rt.Spawn(spec)
	if err != nil {
		return nil, err
	}
	return info, nil
}

func (r *ProcessRunner) Stop(ctx context.Context, sessionID string) error {
	err := r.rt.Stop(sessionID)
	if err != nil && errors.Is(err, ErrSessionNotFound) {
		return ErrSessionNotFound
	}
	return err
}

func (r *ProcessRunner) Get(ctx context.Context, sessionID string) (*SessionInfo, error) {
	_, info := r.rt.Get(sessionID)
	if info == nil {
		return nil, ErrSessionNotFound
	}
	return info, nil
}

func (r *ProcessRunner) List(ctx context.Context) ([]*SessionInfo, error) {
	return r.rt.List(), nil
}

func (r *ProcessRunner) Pause(ctx context.Context, sessionID string, paused bool) error {
	return r.rt.Pause(sessionID, paused)
}

func (r *ProcessRunner) Restart(ctx context.Context, sessionID string) error {
	_, err := r.rt.Restart(sessionID)
	return err
}

func (r *ProcessRunner) Rename(ctx context.Context, sessionID, newName string) error {
	return r.rt.Rename(sessionID, newName)
}

// AttachIO returns a duplex stream wired to the session's PTY via
// session.Subscriber for output and session.Write for input. Closing
// the returned ReadWriteCloser detaches the subscriber without
// stopping the agent.
func (r *ProcessRunner) AttachIO(ctx context.Context, sessionID string) (io.ReadWriteCloser, error) {
	sess, info := r.rt.Get(sessionID)
	if info == nil || sess == nil {
		return nil, ErrSessionNotFound
	}
	sub, replay, err := sess.Subscribe(64 * 1024)
	if err != nil {
		return nil, fmt.Errorf("AttachIO: subscribe: %w", err)
	}
	return &ptyAttach{sub: sub, sess: sess, initial: replay}, nil
}

var _ Runner = (*ProcessRunner)(nil)

// ptyAttach is the io.ReadWriteCloser returned by ProcessRunner.AttachIO.
// Read drains the Subscriber's output channel (initial replay first,
// then live frames); Write sends bytes to the session's PTY stdin;
// Close detaches the subscriber.
type ptyAttach struct {
	sub     *session.Subscriber
	sess    *session.Session
	initial []byte
	pos     int
}

func (a *ptyAttach) Read(p []byte) (int, error) {
	// Drain initial replay first.
	if a.pos < len(a.initial) {
		n := copy(p, a.initial[a.pos:])
		a.pos += n
		return n, nil
	}
	// Then pull from live channel. Subscriber.Ch returns chunks; we
	// proxy one chunk per Read call (caller loops). Subscriber.Done
	// closing signals session exit.
	select {
	case chunk, ok := <-a.sub.Ch:
		if !ok {
			return 0, io.EOF
		}
		return copy(p, chunk), nil
	case <-a.sub.Done:
		return 0, io.EOF
	}
}

func (a *ptyAttach) Write(p []byte) (int, error) {
	return a.sess.Write(p)
}

func (a *ptyAttach) Close() error {
	a.sess.Unsubscribe(a.sub)
	return nil
}

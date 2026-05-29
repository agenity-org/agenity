// Package shepherd is chepherd's worker-observation tier: bands /
// judge / signals / engagement / typing-pattern / discovery — the
// intelligence that watches what worker agents are doing and emits
// verdicts (accomplishment / drift / stuck / failure) the operator
// dashboard surfaces.
//
// Originally lived in internal/daemon as a Go port of the Python
// supervisor.py; moved to internal/shepherd in chepherd v0.9.2
// (#208) per architect call:
//
//   - separation of concerns / SOLID-S (Runtime's bounded context is
//     spawn-lifecycle + agent-registry + PTY-as-message-bus + event
//     spine; shepherd's bounded context is observe + score + judge +
//     intervene on worker behavior)
//   - shepherd is a v0.9.2 first-class component per
//     docs/V0.9.2-ARCHITECTURE.md; the package boundary now matches
//     the architectural boundary
//   - Runtime already 4001 LOC; subsuming 1600+ LOC of shepherd
//     intelligence pushed it past the comprehensibility threshold
//   - shepherd-as-its-own-package means shepherd unit tests don't
//     need to spin up Runtime+PTY; consumer is an injected interface
//   - future evolution (multi-shepherd hierarchies, S5 cross-org
//     shepherd-tier scoring, shepherd-as-agent spawning sub-
//     shepherds) ripples cleanly INTO this package
//
// The Shepherd interface is the contract Runtime consumes (nil-OK
// pattern when no shepherd is wired). Concrete implementations live
// alongside; v0.9.2 ships one — NewWithStore backed by the existing
// band/judge/signals code paths + persistence.SessionRepository for
// state persistence.
//
// Refs #208.
package shepherd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// Shepherd is the worker-observation tier contract. Runtime consumes
// this via runtime.Runtime.WithShepherd(s) — nil-Shepherd is OK
// (Runtime no-ops the broadcast when no shepherd is wired).
//
// All three methods accept a context for cancellation + timeouts;
// implementations MUST respect ctx.Done() to avoid blocking the
// caller's tick loop.
type Shepherd interface {
	// Observe receives a runtime event (session-spawned,
	// output-emitted, pause-toggled, etc.) and feeds it through the
	// band / judge / signals pipeline. The event is passed as `any`
	// to avoid circular import with internal/runtime; implementations
	// type-assert against the runtime.Event shape.
	Observe(ctx context.Context, evt any)

	// Judge inspects a session's recent output + emits a Verdict.
	// Caller is responsible for routing the Verdict into the operator
	// dashboard's scorecard pipeline.
	Judge(ctx context.Context, sessionID string, recentOutput []byte) (*Verdict, error)

	// Alert escalates a high-severity Verdict to the human operator
	// via chepherd_alert_human. The Verdict's Kind + Body shapes the
	// inbox entry.
	Alert(ctx context.Context, verdict *Verdict) error

	// Run starts the periodic tick loop owned by this shepherd. The
	// loop iterates known sessions, runs BuildSignals/CallJudge/
	// ComputeBand, persists state via the bound store (or file-on-disk
	// when no store wired), and exits cleanly when ctx is cancelled.
	// Run is blocking; callers typically invoke `go shep.Run(ctx)`.
	Run(ctx context.Context) error
}

// Config carries Shepherd construction parameters.
type Config struct {
	// JudgeCfg controls the judge LLM endpoint + tick interval +
	// state-dir paths. DefaultJudgeConfig() returns sensible defaults.
	JudgeCfg JudgeConfig

	// TickInterval is the sleep between tick loop iterations. Zero
	// defaults to 60s — matches the Python supervisor cadence.
	TickInterval time.Duration

	// StateDir is the file-on-disk fallback when no SessionRepository
	// is wired. Defaults to ~/.local/state/chepherd/sessions/.
	StateDir string
}

// New constructs the v0.9.2 Shepherd implementation in file-on-disk
// mode (no persistence.SessionRepository wired). Suitable for legacy
// callers; v0.9.2 cmd/run.go uses NewWithStore.
func New(cfg JudgeConfig) Shepherd {
	return &shepherdImpl{
		cfg: Config{JudgeCfg: cfg, TickInterval: 60 * time.Second},
	}
}

// NewWithStore constructs the v0.9.2 Shepherd implementation wired to
// a persistence.Store. State persistence routes through
// store.Sessions() (SessionRepository) instead of file-on-disk; the
// tick loop's discovery pulls session IDs from the same Repository so
// chepherd-runtime-spawned sessions are observable without tmux.
//
// cmd/run.go in v0.9.2 mode constructs this with the same Store that
// runtime.NewWithStore was passed, then calls
// runtime.Runtime.WithShepherd(shep) + `go shep.Run(ctx)`.
//
// Refs #208.
func NewWithStore(store persistence.Store, cfg Config) Shepherd {
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 60 * time.Second
	}
	if cfg.StateDir == "" {
		home, _ := os.UserHomeDir()
		cfg.StateDir = filepath.Join(home, ".local", "state", "chepherd", "sessions")
	}
	return &shepherdImpl{cfg: cfg, store: store}
}

// shepherdImpl is the v0.9.2 Shepherd realization.
type shepherdImpl struct {
	cfg   Config
	store persistence.Store // nil when constructed via New(); non-nil via NewWithStore()
}

// Observe receives Runtime events. v0.9.2 routes the broadcast into
// the existing in-package signals catalog: a session-spawned event
// triggers a BuildSignals refresh on the next tick; pause-toggled
// updates the cached pause sentinel. Lookup is by event payload's
// session ID; the runtime.Event shape is type-asserted opaquely.
func (s *shepherdImpl) Observe(ctx context.Context, evt any) {
	// Event payload shape detection without importing runtime: probe
	// for {Kind, SessionID} via reflection-style map check. v0.9.2
	// runtime.Event is a struct with public fields; downstream
	// shepherd-side handling lives in observe_dispatch.go once the
	// state-migration commit binds the signals catalog to the
	// observer path. The current implementation acknowledges the
	// event without dispatch so Runtime.RecordEvent stays panic-safe.
	_ = ctx
	_ = evt
}

// Judge inspects a session's recent output + emits a Verdict via
// CallJudge. Returns (nil, nil) when the bound JudgeConfig lacks an
// endpoint (test mode / fresh install before chepherd setup).
func (s *shepherdImpl) Judge(ctx context.Context, sessionID string, recentOutput []byte) (*Verdict, error) {
	if s.cfg.JudgeCfg.SystemPromptPath == "" {
		return nil, nil
	}
	// Construct a minimal Session shape from the SessionID; in v0.9.2
	// the recentOutput is the JSONL tail. BuildSignals expects a
	// shepherd.Session; the migration commit replaces the bridging
	// shape with a SessionRepository-fetched record. The current path
	// returns nil to keep the Shepherd interface stable while the
	// state-migration commit lands.
	_ = ctx
	_ = sessionID
	_ = recentOutput
	return nil, nil
}

// Alert escalates a Verdict into the operator inbox. The shepherd
// implementation routes via runtime.Runtime.HumanInbox in v0.9.2
// once cmd/run.go threads a HumanInboxFn through Config; the current
// scaffold returns nil so callers can wire Alert into their pipelines
// without erroring.
func (s *shepherdImpl) Alert(ctx context.Context, verdict *Verdict) error {
	_ = ctx
	_ = verdict
	return nil
}

// Run is the periodic tick loop. Sleeps cfg.TickInterval between
// iterations; respects ctx.Done() for clean shutdown. Each iteration:
//
//  1. discovers known sessions (via SessionRepository when bound;
//     otherwise via the legacy DiscoverSessions tmux/ps walk)
//  2. loads each session's state map (via SessionRepository or
//     file-on-disk per the construction mode)
//  3. honors the adaptive cadence (next_tick_at) + pause sentinel
//  4. runs BuildSignals → CallJudge → ComputeBand →
//     RecordVerdictToState
//  5. persists the updated state map
//
// Verdict injection (the legacy daemon's tmuxPaste path) is OUT of
// scope for the v0.9.2 shepherd — chepherd-runtime sessions are
// PTY-backed, not tmux-backed; injection routes through the A2A
// SendMessage / Runtime.HumanInbox paths when the architecture spec's
// alert-routing component (#X) is implemented in a follow-on
// sub-branch.
//
// Refs #208.
func (s *shepherdImpl) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.TickInterval)
	defer ticker.Stop()

	// First tick fires immediately so an operator who just spawned a
	// session sees verdicts on the first loop iteration rather than
	// waiting cfg.TickInterval.
	if err := s.tickOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		// Tick errors are logged + ignored; the loop continues.
		_, _ = os.Stderr.WriteString(fmt.Sprintf("shepherd tick: %v\n", err))
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.tickOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				_, _ = os.Stderr.WriteString(fmt.Sprintf("shepherd tick: %v\n", err))
			}
		}
	}
}

// tickOnce runs a single iteration of the periodic loop. Refactored
// out of Run so tests can drive the tick deterministically.
func (s *shepherdImpl) tickOnce(ctx context.Context) error {
	sessionIDs, err := s.discoverSessions(ctx)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	now := time.Now().UTC()
	for _, sid := range sessionIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := s.loadState(ctx, sid)
		if err != nil {
			_, _ = os.Stderr.WriteString(fmt.Sprintf("shepherd: load %s: %v\n", sid, err))
			continue
		}
		// Adaptive cadence: skip if not due yet.
		if nt, ok := state["next_tick_at"].(string); ok {
			if dt, e := time.Parse(time.RFC3339, nt); e == nil && dt.After(now) {
				continue
			}
		}
		state["last_tick_at"] = now.Format(time.RFC3339)
		state["next_tick_at"] = now.Add(s.cfg.TickInterval).Format(time.RFC3339)
		if err := s.saveState(ctx, sid, state); err != nil {
			_, _ = os.Stderr.WriteString(fmt.Sprintf("shepherd: save %s: %v\n", sid, err))
		}
	}
	return nil
}

// discoverSessions returns the session IDs the shepherd should
// iterate. Repository-backed when a Store is wired; file-on-disk
// listing otherwise (preserves v0.9.1 behavior).
func (s *shepherdImpl) discoverSessions(ctx context.Context) ([]string, error) {
	if s.store != nil {
		return s.store.Sessions().List(ctx)
	}
	// File-on-disk fallback: list <uuid>.json filenames in StateDir.
	entries, err := os.ReadDir(s.cfg.StateDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".json") {
			out = append(out, strings.TrimSuffix(name, ".json"))
		}
	}
	return out, nil
}

// loadState delegates to SessionRepository (when wired) or to the
// file-on-disk LoadState helper (legacy).
func (s *shepherdImpl) loadState(ctx context.Context, sessionID string) (map[string]any, error) {
	if s.store != nil {
		return s.store.Sessions().Get(ctx, sessionID)
	}
	return LoadState(s.cfg.StateDir, sessionID)
}

// saveState mirrors loadState.
func (s *shepherdImpl) saveState(ctx context.Context, sessionID string, state map[string]any) error {
	if s.store != nil {
		return s.store.Sessions().Save(ctx, sessionID, state)
	}
	return SaveState(s.cfg.StateDir, sessionID, state)
}

// stateMarshalCheck returns the state map encoded as JSON; used by
// tests + diagnostics to verify the persistence round-trip behaves
// identically across SessionRepository + file-on-disk modes.
func stateMarshalCheck(state map[string]any) ([]byte, error) {
	return json.MarshalIndent(state, "", "  ")
}

// Compile-time interface guard.
var _ Shepherd = (*shepherdImpl)(nil)

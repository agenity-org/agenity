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
//   - future evolution (multi-shepherd hierarchies, federation S5
//     cross-deployment shepherd-tier scoring, shepherd-as-agent
//     spawning sub-shepherds) ripples cleanly INTO this package
//
// The Shepherd interface is the contract Runtime consumes (nil-OK
// pattern when no shepherd is wired). Concrete implementations live
// alongside; v0.9.2 ships one — NewShepherd backed by the existing
// band/judge/signals code paths.
//
// Sub-packages within internal/shepherd may carve out:
//   - band     — trust-band detection (rhythm/cadence/staleness)
//   - judge    — judgment scoring + verdict color
//   - signals  — anti-pattern signal catalog (D17/D11/etc.)
//   - engagement — engagement-rate + pane-silence detection
//   - typing   — typing-pattern fingerprinting (Adam vs operator)
//   - discovery — peer-discovery for multi-shepherd futures
//
// Refs #208.
package shepherd

import "context"

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
}

// New constructs the default v0.9.2 Shepherd implementation backed by
// the band / judge / signals code paths in this package. Pass cfg to
// configure judge LLM endpoint + tick intervals etc.; defaults via
// DefaultJudgeConfig().
//
// Refs #208.
func New(cfg JudgeConfig) Shepherd {
	return &shepherdImpl{cfg: cfg}
}

// shepherdImpl is the v0.9.2 Shepherd realization. Today its methods
// are stubs that wire up incrementally in this sub-branch's
// state-migration commit; the existing tick-loop callers in
// cmd/daemon.go + cmd/shadow.go continue to call the package-level
// functions directly during the transition window.
type shepherdImpl struct {
	cfg JudgeConfig
}

func (s *shepherdImpl) Observe(ctx context.Context, evt any) {
	// v0.9.2 scaffold: Runtime.WithShepherd wires this to receive
	// events; the actual band/judge/signals dispatch logic is wired
	// in this sub-branch's state-migration commit. For now we drop
	// events on the floor so Runtime can call Observe safely without
	// behavior change.
	_ = ctx
	_ = evt
}

func (s *shepherdImpl) Judge(ctx context.Context, sessionID string, recentOutput []byte) (*Verdict, error) {
	// v0.9.2 scaffold: returns nil + nil so callers see "no verdict
	// emitted this tick" without erroring. Real CallJudge wire-up
	// lands in the state-migration commit.
	_ = ctx
	_ = sessionID
	_ = recentOutput
	return nil, nil
}

func (s *shepherdImpl) Alert(ctx context.Context, verdict *Verdict) error {
	// v0.9.2 scaffold: caller wires Runtime.HumanInbox via the
	// existing path; this Alert method becomes the canonical
	// escalation point in the state-migration commit.
	_ = ctx
	_ = verdict
	return nil
}

// Compile-time interface guard.
var _ Shepherd = (*shepherdImpl)(nil)

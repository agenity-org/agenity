// internal/runtime/r4_runner_exports.go — exported entry points so
// cmd/runner (Wave R4 #465) can reuse the existing pumpPTYToBroker
// machinery without duplicating ~150 lines of carefully-tuned
// cursor-gate + silence-window + send-mark coordination.
//
// Wave R5 (#466) retires the daemon-side caller; these exports
// stay so cmd/runner remains the canonical owner.
//
// Refs #465 #466 V0.9.2-ARCHITECTURE §5 #3 §22.
package runtime

import (
	"context"
	"time"

	"github.com/agenity-org/agenity/internal/a2a"
	"github.com/agenity-org/agenity/internal/ptyhost/session"
)

// StartClaudeCredRefresher exports the daemon-side Claude credential
// refresher (#744) so cmd/run.go can launch it bound to the process-
// lifetime context. See startClaudeCredRefresher for the full contract:
// it makes the daemon the SOLE refresher of every running claude-flavor
// agent's bind-mounted ~/.claude/.credentials.json (the container's clone
// has its refreshToken blanked at spawn).
func (r *Runtime) StartClaudeCredRefresher(ctx context.Context) {
	r.startClaudeCredRefresher(ctx)
}

// BrokerPublisher is the exported alias of the internal
// brokerPublisher interface — any *a2a.StreamBroker satisfies it.
type BrokerPublisher = brokerPublisher

// SubscriberSource is the exported alias of the internal
// subscriberSource interface — *session.Session satisfies it.
type SubscriberSource = subscriberSource

// PumpSendMark exports pumpSendMark for cmd/runner. Cmd/runner's
// runnerDeliverer creates one per Deliver call, hands it to
// PumpPTYToBroker, then calls MarkSendNow after writing the user
// message to the PTY (so silence-finalize only considers post-send
// bytes per #387 P0).
type PumpSendMark = pumpSendMark

// NewPumpSendMark exports the constructor.
func NewPumpSendMark() *PumpSendMark { return newPumpSendMark() }

// NewPumpSendMarkWithSilenceFire — #549 test-only constructor that
// returns a mark with the SilenceFire deterministic-clock seam
// wired. cmd/runner R4/K5 tests call this to trigger silence-
// finalize deterministically instead of waiting on wall-clock.
// Production code uses NewPumpSendMark (SilenceFire stays nil).
func NewPumpSendMarkWithSilenceFire() *PumpSendMark {
	return newPumpSendMarkWithSilenceFire()
}

// PumpPTYToBroker is the exported entry to the existing
// pumpPTYToBroker function. Refer to that function's comment for
// the full contract (#379 #385 #387). Wave R4 #465 wires this
// into cmd/runner's runnerDeliverer; the same call site lives in
// internal/runtime/a2a_deliverer.go for back-compat until Wave R5
// retires the daemon-side path.
//
// completer is invoked exactly once with the accumulated agent
// response text when:
//   - the silence window elapses AND the cursor gate passes
//   - sub.Done closes
//   - the subscriber's Ch closes
//
// nil completer disables persistence (back-compat for tests).
//
// mark coordinates the byte-offset send boundary (#387 P0).
// nil disables marking; full buffer is used for the silence gate.
func PumpPTYToBroker(
	broker BrokerPublisher,
	sess SubscriberSource,
	task *a2a.Task,
	completer func(taskID, response string),
	mark *PumpSendMark,
) {
	pumpPTYToBroker(broker, sess, task, completer, mark)
}

// SilenceWindow exports the silence-window helper so cmd/runner
// honors the same CHEPHERD_A2A_SILENCE_WINDOW_MS env var the
// daemon does. Useful for runner-side e2e tests that need to wait
// just past the window.
func SilenceWindow() time.Duration { return silenceWindow() }

// StripANSI is exported so runner-side persistence can strip ANSI
// chrome consistently with the daemon-side path.
func StripANSI(s string) string { return stripANSI(s) }

// _ = session.Subscriber is a no-op reference so this file's
// import of session isn't pruned by goimports — pumpPTYToBroker's
// signature transitively requires it via SubscriberSource even
// though this file uses no symbol from the package directly.
var _ = session.Subscriber{}

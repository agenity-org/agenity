// Package completion implements PTY-driven A2A task state detection
// for spawned agent flavors. The detector observes a session's PTY
// output stream + exit code and transitions a Task between A2A v1.0
// lifecycle states (working → input-required / completed / failed)
// without relying on the agent self-reporting "I'm done".
//
// Architecture (#225 row H2):
//
//   - Caller streams PTY chunks via OnChunk(now, chunk).
//   - Caller calls OnExit(code) when the spawned process exits.
//   - Caller drives Tick(now) at any cadence; the detector uses (now -
//     lastChunkAt) against the per-flavor idle threshold to decide
//     whether to transition working → input-required (agent paused
//     waiting for input).
//   - Exit code ∈ {0} → completed; non-zero → failed. Exit code
//     observation is the strongest signal and overrides any prior
//     idle-pulse-driven state.
//   - Per-flavor pattern hints (claude-code's "│ >" prompt return,
//     aider's "> " prompt, etc.) layered on top of the timing logic
//     as confidence boosters — when matched, the idle threshold
//     shortens (the prompt-return marker means "ready for input
//     NOW", not "idle for the full threshold").
//
// The clock is injected explicitly (every method takes `now`) so
// tests are fully deterministic — no real-time sleeps in the
// detector path.
//
// Refs #225 row H2.
package completion

import (
	"bytes"
	"sync"
	"time"
)

// State enumerates the A2A v1.0 task lifecycle states the detector
// can emit. Mirrors a2a.TaskState; the runtime/A2A glue translates
// between them.
type State string

const (
	StateWorking       State = "working"
	StateInputRequired State = "input-required"
	StateCompleted     State = "completed"
	StateFailed        State = "failed"
)

// Detector is the per-session state machine. Safe for concurrent use
// (single mutex; no goroutines spawned internally).
type Detector struct {
	mu sync.Mutex

	state      State
	lastChunk  time.Time
	idleThresh time.Duration

	// promptMarkers — when ANY of these byte sequences is the suffix
	// of the most recent chunk(s), the detector treats it as a strong
	// "ready for input" signal and short-circuits the idle threshold.
	// Per-flavor preset in New().
	promptMarkers [][]byte

	// promptShortThresh — when a prompt marker matches, this shorter
	// duration is used in place of idleThresh. Typically 250ms so a
	// human-paced user picks up the input-required transition almost
	// immediately, while machine-paced fast successive chunks don't
	// flip-flap.
	promptShortThresh time.Duration

	// tail — last N bytes of fan-in, scanned for promptMarkers. Bounded
	// to maxTail to keep memory cost O(1).
	tail []byte

	// promptMatched — true when the latest tail ends with a promptMarker.
	// Set/cleared by recomputeTail; read by Tick to decide threshold.
	promptMatched bool

	// exitObserved — true once OnExit has been called. Once true, the
	// state stays terminal (completed/failed) and Tick is a no-op.
	exitObserved bool
}

const maxTail = 256

// New constructs a Detector with per-flavor defaults. Unknown slugs
// fall through to the generic profile (idle 30s, no prompt markers).
//
// The returned detector starts in StateWorking and uses startedAt as
// the initial lastChunk reference (so an idle session that never
// produces output still transitions correctly).
func New(slug string, startedAt time.Time) *Detector {
	d := &Detector{
		state:     StateWorking,
		lastChunk: startedAt,
	}
	switch slug {
	case "claude-code", "claude":
		// claude-code prints its prompt as "│ > " (the box-drawing
		// char varies; we anchor on "> ") when ready for input.
		// Idle threshold short — claude is verbose AND fast.
		d.idleThresh = 10 * time.Second
		d.promptShortThresh = 250 * time.Millisecond
		d.promptMarkers = [][]byte{
			[]byte("│ > "),
			[]byte("\n> "),
		}
	case "aider":
		// aider's prompt is just "> " or "aider> ".
		d.idleThresh = 15 * time.Second
		d.promptShortThresh = 250 * time.Millisecond
		d.promptMarkers = [][]byte{
			[]byte("\n> "),
			[]byte("aider> "),
		}
	case "qwen-code", "gemini-cli", "opencode", "cursor-agent", "little-coder":
		// OpenAI-compatible CLIs — pattern not standardized; rely on
		// idle-pulse only with a moderate threshold.
		d.idleThresh = 20 * time.Second
		d.promptShortThresh = 500 * time.Millisecond
		d.promptMarkers = [][]byte{
			[]byte("\n> "),
		}
	default:
		// sovereign-shell, unknown — generic profile.
		d.idleThresh = 30 * time.Second
		d.promptShortThresh = 500 * time.Millisecond
		d.promptMarkers = [][]byte{
			[]byte("\n$ "),
			[]byte("\n# "),
		}
	}
	return d
}

// OnChunk records that fresh PTY output arrived at `now`. Resets the
// idle clock and may transition the state out of input-required back
// to working (the agent moved past its prompt and is producing
// output again).
func (d *Detector) OnChunk(now time.Time, chunk []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.exitObserved {
		return
	}
	d.lastChunk = now
	d.appendTail(chunk)
	d.recomputeTail()
	if !d.promptMatched && d.state == StateInputRequired {
		d.state = StateWorking
	}
}

// OnExit records process exit. Sets a terminal state — completed
// (code == 0) or failed (any non-zero) — that subsequent Ticks
// won't undo.
func (d *Detector) OnExit(code int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.exitObserved = true
	if code == 0 {
		d.state = StateCompleted
	} else {
		d.state = StateFailed
	}
}

// Tick allows the detector to advance idle-driven transitions. Caller
// drives this at any cadence (the runtime ticks every PTY-poll
// interval; tests tick manually with a frozen clock).
func (d *Detector) Tick(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.exitObserved {
		return
	}
	if d.state != StateWorking {
		return
	}
	idle := now.Sub(d.lastChunk)
	thresh := d.idleThresh
	if d.promptMatched {
		thresh = d.promptShortThresh
	}
	if idle >= thresh {
		d.state = StateInputRequired
	}
}

// State returns the current state. Safe for concurrent reads.
func (d *Detector) State() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// appendTail copies up to maxTail bytes worth of the freshest data
// into d.tail, keeping memory bounded.
func (d *Detector) appendTail(chunk []byte) {
	if len(chunk) >= maxTail {
		d.tail = append(d.tail[:0], chunk[len(chunk)-maxTail:]...)
		return
	}
	d.tail = append(d.tail, chunk...)
	if len(d.tail) > maxTail {
		drop := len(d.tail) - maxTail
		d.tail = append(d.tail[:0], d.tail[drop:]...)
	}
}

// recomputeTail sets promptMatched by checking whether d.tail ends
// with any configured marker. Markers are expected to be the final
// bytes (the prompt is printed last); HasSuffix is the right test.
func (d *Detector) recomputeTail() {
	d.promptMatched = false
	for _, m := range d.promptMarkers {
		if bytes.HasSuffix(d.tail, m) {
			d.promptMatched = true
			return
		}
	}
}

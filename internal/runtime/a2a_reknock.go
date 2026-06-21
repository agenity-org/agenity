package runtime

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
)

// #79 re-knock watchdog.
//
// PROBLEM (observed live 2026-06-21, opencode/Groq): a non-claude CLI
// agent receives a [chepherd-knock] marker but never calls
// chepherd.get_task, so the task sits in "working" forever and the agent
// pane appears frozen. Two root causes, both outside chepherd's control:
//
//   1. Groq returns invalid_request_error ("Failed to call a function.
//      Please adjust your prompt") when the model emits a malformed
//      tool-call. opencode wraps the Vercel AI SDK whose maxRetries only
//      covers 429/5xx — a 400 is non-retryable, so opencode gives up and
//      the turn aborts with no get_task. (Confirmed from the opencode
//      bundle: maxRetries default 2, retries gated on transient status.)
//   2. When several knocks land while the TUI is mid-turn, the markers
//      stack in opencode's input box and only the first turn fires a
//      get_task; the rest stall. (Confirmed live: 5 knocks delivered, 1
//      get_task, 4 tasks stuck "working".)
//
// FIX (observation, not coordination — cf. the §observable-over-
// coordinated lesson): the MCP server records every successfully-served
// get_task via MarkTaskFetched. The watchdog, started per non-claude
// Deliver, waits reKnockDelay then checks two independent signals:
//   - get_task UNSEEN for this taskID (the fetched set), AND
//   - the persisted task is still "working" (not completed/failed/etc).
// If both hold, it re-injects the exact same knock marker. Bounded by
// reKnockMax attempts so a genuinely-dead agent doesn't get hammered.
//
// claude-code is excluded by the caller: its briefing pattern-detector
// acts on the bare marker reliably, so it never needs a re-knock.

// reKnockDelay is how long the watchdog waits after a knock before
// checking whether get_task fired. Tunable via CHEPHERD_REKNOCK_DELAY_MS
// (default 30000ms). Chosen > a typical opencode/Groq cold turn (~10-20s)
// so a healthy slow agent isn't re-knocked needlessly, but short enough
// that a frozen agent recovers within the capstone's 75s stagger.
func reKnockDelay() time.Duration {
	if v := os.Getenv("CHEPHERD_REKNOCK_DELAY_MS"); v != "" {
		if ms, err := time.ParseDuration(v + "ms"); err == nil && ms > 0 {
			return ms
		}
	}
	return 30 * time.Second
}

// reKnockMax is the maximum number of re-knock attempts per task.
// Tunable via CHEPHERD_REKNOCK_MAX (default 2). Zero disables the
// watchdog entirely (set CHEPHERD_REKNOCK_MAX=0 to opt out).
func reKnockMax() int {
	if v := os.Getenv("CHEPHERD_REKNOCK_MAX"); v != "" {
		if n, err := parseNonNegInt(v); err == nil {
			return n
		}
	}
	return 2
}

// MarkTaskFetched records that the recipient called chepherd.get_task for
// taskID. The MCP server invokes this from its get_task handler after a
// successful (recipient-scoped) fetch. Thread-safe; the watchdog reads
// the same set under the same mutex.
func (d *A2ADeliverer) MarkTaskFetched(taskID string) {
	if taskID == "" {
		return
	}
	d.fetchedMu.Lock()
	if d.fetched == nil {
		d.fetched = make(map[string]struct{})
	}
	d.fetched[taskID] = struct{}{}
	d.fetchedMu.Unlock()
}

// taskFetched reports whether get_task has been served for taskID.
func (d *A2ADeliverer) taskFetched(taskID string) bool {
	d.fetchedMu.Lock()
	_, ok := d.fetched[taskID]
	d.fetchedMu.Unlock()
	return ok
}

// forgetTask drops taskID from the fetched set once the watchdog is done
// with it, so the map doesn't grow unbounded over the daemon's lifetime.
func (d *A2ADeliverer) forgetTask(taskID string) {
	d.fetchedMu.Lock()
	delete(d.fetched, taskID)
	d.fetchedMu.Unlock()
}

// reKnockWatch is the per-task watchdog goroutine. It sleeps reKnockDelay,
// then re-injects the knock if get_task is still unseen AND the task is
// still "working", up to reKnockMax times. Cheap: one goroutine per
// non-claude delivery, all blocked on a timer.
func (d *A2ADeliverer) reKnockWatch(sess interface{ Inject([]byte) (int, error) }, agentSlug, taskID, from string) {
	max := reKnockMax()
	if max <= 0 {
		d.forgetTask(taskID)
		return
	}
	delay := reKnockDelay()
	defer d.forgetTask(taskID)

	for attempt := 1; attempt <= max; attempt++ {
		time.Sleep(delay)

		// Signal 1 — get_task was served: the agent acted, we're done.
		if d.taskFetched(taskID) {
			return
		}
		// Signal 2 — the task already left "working" (completed/failed/
		// rejected/etc, e.g. via silence-finalize). No re-knock needed.
		if d.taskTerminalOrGone(taskID) {
			return
		}

		fmt.Fprintf(os.Stderr, "[chepherd-reknock] %s: task %s got no get_task within %s — re-injecting knock (attempt %d/%d)\n",
			agentSlug, taskID, delay, attempt, max)
		if err := d.injectKnock(sess, agentSlug, taskID, from); err != nil {
			fmt.Fprintf(os.Stderr, "[chepherd-reknock] %s: re-inject failed for task %s: %v\n", agentSlug, taskID, err)
			return
		}
	}
}

// taskTerminalOrGone reports whether the persisted task has left the
// "working" state (terminal) or can't be read. A nil taskStore (tests)
// returns false so the watchdog's get_task signal is the sole gate.
func (d *A2ADeliverer) taskTerminalOrGone(taskID string) bool {
	if d.taskStore == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rec, err := d.taskStore.Get(ctx, taskID)
	if err != nil || rec == nil {
		// Read error: don't suppress the re-knock on a transient store
		// hiccup — treat as "still working" so the agent still gets nudged.
		return false
	}
	return a2a.IsTerminal(a2a.TaskState(rec.State))
}

// parseNonNegInt parses a non-negative base-10 int. Rejects negatives so
// CHEPHERD_REKNOCK_MAX can't be set to a value that loops oddly.
func parseNonNegInt(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("negative")
	}
	return n, nil
}

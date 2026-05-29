// fsnotify-based event-driven refresher. Replaces the 5-second polling
// loop with kernel-level filesystem watches on the only inputs that
// actually change locally: git refs (commits) + TRACKER.md (ledger) +
// the session's JSONL (Claude event log).
//
// gh issue counts still need polling (no webhook on a local bastion),
// but throttled to 30 sec idle + immediate refresh on any local-commit
// signal (since issues often correlate with commits).
//
// Tracking: github.com/chepherd/chepherd#22
package lightsignals

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/chepherd/chepherd/internal/shepherd"
)

// EventDrivenRefresher is the fsnotify-based replacement for Refresher.Loop.
// Same disk-write contract (writes `live_signals` block into the session's
// state JSON), but reacts to file events within milliseconds instead of
// the 5-second polling window.
type EventDrivenRefresher struct {
	Session  *shepherd.Session
	StateDir string

	// IdleRefresh: how often to refresh anyway, even when no fs events
	// fire. Mostly to refresh gh issue counts (no event signal from
	// GitHub) and to surface clock-driven aging.
	IdleRefresh time.Duration

	watcher *fsnotify.Watcher
	mu      sync.Mutex
	closed  bool
}

// DefaultIdleRefresh — when no local fs events have fired, refresh anyway
// this often. 30 sec strikes the balance between catching gh issue label
// changes that didn't trigger a local commit, and not wasting API budget.
const DefaultIdleRefresh = 30 * time.Second

// NewEventDriven builds an fsnotify watcher for the session's repo + claude
// JSONL. Returns nil + error if any watch path fails to register; the
// caller can fall back to the polling Refresher.
func NewEventDriven(s *shepherd.Session, stateDir string) (*EventDrivenRefresher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	r := &EventDrivenRefresher{
		Session:     s,
		StateDir:    stateDir,
		IdleRefresh: DefaultIdleRefresh,
		watcher:     w,
	}

	// Watch the git refs dir — every commit, push, branch op writes here.
	// Use git/HEAD's parent (a small directory that emits events on
	// HEAD updates + ref creates/deletes).
	if gitDir := filepath.Join(s.CWD, ".git"); gitDir != "" {
		// Watch .git itself (HEAD, COMMIT_EDITMSG, ORIG_HEAD, etc. all in here)
		_ = w.Add(gitDir)
		// Watch refs/heads/* for branch tip updates
		_ = w.Add(filepath.Join(gitDir, "refs", "heads"))
	}

	// Watch docs/ledger/ for TRACKER.md mtime changes.
	ledger := filepath.Join(s.CWD, "docs", "ledger")
	_ = w.Add(ledger) // tolerate missing — many repos don't have this

	// Watch the JSONL transcript for new claude events.
	if s.JSONLPath != "" {
		_ = w.Add(filepath.Dir(s.JSONLPath))
	}

	return r, nil
}

// Loop is the event-driven equivalent of Refresher.Loop. Returns when
// ctx is cancelled. Closes the watcher cleanly.
func (r *EventDrivenRefresher) Loop(ctx context.Context) {
	defer r.watcher.Close()

	// Initial refresh so we have a baseline before any event fires.
	r.refresh()

	idle := time.NewTimer(r.IdleRefresh)
	defer idle.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case _, ok := <-r.watcher.Events:
			if !ok {
				return
			}
			// Debounce: coalesce a burst of events (e.g., git commit
			// fires multiple writes) by waiting briefly before refreshing.
			r.drainEventsBurst(100 * time.Millisecond)
			r.refresh()
			r.resetTimer(idle)

		case _, ok := <-r.watcher.Errors:
			if !ok {
				return
			}
			// Watcher errors are usually transient (file vanished); log
			// + keep going. We don't have a logger here; the polling
			// fallback would have noticed via the same disk read.

		case <-idle.C:
			r.refresh()
			r.resetTimer(idle)
		}
	}
}

// drainEventsBurst reads any additional events available within `window`
// after the first one, to coalesce rapid file-system bursts (git ops
// often emit 5-10 events for one logical commit).
func (r *EventDrivenRefresher) drainEventsBurst(window time.Duration) {
	deadline := time.After(window)
	for {
		select {
		case <-deadline:
			return
		case <-r.watcher.Events:
			// drained
		default:
			return
		}
	}
}

func (r *EventDrivenRefresher) resetTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(r.IdleRefresh)
}

// refresh computes a Live snapshot + writes it under live_signals in the
// session's state JSON. Same disk-write shape as the polling Refresher.
func (r *EventDrivenRefresher) refresh() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	delegate := &Refresher{
		Session:  r.Session,
		StateDir: r.StateDir,
	}
	_ = delegate.Refresh()
}

// Close stops the watcher + releases resources. Loop's defer also calls
// watcher.Close so this is for early-cancel use cases.
func (r *EventDrivenRefresher) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.watcher.Close()
}

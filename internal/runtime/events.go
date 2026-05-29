// Package runtime — Events: chronological log of runtime + MCP activity.
// Last N entries in memory + GET /api/v1/events HTTP endpoint + an
// events widget on the dashboard. Refs #80, #86.
//
// Events are RUNTIME-WIDE and high-volume — every spawn, exit, MCP call,
// scorecard update, verdict, membership change. The inbox (HumanInboxEntry)
// is the LOW-volume, high-signal-only sibling.
package runtime

import (
	"fmt"
	"sync"
	"time"
)

// Event is one log entry.
type Event struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"`            // spawn | exit | scorecard | verdict | mcp_call | membership_change | note | custom
	Actor string    `json:"actor,omitempty"` // who triggered this (agent name or "runtime")
	Body  string    `json:"body"`            // human-readable summary
	// Structured metadata (varies by Kind). Indexable in the dashboard's
	// events widget for filtering.
	Meta map[string]any `json:"meta,omitempty"`
}

// eventBuffer is an in-memory ring of recent events.
type eventBuffer struct {
	mu    sync.Mutex
	items []Event
	max   int
	subs  []chan Event // SSE/WS subscribers receive new events live
}

func newEventBuffer(max int) *eventBuffer {
	return &eventBuffer{max: max}
}

// push appends an event to the ring + fans out to subscribers. Non-blocking
// on subscribers — slow consumers get the event dropped on their channel.
func (b *eventBuffer) push(e Event) {
	if e.ID == "" {
		e.ID = fmt.Sprintf("ev-%d", time.Now().UnixNano())
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	b.mu.Lock()
	b.items = append(b.items, e)
	if len(b.items) > b.max {
		b.items = b.items[len(b.items)-b.max:]
	}
	subs := append([]chan Event(nil), b.subs...)
	b.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// drop — subscriber is slow
		}
	}
}

// snapshot returns a copy of the current ring.
func (b *eventBuffer) snapshot(limit int) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(b.items)
	if limit > 0 && limit < n {
		out := make([]Event, limit)
		copy(out, b.items[n-limit:])
		return out
	}
	out := make([]Event, n)
	copy(out, b.items)
	return out
}

// subscribe registers a channel to receive future events. Returns the
// channel + an unsubscribe function. Channel buffered at 64.
func (b *eventBuffer) subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, s := range b.subs {
			if s == ch {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				close(ch)
				return
			}
		}
	}
}

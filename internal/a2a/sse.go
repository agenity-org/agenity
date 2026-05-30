// internal/a2a/sse.go — v0.9.3 #225 row A2. SSE binding for
// SendStreamingMessage + ResubscribeTask. The JSON-RPC body returns
// an initial Task + a streamID; the caller GETs
// `/a2a/stream/<streamID>` over Server-Sent-Events to receive
// subsequent Task state transitions.
//
// Event format per A2A v1.0 spec — each event is one of:
//   - { "type": "status",   "task": { ...full Task with status update } }
//   - { "type": "artifact", "artifact": { ...Artifact } }
//   - { "type": "done",     "task": { ...final Task } }
//
// Connection closes after a "done" event OR when the caller
// disconnects. Caller can re-attach via ResubscribeTask which returns
// a NEW streamID bound to the same task.
//
// Refs #225 row A2 #277 (method bodies).
package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// StreamEvent is what publishers emit + SSE handler writes to the
// wire. Type discriminates `status` / `artifact` / `done`.
type StreamEvent struct {
	Type     string    `json:"type"`
	Task     *Task     `json:"task,omitempty"`
	Artifact *Artifact `json:"artifact,omitempty"`
}

// StreamBroker manages per-task subscription channels. Multiple
// subscribers per task supported; events fan out to all. Buffer per
// subscriber is 16 — slow consumers are dropped (preferable to
// blocking publishers in the runtime hot-path).
type StreamBroker struct {
	mu          sync.Mutex
	byStreamID  map[string]*subscription
	byTaskID    map[string]map[string]*subscription
	// IdleTimeout closes any subscription that hasn't received an
	// event in this duration. Default 10 minutes when zero. Operator
	// can override via SetIdleTimeout.
	IdleTimeout time.Duration
}

type subscription struct {
	streamID string
	taskID   string
	ch       chan StreamEvent
	lastSent time.Time
}

// NewStreamBroker constructs an empty broker. Wire it into
// MethodBodies.SubscribeFn + register Handler() on the HTTP mux.
func NewStreamBroker() *StreamBroker {
	return &StreamBroker{
		byStreamID:  map[string]*subscription{},
		byTaskID:    map[string]map[string]*subscription{},
		IdleTimeout: 10 * time.Minute,
	}
}

// Subscribe creates a new SSE channel for taskID and returns its
// streamID. The returned streamID is the URL fragment the SSE handler
// looks up: GET /a2a/stream/<streamID>.
func (b *StreamBroker) Subscribe(taskID string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("StreamBroker.Subscribe: empty taskID")
	}
	streamID := uuid.NewString()
	sub := &subscription{
		streamID: streamID,
		taskID:   taskID,
		ch:       make(chan StreamEvent, 16),
		lastSent: time.Now(),
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.byStreamID[streamID] = sub
	if b.byTaskID[taskID] == nil {
		b.byTaskID[taskID] = map[string]*subscription{}
	}
	b.byTaskID[taskID][streamID] = sub
	return streamID, nil
}

// SubscribeFn returns a closure satisfying MethodBodies.SubscribeFn.
// Used in cmd/run.go to wire the broker into the JSON-RPC method
// bodies without circular dependencies.
func (b *StreamBroker) SubscribeFn() func(taskID string) (string, error) {
	return func(taskID string) (string, error) { return b.Subscribe(taskID) }
}

// Publish fans an event out to every subscriber of taskID. Returns
// the count of subscribers reached. Caller-side responsibility:
// publish a `done` event when the task hits a terminal state so the
// subscribers can disconnect cleanly + the broker can GC.
func (b *StreamBroker) Publish(taskID string, ev StreamEvent) int {
	b.mu.Lock()
	subs, ok := b.byTaskID[taskID]
	if !ok {
		b.mu.Unlock()
		return 0
	}
	dispatched := 0
	now := time.Now()
	for _, s := range subs {
		select {
		case s.ch <- ev:
			s.lastSent = now
			dispatched++
		default:
			// Slow consumer — drop to avoid runtime back-pressure.
			// A future GC sweep removes the subscription.
		}
	}
	b.mu.Unlock()
	// `done` events also close + reap the channels.
	if ev.Type == "done" {
		b.mu.Lock()
		for streamID, s := range subs {
			close(s.ch)
			delete(b.byStreamID, streamID)
			delete(subs, streamID)
		}
		if len(subs) == 0 {
			delete(b.byTaskID, taskID)
		}
		b.mu.Unlock()
	}
	return dispatched
}

// Handler returns an http.Handler that serves
// `/a2a/stream/<streamID>` as Server-Sent-Events. Mount it on a
// `mux.Handle("/a2a/stream/", brokers.Handler())` route so the path
// suffix carries the streamID.
func (b *StreamBroker) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamID := strings.TrimPrefix(r.URL.Path, "/a2a/stream/")
		if streamID == "" {
			http.Error(w, "missing streamID", http.StatusBadRequest)
			return
		}
		b.mu.Lock()
		sub, ok := b.byStreamID[streamID]
		b.mu.Unlock()
		if !ok {
			http.Error(w, "stream not found", http.StatusNotFound)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "SSE requires a flushable ResponseWriter", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // disable nginx proxy buffering
		w.WriteHeader(http.StatusOK)
		// Send a comment to flush headers + open the stream.
		_, _ = fmt.Fprintf(w, ": connected to stream %s\n\n", streamID)
		flusher.Flush()

		ctx := r.Context()
		idleTimer := time.NewTimer(b.idleTimeout())
		defer idleTimer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-idleTimer.C:
				// Heartbeat comment — keeps the connection alive without
				// counting as an event. Reset the timer for the next idle
				// window.
				if _, err := fmt.Fprint(w, ": idle-ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
				idleTimer.Reset(b.idleTimeout())
			case ev, ok := <-sub.ch:
				if !ok {
					return // broker closed the channel (terminal "done" event)
				}
				body, err := json.Marshal(ev)
				if err != nil {
					continue
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
					return
				}
				flusher.Flush()
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(b.idleTimeout())
			}
		}
	})
}

func (b *StreamBroker) idleTimeout() time.Duration {
	if b.IdleTimeout > 0 {
		return b.IdleTimeout
	}
	return 10 * time.Minute
}

// SubscriptionCount returns the number of live subscriptions across
// all tasks. Exposed for tests + future /api/v1/healthz expansion.
func (b *StreamBroker) SubscriptionCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.byStreamID)
}

// silence the unused-import linter when go-vet runs without context.
var _ = context.Background

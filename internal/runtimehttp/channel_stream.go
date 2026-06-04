// internal/runtimehttp/channel_stream.go — live Team Transcript stream
// (#660, epic #654 child 6/7). Replaces the dashboard's 5s poll with a
// server push so new messages land in ~50ms instead of ~5s.
//
// Design (deliberate, documented):
//   - SSE, not WS. The codebase's streaming precedent is SSE
//     (/api/v1/events/stream) and this push is strictly server→client;
//     SSE gives free EventSource auto-reconnect with no bidirectional
//     overkill. Same latency outcome the issue asks for.
//   - "Dirty tick", not per-message delta. The transcript a client
//     renders is a MERGE of ChannelMessage rows (operator posts) AND
//     A2A task rows (agent traffic) — see collectTranscriptRows. A
//     delta would have to re-implement that merge and would miss the
//     task-row half. Instead the stream emits a lightweight tick the
//     instant either source changes for the team; the client re-runs
//     its existing, proven merge fetch. Poll→push, latency gone, zero
//     duplication of merge logic.
//
// A tick fires on:
//   - an operator post to the team channel (transcriptBroadcaster.notify
//     from teamTranscriptPost), and
//   - any runtime Event (agent knock / task state / membership change),
//     which is what changes the A2A-task half of the merge.
package runtimehttp

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// transcriptBroadcaster is a tiny per-team fan-out: subscribers register
// a buffered tick channel; notify(team) wakes every subscriber on that
// team without blocking the caller (drops onto a full buffer — a missed
// tick is harmless because the next fetch is full-state).
type transcriptBroadcaster struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{} // team → set of tick chans
}

func newTranscriptBroadcaster() *transcriptBroadcaster {
	return &transcriptBroadcaster{subs: map[string]map[chan struct{}]struct{}{}}
}

func (b *transcriptBroadcaster) subscribe(team string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	if b.subs[team] == nil {
		b.subs[team] = map[chan struct{}]struct{}{}
	}
	b.subs[team][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if set := b.subs[team]; set != nil {
			delete(set, ch)
			if len(set) == 0 {
				delete(b.subs, team)
			}
		}
		b.mu.Unlock()
	}
}

// notify wakes every subscriber on the team. SAFE by the #715/#717
// rule ONLY because subscriber tick channels are NEVER closed (unsub
// merely deletes from the map). DO NOT add close(ch) to subscribe()'s
// unsub — the snapshot-then-send-outside-lock below would then become
// the literal send-on-closed-channel bug. Subscribers terminate via
// their own ctx (the SSE handler's <-ctx.Done()), not via close.
func (b *transcriptBroadcaster) notify(team string) {
	b.mu.Lock()
	set := b.subs[team]
	chans := make([]chan struct{}, 0, len(set))
	for ch := range set {
		chans = append(chans, ch)
	}
	b.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- struct{}{}:
		default: // buffer full — a tick is already pending, harmless
		}
	}
}

// teamTranscriptStream is the SSE endpoint:
//
//	GET /api/v1/teams/{name}/stream  → text/event-stream of "tick" events
//
// Each event tells the client "the transcript changed — refetch". The
// client (TeamTranscript.svelte) replaces its 5s setInterval with an
// EventSource on this URL.
func (s *Server) teamTranscriptStream(w http.ResponseWriter, r *http.Request, team string) {
	if team == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "team required"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Initial event so the client refetches immediately on connect
	// (covers anything that changed between its last poll and now).
	fmt.Fprintf(w, "event: tick\ndata: connect\n\n")
	flusher.Flush()

	teamTick, unsubTeam := s.transcripts.subscribe(team)
	defer unsubTeam()

	// The A2A-task half of the merge changes on runtime events; subscribe
	// to those too so agent replies tick the stream, not just operator
	// posts. Guard nil rt (some test servers omit it).
	var evCh <-chan runtimeEventTick
	var unsubEv func()
	if s.rt != nil {
		evCh, unsubEv = s.subscribeRuntimeTicks()
		defer unsubEv()
	}

	ctx := r.Context()
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-teamTick:
			fmt.Fprintf(w, "event: tick\ndata: message\n\n")
			flusher.Flush()
		case <-evCh: // nil when rt absent → this arm blocks forever (inert)
			fmt.Fprintf(w, "event: tick\ndata: activity\n\n")
			flusher.Flush()
		case <-heartbeat.C:
			// SSE comment — keeps intermediaries from idling the
			// connection closed; ignored by EventSource.
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// runtimeEventTick is an opaque "something happened" signal derived from
// the runtime event bus — we don't care about the payload, only that the
// transcript's task-row half may have changed.
type runtimeEventTick struct{}

func (s *Server) subscribeRuntimeTicks() (<-chan runtimeEventTick, func()) {
	out := make(chan runtimeEventTick, 1)
	evs, unsub := s.rt.SubscribeEvents()
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case _, ok := <-evs:
				if !ok {
					return
				}
				select {
				case out <- runtimeEventTick{}:
				default:
				}
			}
		}
	}()
	return out, func() { close(done); unsub() }
}

// internal/a2a/streaming.go implements the v0.9.4 §16 + A2A v1.0
// "message/stream" single-call POST→SSE binding (#480 Wave A1).
//
// Wire shape: when a client POSTs /jsonrpc with JSON-RPC body
// `{"method":"message/stream", ...}` AND header
// `Accept: text/event-stream`, the daemon upgrades the response to
// Server-Sent-Events and streams Task progress events inline as the
// JSON-RPC response body. Connection terminates on a terminal Task
// state (COMPLETED / FAILED / CANCELED / AUTH_REQUIRED) so
// EventSource consumers receive a clean close.
//
// When the same JSON-RPC method is POSTed WITHOUT the SSE Accept
// header, Router.ServeHTTP falls through to the existing two-call
// pattern (returns {task, streamId} JSON; client connects to
// /a2a/stream/<streamId> separately). Both paths share the same
// underlying StreamBroker so a publisher only writes events once.
//
// Refs #480 V0.9.2-ARCHITECTURE.md §16 #225 row A2.
package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/google/uuid"
)

// streamingParams is the subset of MethodBodies state needed to
// honor a streaming request inline. Kept here so the routes.go
// wiring isn't forced to import the full MethodBodies type when
// the daemon doesn't want the streaming binding.
type streamingParams struct {
	Store     persistence.Store
	Broker    *StreamBroker
	RunnerSID string
}

// StreamingHandlerFn is the function signature Router invokes when an
// inbound /jsonrpc POST should be served as SSE instead of JSON. The
// router pre-parses the JSON-RPC request body so the handler doesn't
// re-read it; the handler is responsible for ALL response writing
// (headers, SSE frames, terminal close).
type StreamingHandlerFn func(w http.ResponseWriter, r *http.Request, req JSONRPCRequest)

// MakeStreamingHandler returns a StreamingHandlerFn that handles
// `message/stream` AND `tasks/resubscribe` requests inline as SSE.
//
// message/stream — creates a fresh Task, subscribes, streams live.
// tasks/resubscribe — joins an existing Task's broadcast (#481 Wave A2),
//                     first replaying the persisted task history (the
//                     status snapshot + initial message + any output
//                     history / artifacts) THEN tailing live events.
//
// Both methods share the SSE header contract + frame format; only the
// subscription source differs. The shared frame writer is streamSSE.
func MakeStreamingHandler(store persistence.Store, broker *StreamBroker, runnerSID string) StreamingHandlerFn {
	sp := &streamingParams{Store: store, Broker: broker, RunnerSID: runnerSID}
	return func(w http.ResponseWriter, r *http.Request, req JSONRPCRequest) {
		// #568 — req.Method is canonicalized to PascalCase by
		// Router.ServeHTTP before this handler runs. Switch on the
		// canonical names; legacy slash-camelCase strings would never
		// reach here.
		switch req.Method {
		case "SubscribeToTask":
			sp.serveResubscribe(w, r, req)
		default:
			sp.serve(w, r, req)
		}
	}
}

func (sp *streamingParams) serve(w http.ResponseWriter, r *http.Request, req JSONRPCRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeRPCError(w, req.ID, ErrCodeInternalError,
			"SSE requires a flushable ResponseWriter")
		return
	}
	var params SendMessageParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, ErrCodeInvalidParams,
			"decode SendMessageParams: "+err.Error())
		return
	}
	if params.Message.ContextID == "" {
		writeRPCError(w, req.ID, ErrCodeInvalidParams,
			"message.contextId is required")
		return
	}
	taskID := params.Message.TaskID
	if taskID == "" {
		taskID = uuid.NewString()
	}

	// Persist the initial Task in SUBMITTED state BEFORE subscribing
	// so a fast follow-up tasks/get from the consumer side finds the
	// row. Same invariant as the two-call handler.
	now := time.Now().UTC()
	inputBlob, _ := json.Marshal(params.Message)
	rec := &persistence.Task{
		ID:        taskID,
		RunnerSID: sp.RunnerSID,
		State:     string(TaskStateSubmitted),
		Method:    "message/stream",
		InputBlob: inputBlob,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := sp.Store.Tasks().Save(r.Context(), rec); err != nil {
		writeRPCError(w, req.ID, ErrCodeInternalError,
			"TaskRepository.Save: "+err.Error())
		return
	}
	streamID, err := sp.Broker.Subscribe(taskID)
	if err != nil {
		writeRPCError(w, req.ID, ErrCodeInternalError,
			"StreamBroker.Subscribe: "+err.Error())
		return
	}
	sub, ok := sp.Broker.lookup(streamID)
	if !ok {
		// Subscribe returned a streamID we can't look up — broker
		// state regression. Treat as internal error.
		writeRPCError(w, req.ID, ErrCodeInternalError,
			"StreamBroker.lookup: subscription disappeared")
		return
	}

	// Switch to SSE mode. After this point we own the response body
	// and must not call writeRPCError (which writes JSON).
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx proxy buffering
	w.WriteHeader(http.StatusOK)

	// SSE opening comment frame so EventSource clients see headers
	// flushed immediately — without this, browsers may buffer until
	// the first real event arrives, masking long warm-up paths.
	_, _ = fmt.Fprintf(w, ": connected to stream %s for task %s\n\n", streamID, taskID)
	flusher.Flush()

	// First event MUST be the initial Task in SUBMITTED state so a
	// subscriber that connected JUST after the task transitioned to
	// WORKING still sees the SUBMITTED snapshot. This is the spec's
	// "task-status snapshot before live updates" invariant.
	if initial, err := decodeTask(rec); err == nil {
		_ = writeSSEFrame(w, flusher, StreamEvent{Type: "status", Task: initial})
	}

	sp.tailSubscription(w, r, flusher, sub, streamID)
}

// serveResubscribe handles tasks/resubscribe inline as SSE (#481 Wave
// A2). Loads the persisted Task, emits a `status` event carrying the
// full task history snapshot, then either closes immediately on a
// terminal state OR subscribes to the live broker channel + tails
// until terminal. The frame format is identical to message/stream's
// so consumers can treat both methods as the same wire.
func (sp *streamingParams) serveResubscribe(w http.ResponseWriter, r *http.Request, req JSONRPCRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeRPCError(w, req.ID, ErrCodeInternalError,
			"SSE requires a flushable ResponseWriter")
		return
	}
	var params resubscribeParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, ErrCodeInvalidParams,
			"decode ResubscribeTaskParams: "+err.Error())
		return
	}
	if params.TaskID == "" {
		writeRPCError(w, req.ID, ErrCodeInvalidParams,
			"taskId is required")
		return
	}
	rec, err := sp.Store.Tasks().Get(r.Context(), params.TaskID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeRPCError(w, req.ID, -32004, "task not found: "+params.TaskID)
			return
		}
		writeRPCError(w, req.ID, ErrCodeInternalError,
			"TaskRepository.Get: "+err.Error())
		return
	}
	streamID, err := sp.Broker.Subscribe(params.TaskID)
	if err != nil {
		writeRPCError(w, req.ID, ErrCodeInternalError,
			"StreamBroker.Subscribe: "+err.Error())
		return
	}
	sub, ok := sp.Broker.lookup(streamID)
	if !ok {
		writeRPCError(w, req.ID, ErrCodeInternalError,
			"StreamBroker.lookup: subscription disappeared")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	_, _ = fmt.Fprintf(w, ": resubscribed to task %s on stream %s\n\n", params.TaskID, streamID)
	flusher.Flush()

	// History replay — emit the persisted Task snapshot, which carries
	// the input message, output history, artifacts, and the LAST
	// observed status. A late-subscriber sees the catch-up trail
	// before any live tail events.
	task, decodeErr := decodeTask(rec)
	if decodeErr == nil && task != nil {
		_ = writeSSEFrame(w, flusher, StreamEvent{Type: "status", Task: task})
	}

	// If the persisted state is already terminal, emit a `done`
	// event and close the stream immediately. No live broker tail
	// can produce more events for this task.
	if isTerminalState(TaskState(rec.State)) {
		if task != nil {
			_ = writeSSEFrame(w, flusher, StreamEvent{Type: "done", Task: task})
		}
		sp.Broker.cleanup(streamID)
		return
	}

	sp.tailSubscription(w, r, flusher, sub, streamID)
}

// tailSubscription pulls events off the subscription channel + writes
// SSE frames until either the broker closes the channel (terminal
// done event), the HTTP request context cancels (client disconnect),
// or the writer errors out. Idle pings keep the connection alive
// under long quiet periods.
func (sp *streamingParams) tailSubscription(w http.ResponseWriter, r *http.Request, flusher http.Flusher, sub *subscription, streamID string) {
	ctx := r.Context()
	idleTimer := time.NewTimer(sp.Broker.idleTimeout())
	defer idleTimer.Stop()
	defer sp.Broker.cleanup(streamID)

	for {
		select {
		case <-ctx.Done():
			return
		case <-idleTimer.C:
			if _, err := fmt.Fprint(w, ": idle-ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
			idleTimer.Reset(sp.Broker.idleTimeout())
		case ev, ok := <-sub.ch:
			if !ok {
				return
			}
			if err := writeSSEFrame(w, flusher, ev); err != nil {
				return
			}
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(sp.Broker.idleTimeout())
			if ev.Type == "done" {
				return
			}
		}
	}
}

// isTerminalState reports whether the A2A task state is one that the
// broker will never publish further events for. Resubscribe to a
// terminal-state task emits the snapshot + done and closes; no live
// tail is possible.
func isTerminalState(s TaskState) bool {
	switch s {
	case TaskStateCompleted, TaskStateFailed, TaskStateCanceled:
		return true
	}
	return false
}

func writeSSEFrame(w http.ResponseWriter, flusher http.Flusher, ev StreamEvent) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// writeRPCError writes a JSON-RPC error envelope on the response. Only
// usable BEFORE the SSE Content-Type header has been written; after
// that the response is committed to SSE and callers must signal errors
// via SSE frames or by closing the connection.
func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(JSONRPCResponse{
		JSONRPC: "2.0", ID: id,
		Error: &JSONRPCError{Code: code, Message: msg},
	})
}

// lookup returns the subscription for a given streamID, or
// (nil, false) if it has been reaped. Package-private because the
// inline SSE handler needs the subscription's channel; external
// callers should use the higher-level Subscribe / Handler surface.
func (b *StreamBroker) lookup(streamID string) (*subscription, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub, ok := b.byStreamID[streamID]
	return sub, ok
}

// cleanup tears down a subscription created by the inline SSE handler
// when the connection ends without a terminal "done" event (client
// disconnect, context cancel). Without this the slice of by-task
// subscriptions leaks the dangling streamID until a "done" event
// arrives (which may never happen).
func (b *StreamBroker) cleanup(streamID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub, ok := b.byStreamID[streamID]
	if !ok {
		return
	}
	delete(b.byStreamID, streamID)
	if subs, exists := b.byTaskID[sub.taskID]; exists {
		delete(subs, streamID)
		if len(subs) == 0 {
			delete(b.byTaskID, sub.taskID)
		}
	}
	// Channel may already be closed (terminal Publish path). Use
	// recover to make double-close safe.
	defer func() { _ = recover() }()
	close(sub.ch)
}

// acceptsEventStream reports whether the Accept header advertises a
// preference for text/event-stream. Any quality parameter or
// charset suffix is tolerated; we just need a substring match.
func acceptsEventStream(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}

// Compile-time guards against silent renames elsewhere in the package.
var (
	_ = errors.New
	_ = context.Background
)

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/ptyhost/agentcatalog"
	"github.com/chepherd/chepherd/internal/runtime/agentpatterns"
)

// A2ADeliverer implements a2a.Deliverer by routing A2A SendMessage
// payloads into a target chepherd session's PTY (interactive mode).
//
// A2A spec binding for chepherd interactive mode:
//
//   - msg.ContextID = the chepherd session — either the long-form
//     session ID (e.g. "shepherd-1780057429428571338" as returned by
//     /api/v1/sessions) OR the short @-name ("shepherd"). Resolution
//     order is byID first, byName as fallback (cf.
//     Runtime.GetByContextID). REQUIRED — caller must provide one.
//     The "accepts either" shape was locked after PR #216's walk
//     surfaced the byName-only limitation as a -32603 error envelope.
//   - msg.TaskID = per-Message discrete unit of work. Optional; if
//     empty, server auto-generates a UUIDv7. Multiple in-flight tasks
//     CAN share a ContextID per A2A v1.0 spec.
//
// For each Deliver call:
//
//  1. Resolves the target session via Runtime.Get(msg.ContextID).
//  2. Extracts plain text from msg.Parts via a2a.ExtractText (errors
//     out on FilePart/DataPart until later sub-branches add support).
//  3. Writes the text into the session's PTY.
//  4. Writes the flavor-specific submit sequence
//     (agentcatalog.Agent.EffectiveSubmitSequence()) — defaults to CR
//     (0x0d) when the flavor doesn't override.
//  5. Returns an a2a.Task with state="working" so the A2A caller can
//     poll GetTask / SubscribeToTask for completion.
//
// For headless-iogrid mode (no PTY, SessionRepository-mediated async)
// a sibling Deliverer (NOT this struct) is used; the chepherd-headless
// API constructs it from the same persistence.Store. See
// docs/V0.9.2-ARCHITECTURE.md §3 operation modes.
//
// Refs #208 #225 row A4 (persistence wiring).
type A2ADeliverer struct {
	rt        *Runtime
	broker    brokerPublisher            // #225 row A3 — set via SetBroker; nil disables publishing
	taskStore persistence.TaskRepository // #225 row A4 — set via SetTaskStore; nil disables persistence
	runnerSID string                     // #225 row A4 — chepherd-instance ID stamped on persisted Task rows

	// #484 Wave A5 — pre-execution RBAC grant check. When non-nil,
	// every Deliver call invokes this before persisting/working;
	// allowed=false → Task is returned in TaskStateRejected with the
	// supplied reason + persisted + published (so SSE/webhook
	// consumers see the denial visibly). nil = no check (back-compat
	// for tests + intra-org deployments where every caller is
	// already trusted).
	grantCheck GrantCheckFn
}

// GrantCheckFn is the pre-execution RBAC seam injected via
// SetGrantCheck. Inputs: caller SID (typically the calling agent's
// session ID, derived from JWT sub claim or from the local-auth
// subject), target SID (the agent being called). Returns
// allowed=false + a human-readable reason to trigger a REJECTED
// Task. allowed=true skips REJECTED and lets Deliver proceed as
// normal.
type GrantCheckFn func(callerSID, targetSID string) (allowed bool, reason string)

// SetGrantCheck wires the pre-execution RBAC check (#484 Wave A5).
// nil clears any previously-set check. Same seam shape as
// runtimehttp.Server.GrantCheck — production cmd/run.go can wire
// both fields to the same PersistenceGrantCheck.
func (d *A2ADeliverer) SetGrantCheck(fn GrantCheckFn) { d.grantCheck = fn }

// NewA2ADeliverer wraps a Runtime as an a2a.Deliverer.
func NewA2ADeliverer(rt *Runtime) *A2ADeliverer {
	return &A2ADeliverer{rt: rt}
}

// SetTaskStore wires the TaskRepository so each Deliver call persists
// the issued Task row. nil disables persistence (back-compat for tests
// + pre-A4 deployments). runnerSID is stamped on every row so multi-
// runner queries can scope by origin.
//
// Refs #225 row A4.
func (d *A2ADeliverer) SetTaskStore(store persistence.TaskRepository, runnerSID string) {
	d.taskStore = store
	d.runnerSID = runnerSID
}

// Deliver routes msg into the target session's PTY and returns the
// tracking Task.
func (d *A2ADeliverer) Deliver(ctx context.Context, msg a2a.Message) (*a2a.Task, error) {
	if msg.ContextID == "" {
		return nil, errors.New("A2ADeliverer: msg.ContextID required (chepherd session ID)")
	}
	// #484 Wave A5 — pre-execution RBAC check. Fires BEFORE session
	// lookup so we don't leak "session exists" info to a denied
	// caller. Caller SID comes from msg.Metadata when an A2A peer
	// included it; absent metadata is the intra-runner case where
	// no cross-agent grant is needed (and so a nil grantCheck or a
	// trivially-allowing one matches the right behavior).
	if d.grantCheck != nil {
		callerSID := callerSIDFromMessage(msg)
		if allowed, reason := d.grantCheck(callerSID, msg.ContextID); !allowed {
			rejected := d.rejectedTask(msg, reason)
			d.persistTask(ctx, msg, rejected, "message/send")
			if d.broker != nil {
				d.broker.Publish(rejected.ID, a2a.StreamEvent{
					Type: "done",
					Task: rejected,
				})
			}
			return rejected, nil
		}
	}
	// Accept ContextID as EITHER the session ID OR the @-name (#217).
	// Runtime.GetByContextID tries r.info[ContextID] first (full ID),
	// then r.byName[ContextID] (short name). Pre-#217 this was a
	// byName-only Get which made /api/v1/sessions-returned IDs error
	// out with -32603 even though those IDs are the canonical chepherd
	// session handle.
	sess, info := d.rt.GetByContextID(msg.ContextID)
	if info == nil || sess == nil {
		failed := d.failedTask(msg, "target session not found")
		d.persistTask(ctx, msg, failed, "message/send")
		return failed, fmt.Errorf("a2a.SendMessage: target session %q not found", msg.ContextID)
	}
	text, err := a2a.ExtractText(msg)
	if err != nil {
		failed := d.failedTask(msg, err.Error())
		d.persistTask(ctx, msg, failed, "message/send")
		return failed, err
	}
	task := d.workingTask(msg)
	// #225 row A4 — persist the Task row so GetTask/ListTasks see it.
	d.persistTask(ctx, msg, task, "message/send")
	// #225 row A3 — pump PTY output through the broker so SSE
	// subscribers see streaming task progress. No-op when broker
	// is unset (back-compat for tests + pre-A3 deployments).
	//
	// #379 P0 — pumpPTYToBroker also drives the A2A RECEIVE loop:
	// silence-window detect → persist agent message in history →
	// flip State to "completed".
	//
	// #387 P0 — pump MUST subscribe BEFORE we write the user's
	// message so the byte-offset send-mark splits banner (pre-send)
	// from response (post-send). Pre-#387 we wrote the message
	// first, then spawned pump — but claude-code's input-box redraw
	// emits cursor bytes into the response window, defeating #385's
	// cursor-gate. Order:
	//
	//   1. Create mark
	//   2. Spawn pump; pump subscribes; pump signals mark.Subscribed
	//   3. We wait for Subscribed
	//   4. Write user's message + submit sequence
	//   5. Call mark.MarkSendNow() — pump records sendOffset
	//   6. Silence-finalize slices buf[sendOffset:] for the gate +
	//      the completer's response text
	var mark *pumpSendMark
	if d.broker != nil || d.taskStore != nil {
		mark = newPumpSendMark()
		completer := d.taskCompleter(info.AgentSlug)
		decideState := makeDecideStateFn(info.AgentSlug)
		go pumpPTYToBrokerWithState(d.broker, sess, task, completer, mark, decideState)
		// Brief bound so a slow Subscribe doesn't wedge Deliver. In
		// practice subscribe is microseconds; 1s is paranoid.
		select {
		case <-mark.Subscribed:
		case <-time.After(1 * time.Second):
			// Pump didn't subscribe in time. Continue without
			// marking — degrades to pre-#387 behavior (full-buffer
			// cursor gate, may capture banner chrome on first
			// message). Better than blocking the entire Deliver.
		}
	}
	if _, err := sess.Write([]byte(text)); err != nil {
		failed := d.failedTask(msg, "PTY write: "+err.Error())
		d.persistTask(ctx, msg, failed, "message/send")
		return failed, err
	}
	// Submit via flavor-specific sequence (defaults to CR when no
	// override). agentcatalog lookup keyed on the session's agent slug.
	submitSeq := d.submitSequenceFor(info.AgentSlug)
	if _, err := sess.Write(submitSeq); err != nil {
		failed := d.failedTask(msg, "PTY submit: "+err.Error())
		d.persistTask(ctx, msg, failed, "message/send")
		return failed, err
	}
	// #387 P0 — mark the send boundary AFTER both writes land. The
	// pump's responseBuf may already contain banner chunks at this
	// point; sendOffset captures the current length so silence-
	// finalize only sees response bytes (post-send).
	if mark != nil {
		mark.MarkSendNow()
	}
	return task, nil
}

// taskCompleter returns the callback pumpPTYToBroker invokes once
// per task when the silence window elapses (response complete). The
// callback reads the Task row, appends a Message{role:"agent"} with
// the agent's accumulated response, flips State to completed, and
// Save()s. nil when taskStore is unset (tests).
//
// #379 P0 receive-loop fix.
// callerSIDFromMessage extracts the caller's SID from the A2A
// Message envelope. v0.9.4 keeps the chepherd Message struct
// metadata-free; cross-org callers will surface their identity via
// the §15.2 JWT sub claim threaded through the JSON-RPC HTTP layer
// in a follow-up Wave. Today this returns the empty string so the
// grantCheck closure sees a consistent "intra-runner" signal.
func callerSIDFromMessage(_ a2a.Message) string { return "" }

// makeDecideStateFn returns the per-slug decideState closure
// pumpPTYToBrokerWithState uses to translate response bytes into the
// post-silence Task state. Same agentpatterns lookup as the
// completer (symmetric) so the SSE event's state field matches the
// persisted record. AUTH_REQUIRED wins over INPUT_REQUIRED when
// both fire — the user has to satisfy auth before clarifying
// questions become answerable.
func makeDecideStateFn(agentSlug string) decideStateFn {
	flavor := agentpatterns.ByAgentSlug(agentSlug)
	return func(buf []byte) a2a.TaskState {
		if flavor.IsAuthRequired(buf).Match {
			return a2a.TaskStateAuthRequired
		}
		if flavor.IsInputRequired(buf).Match {
			return a2a.TaskStateInputRequired
		}
		return a2a.TaskStateCompleted
	}
}

func (d *A2ADeliverer) taskCompleter(agentSlug string) func(taskID, response string) {
	if d.taskStore == nil {
		return nil
	}
	flavor := agentpatterns.ByAgentSlug(agentSlug)
	return func(taskID, response string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rec, err := d.taskStore.Get(ctx, taskID)
		if err != nil || rec == nil {
			return // not persisted (shouldn't happen — Deliver persists first)
		}
		// Terminal states already final — don't re-write.
		if a2a.IsTerminal(a2a.TaskState(rec.State)) {
			return
		}
		agentMsg := a2a.Message{
			Role: "agent",
			Kind: "message",
			Parts: []a2a.Part{
				{Kind: "text", Text: stripANSI(response)},
			},
		}
		// OutputBlob shape per a2a.decodeTask: {artifacts, history,
		// statusDetails}. Preserve any prior history + details that
		// were written.
		var out struct {
			Artifacts     []a2a.Artifact         `json:"artifacts,omitempty"`
			History       []a2a.Message          `json:"history,omitempty"`
			StatusDetails *a2a.TaskStatusDetails `json:"statusDetails,omitempty"`
		}
		if len(rec.OutputBlob) > 0 {
			_ = json.Unmarshal(rec.OutputBlob, &out)
		}
		out.History = append(out.History, agentMsg)
		// #484 Wave A5 + #503 Wave H5 — decide the post-silence
		// state via the per-flavor pattern-match library. Order:
		// AUTH_REQUIRED > INPUT_REQUIRED > COMPLETED. AUTH wins
		// over INPUT when both fire because an OAuth challenge is
		// the stronger signal — the user needs to satisfy the auth
		// before clarifying questions become answerable.
		nextState := a2a.TaskStateCompleted
		responseBytes := []byte(response)
		if flavor.IsAuthRequired(responseBytes).Match {
			nextState = a2a.TaskStateAuthRequired
			// #503 Wave H5 — surface AUTH_REQUIRED details via
			// the OutputBlob so SSE/push/poll consumers can render
			// the operator prompt without re-parsing agent bytes.
			if ch := flavor.ExtractAuthChallenge(responseBytes); ch != nil {
				out.StatusDetails = &a2a.TaskStatusDetails{
					AuthProvider: ch.Provider,
					AuthMessage:  ch.Message,
					AuthURL:      ch.URL,
				}
			}
		} else if flavor.IsInputRequired(responseBytes).Match {
			nextState = a2a.TaskStateInputRequired
		}
		blob, _ := json.Marshal(out)
		rec.OutputBlob = blob
		if err := a2a.TransitionTask(rec, nextState, "silence-finalize via "+agentSlug); err != nil {
			// Illegal transition — log via UpdatedAt+state-unchanged.
			// Falls back to whatever state was already persisted.
			return
		}
		_ = d.taskStore.Save(ctx, rec)
	}
}

// persistTask serialises Message + Task and writes to TaskRepository.
// Error is swallowed: the Task return path is already committed to
// the caller; failure to persist is a downstream observability gap,
// not a delivery failure.
//
// Refs #225 row A4.
func (d *A2ADeliverer) persistTask(ctx context.Context, msg a2a.Message, task *a2a.Task, method string) {
	if d.taskStore == nil {
		return
	}
	inputBlob, _ := json.Marshal(msg)
	outputBlob, _ := json.Marshal(task)
	now := time.Now().UTC()
	rec := &persistence.Task{
		ID:         task.ID,
		RunnerSID:  d.runnerSID,
		State:      string(task.Status.State),
		Method:     method,
		InputBlob:  inputBlob,
		OutputBlob: outputBlob,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	_ = d.taskStore.Save(ctx, rec)
}

// submitSequenceFor returns the flavor's submit byte sequence; falls
// back to the default CR when the catalog lookup fails (e.g. an agent
// slug introduced after the catalog was loaded).
func (d *A2ADeliverer) submitSequenceFor(slug string) []byte {
	agent, err := agentcatalog.Lookup(slug)
	if err != nil {
		return []byte{0x0d}
	}
	return agent.EffectiveSubmitSequence()
}

// taskIDOrGenerate returns the caller-provided TaskID, or a fresh
// UUIDv7 when missing. UUIDv7 is time-ordered which helps downstream
// sorting + cursor pagination in TaskRepository (later sub-branch).
func taskIDOrGenerate(taskID string) string {
	if taskID != "" {
		return taskID
	}
	id, err := uuid.NewV7()
	if err != nil {
		// uuid.NewV7 only fails when crypto/rand is broken; fall back
		// to V4 which uses the same RNG and is also UUID-format-valid.
		id = uuid.New()
	}
	return id.String()
}

func (d *A2ADeliverer) workingTask(msg a2a.Message) *a2a.Task {
	return &a2a.Task{
		ID:        taskIDOrGenerate(msg.TaskID),
		ContextID: msg.ContextID,
		Kind:      "task",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateWorking,
		},
	}
}

func (d *A2ADeliverer) failedTask(msg a2a.Message, reason string) *a2a.Task {
	return &a2a.Task{
		ID:        taskIDOrGenerate(msg.TaskID),
		ContextID: msg.ContextID,
		Kind:      "task",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateFailed,
			Message: &a2a.Message{
				Role: "agent",
				Kind: "message",
				Parts: []a2a.Part{
					{Kind: "text", Text: reason},
				},
			},
		},
	}
}

// rejectedTask constructs a Task in TaskStateRejected — the
// pre-execution RBAC-denial state per #484 Wave A5. Distinct from
// failedTask in that REJECTED is a denial BEFORE any agent
// execution started; FAILED is for runtime errors mid-execution.
// SSE / webhook consumers use the difference to surface
// "rejected by policy" vs "agent crashed" appropriately.
func (d *A2ADeliverer) rejectedTask(msg a2a.Message, reason string) *a2a.Task {
	return &a2a.Task{
		ID:        taskIDOrGenerate(msg.TaskID),
		ContextID: msg.ContextID,
		Kind:      "task",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateRejected,
			Message: &a2a.Message{
				Role: "agent",
				Kind: "message",
				Parts: []a2a.Part{
					{Kind: "text", Text: reason},
				},
			},
		},
	}
}

var _ a2a.Deliverer = (*A2ADeliverer)(nil)

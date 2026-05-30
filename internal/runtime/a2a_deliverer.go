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
}

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
	task := d.workingTask(msg)
	// #225 row A4 — persist the Task row so GetTask/ListTasks see it.
	d.persistTask(ctx, msg, task, "message/send")
	// #225 row A3 — pump PTY output through the broker so SSE
	// subscribers see streaming task progress. No-op when broker
	// is unset (back-compat for tests + pre-A3 deployments).
	//
	// #379 P0 — pumpPTYToBroker also drives the A2A RECEIVE loop:
	// it accumulates PTY chunks, detects "response complete" via a
	// silence window, then persists the agent's response as a
	// Message{role:"agent"} appended to the Task's history AND
	// flips Task.State to "completed". Without this, the task row
	// in taskStore stays at "working" forever — tasks/get returns
	// "working", peer agents poll indefinitely, federation broken.
	// Pre-#379 the SSE broker saw the artifact events but the
	// persisted row did not, so the public A2A API was one-way.
	if d.broker != nil || d.taskStore != nil {
		completer := d.taskCompleter()
		go pumpPTYToBroker(d.broker, sess, task, completer)
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
func (d *A2ADeliverer) taskCompleter() func(taskID, response string) {
	if d.taskStore == nil {
		return nil
	}
	return func(taskID, response string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rec, err := d.taskStore.Get(ctx, taskID)
		if err != nil || rec == nil {
			return // not persisted (shouldn't happen — Deliver persists first)
		}
		// Terminal states already final — don't re-write.
		if rec.State == string(a2a.TaskStateCompleted) ||
			rec.State == string(a2a.TaskStateFailed) ||
			rec.State == string(a2a.TaskStateCanceled) {
			return
		}
		agentMsg := a2a.Message{
			Role: "agent",
			Kind: "message",
			Parts: []a2a.Part{
				{Kind: "text", Text: stripANSI(response)},
			},
		}
		// OutputBlob shape per a2a.decodeTask: {artifacts, history}.
		// Preserve any prior history that was written.
		var out struct {
			Artifacts []a2a.Artifact `json:"artifacts,omitempty"`
			History   []a2a.Message  `json:"history,omitempty"`
		}
		if len(rec.OutputBlob) > 0 {
			_ = json.Unmarshal(rec.OutputBlob, &out)
		}
		out.History = append(out.History, agentMsg)
		blob, _ := json.Marshal(out)
		rec.OutputBlob = blob
		rec.State = string(a2a.TaskStateCompleted)
		rec.UpdatedAt = time.Now().UTC()
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

var _ a2a.Deliverer = (*A2ADeliverer)(nil)

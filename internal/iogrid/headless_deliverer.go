// internal/iogrid/headless_deliverer.go — #319 (#225 row E3).
// HeadlessIOgridDeliverer implements a2a.Deliverer for the
// chepherd-headless operation mode: incoming A2A messages aren't
// written to a PTY (no live agent session) but instead persisted as
// task records that an iogrid worker will pick up asynchronously.
//
// Operation modes (per docs/V0.9.2-ARCHITECTURE.md §3):
//   - interactive: runtime.A2ADeliverer — Deliver writes msg.Parts
//     into a live agent's PTY, agent responds in-process
//   - headless-iogrid: this file — Deliver persists msg.Parts as a
//     TaskRepository record, an iogrid worker process polls + handles
//     the work asynchronously, results land in the same Task record's
//     OutputBlob + ArtifactRepository (H3 #298)
//
// The two Deliverers share the same a2a.Deliverer interface so the
// chepherd binary picks one based on operator configuration. When
// --iogrid-endpoint is set + no live agents are spawned, the
// HeadlessIOgridDeliverer is the active Deliverer.
//
// Refs #319 (#225 row E3) + #304 (E2 substrate) + #298 (H3 artifacts).
package iogrid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/persistence"
)

// HeadlessIOgridDeliverer implements a2a.Deliverer by persisting
// inbound messages as TaskRepository records. An iogrid worker
// process polls TaskRepository.List(state=submitted) and handles
// them out-of-band.
//
// Refs #319 (#225 row E3).
type HeadlessIOgridDeliverer struct {
	// TaskStore is where inbound messages land as Task{state=submitted}
	// records. Required.
	TaskStore persistence.TaskRepository

	// RunnerSID is this chepherd's instance identifier (used so an
	// iogrid worker can route results back via the same SID). Required.
	RunnerSID string

	// Method is the A2A method name that fired this Deliver call.
	// Optional — defaults to "message/send" when empty. Set by
	// the caller via SetMethod for non-default methods.
	Method string
}

// Deliver persists msg as a Task{state=submitted} record + returns
// the Task so the A2A caller can poll GetTask for state transitions.
// Does NOT block on iogrid worker pickup — that's intentional async.
//
// Errors at any layer (validation, marshal, persistence) surface as
// a failed Task + an error returned to the JSON-RPC method body
// (a2a.MethodBodies translates that into a -32603 envelope).
func (d *HeadlessIOgridDeliverer) Deliver(ctx context.Context, msg a2a.Message) (*a2a.Task, error) {
	if d.TaskStore == nil {
		return nil, errors.New("HeadlessIOgridDeliverer: nil TaskStore")
	}
	if d.RunnerSID == "" {
		return nil, errors.New("HeadlessIOgridDeliverer: empty RunnerSID")
	}
	if msg.ContextID == "" {
		return nil, errors.New("HeadlessIOgridDeliverer: msg.ContextID required")
	}
	if _, err := a2a.ExtractText(msg); err != nil {
		return d.failedTask(msg, err.Error()), err
	}

	taskID := msg.TaskID
	if taskID == "" {
		id, err := uuid.NewV7()
		if err != nil {
			id = uuid.New()
		}
		taskID = id.String()
	}

	inputBlob, err := json.Marshal(msg)
	if err != nil {
		return d.failedTask(msg, "marshal input: "+err.Error()), err
	}

	method := d.Method
	if method == "" {
		method = "message/send"
	}

	rec := &persistence.Task{
		ID:        taskID,
		RunnerSID: d.RunnerSID,
		State:     string(a2a.TaskStateSubmitted),
		Method:    method,
		InputBlob: inputBlob,
	}
	if err := d.TaskStore.Save(ctx, rec); err != nil {
		return d.failedTask(msg, "persist task: "+err.Error()), err
	}

	return &a2a.Task{
		ID:        taskID,
		ContextID: msg.ContextID,
		Kind:      "task",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateSubmitted,
		},
	}, nil
}

// failedTask builds the rejection Task that the A2A caller sees when
// Deliver hits an error. Caller still gets a Task envelope (not just
// an error) so the JSON-RPC body can serialize it consistently.
func (d *HeadlessIOgridDeliverer) failedTask(msg a2a.Message, reason string) *a2a.Task {
	taskID := msg.TaskID
	if taskID == "" {
		taskID = fmt.Sprintf("failed-%d", time.Now().UnixNano())
	}
	return &a2a.Task{
		ID:        taskID,
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

var _ a2a.Deliverer = (*HeadlessIOgridDeliverer)(nil)

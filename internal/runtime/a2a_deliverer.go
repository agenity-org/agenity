package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/ptyhost/agentcatalog"
)

// A2ADeliverer implements a2a.Deliverer by routing A2A SendMessage
// payloads into a target chepherd session's PTY (interactive mode).
//
// A2A spec binding for chepherd interactive mode (per architect
// 2026-05-29 scope-lock):
//
//   - msg.ContextID = chepherd session ID (the long-running PTY-backed
//     conversation handle). REQUIRED — caller must provide.
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
// Refs #208.
type A2ADeliverer struct {
	rt *Runtime
}

// NewA2ADeliverer wraps a Runtime as an a2a.Deliverer.
func NewA2ADeliverer(rt *Runtime) *A2ADeliverer {
	return &A2ADeliverer{rt: rt}
}

// Deliver routes msg into the target session's PTY and returns the
// tracking Task.
func (d *A2ADeliverer) Deliver(ctx context.Context, msg a2a.Message) (*a2a.Task, error) {
	if msg.ContextID == "" {
		return nil, errors.New("A2ADeliverer: msg.ContextID required (chepherd session ID)")
	}
	sess, info := d.rt.Get(msg.ContextID)
	if info == nil || sess == nil {
		return d.failedTask(msg, "target session not found"),
			fmt.Errorf("a2a.SendMessage: target session %q not found", msg.ContextID)
	}
	text, err := a2a.ExtractText(msg)
	if err != nil {
		return d.failedTask(msg, err.Error()), err
	}
	if _, err := sess.Write([]byte(text)); err != nil {
		return d.failedTask(msg, "PTY write: "+err.Error()), err
	}
	// Submit via flavor-specific sequence (defaults to CR when no
	// override). agentcatalog lookup keyed on the session's agent slug.
	submitSeq := d.submitSequenceFor(info.AgentSlug)
	if _, err := sess.Write(submitSeq); err != nil {
		return d.failedTask(msg, "PTY submit: "+err.Error()), err
	}
	return d.workingTask(msg), nil
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

package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
)

// A2ADeliverer implements a2a.Deliverer by routing A2A SendMessage
// payloads into a target chepherd session's PTY (interactive mode).
// For each Deliver call:
//
//  1. Resolves the target session via Runtime.Get(msg.TaskID).
//  2. Extracts plain text from msg.Parts via a2a.ExtractText (errors
//     out on FilePart/DataPart until later sub-branches add support).
//  3. Writes the text + a trailing CR (unless NoSubmit-like behavior
//     is hinted via params extensions — v0.9.2 scaffold always
//     submits) into the session's PTY.
//  4. Returns an a2a.Task with state="working" so the A2A caller can
//     poll GetTask / SubscribeToTask for completion.
//
// For headless-iogrid mode (no PTY, SessionRepository-mediated
// async) a sibling Deliverer (NOT this struct) is used; the
// chepherd-headless API constructs it from the same persistence.Store.
// See docs/V0.9.2-ARCHITECTURE.md §3 operation modes.
//
// Refs #208.
type A2ADeliverer struct {
	rt      *Runtime
	taskSeq uint64
}

// NewA2ADeliverer wraps a Runtime as an a2a.Deliverer.
func NewA2ADeliverer(rt *Runtime) *A2ADeliverer {
	return &A2ADeliverer{rt: rt}
}

// Deliver routes msg into the target session's PTY and returns the
// tracking Task.
func (d *A2ADeliverer) Deliver(ctx context.Context, msg a2a.Message) (*a2a.Task, error) {
	if msg.TaskID == "" {
		return nil, errors.New("A2ADeliverer: msg.TaskID required")
	}
	sess, info := d.rt.Get(msg.TaskID)
	if info == nil || sess == nil {
		return d.failedTask(msg, "target session not found"), fmt.Errorf("a2a.SendMessage: target session %q not found", msg.TaskID)
	}
	text, err := a2a.ExtractText(msg)
	if err != nil {
		return d.failedTask(msg, err.Error()), err
	}
	if _, err := sess.Write([]byte(text)); err != nil {
		return d.failedTask(msg, "PTY write: "+err.Error()), err
	}
	// Submit by writing CR; chepherd CLI worker agents (claude-code et
	// al) interpret CR as "submit this line".
	if _, err := sess.Write([]byte{0x0d}); err != nil {
		return d.failedTask(msg, "PTY CR: "+err.Error()), err
	}
	return d.workingTask(msg), nil
}

func (d *A2ADeliverer) workingTask(msg a2a.Message) *a2a.Task {
	return &a2a.Task{
		ID:        msg.TaskID,
		ContextID: msg.ContextID,
		Kind:      "task",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateWorking,
		},
	}
}

func (d *A2ADeliverer) failedTask(msg a2a.Message, reason string) *a2a.Task {
	return &a2a.Task{
		ID:        d.nextSyntheticTaskID(msg.TaskID),
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

func (d *A2ADeliverer) nextSyntheticTaskID(target string) string {
	n := atomic.AddUint64(&d.taskSeq, 1)
	return fmt.Sprintf("failed-%s-%d-%d", target, time.Now().UnixNano(), n)
}

var _ a2a.Deliverer = (*A2ADeliverer)(nil)

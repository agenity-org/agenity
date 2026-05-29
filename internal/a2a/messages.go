package a2a

import (
	"context"
	"encoding/json"
	"errors"
)

// Message is the A2A v1.0 Message envelope. Carries a role + parts
// array + identity metadata. SendMessage's handler decodes a Message,
// extracts the textual body from TextPart entries, looks up the
// target session by TaskID, and delivers via the bound Deliverer.
//
// v0.9.2 scaffold supports TextPart only; FilePart and DataPart return
// -32602 invalid params from the handler until later sub-branches
// implement file-upload + structured-data binding.
//
// Refs #208 + docs/V0.9.2-ARCHITECTURE.md §3 operation modes (PTY
// for interactive; SessionRepository-mediated for headless-iogrid).
type Message struct {
	// Role distinguishes user-originated (operator or external A2A
	// caller) vs agent-emitted messages. v0.9.2 scaffold uses "user"
	// for inbound SendMessage; "agent" for the response Message
	// carried back in the Task's output artifact.
	Role string `json:"role"`

	// Parts carries the payload. Each Part has a Kind ("text" /
	// "file" / "data") + a kind-specific payload field.
	Parts []Part `json:"parts"`

	// MessageID is the caller-assigned id of this Message (UUID
	// recommended). Used for idempotency / retry detection.
	MessageID string `json:"messageId,omitempty"`

	// TaskID identifies the discrete unit of work within this
	// Message's ContextID-scoped conversation. Optional; if missing,
	// the SendMessage handler auto-generates a UUIDv7 server-side and
	// returns it in SendMessageResult.Task.ID.
	//
	// A single ContextID may host MANY tasks concurrently (per A2A v1.0
	// spec) — taskId is the per-request handle for poll/subscribe/cancel.
	TaskID string `json:"taskId,omitempty"`

	// ContextID is the long-running conversation grouping. REQUIRED
	// for SendMessage in v0.9.2 interactive mode — resolves to the
	// target chepherd session ID (the PTY-backed conversation handle).
	// Headless-iogrid mode (later sub-branch) accepts taskId-only and
	// treats contextId as optional grouping.
	ContextID string `json:"contextId,omitempty"`

	// Kind discriminates Message from other Result types in JSON-RPC
	// envelopes. Always "message" for spec compliance.
	Kind string `json:"kind,omitempty"`
}

// Part is one entry in Message.Parts. Discriminated by Kind.
//
// TextPart: { kind: "text", text: "..." }
// FilePart: { kind: "file", file: { name, mimeType, bytes | uri } }
// DataPart: { kind: "data", data: {...} }
type Part struct {
	Kind string          `json:"kind"`
	Text string          `json:"text,omitempty"`
	File *FilePayload    `json:"file,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// FilePayload is the A2A FilePart payload. v0.9.2 scaffold doesn't
// process FileParts; later sub-branches decode bytes/URI.
type FilePayload struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Bytes    string `json:"bytes,omitempty"` // base64
	URI      string `json:"uri,omitempty"`
}

// SendMessageParams wraps the inbound Message per A2A JSON-RPC convention.
type SendMessageParams struct {
	Message Message `json:"message"`
}

// SendMessageResult is the A2A SendMessage response. The Task object
// tracks the message's lifecycle (Submitted → Working → ...).
type SendMessageResult struct {
	Task *Task `json:"task,omitempty"`
}

// Task tracks the lifecycle of an A2A message delivery + response. A
// freshly-delivered SendMessage returns a Task in state "working";
// callers poll GetTask or subscribe via SendStreamingMessage for
// the state transition to "completed" / "failed" / "input-required".
type Task struct {
	ID        string     `json:"id"`
	ContextID string     `json:"contextId,omitempty"`
	Status    TaskStatus `json:"status"`
	History   []Message  `json:"history,omitempty"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
	Kind      string     `json:"kind,omitempty"`
}

// TaskStatus is the current state + optional latest-Message snapshot.
type TaskStatus struct {
	State   TaskState `json:"state"`
	Message *Message  `json:"message,omitempty"`
}

// TaskState enumerates the A2A v1.0 task lifecycle states.
type TaskState string

const (
	TaskStateSubmitted     TaskState = "submitted"
	TaskStateWorking       TaskState = "working"
	TaskStateInputRequired TaskState = "input-required"
	TaskStateCompleted     TaskState = "completed"
	TaskStateFailed        TaskState = "failed"
	TaskStateCanceled      TaskState = "canceled"
)

// Artifact is the agent's emitted output. v0.9.2 scaffold returns
// artifacts as text parts; later sub-branches carry file + data
// artifacts per spec.
type Artifact struct {
	ArtifactID string `json:"artifactId"`
	Name       string `json:"name,omitempty"`
	Parts      []Part `json:"parts"`
}

// Deliverer is the abstraction backing the A2A SendMessage handler.
// Implementations look up the target session and deliver the Message
// body via the appropriate transport:
//
//   - interactive mode (Runtime.A2ADeliverer): PTY write via
//     session.Write
//   - headless-iogrid mode: SessionRepository-mediated async record
//     of the inbound message + task lifecycle
//
// Returns the Task that tracks the delivered message's lifecycle.
type Deliverer interface {
	Deliver(ctx context.Context, msg Message) (*Task, error)
}

// ExtractText concatenates every TextPart's Text in Message.Parts.
// Helper used by Deliverers that need a plain-text body (e.g. the
// PTY-bound interactive Deliverer). Returns the empty string + nil
// when Parts is empty.
func ExtractText(msg Message) (string, error) {
	var out string
	for i, p := range msg.Parts {
		switch p.Kind {
		case "text":
			out += p.Text
		case "file", "data":
			return "", errors.New("a2a.ExtractText: part " +
				partIndex(i) + " has unsupported Kind " + p.Kind +
				" (v0.9.2 scaffold supports TextPart only)")
		default:
			return "", errors.New("a2a.ExtractText: part " +
				partIndex(i) + " has unknown Kind " + p.Kind)
		}
	}
	return out, nil
}

func partIndex(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	out := ""
	for i > 0 {
		out = string(digits[i%10]) + out
		i /= 10
	}
	return out
}

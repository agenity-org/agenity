// internal/runtime/audit_event.go — #488 Wave AU1.
//
// Per V0.9.2-ARCHITECTURE.md §10 Pattern 1 steps 23-24 + §5 #8: the
// runner emits structured audit events on every A2A call boundary.
// AU1 ships the wire transport + event shape; daemon-side receiver
// stubs to stderr log (AU2 #489 swaps in persistence; AU3 #490
// surfaces in dashboard).
//
// Event types:
//   - "audit.sent"     — outbound A2A call SUCCEEDED at the runner
//   - "audit.received" — inbound A2A call HANDLED at the runner
//
// Both are emitted POST-completion (after method body returns) so
// latency_ms is meaningful. Failed calls also emit with status="error"
// + the error message; status="success" otherwise.
//
// Refs #488 V0.9.2-ARCHITECTURE.md §10 #5 §8.
package runtime

import (
	"time"
)

// AuditEvent is the canonical wire shape for runner→daemon audit
// events. JSON tag names are frozen per §10 step 24 — DO NOT rename
// without an ADR (AU2/AU3 + future dashboard consumers depend on
// the exact field names).
type AuditEvent struct {
	// EventType is "audit.sent" or "audit.received".
	EventType string `json:"event_type"`

	// Timestamp is the event's emission time on the runner. RFC 3339.
	Timestamp time.Time `json:"timestamp"`

	// Caller is the JWT sub claim for inbound events, or the
	// runner's own SID for outbound events.
	Caller string `json:"caller"`

	// Callee is the target peer's SID for outbound events, or the
	// runner's own SID for inbound events.
	Callee string `json:"callee"`

	// Method is the A2A JSON-RPC method name (e.g. "message/send",
	// "tasks/get").
	Method string `json:"method"`

	// LatencyMS is the request handling latency in milliseconds.
	LatencyMS int64 `json:"latency_ms"`

	// JTI is the JWT id (when present in the request token; empty
	// for unauthenticated dev mode).
	JTI string `json:"jti,omitempty"`

	// Status is "success" or "error".
	Status string `json:"status"`

	// Error is the error message when Status=="error". Empty
	// otherwise.
	Error string `json:"error,omitempty"`

	// TaskID is the A2A task id when the method produces/operates
	// on a task (message/send, tasks/get, tasks/cancel, etc.).
	// Empty for methods that don't carry one.
	TaskID string `json:"task_id,omitempty"`
}

// AuditEventTypeSent is the outbound event type.
const AuditEventTypeSent = "audit.sent"

// AuditEventTypeReceived is the inbound event type.
const AuditEventTypeReceived = "audit.received"

// NewAuditEvent populates Timestamp + the given event type fields.
// All other fields are caller-set.
func NewAuditEvent(eventType, method, caller, callee string) AuditEvent {
	return AuditEvent{
		EventType: eventType,
		Timestamp: time.Now().UTC(),
		Caller:    caller,
		Callee:    callee,
		Method:    method,
		Status:    "success",
	}
}

// AuditEmitter is the seam used by runner-side hooks (the inbound
// A2A middleware + the outbound A2A client wrapper) to push events
// up to the daemon. Production impl is cmd/runner's daemonClient;
// tests provide a recording stub.
type AuditEmitter interface {
	EmitAuditEvent(e AuditEvent)
}

// internal/runtime/audit_event_test.go — #488 Wave AU1 unit
// assertions on the AuditEvent wire shape.
//
// Named assertions AU1.V1-V5:
//
//	V1 — JSON field names exact match per §10 step 24
//	V2 — NewAuditEvent populates timestamp + status=success by default
//	V3 — AuditEventTypeSent/Received constants don't drift
//	V4 — empty AuditEvent omits omitempty-tagged optional fields
//	V5 — non-empty optional fields round-trip
//
// Refs #488.
package runtime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAU1_V1_JSONFieldNamesAreSpecFrozen(t *testing.T) {
	e := AuditEvent{
		EventType: "audit.received",
		Timestamp: time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC),
		Caller:    "agent-a",
		Callee:    "agent-b",
		Method:    "message/send",
		LatencyMS: 42,
		JTI:       "j-1",
		Status:    "success",
		TaskID:    "task-99",
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"event_type":"audit.received"`,
		`"timestamp":"2026-05-31T14:00:00Z"`,
		`"caller":"agent-a"`,
		`"callee":"agent-b"`,
		`"method":"message/send"`,
		`"latency_ms":42`,
		`"jti":"j-1"`,
		`"status":"success"`,
		`"task_id":"task-99"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("V1 FAIL: JSON missing %q. Full: %s", want, raw)
		}
	}
}

func TestAU1_V2_NewAuditEvent(t *testing.T) {
	before := time.Now().UTC()
	e := NewAuditEvent(AuditEventTypeSent, "tasks/get", "alpha", "beta")
	after := time.Now().UTC()
	if e.EventType != "audit.sent" {
		t.Errorf("V2 FAIL: event_type = %q, want audit.sent", e.EventType)
	}
	if e.Method != "tasks/get" {
		t.Errorf("V2 FAIL: method = %q, want tasks/get", e.Method)
	}
	if e.Caller != "alpha" || e.Callee != "beta" {
		t.Errorf("V2 FAIL: caller/callee = %q/%q, want alpha/beta", e.Caller, e.Callee)
	}
	if e.Status != "success" {
		t.Errorf("V2 FAIL: status = %q, want success", e.Status)
	}
	if e.Timestamp.Before(before) || e.Timestamp.After(after) {
		t.Errorf("V2 FAIL: timestamp %v out of bounds [%v..%v]", e.Timestamp, before, after)
	}
}

func TestAU1_V3_EventTypeConstants(t *testing.T) {
	if AuditEventTypeSent != "audit.sent" {
		t.Errorf("V3 FAIL: AuditEventTypeSent = %q, want audit.sent", AuditEventTypeSent)
	}
	if AuditEventTypeReceived != "audit.received" {
		t.Errorf("V3 FAIL: AuditEventTypeReceived = %q, want audit.received", AuditEventTypeReceived)
	}
}

func TestAU1_V4_OmittedFieldsAbsent(t *testing.T) {
	// No JTI, Error, TaskID set.
	e := NewAuditEvent(AuditEventTypeReceived, "message/send", "x", "y")
	raw, _ := json.Marshal(e)
	for _, banned := range []string{`"jti":`, `"error":`, `"task_id":`} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("V4 FAIL: omitempty field %q present in JSON: %s", banned, raw)
		}
	}
}

func TestAU1_V5_RoundTrip(t *testing.T) {
	e := AuditEvent{
		EventType: "audit.sent",
		Timestamp: time.Date(2026, 5, 31, 14, 0, 0, 0, time.UTC),
		Caller:    "runner-A",
		Callee:    "runner-B",
		Method:    "message/send",
		LatencyMS: 17,
		JTI:       "j-77",
		Status:    "error",
		Error:     "peer unreachable",
		TaskID:    "task-42",
	}
	raw, _ := json.Marshal(e)
	var decoded AuditEvent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("V5 FAIL: unmarshal: %v", err)
	}
	if decoded.EventType != e.EventType ||
		!decoded.Timestamp.Equal(e.Timestamp) ||
		decoded.Caller != e.Caller ||
		decoded.Callee != e.Callee ||
		decoded.Method != e.Method ||
		decoded.LatencyMS != e.LatencyMS ||
		decoded.JTI != e.JTI ||
		decoded.Status != e.Status ||
		decoded.Error != e.Error ||
		decoded.TaskID != e.TaskID {
		t.Errorf("V5 FAIL: round-trip mismatch: %+v vs %+v", e, decoded)
	}
}

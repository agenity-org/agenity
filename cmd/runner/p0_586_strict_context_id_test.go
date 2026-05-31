// cmd/runner/p0_586_strict_context_id_test.go pins #586: the
// runner's per-session A2A endpoint must STRICT-MATCH the
// ContextID against the runner's own sid (or accept empty).
//
// Pre-#586 the runner silently auto-created a task with whatever
// ContextID arrived, more permissive than the daemon's equivalent
// surface (which returned -32603 InternalError on unknown
// contextId). The asymmetry confused downstream tooling + masked
// routing bugs where a SendMessage hit the wrong runner.
//
// Coverage:
//   - Matching ContextID → task accepted (regression guard for
//     legitimate sends)
//   - Empty ContextID → accepted (compat with clients that omit it;
//     /a2a/<sid> path disambiguates anyway)
//   - Mismatched ContextID → error returned with both ids in
//     message (replaces silent auto-create + diagnostic-friendly)
//
// Refs #586 V0.9.2-ARCH §10 Pattern 1.
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

func newTestDeliverer(t *testing.T, sid string) *runnerDeliverer {
	t.Helper()
	tmp := t.TempDir() + "/runner.sqlite"
	store, err := sqlite.NewStore(context.Background(), tmp)
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return newRunnerDeliverer(store, sid)
}

func TestP0_586_Deliver_MatchingContextID_Accepts(t *testing.T) {
	d := newTestDeliverer(t, "runner-sid-abc")
	msg := a2a.Message{
		ContextID: "runner-sid-abc", // matches
		Parts:     []a2a.Part{{Kind: "text", Text: "hello"}},
	}
	task, err := d.Deliver(context.Background(), msg)
	if err != nil {
		t.Fatalf("matching ContextID should accept: %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task on matching ContextID")
	}
	if task.ContextID != "runner-sid-abc" {
		t.Errorf("task.ContextID = %q, want %q", task.ContextID, "runner-sid-abc")
	}
}

func TestP0_586_Deliver_EmptyContextID_Accepts(t *testing.T) {
	d := newTestDeliverer(t, "runner-sid-xyz")
	msg := a2a.Message{
		ContextID: "", // empty — backwards compat
		Parts:     []a2a.Part{{Kind: "text", Text: "hello"}},
	}
	task, err := d.Deliver(context.Background(), msg)
	if err != nil {
		t.Fatalf("empty ContextID should accept (compat): %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task on empty ContextID")
	}
}

func TestP0_586_Deliver_MismatchedContextID_RejectsWithDiagnosticError(t *testing.T) {
	d := newTestDeliverer(t, "runner-sid-OUR-RUNNER")
	msg := a2a.Message{
		ContextID: "some-OTHER-session",
		Parts:     []a2a.Part{{Kind: "text", Text: "wrong-runner"}},
	}
	_, err := d.Deliver(context.Background(), msg)
	if err == nil {
		t.Fatal("mismatched ContextID should reject, got nil err")
	}
	if !strings.Contains(err.Error(), "some-OTHER-session") {
		t.Errorf("error should cite the bad contextId: %q", err)
	}
	if !strings.Contains(err.Error(), "runner-sid-OUR-RUNNER") {
		t.Errorf("error should cite the runner's sid: %q", err)
	}
	if !strings.Contains(err.Error(), "/a2a/<sid>") {
		t.Errorf("error should hint at routing model: %q", err)
	}
}

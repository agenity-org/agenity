// internal/iogrid/headless_deliverer_test.go — pins #319 (#225 row E3)
// HeadlessIOgridDeliverer behavior.
//
// Refs #319 (#225 row E3).
package iogrid

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

func newE3Store(t *testing.T) *sqlite.Store {
	t.Helper()
	ctx := context.Background()
	s, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "e3.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestHeadlessIOgridDeliverer_Deliver_PersistsSubmittedTask(t *testing.T) {
	t.Parallel()
	store := newE3Store(t)
	d := &HeadlessIOgridDeliverer{
		TaskStore: store.Tasks(),
		RunnerSID: "chepherd-instance-A",
	}
	task, err := d.Deliver(context.Background(), a2a.Message{
		Role: "user", Kind: "message",
		ContextID: "session-1",
		Parts:     []a2a.Part{{Kind: "text", Text: "hello iogrid"}},
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if task == nil {
		t.Fatal("Task nil")
	}
	if task.Status.State != a2a.TaskStateSubmitted {
		t.Errorf("State = %q, want %q", task.Status.State, a2a.TaskStateSubmitted)
	}
	if task.ContextID != "session-1" {
		t.Errorf("ContextID = %q, want session-1", task.ContextID)
	}
	if task.ID == "" {
		t.Error("Task.ID auto-generated, expected non-empty")
	}

	// Verify TaskRepository round-trips the same record.
	rec, err := store.Tasks().Get(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.RunnerSID != "chepherd-instance-A" {
		t.Errorf("rec.RunnerSID = %q, want chepherd-instance-A", rec.RunnerSID)
	}
	if rec.State != string(a2a.TaskStateSubmitted) {
		t.Errorf("rec.State = %q, want submitted", rec.State)
	}
	if rec.Method != "message/send" {
		t.Errorf("rec.Method = %q, want message/send (default)", rec.Method)
	}
	if !strings.Contains(string(rec.InputBlob), "hello iogrid") {
		t.Errorf("InputBlob = %s, want contains 'hello iogrid'", rec.InputBlob)
	}
}

func TestHeadlessIOgridDeliverer_Deliver_HonorsCallerTaskID(t *testing.T) {
	t.Parallel()
	store := newE3Store(t)
	d := &HeadlessIOgridDeliverer{TaskStore: store.Tasks(), RunnerSID: "sid-1"}
	task, err := d.Deliver(context.Background(), a2a.Message{
		Role: "user", Kind: "message",
		ContextID: "ctx-1", TaskID: "explicit-task-id",
		Parts: []a2a.Part{{Kind: "text", Text: "x"}},
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if task.ID != "explicit-task-id" {
		t.Errorf("Task.ID = %q, want explicit-task-id (caller-provided)", task.ID)
	}
}

func TestHeadlessIOgridDeliverer_Deliver_HonorsMethodOverride(t *testing.T) {
	t.Parallel()
	store := newE3Store(t)
	d := &HeadlessIOgridDeliverer{
		TaskStore: store.Tasks(), RunnerSID: "sid-1",
		Method: "tasks/get",
	}
	task, err := d.Deliver(context.Background(), a2a.Message{
		Role: "user", Kind: "message",
		ContextID: "ctx-1",
		Parts:     []a2a.Part{{Kind: "text", Text: "x"}},
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	rec, err := store.Tasks().Get(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Method != "tasks/get" {
		t.Errorf("rec.Method = %q, want tasks/get", rec.Method)
	}
}

func TestHeadlessIOgridDeliverer_Deliver_Validation(t *testing.T) {
	t.Parallel()
	store := newE3Store(t)
	ctx := context.Background()
	msg := a2a.Message{Role: "user", Kind: "message", ContextID: "c", Parts: []a2a.Part{{Kind: "text", Text: "t"}}}

	// nil store
	if _, err := (&HeadlessIOgridDeliverer{RunnerSID: "x"}).Deliver(ctx, msg); err == nil {
		t.Error("nil TaskStore accepted")
	}
	// empty runner sid
	if _, err := (&HeadlessIOgridDeliverer{TaskStore: store.Tasks()}).Deliver(ctx, msg); err == nil {
		t.Error("empty RunnerSID accepted")
	}
	// empty context id
	d := &HeadlessIOgridDeliverer{TaskStore: store.Tasks(), RunnerSID: "x"}
	if _, err := d.Deliver(ctx, a2a.Message{Role: "user", Kind: "message", Parts: []a2a.Part{{Kind: "text", Text: "x"}}}); err == nil {
		t.Error("empty ContextID accepted")
	}
}

func TestHeadlessIOgridDeliverer_Deliver_RejectsNonTextParts(t *testing.T) {
	t.Parallel()
	store := newE3Store(t)
	d := &HeadlessIOgridDeliverer{TaskStore: store.Tasks(), RunnerSID: "x"}
	task, err := d.Deliver(context.Background(), a2a.Message{
		Role: "user", Kind: "message", ContextID: "c",
		Parts: []a2a.Part{{Kind: "file"}},
	})
	if err == nil {
		t.Error("non-text Part accepted (should reject per a2a.ExtractText)")
	}
	if task == nil || task.Status.State != a2a.TaskStateFailed {
		t.Errorf("rejected Task = %+v, want state=failed", task)
	}
}

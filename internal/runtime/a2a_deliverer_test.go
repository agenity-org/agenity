package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/chepherd/chepherd/internal/a2a"
)

func TestA2ADeliverer_RejectsEmptyContextID(t *testing.T) {
	t.Parallel()
	d := NewA2ADeliverer(nil) // rt nil OK; we return before touching it
	if _, err := d.Deliver(context.Background(), a2a.Message{}); err == nil {
		t.Error("Deliver empty ContextID: want error, got nil")
	} else if !strings.Contains(err.Error(), "ContextID") {
		t.Errorf("Deliver empty ContextID err = %v, want mentions ContextID", err)
	}
}

func TestA2ADeliverer_TargetSessionNotFound(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d := NewA2ADeliverer(rt)
	task, err := d.Deliver(context.Background(), a2a.Message{
		ContextID: "nonexistent-session",
		Parts:     []a2a.Part{{Kind: "text", Text: "hi"}},
	})
	if err == nil {
		t.Error("Deliver nonexistent session: want error, got nil")
	}
	if task == nil || task.Status.State != a2a.TaskStateFailed {
		t.Errorf("failedTask = %+v, want state=failed", task)
	}
	if task.ContextID != "nonexistent-session" {
		t.Errorf("Task.ContextID = %q, want propagated", task.ContextID)
	}
}

func TestTaskIDOrGenerate_UsesCallerProvided(t *testing.T) {
	t.Parallel()
	got := taskIDOrGenerate("caller-supplied-task-id")
	if got != "caller-supplied-task-id" {
		t.Errorf("got %q, want caller-supplied-task-id", got)
	}
}

func TestTaskIDOrGenerate_GeneratesUUIDv7WhenEmpty(t *testing.T) {
	t.Parallel()
	got := taskIDOrGenerate("")
	if got == "" {
		t.Fatal("got empty string, want generated UUID")
	}
	// Parse + verify it's a valid UUID. Version 7 isn't strictly checked
	// (fallback to V4 on RNG failure is documented + acceptable); we
	// just want a UUID-format-valid id.
	parsed, err := uuid.Parse(got)
	if err != nil {
		t.Errorf("generated id %q is not a valid UUID: %v", got, err)
	}
	if parsed.Version() != 7 && parsed.Version() != 4 {
		t.Errorf("generated UUID version = %d, want 7 (or 4 fallback)", parsed.Version())
	}
}

func TestTaskIDOrGenerate_GeneratesUnique(t *testing.T) {
	t.Parallel()
	a := taskIDOrGenerate("")
	b := taskIDOrGenerate("")
	if a == b {
		t.Errorf("two auto-generated ids should differ; got %q twice", a)
	}
}

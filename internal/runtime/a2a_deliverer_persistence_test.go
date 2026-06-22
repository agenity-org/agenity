// internal/runtime/a2a_deliverer_persistence_test.go — v0.9.3 #225 row A4.
// Pins: A2ADeliverer.Deliver must persist the issued Task via
// TaskRepository.Save so GetTask/ListTasks see it after the call
// returns.
//
// Refs #307.
package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/agenity-org/agenity/internal/a2a"
	"github.com/agenity-org/agenity/internal/persistence"
)

func mkTestMessage(contextID, taskID, text string) a2a.Message {
	return a2a.Message{
		Role:      "user",
		Kind:      "message",
		ContextID: contextID,
		TaskID:    taskID,
		Parts:     []a2a.Part{{Kind: "text", Text: text}},
	}
}

// captureTaskRepo is a minimal persistence.TaskRepository that records
// every Save call so the test can assert what was persisted.
type captureTaskRepo struct {
	mu    sync.Mutex
	saved []*persistence.Task
}

func (r *captureTaskRepo) Get(_ context.Context, id string) (*persistence.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.saved {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, nil
}
func (r *captureTaskRepo) Save(_ context.Context, t *persistence.Task) error {
	r.mu.Lock()
	r.saved = append(r.saved, t)
	r.mu.Unlock()
	return nil
}
func (r *captureTaskRepo) List(_ context.Context, _ persistence.TaskListOpts) ([]*persistence.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*persistence.Task, len(r.saved))
	copy(out, r.saved)
	return out, nil
}
func (r *captureTaskRepo) Delete(_ context.Context, _ string) error { return nil }

func TestA2ADeliverer_SetTaskStore_NilByDefault(t *testing.T) {
	d := NewA2ADeliverer(nil)
	if d.taskStore != nil {
		t.Errorf("fresh A2ADeliverer.taskStore = %v, want nil", d.taskStore)
	}
	if d.runnerSID != "" {
		t.Errorf("fresh A2ADeliverer.runnerSID = %q, want \"\"", d.runnerSID)
	}
}

func TestA2ADeliverer_SetTaskStore_Assigns(t *testing.T) {
	repo := &captureTaskRepo{}
	d := NewA2ADeliverer(nil)
	d.SetTaskStore(repo, "test-runner-sid")
	if d.taskStore != repo {
		t.Errorf("SetTaskStore did not assign repo")
	}
	if d.runnerSID != "test-runner-sid" {
		t.Errorf("SetTaskStore runnerSID = %q, want test-runner-sid", d.runnerSID)
	}
}

func TestA2ADeliverer_SetTaskStore_NilDisablesPersistence(t *testing.T) {
	d := NewA2ADeliverer(nil)
	d.SetTaskStore(nil, "")
	// Should not panic when called with nil — back-compat exit path.
	// persistTask early-returns when taskStore is nil.
}

// Direct test of persistTask shape — bypasses Deliver's PTY round-trip
// (which needs a Runtime + Session to spawn). This isolates the A4
// persistence seam from A1's delivery seam.
func TestA2ADeliverer_persistTask_StampsRunnerSIDAndState(t *testing.T) {
	repo := &captureTaskRepo{}
	d := NewA2ADeliverer(nil)
	d.SetTaskStore(repo, "runner-A")

	msg := mkTestMessage("ctx-1", "task-1", "hello")
	task := d.workingTask(msg)
	d.persistTask(context.Background(), msg, task, "message/send")

	if len(repo.saved) != 1 {
		t.Fatalf("repo.saved len = %d, want 1", len(repo.saved))
	}
	got := repo.saved[0]
	if got.ID != task.ID {
		t.Errorf("saved.ID = %q, want %q", got.ID, task.ID)
	}
	if got.RunnerSID != "runner-A" {
		t.Errorf("saved.RunnerSID = %q, want runner-A", got.RunnerSID)
	}
	if got.State != "TASK_STATE_WORKING" {
		t.Errorf("saved.State = %q, want TASK_STATE_WORKING", got.State)
	}
	if got.Method != "message/send" {
		t.Errorf("saved.Method = %q, want message/send", got.Method)
	}
	if len(got.InputBlob) == 0 {
		t.Error("saved.InputBlob empty")
	}
	if len(got.OutputBlob) == 0 {
		t.Error("saved.OutputBlob empty")
	}
	if got.CreatedAt.IsZero() {
		t.Error("saved.CreatedAt zero")
	}
}

func TestA2ADeliverer_persistTask_FailedStateAlsoPersists(t *testing.T) {
	repo := &captureTaskRepo{}
	d := NewA2ADeliverer(nil)
	d.SetTaskStore(repo, "runner-B")

	msg := mkTestMessage("ctx-2", "task-2", "broken")
	failed := d.failedTask(msg, "test failure reason")
	d.persistTask(context.Background(), msg, failed, "message/send")

	if len(repo.saved) != 1 {
		t.Fatalf("repo.saved len = %d, want 1", len(repo.saved))
	}
	if repo.saved[0].State != "TASK_STATE_FAILED" {
		t.Errorf("failed Task persisted with State = %q, want TASK_STATE_FAILED", repo.saved[0].State)
	}
}

func TestA2ADeliverer_persistTask_NilStore_NoPanic(t *testing.T) {
	d := NewA2ADeliverer(nil)
	// taskStore stays nil — should silently no-op.
	msg := mkTestMessage("ctx-3", "task-3", "hi")
	task := d.workingTask(msg)
	d.persistTask(context.Background(), msg, task, "message/send")
	// If we got here, no panic — pass.
}

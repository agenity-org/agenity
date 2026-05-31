// internal/mcpserver/get_task_test.go — #473 Wave K2 unit tests for
// the chepherd.get_task MCP tool.
//
// Named assertions K2.G1-G5:
//
//	G1 — Happy path: caller matches task.ContextID → returns task
//	     envelope + input message
//	G2 — Forbidden: caller != task.ContextID → -32004
//	G3 — Not-found: unknown taskID → -32603
//	G4 — Missing arg: empty taskID → -32602
//	G5 — Store not wired: nil taskStore → -32000
//
// Refs #473.
package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/persistence"
)

// fakeTaskStore implements persistence.TaskRepository with an
// in-memory map. Returns ErrNotFound shape via nil + nil to match
// the get_task handler's expectation.
type fakeTaskStore struct {
	tasks map[string]*persistence.Task
}

func (f *fakeTaskStore) Get(_ context.Context, id string) (*persistence.Task, error) {
	t, ok := f.tasks[id]
	if !ok {
		return nil, nil
	}
	return t, nil
}
func (f *fakeTaskStore) Save(_ context.Context, _ *persistence.Task) error { return nil }
func (f *fakeTaskStore) List(_ context.Context, _ persistence.TaskListOpts) ([]*persistence.Task, error) {
	return nil, nil
}
func (f *fakeTaskStore) Delete(_ context.Context, _ string) error { return nil }

// dispatch helper — invokes tools/call with the named tool + args
// + caller. Returns the response error code (or 0 on success) +
// inner result map.
//
// For tools/call paths the dispatch wraps the result in an MCP
// content envelope (content[0].text = JSON-stringified inner). For
// isError=true the inner contains {"error": "<msg>"}. To extract
// the underlying handler's -32xxx code we go DIRECT through
// toolCallDirect — bypasses the content-wrapper but exposes the
// raw rpcErr code.
func dispatchGetTask(s *Server, caller, taskID string) (int, map[string]any) {
	s.lastCaller = caller
	args := json.RawMessage(`{"taskID":"` + taskID + `"}`)
	inner := s.toolCallDirect(nil, "get_task", args)
	if inner.Error != nil {
		return inner.Error.Code, nil
	}
	out, _ := inner.Result.(map[string]any)
	return 0, out
}

func seedTask(taskID, contextID, fromCaller string) *persistence.Task {
	msg := a2a.Message{
		Role:      "user",
		ContextID: contextID,
		Parts:     []a2a.Part{{Kind: "text", Text: "hello"}},
	}
	inputBlob, _ := json.Marshal(msg)
	task := &a2a.Task{
		ID:        taskID,
		ContextID: contextID,
		Kind:      "task",
		Status:    a2a.TaskStatus{State: a2a.TaskStateWorking},
	}
	outputBlob, _ := json.Marshal(task)
	return &persistence.Task{
		ID:         taskID,
		State:      "working",
		Method:     "message/send",
		InputBlob:  inputBlob,
		OutputBlob: outputBlob,
	}
}

func TestK2_G1_HappyPath_RecipientCallerSucceeds(t *testing.T) {
	s := New(nil)
	store := &fakeTaskStore{tasks: map[string]*persistence.Task{
		"task-1": seedTask("task-1", "runner-bob", "alpha"),
	}}
	s.SetTaskStore(store)

	code, inner := dispatchGetTask(s, "runner-bob", "task-1")
	if code != 0 {
		t.Fatalf("G1 FAIL: code = %d, want 0 (success); inner=%+v", code, inner)
	}
	if inner == nil {
		t.Fatalf("G1 FAIL: empty inner result")
	}
	taskEnv, _ := inner["task"].(map[string]any)
	if taskEnv == nil {
		t.Fatalf("G1 FAIL: inner.task missing; inner=%+v", inner)
	}
	if taskEnv["id"] != "task-1" {
		t.Errorf("G1 FAIL: task.id = %v, want task-1", taskEnv["id"])
	}
}

func TestK2_G2_Forbidden_NonRecipientCaller(t *testing.T) {
	s := New(nil)
	store := &fakeTaskStore{tasks: map[string]*persistence.Task{
		"task-1": seedTask("task-1", "runner-bob", "alpha"),
	}}
	s.SetTaskStore(store)

	code, _ := dispatchGetTask(s, "eve-attacker", "task-1")
	if code != -32004 {
		t.Errorf("G2 FAIL: code = %d, want -32004 (forbidden)", code)
	}
}

func TestK2_G3_NotFound_UnknownTaskID(t *testing.T) {
	s := New(nil)
	s.SetTaskStore(&fakeTaskStore{tasks: map[string]*persistence.Task{}})

	code, _ := dispatchGetTask(s, "anyone", "ghost-task")
	if code != -32603 {
		t.Errorf("G3 FAIL: code = %d, want -32603 (not found)", code)
	}
}

func TestK2_G4_MissingArg_EmptyTaskID(t *testing.T) {
	s := New(nil)
	s.SetTaskStore(&fakeTaskStore{tasks: map[string]*persistence.Task{}})

	code, _ := dispatchGetTask(s, "anyone", "")
	if code != -32602 {
		t.Errorf("G4 FAIL: code = %d, want -32602 (invalid params)", code)
	}
}

func TestK2_G5_StoreNotWired_TaskStoreNil(t *testing.T) {
	s := New(nil)
	// SetTaskStore intentionally NOT called.
	code, _ := dispatchGetTask(s, "anyone", "task-1")
	if code != -32000 {
		t.Errorf("G5 FAIL: code = %d, want -32000 (store not wired)", code)
	}
}

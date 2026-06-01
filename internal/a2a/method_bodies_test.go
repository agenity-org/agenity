// internal/a2a/method_bodies_test.go — v0.9.3 #277 regression coverage
// for the 10 method bodies. Uses an in-memory SQLite store + a fake
// AgentCard provider + a fake SubscribeFn so the suite exercises each
// handler end-to-end through the JSON-RPC envelope.
//
// Refs #277 #225.
package a2a

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

func newTestRouter(t *testing.T) (*Router, *MethodBodies) {
	t.Helper()
	store, err := sqlite.NewStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mb := &MethodBodies{
		Store:       store,
		AgentCardFn: func() AgentCard { return AgentCard{ProtocolVersion: "1.0.0", Name: "test-runner"} },
		RunnerSID:   "test-runner",
		SubscribeFn: func(taskID string) (string, error) { return "stream-" + taskID, nil },
	}
	r := NewRouter()
	if err := mb.Register(r); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return r, mb
}

func call(t *testing.T, r *Router, method string, params any) JSONRPCResponse {
	t.Helper()
	body, _ := json.Marshal(params)
	canonical := canonicalizeMethod(method)
	req := JSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`"1"`), Method: canonical, Params: body}
	h, ok := r.handlers[canonical]
	if !ok {
		t.Fatalf("call: no handler for %q (canonicalized from %q)", canonical, method)
	}
	return h(req)
}

// TestGetTask_NotFound pins the not-found error code.
func TestGetTask_NotFound(t *testing.T) {
	r, _ := newTestRouter(t)
	resp := call(t, r, "tasks/get", getTaskParams{TaskID: "missing"})
	if resp.Error == nil || resp.Error.Code != ErrCodeTaskNotFound {
		t.Errorf("expected -32001 TaskNotFound, got %+v", resp.Error)
	}
}

// TestSendStreamingMessage_PersistsAndStreams pins the happy path:
// task gets persisted, response carries streamID + initial Task.
func TestSendStreamingMessage_PersistsAndStreams(t *testing.T) {
	r, mb := newTestRouter(t)
	params := SendMessageParams{Message: Message{
		Role:      "user",
		ContextID: "ctx-1",
		Parts:     []Part{{Kind: "text", Text: "hello"}},
	}}
	resp := call(t, r, "message/stream", params)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result, ok := resp.Result.(subscribeResult)
	if !ok {
		t.Fatalf("expected subscribeResult, got %T", resp.Result)
	}
	if result.Task == nil || result.Task.ID == "" {
		t.Errorf("expected task with id, got %+v", result.Task)
	}
	if !strings.HasPrefix(result.StreamID, "stream-") {
		t.Errorf("expected streamId to start with stream-, got %q", result.StreamID)
	}
	// Persisted state must be SUBMITTED so a follow-up GetTask works.
	rec, err := mb.Store.Tasks().Get(context.Background(), result.Task.ID)
	if err != nil || rec == nil {
		t.Fatalf("persisted task not found: err=%v rec=%v", err, rec)
	}
	if rec.State != string(TaskStateSubmitted) {
		t.Errorf("expected state=submitted after streaming send, got %q", rec.State)
	}
}

// TestCancelTask_TerminalStateReturnsTaskNotCancelable — #576 pins the
// spec invariant: A2A v1.0 §3.1.5 + §5.4 mandate
// TaskNotCancelableError (-32002) when canceling a task already in
// a terminal state. Pre-#576 chepherd silently returned the task as
// success, masking the invalid transition.
func TestCancelTask_TerminalStateReturnsTaskNotCancelable(t *testing.T) {
	r, mb := newTestRouter(t)
	if err := mb.Store.Tasks().Save(context.Background(), seedTask("t-completed", string(TaskStateCompleted))); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resp := call(t, r, "tasks/cancel", cancelTaskParams{TaskID: "t-completed"})
	if resp.Error == nil || resp.Error.Code != ErrCodeTaskNotCancelable {
		t.Errorf("expected ErrCodeTaskNotCancelable (-32002), got %+v", resp.Error)
	}
}

// TestCancelTask_WorkingTransitionsToCanceled pins the happy path.
func TestCancelTask_WorkingTransitionsToCanceled(t *testing.T) {
	r, mb := newTestRouter(t)
	if err := mb.Store.Tasks().Save(context.Background(), seedTask("t-working", string(TaskStateWorking))); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resp := call(t, r, "tasks/cancel", cancelTaskParams{TaskID: "t-working"})
	result := resp.Result.(cancelTaskResult)
	if result.Task.Status.State != TaskStateCanceled {
		t.Errorf("expected CANCELED after cancel, got %q", result.Task.Status.State)
	}
	rec, _ := mb.Store.Tasks().Get(context.Background(), "t-working")
	if rec.State != string(TaskStateCanceled) {
		t.Errorf("persisted state should be CANCELED, got %q", rec.State)
	}
}

// TestPushNotificationConfigCRUD round-trips Set → Get → List → Delete.
func TestPushNotificationConfigCRUD(t *testing.T) {
	r, _ := newTestRouter(t)
	// Set
	setResp := call(t, r, "tasks/pushNotificationConfig/set", pushConfig{
		TaskID:     "task-x",
		URL:        "https://example.com/hook",
		SigningKey: "secret",
		Filters:    []string{"state-change"},
	})
	if setResp.Error != nil {
		t.Fatalf("Set: %+v", setResp.Error)
	}
	cfgID := setResp.Result.(setPushConfigResult).Config.ID
	if cfgID == "" {
		t.Fatal("Set returned empty id")
	}
	// SigningKey must NOT echo back (write-only secret).
	if setResp.Result.(setPushConfigResult).Config.SigningKey != "" {
		t.Errorf("server echoed signing key")
	}
	// Get
	getResp := call(t, r, "tasks/pushNotificationConfig/get", getPushConfigParams{ID: cfgID})
	if getResp.Error != nil {
		t.Fatalf("Get: %+v", getResp.Error)
	}
	if getResp.Result.(setPushConfigResult).Config.URL != "https://example.com/hook" {
		t.Errorf("Get URL mismatch: %+v", getResp.Result)
	}
	// List
	listResp := call(t, r, "tasks/pushNotificationConfig/list", listPushConfigsParams{TaskID: "task-x"})
	if listResp.Error != nil {
		t.Fatalf("List: %+v", listResp.Error)
	}
	if got := len(listResp.Result.(listPushConfigsResult).Configs); got != 1 {
		t.Errorf("List expected 1 config, got %d", got)
	}
	// Delete
	delResp := call(t, r, "tasks/pushNotificationConfig/delete", deletePushConfigParams{ID: cfgID})
	if delResp.Error != nil {
		t.Fatalf("Delete: %+v", delResp.Error)
	}
	if !delResp.Result.(deletePushConfigResult).OK {
		t.Errorf("Delete returned ok=false")
	}
	// Get-after-delete is not found.
	gone := call(t, r, "tasks/pushNotificationConfig/get", getPushConfigParams{ID: cfgID})
	// #630 — not-found uses -32602 InvalidParams, not -32001 TaskNotFound.
	if gone.Error == nil || gone.Error.Code != ErrCodeInvalidParams {
		t.Errorf("expected -32602 InvalidParams after delete, got %+v", gone.Error)
	}
}

// TestPushNotificationConfig_SpecNestedSet verifies #572: set accepts the
// A2A v1.0 spec-nested shape {"taskId":"...","pushNotificationConfig":{...}}.
func TestPushNotificationConfig_SpecNestedSet(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)
	// Spec-nested shape per a2a.proto.
	type nestedParams struct {
		TaskID                string `json:"taskId"`
		PushNotificationConfig struct {
			URL     string   `json:"url"`
			Filters []string `json:"filters,omitempty"`
		} `json:"pushNotificationConfig"`
	}
	p := nestedParams{TaskID: "task-nested"}
	p.PushNotificationConfig.URL = "https://hook.example/spec"
	p.PushNotificationConfig.Filters = []string{"state-change"}
	resp := call(t, r, "tasks/pushNotificationConfig/set", p)
	if resp.Error != nil {
		t.Fatalf("spec-nested set failed: %+v", resp.Error)
	}
	cfg := resp.Result.(setPushConfigResult).Config
	if cfg.ID == "" {
		t.Fatal("spec-nested set: empty id")
	}
	if cfg.URL != "https://hook.example/spec" {
		t.Errorf("spec-nested set: URL=%q, want https://hook.example/spec", cfg.URL)
	}
	if cfg.TaskID != "task-nested" {
		t.Errorf("spec-nested set: TaskID=%q, want task-nested", cfg.TaskID)
	}
}

// TestPushNotificationConfig_ListByTaskID verifies #572: list accepts
// {"taskId":"..."} — was broken (json tag was "id" not "taskId").
func TestPushNotificationConfig_ListByTaskID(t *testing.T) {
	t.Parallel()
	r, _ := newTestRouter(t)
	// Insert two configs for the same taskId.
	for i := range 2 {
		_ = i
		resp := call(t, r, "tasks/pushNotificationConfig/set", setPushConfigParams{
			TaskID: "list-task",
			URL:    "https://hook.example/list",
		})
		if resp.Error != nil {
			t.Fatalf("set for list: %+v", resp.Error)
		}
	}
	// List by taskId — previously the json tag was "id", so {"taskId":"..."} was ignored.
	listResp := call(t, r, "tasks/pushNotificationConfig/list",
		listPushConfigsParams{TaskID: "list-task"})
	if listResp.Error != nil {
		t.Fatalf("list: %+v", listResp.Error)
	}
	got := len(listResp.Result.(listPushConfigsResult).Configs)
	if got != 2 {
		t.Errorf("list by taskId: got %d configs, want 2", got)
	}
}

// TestGetAuthenticatedExtendedCard returns the wired card body.
// #483 Wave A4 turned the stub into an auth-gated extended-card
// handler — the test now requires a non-empty AuthSubject + asserts
// the ExtendedAgentCard wrapper shape.
func TestGetAuthenticatedExtendedCard(t *testing.T) {
	r, _ := newTestRouter(t)
	body, _ := json.Marshal(struct{}{})
	req := JSONRPCRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`"1"`),
		Method: "agent/getAuthenticatedExtendedCard", Params: body,
		AuthSubject: "operator",
	}
	resp := r.handlers[canonicalizeMethod("agent/getAuthenticatedExtendedCard")](req)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result, ok := resp.Result.(ExtendedAgentCard)
	if !ok {
		t.Fatalf("Result type = %T, want ExtendedAgentCard (direct per #570)", resp.Result)
	}
	if result.ProtocolVersion != "1.0.0" || result.Name != "test-runner" {
		t.Errorf("unexpected card payload: %+v", result)
	}
	if result.XChepherdAuth == nil || result.XChepherdAuth.Subject != "operator" {
		t.Errorf("x-chepherd-auth missing or wrong: %+v", result.XChepherdAuth)
	}
}

// TestStreamingMethods_WithoutSubscribeFn — streaming methods return
// ErrCodeUnsupportedOperation (-32004) when the runner has no SSE
// binding wired (SubscribeFn nil). Per A2A v1.0 §5.4 this is the
// correct mapping: "operation not supported on this runner".
func TestStreamingMethods_WithoutSubscribeFn(t *testing.T) {
	store, err := sqlite.NewStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mb := &MethodBodies{Store: store, AgentCardFn: func() AgentCard { return AgentCard{} }, SubscribeFn: nil}
	r := NewRouter()
	_ = mb.Register(r)
	for _, m := range []string{"message/stream", "tasks/resubscribe"} {
		resp := call(t, r, m, map[string]any{"taskId": "x", "message": map[string]any{"contextId": "c", "parts": []any{}}})
		if resp.Error == nil || resp.Error.Code != ErrCodeUnsupportedOperation {
			t.Errorf("%s should return ErrCodeUnsupportedOperation (-32004) without SubscribeFn, got %+v", m, resp.Error)
		}
	}
}

// ─── helpers ──────────────────────────────────────────────────────

func seedTask(id, state string) *persistence.Task {
	return &persistence.Task{
		ID:        id,
		RunnerSID: "test-runner",
		State:     state,
		Method:    "message/send",
		InputBlob: []byte(`{"role":"user","contextId":"ctx","parts":[]}`),
	}
}

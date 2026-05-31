// internal/a2a/method_bodies.go — v0.9.3 #277. Real implementations
// of the remaining 10 A2A v1.0 JSON-RPC methods (SendMessage is wired
// separately via Router.WireDeliverer in jsonrpc.go).
//
// All bodies are persistence-backed: GetTask / ListTasks / CancelTask
// read+write the TaskRepository; push-notification CRUD operates on
// PushNotificationConfigRepository; GetAuthenticatedExtendedCard
// returns the locally-published AgentCard. Streaming methods
// (SendStreamingMessage / ResubscribeTask) require SSE binding at the
// HTTP layer and are routed through SubscribeFunc — the JSON-RPC body
// returns an "initial" Task and the caller transitions to the SSE
// stream via the response's eventStream field.
//
// Refs #277 #225 (v0.9.3 epic).
package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/google/uuid"
)

// isNotFound matches the repository's "not found" sentinel without
// depending on a specific error type. Both sqlite and postgres
// repositories surface "not found" via fmt.Errorf, so a stringy match
// is the lowest-coupling check.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
}

// MethodBodies wires concrete handlers for the remaining 10 A2A
// methods against a Store, an AgentCard provider, and an optional
// streaming subscribe func.
type MethodBodies struct {
	Store       persistence.Store
	AgentCardFn func() AgentCard
	// RunnerSID is the chepherd-instance identifier stored on every
	// persisted Task row. Required by the SQLite TaskRepository's
	// not-empty check. Typically set to Runtime.InstanceUUID().
	RunnerSID string
	// SubscribeFn returns a streamID that the SSE handler at /sse/<id>
	// can attach to; nil means streaming methods return -32004 (not
	// supported on this runner — e.g. headless mode without SSE).
	SubscribeFn func(taskID string) (streamID string, err error)
	// PublishFn fans a state-transition event out through the broker
	// to all SSE subscribers + registered push-notification webhooks
	// (#482 Wave A3). nil disables out-of-band notification for the
	// state changes triggered by handler-driven paths (e.g. cancel);
	// the runtime's PTY-driven publishes are unaffected either way.
	PublishFn func(taskID string, ev StreamEvent)
}

// Register registers all 10 method bodies on the given Router. The
// SendMessage handler is registered separately via Router.WireDeliverer
// because it owns the Deliverer dependency — this Register replaces
// the scaffold stubs for the other 10.
func (m *MethodBodies) Register(r *Router) error {
	regs := []struct {
		name string
		h    methodHandler
	}{
		{"tasks/get", m.handleGetTask},
		{"tasks/list", m.handleListTasks},
		{"tasks/cancel", m.handleCancelTask},
		{"tasks/resubscribe", m.handleResubscribeTask},
		{"message/stream", m.handleSendStreamingMessage},
		{"tasks/pushNotificationConfig/set", m.handleSetTaskPushNotificationConfig},
		{"tasks/pushNotificationConfig/get", m.handleGetTaskPushNotificationConfig},
		{"tasks/pushNotificationConfig/list", m.handleListTaskPushNotificationConfigs},
		{"tasks/pushNotificationConfig/delete", m.handleDeleteTaskPushNotificationConfig},
		{"agent/getAuthenticatedExtendedCard", m.handleGetAuthenticatedExtendedCard},
	}
	for _, reg := range regs {
		if err := r.Register(reg.name, reg.h); err != nil {
			return fmt.Errorf("a2a.MethodBodies.Register %s: %w", reg.name, err)
		}
	}
	return nil
}

// ─── GetTask ──────────────────────────────────────────────────────

type getTaskParams struct {
	TaskID string `json:"taskId"`
}

type getTaskResult struct {
	Task *Task `json:"task"`
}

func (m *MethodBodies) handleGetTask(req JSONRPCRequest) JSONRPCResponse {
	var p getTaskParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResp(req.ID, ErrCodeInvalidParams, "decode GetTaskParams: "+err.Error())
	}
	if p.TaskID == "" {
		return errorResp(req.ID, ErrCodeInvalidParams, "taskId is required")
	}
	rec, err := m.Store.Tasks().Get(context.Background(), p.TaskID)
	if isNotFound(err) {
		return errorResp(req.ID, -32004, "task not found: "+p.TaskID)
	}
	if err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "TaskRepository.Get: "+err.Error())
	}
	task, err := decodeTask(rec)
	if err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "decode task: "+err.Error())
	}
	return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: getTaskResult{Task: task}}
}

// ─── ListTasks ────────────────────────────────────────────────────

type listTasksParams struct {
	State   string `json:"state,omitempty"`
	SinceID string `json:"sinceId,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type listTasksResult struct {
	Tasks []*Task `json:"tasks"`
}

func (m *MethodBodies) handleListTasks(req JSONRPCRequest) JSONRPCResponse {
	var p listTasksParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errorResp(req.ID, ErrCodeInvalidParams, "decode ListTasksParams: "+err.Error())
		}
	}
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}
	recs, err := m.Store.Tasks().List(context.Background(), persistence.TaskListOpts{
		State:   p.State,
		SinceID: p.SinceID,
		Limit:   p.Limit,
	})
	if err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "TaskRepository.List: "+err.Error())
	}
	out := make([]*Task, 0, len(recs))
	for _, rec := range recs {
		t, err := decodeTask(rec)
		if err != nil {
			continue // skip malformed rows
		}
		out = append(out, t)
	}
	return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: listTasksResult{Tasks: out}}
}

// ─── CancelTask ───────────────────────────────────────────────────

type cancelTaskParams struct {
	TaskID string `json:"taskId"`
	Reason string `json:"reason,omitempty"`
}

type cancelTaskResult struct {
	Task *Task `json:"task"`
}

func (m *MethodBodies) handleCancelTask(req JSONRPCRequest) JSONRPCResponse {
	var p cancelTaskParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResp(req.ID, ErrCodeInvalidParams, "decode CancelTaskParams: "+err.Error())
	}
	if p.TaskID == "" {
		return errorResp(req.ID, ErrCodeInvalidParams, "taskId is required")
	}
	ctx := context.Background()
	rec, err := m.Store.Tasks().Get(ctx, p.TaskID)
	if isNotFound(err) {
		return errorResp(req.ID, -32004, "task not found: "+p.TaskID)
	}
	if err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "TaskRepository.Get: "+err.Error())
	}
	// Terminal states can't be canceled — return current state without modification.
	if rec.State == string(TaskStateCompleted) || rec.State == string(TaskStateFailed) || rec.State == string(TaskStateCanceled) {
		t, _ := decodeTask(rec)
		return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: cancelTaskResult{Task: t}}
	}
	rec.State = string(TaskStateCanceled)
	rec.UpdatedAt = time.Now().UTC()
	if err := m.Store.Tasks().Save(ctx, rec); err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "TaskRepository.Save: "+err.Error())
	}
	t, err := decodeTask(rec)
	if err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "decode task after cancel: "+err.Error())
	}
	// #482 Wave A3 — fan the cancel state-transition out through the
	// broker so SSE subscribers see it AND any registered push-
	// notification webhooks fire. `done` semantics: cancel is a
	// terminal state per A2A v1.0, so the broker reaps the channel
	// after publishing.
	if m.PublishFn != nil {
		m.PublishFn(p.TaskID, StreamEvent{Type: "done", Task: t})
	}
	return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: cancelTaskResult{Task: t}}
}

// ─── ResubscribeTask + SendStreamingMessage ───────────────────────
// Both methods return an "initial" Task synchronously + a streamId
// the caller GETs at /a2a/stream/<id> to receive Server-Sent-Events
// for subsequent state transitions. When SubscribeFn is nil the
// runner is in non-streaming mode and these return -32004.

type subscribeResult struct {
	Task     *Task  `json:"task"`
	StreamID string `json:"streamId"`
}

type resubscribeParams struct {
	TaskID string `json:"taskId"`
}

func (m *MethodBodies) handleResubscribeTask(req JSONRPCRequest) JSONRPCResponse {
	if m.SubscribeFn == nil {
		return errorResp(req.ID, -32004, "streaming not supported on this runner (no SSE binding)")
	}
	var p resubscribeParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResp(req.ID, ErrCodeInvalidParams, "decode ResubscribeTaskParams: "+err.Error())
	}
	if p.TaskID == "" {
		return errorResp(req.ID, ErrCodeInvalidParams, "taskId is required")
	}
	rec, err := m.Store.Tasks().Get(context.Background(), p.TaskID)
	if isNotFound(err) {
		return errorResp(req.ID, -32004, "task not found: "+p.TaskID)
	}
	if err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "TaskRepository.Get: "+err.Error())
	}
	streamID, err := m.SubscribeFn(p.TaskID)
	if err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "SubscribeFn: "+err.Error())
	}
	t, _ := decodeTask(rec)
	return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: subscribeResult{Task: t, StreamID: streamID}}
}

func (m *MethodBodies) handleSendStreamingMessage(req JSONRPCRequest) JSONRPCResponse {
	if m.SubscribeFn == nil {
		return errorResp(req.ID, -32004, "streaming not supported on this runner (no SSE binding)")
	}
	var p SendMessageParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResp(req.ID, ErrCodeInvalidParams, "decode SendMessageParams: "+err.Error())
	}
	if p.Message.ContextID == "" {
		return errorResp(req.ID, ErrCodeInvalidParams, "message.contextId is required")
	}
	taskID := p.Message.TaskID
	if taskID == "" {
		taskID = uuid.NewString()
	}
	// Persist the initial task BEFORE returning the streamId so that
	// the SSE consumer's first GetTask race after subscribe can find a row.
	now := time.Now().UTC()
	inputBlob, _ := json.Marshal(p.Message)
	rec := &persistence.Task{
		ID:        taskID,
		RunnerSID: m.RunnerSID,
		State:     string(TaskStateSubmitted),
		Method:    "message/stream",
		InputBlob: inputBlob,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := m.Store.Tasks().Save(context.Background(), rec); err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "TaskRepository.Save: "+err.Error())
	}
	streamID, err := m.SubscribeFn(taskID)
	if err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "SubscribeFn: "+err.Error())
	}
	t, _ := decodeTask(rec)
	return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: subscribeResult{Task: t, StreamID: streamID}}
}

// ─── Push-notification config CRUD ────────────────────────────────

type pushConfig struct {
	ID         string   `json:"id,omitempty"`
	TaskID     string   `json:"taskId"`
	URL        string   `json:"url"`
	SigningKey string   `json:"signingKey,omitempty"` // base64 HMAC secret (server omits in responses)
	Filters    []string `json:"filters,omitempty"`
}

type setPushConfigResult struct {
	Config pushConfig `json:"config"`
}

func (m *MethodBodies) handleSetTaskPushNotificationConfig(req JSONRPCRequest) JSONRPCResponse {
	var p pushConfig
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResp(req.ID, ErrCodeInvalidParams, "decode SetTaskPushNotificationConfigParams: "+err.Error())
	}
	if p.TaskID == "" || p.URL == "" {
		return errorResp(req.ID, ErrCodeInvalidParams, "taskId and url are required")
	}
	cfg := &persistence.PushNotificationConfig{
		ID:         uuid.NewString(),
		TaskID:     p.TaskID,
		URL:        p.URL,
		SigningKey: []byte(p.SigningKey),
		Filters:    p.Filters,
		CreatedAt:  time.Now().UTC(),
	}
	if err := m.Store.PushConfigs().Save(context.Background(), cfg); err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "PushConfigs.Save: "+err.Error())
	}
	resp := pushConfig{ID: cfg.ID, TaskID: cfg.TaskID, URL: cfg.URL, Filters: cfg.Filters}
	// Never echo back the signing key (treat as write-only secret).
	return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: setPushConfigResult{Config: resp}}
}

type getPushConfigParams struct {
	ID string `json:"id"`
}

func (m *MethodBodies) handleGetTaskPushNotificationConfig(req JSONRPCRequest) JSONRPCResponse {
	var p getPushConfigParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResp(req.ID, ErrCodeInvalidParams, "decode GetTaskPushNotificationConfigParams: "+err.Error())
	}
	if p.ID == "" {
		return errorResp(req.ID, ErrCodeInvalidParams, "id is required")
	}
	cfg, err := m.Store.PushConfigs().Get(context.Background(), p.ID)
	if isNotFound(err) {
		return errorResp(req.ID, -32004, "push-notification-config not found: "+p.ID)
	}
	if err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "PushConfigs.Get: "+err.Error())
	}
	resp := pushConfig{ID: cfg.ID, TaskID: cfg.TaskID, URL: cfg.URL, Filters: cfg.Filters}
	return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: setPushConfigResult{Config: resp}}
}

type listPushConfigsParams struct {
	TaskID string `json:"taskId"`
}

type listPushConfigsResult struct {
	Configs []pushConfig `json:"configs"`
}

func (m *MethodBodies) handleListTaskPushNotificationConfigs(req JSONRPCRequest) JSONRPCResponse {
	var p listPushConfigsParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResp(req.ID, ErrCodeInvalidParams, "decode ListTaskPushNotificationConfigsParams: "+err.Error())
	}
	if p.TaskID == "" {
		return errorResp(req.ID, ErrCodeInvalidParams, "taskId is required")
	}
	cfgs, err := m.Store.PushConfigs().List(context.Background(), p.TaskID)
	if err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "PushConfigs.List: "+err.Error())
	}
	out := make([]pushConfig, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, pushConfig{ID: c.ID, TaskID: c.TaskID, URL: c.URL, Filters: c.Filters})
	}
	return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: listPushConfigsResult{Configs: out}}
}

type deletePushConfigParams struct {
	ID string `json:"id"`
}

type deletePushConfigResult struct {
	OK bool `json:"ok"`
}

func (m *MethodBodies) handleDeleteTaskPushNotificationConfig(req JSONRPCRequest) JSONRPCResponse {
	var p deletePushConfigParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResp(req.ID, ErrCodeInvalidParams, "decode DeleteTaskPushNotificationConfigParams: "+err.Error())
	}
	if p.ID == "" {
		return errorResp(req.ID, ErrCodeInvalidParams, "id is required")
	}
	if err := m.Store.PushConfigs().Delete(context.Background(), p.ID); err != nil {
		return errorResp(req.ID, ErrCodeInternalError, "PushConfigs.Delete: "+err.Error())
	}
	return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: deletePushConfigResult{OK: true}}
}

// ─── GetAuthenticatedExtendedCard ─────────────────────────────────
// Returns this runner's AgentCard. "Extended" implies enriched detail
// only visible to authenticated callers (peer trust list, RBAC scope
// hints, etc.). v0.9.3 returns the canonical card; future revs can
// scope extra fields by caller identity.

type getAgentCardResult struct {
	Card AgentCard `json:"card"`
}

// handleGetAuthenticatedExtendedCard moved to extended_card.go for
// #483 Wave A4. The body now performs the full auth-gate + grant
// lookup + extension construction. getAgentCardResult remains
// referenced by the public AgentCard path tests.
var _ = getAgentCardResult{}

// ─── decodeTask ──────────────────────────────────────────────────
// Translates a persistence.Task row into the A2A wire Task. The
// History + Artifacts blobs are JSON-encoded in the Output column;
// state lives in State; ContextID lives in the InputBlob's Message.

func decodeTask(rec *persistence.Task) (*Task, error) {
	t := &Task{
		ID:     rec.ID,
		Status: TaskStatus{State: TaskState(rec.State)},
		Kind:   "task",
	}
	if len(rec.InputBlob) > 0 {
		var msg Message
		if err := json.Unmarshal(rec.InputBlob, &msg); err == nil {
			t.ContextID = msg.ContextID
			t.History = []Message{msg}
		}
	}
	if len(rec.OutputBlob) > 0 {
		var out struct {
			Artifacts []Artifact `json:"artifacts"`
			History   []Message  `json:"history"`
		}
		if err := json.Unmarshal(rec.OutputBlob, &out); err == nil {
			if len(out.History) > 0 {
				t.History = append(t.History, out.History...)
			}
			t.Artifacts = out.Artifacts
		}
	}
	return t, nil
}

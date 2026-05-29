package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractText_OnlyTextParts(t *testing.T) {
	t.Parallel()
	got, err := ExtractText(Message{Parts: []Part{
		{Kind: "text", Text: "hello "},
		{Kind: "text", Text: "world"},
	}})
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestExtractText_RejectsFilePart(t *testing.T) {
	t.Parallel()
	_, err := ExtractText(Message{Parts: []Part{
		{Kind: "file", File: &FilePayload{Name: "x.txt"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "FilePart") && !strings.Contains(err.Error(), "v0.9.2 scaffold") {
		t.Errorf("FilePart err = %v, want unsupported-Kind", err)
	}
}

func TestExtractText_EmptyParts(t *testing.T) {
	t.Parallel()
	got, err := ExtractText(Message{})
	if err != nil {
		t.Errorf("empty parts: err=%v", err)
	}
	if got != "" {
		t.Errorf("empty parts: got %q, want \"\"", got)
	}
}

// fakeDeliverer captures the message handed to Deliver + returns a
// canned Task. Used to verify the WireDeliverer handler decodes
// SendMessageParams correctly and propagates the Task.
type fakeDeliverer struct {
	captured Message
	want     *Task
	err      error
}

func (f *fakeDeliverer) Deliver(_ context.Context, msg Message) (*Task, error) {
	f.captured = msg
	return f.want, f.err
}

func TestWireDeliverer_DispatchesSendMessage(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	want := &Task{ID: "sess-1", Status: TaskStatus{State: TaskStateWorking}}
	deliverer := &fakeDeliverer{want: want}
	if err := r.WireDeliverer(deliverer); err != nil {
		t.Fatalf("WireDeliverer: %v", err)
	}
	srv := httptest.NewServer(r)
	defer srv.Close()

	body, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "SendMessage",
		Params: jsonRaw(t, SendMessageParams{Message: Message{
			Role:   "user",
			TaskID: "sess-1",
			Parts:  []Part{{Kind: "text", Text: "ping"}},
		}}),
	})
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var got JSONRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %+v", got.Error)
	}
	if deliverer.captured.TaskID != "sess-1" {
		t.Errorf("Deliverer received TaskID=%q, want sess-1", deliverer.captured.TaskID)
	}
}

func TestWireDeliverer_RejectsEmptyTaskID(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	if err := r.WireDeliverer(&fakeDeliverer{}); err != nil {
		t.Fatalf("WireDeliverer: %v", err)
	}
	srv := httptest.NewServer(r)
	defer srv.Close()

	body, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "SendMessage",
		Params: jsonRaw(t, SendMessageParams{Message: Message{
			Role:  "user",
			Parts: []Part{{Kind: "text", Text: "ping"}},
		}}),
	})
	resp, _ := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	defer resp.Body.Close()
	var got JSONRPCResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.Error == nil || got.Error.Code != ErrCodeInvalidParams {
		t.Errorf("error = %+v, want code -32602 invalid params", got.Error)
	}
	if !strings.Contains(got.Error.Message, "taskId") {
		t.Errorf("message = %q, want mentions taskId", got.Error.Message)
	}
}

func TestWireDeliverer_PropagatesDelivererError(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	deliverer := &fakeDeliverer{err: errors.New("simulated deliver failure")}
	_ = r.WireDeliverer(deliverer)
	srv := httptest.NewServer(r)
	defer srv.Close()

	body, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "SendMessage",
		Params: jsonRaw(t, SendMessageParams{Message: Message{
			Role: "user", TaskID: "sess-x",
			Parts: []Part{{Kind: "text", Text: "ping"}},
		}}),
	})
	resp, _ := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	defer resp.Body.Close()
	var got JSONRPCResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.Error == nil || got.Error.Code != ErrCodeInternalError {
		t.Errorf("error = %+v, want -32603", got.Error)
	}
	if !strings.Contains(got.Error.Message, "simulated deliver failure") {
		t.Errorf("error message = %q, want propagated", got.Error.Message)
	}
}

func jsonRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("jsonRaw marshal: %v", err)
	}
	return b
}

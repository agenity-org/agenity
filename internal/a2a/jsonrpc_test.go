package a2a

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestA2AMethodNames_AllElevenPascalCase(t *testing.T) {
	t.Parallel()
	want := []string{
		"SendMessage",
		"SendStreamingMessage",
		"GetTask",
		"ListTasks",
		"CancelTask",
		"ResubscribeTask",
		"CreateTaskPushNotificationConfig",
		"GetTaskPushNotificationConfig",
		"ListTaskPushNotificationConfigs",
		"DeleteTaskPushNotificationConfig",
		"GetAuthenticatedExtendedCard",
	}
	got := A2AMethodNames()
	if len(got) != 11 {
		t.Fatalf("len(A2AMethodNames) = %d, want 11", len(got))
	}
	for i, m := range got {
		if m != want[i] {
			t.Errorf("[%d] = %q, want %q", i, m, want[i])
		}
		// PascalCase guard: first letter upper, no underscore.
		if m[0] < 'A' || m[0] > 'Z' || strings.Contains(m, "_") {
			t.Errorf("method %q not PascalCase", m)
		}
	}
}

func TestRouter_StubReturns_InternalError(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	srv := httptest.NewServer(r)
	defer srv.Close()

	body, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "SendMessage",
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
	if got.Error == nil || got.Error.Code != ErrCodeInternalError {
		t.Errorf("error = %+v, want code -32603", got.Error)
	}
	if !strings.Contains(got.Error.Message, "scaffold") {
		t.Errorf("message = %q, want 'scaffold' marker", got.Error.Message)
	}
}

func TestRouter_RejectsUnknownMethod(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	srv := httptest.NewServer(r)
	defer srv.Close()

	body, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "send_message", // snake_case is wrong
	})
	resp, _ := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	defer resp.Body.Close()
	var got JSONRPCResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.Error == nil || got.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("error = %+v, want code -32601 (method not found)", got.Error)
	}
}

func TestRouter_RegisterOverrideStub(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	called := false
	err := r.Register("SendMessage", func(req JSONRPCRequest) JSONRPCResponse {
		called = true
		return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: "ok"}
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	srv := httptest.NewServer(r)
	defer srv.Close()

	body, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "SendMessage",
	})
	_, _ = http.Post(srv.URL, "application/json", bytes.NewReader(body))
	if !called {
		t.Error("registered handler not invoked")
	}
}

func TestRouter_RegisterUnknownReturnsError(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	err := r.Register("NotASpecMethod", func(JSONRPCRequest) JSONRPCResponse {
		return JSONRPCResponse{}
	})
	if err == nil {
		t.Error("Register unknown method should error")
	}
}

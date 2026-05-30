package a2a

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestA2AMethodNames_AllElevenSpecWireShape — #291 contract: every
// canonical A2A v1.0 method name in A2AMethodNames() matches the
// spec's wire shape (slash + camelCase, lowercase first segment).
// Pre-#291 these were PascalCase and broke real-world interop with
// Google's A2A SDK + spec-compliant peers.
func TestA2AMethodNames_AllElevenSpecWireShape(t *testing.T) {
	t.Parallel()
	want := []string{
		"message/send",
		"message/stream",
		"tasks/get",
		"tasks/list",
		"tasks/cancel",
		"tasks/resubscribe",
		"tasks/pushNotificationConfig/set",
		"tasks/pushNotificationConfig/get",
		"tasks/pushNotificationConfig/list",
		"tasks/pushNotificationConfig/delete",
		"agent/getAuthenticatedExtendedCard",
	}
	got := A2AMethodNames()
	if len(got) != 11 {
		t.Fatalf("len(A2AMethodNames) = %d, want 11", len(got))
	}
	for i, m := range got {
		if m != want[i] {
			t.Errorf("[%d] = %q, want %q", i, m, want[i])
		}
		// Spec wire-shape guard: lowercase first letter, must contain
		// '/', no underscores.
		if m[0] < 'a' || m[0] > 'z' {
			t.Errorf("method %q first letter not lowercase — spec mandates lowercase first segment", m)
		}
		if !strings.Contains(m, "/") {
			t.Errorf("method %q missing '/' — spec mandates slash-separated namespacing", m)
		}
		if strings.Contains(m, "_") {
			t.Errorf("method %q contains underscore — spec uses camelCase for multi-word segments", m)
		}
	}
}

// TestRouter_AllElevenSpecMethodsAreNotMethodNotFound — defense-in-depth
// per #291 RCA: every spec method name must NOT return -32601 against
// a fresh Router. This is the integration gate whose ABSENCE in #208
// let the PascalCase scaffold ship as "spec-compliant" for 22 PRs.
func TestRouter_AllElevenSpecMethodsAreNotMethodNotFound(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	srv := httptest.NewServer(r)
	defer srv.Close()
	for _, method := range A2AMethodNames() {
		body, _ := json.Marshal(JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      json.RawMessage(`1`),
			Method:  method,
		})
		resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", method, err)
		}
		var got JSONRPCResponse
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode %s: %v", method, err)
		}
		resp.Body.Close()
		if got.Error != nil && got.Error.Code == ErrCodeMethodNotFound {
			t.Errorf("method %q returned -32601 method-not-found — A2A spec-compliance broken", method)
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
		Method:  "message/send",
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
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
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
	err := r.Register("message/send", func(req JSONRPCRequest) JSONRPCResponse {
		called = true
		return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: "ok"}
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	srv := httptest.NewServer(r)
	defer srv.Close()

	body, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "message/send",
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

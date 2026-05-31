package a2a

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestA2AMethodNames_AllElevenSpecPascalCase — #568 contract: every
// canonical A2A v1.0 method name in A2AMethodNames() matches the
// spec's PascalCase wire shape per A2A v1.0 §9.1 + §5.3 + a2a.proto
// service definition. This supersedes the #291 slash-camelCase pin —
// #291 misread the spec (probably mapped from REST endpoint paths) and
// shipped a regression; #568 reverts to the spec-correct PascalCase
// form aligned with the canonical a2a-python SDK
// (a2aproject/a2a-python). Inbound slash-camelCase from stale a2a-js
// clients is still accepted via MethodAliases() — covered by the
// separate alias test below.
//
// Refs #291 #561 #568.
func TestA2AMethodNames_AllElevenSpecPascalCase(t *testing.T) {
	t.Parallel()
	want := []string{
		"SendMessage",
		"SendStreamingMessage",
		"GetTask",
		"ListTasks",
		"CancelTask",
		"SubscribeToTask",
		"CreateTaskPushNotificationConfig",
		"GetTaskPushNotificationConfig",
		"ListTaskPushNotificationConfigs",
		"DeleteTaskPushNotificationConfig",
		"GetExtendedAgentCard",
	}
	got := A2AMethodNames()
	if len(got) != 11 {
		t.Fatalf("len(A2AMethodNames) = %d, want 11", len(got))
	}
	for i, m := range got {
		if m != want[i] {
			t.Errorf("[%d] = %q, want %q", i, m, want[i])
		}
		// Spec wire-shape guard: PascalCase per §9.1 — uppercase first
		// letter, no slash, no underscore.
		if m[0] < 'A' || m[0] > 'Z' {
			t.Errorf("method %q first letter not uppercase — spec §9.1 mandates PascalCase", m)
		}
		if strings.Contains(m, "/") {
			t.Errorf("method %q contains '/' — that's the slash-camelCase legacy form; spec §9.1 PascalCase has no separator", m)
		}
		if strings.Contains(m, "_") {
			t.Errorf("method %q contains underscore — spec PascalCase has no underscores", m)
		}
	}
}

// TestMethodAliases_AllElevenLegacyFormsResolve — #568 alias contract:
// every slash-camelCase legacy form (pre-#568 chepherd, stale a2a-js)
// resolves through MethodAliases() to its PascalCase canonical name.
// The Agent Card publishes this map verbatim via
// x-chepherd-method-aliases so peers discover the dual acceptance.
func TestMethodAliases_AllElevenLegacyFormsResolve(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"message/send":                        "SendMessage",
		"message/stream":                      "SendStreamingMessage",
		"tasks/get":                           "GetTask",
		"tasks/list":                          "ListTasks",
		"tasks/cancel":                        "CancelTask",
		"tasks/resubscribe":                   "SubscribeToTask",
		"tasks/pushNotificationConfig/set":    "CreateTaskPushNotificationConfig",
		"tasks/pushNotificationConfig/get":    "GetTaskPushNotificationConfig",
		"tasks/pushNotificationConfig/list":   "ListTaskPushNotificationConfigs",
		"tasks/pushNotificationConfig/delete": "DeleteTaskPushNotificationConfig",
		"agent/getAuthenticatedExtendedCard":  "GetExtendedAgentCard",
	}
	got := MethodAliases()
	if len(got) != 11 {
		t.Fatalf("len(MethodAliases) = %d, want 11", len(got))
	}
	canonical := map[string]bool{}
	for _, n := range A2AMethodNames() {
		canonical[n] = true
	}
	for alias, target := range want {
		if got[alias] != target {
			t.Errorf("MethodAliases()[%q] = %q, want %q", alias, got[alias], target)
		}
		if !canonical[target] {
			t.Errorf("alias target %q for %q not in A2AMethodNames()", target, alias)
		}
		if canonicalizeMethod(alias) != target {
			t.Errorf("canonicalizeMethod(%q) = %q, want %q", alias, canonicalizeMethod(alias), target)
		}
	}
}

// TestRouter_AllElevenSpecMethodsAreNotMethodNotFound — defense-in-depth
// per #561 RCA: every spec PascalCase method name must NOT return
// -32601 against a fresh Router. This is the integration gate whose
// ABSENCE in #208 let the PascalCase scaffold ship as "spec-compliant"
// for 22 PRs and then #291's removed-PascalCase regression slip
// through. With #568 this test asserts the SPEC form again — and the
// sister TestRouter_AllElevenAliasFormsAlsoDispatch asserts the alias
// path also resolves so we don't regress legacy slash-camelCase
// clients.
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
			t.Errorf("PascalCase method %q returned -32601 method-not-found — A2A v1.0 §9.1 spec-compliance broken", method)
		}
	}
}

// TestRouter_AllElevenAliasFormsAlsoDispatch — #568 alias-path
// integration gate. Every slash-camelCase legacy form must dispatch
// the same handler as its PascalCase canonical name (no -32601).
// This is what preserves interop with stale a2a-js clients + every
// chepherd test/binary that shipped between #291 and #568.
func TestRouter_AllElevenAliasFormsAlsoDispatch(t *testing.T) {
	t.Parallel()
	r := NewRouter()
	srv := httptest.NewServer(r)
	defer srv.Close()
	for alias := range MethodAliases() {
		body, _ := json.Marshal(JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      json.RawMessage(`1`),
			Method:  alias,
		})
		resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", alias, err)
		}
		var got JSONRPCResponse
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode %s: %v", alias, err)
		}
		resp.Body.Close()
		if got.Error != nil && got.Error.Code == ErrCodeMethodNotFound {
			t.Errorf("alias method %q returned -32601 — alias map broken", alias)
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

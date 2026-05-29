// internal/a2a/routes_auth_test.go — v0.9.3 #225 row B1.
// Pins the /jsonrpc endpoint auth-gate behaviour added by
// AuthMiddleware in routes.go.
//
// Refs #225 #277.
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

type fakeValidator struct {
	want   string
	reject error
}

func (f *fakeValidator) Validate(_ context.Context, token string) (string, error) {
	if f.reject != nil {
		return "", f.reject
	}
	if token != f.want {
		return "", errors.New("unexpected token")
	}
	return "operator", nil
}

func TestAuthMiddleware_MissingAuthorization(t *testing.T) {
	mux := http.NewServeMux()
	r := NewRouter()
	RegisterRoutes(mux, &AgentCard{}, r, &fakeValidator{want: "good"})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/jsonrpc", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"GetTask"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("expected Bearer WWW-Authenticate, got %q", got)
	}
	var rpc JSONRPCResponse
	_ = json.NewDecoder(resp.Body).Decode(&rpc)
	if rpc.Error == nil || rpc.Error.Code != -32001 {
		t.Errorf("expected -32001 in body, got %+v", rpc.Error)
	}
}

func TestAuthMiddleware_BadToken(t *testing.T) {
	mux := http.NewServeMux()
	r := NewRouter()
	RegisterRoutes(mux, &AgentCard{}, r, &fakeValidator{want: "good"})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/jsonrpc",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"GetTask"}`))
	req.Header.Set("Authorization", "Bearer wrong-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 on bad token, got %d", resp.StatusCode)
	}
}

func TestAuthMiddleware_HappyPath(t *testing.T) {
	mux := http.NewServeMux()
	r := NewRouter()
	// Replace the GetTask stub with a handler that confirms it ran.
	called := false
	_ = r.Register("GetTask", func(req JSONRPCRequest) JSONRPCResponse {
		called = true
		return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]string{"ok": "yes"}}
	})
	RegisterRoutes(mux, &AgentCard{}, r, &fakeValidator{want: "good"})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/jsonrpc",
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"GetTask"}`)))
	req.Header.Set("Authorization", "Bearer good")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if !called {
		t.Errorf("expected GetTask handler to fire on authenticated request")
	}
}

func TestAuthMiddleware_NilValidatorIsDevPassthrough(t *testing.T) {
	mux := http.NewServeMux()
	r := NewRouter()
	called := false
	_ = r.Register("GetTask", func(req JSONRPCRequest) JSONRPCResponse {
		called = true
		return JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]string{"ok": "yes"}}
	})
	RegisterRoutes(mux, &AgentCard{}, r, nil) // no auth — back-compat

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// No Authorization header — should pass.
	resp, err := http.Post(srv.URL+"/jsonrpc", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"GetTask"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 in dev passthrough, got %d", resp.StatusCode)
	}
	if !called {
		t.Errorf("expected GetTask handler to fire when no validator wired")
	}
}

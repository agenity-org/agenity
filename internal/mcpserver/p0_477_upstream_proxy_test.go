// internal/mcpserver/p0_477_upstream_proxy_test.go pins the v0.9.4
// §22 MCP upstream-proxy seam (#477 Wave M1) — when chepherd-runner
// sets SetUpstreamProxy, every JSON-RPC dispatch is forwarded
// through the proxy fn instead of routed to the local tool catalog.
// chepherd-daemon's default (proxy nil) keeps local dispatch
// unchanged.
//
// Refs #477 V0.9.2-ARCHITECTURE.md §22.
package mcpserver

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWaveM1_UpstreamProxy_InterceptsDispatch(t *testing.T) {
	t.Parallel()
	srv := New(nil) // rt=nil — runner-style construction
	var calls int32
	srv.SetUpstreamProxy(func(method string, params json.RawMessage, agent string) (any, int, string) {
		atomic.AddInt32(&calls, 1)
		if method != "tools/list" {
			return nil, -32601, "unexpected method in test: " + method
		}
		return map[string]any{"tools": []any{map[string]any{"name": "fake.tool"}}}, 0, ""
	})

	req := &rpcReq{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list"}
	resp := srv.dispatch(req)
	if resp.Error != nil {
		t.Fatalf("dispatch via proxy returned error: %+v", resp.Error)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("proxy fn called %d times, want 1", calls)
	}
	result, _ := resp.Result.(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Errorf("proxy result not propagated: %v", resp.Result)
	}
}

func TestWaveM1_UpstreamProxy_PropagatesErrorEnvelope(t *testing.T) {
	t.Parallel()
	srv := New(nil)
	srv.SetUpstreamProxy(func(_ string, _ json.RawMessage, _ string) (any, int, string) {
		return nil, -32602, "invalid params from upstream"
	})

	req := &rpcReq{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call"}
	resp := srv.dispatch(req)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("expected upstream error code, got %+v", resp)
	}
	if !strings.Contains(resp.Error.Message, "invalid params from upstream") {
		t.Errorf("error message not propagated: %s", resp.Error.Message)
	}
}

func TestWaveM1_NilProxy_KeepsLocalDispatch(t *testing.T) {
	t.Parallel()
	srv := New(nil)
	// No SetUpstreamProxy call — local dispatch should run.
	req := &rpcReq{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize"}
	resp := srv.dispatch(req)
	if resp.Error != nil {
		t.Fatalf("local initialize via nil proxy errored: %+v", resp.Error)
	}
	if resp.Result == nil {
		t.Fatal("local dispatch returned no result")
	}
}

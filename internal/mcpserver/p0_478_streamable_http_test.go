// internal/mcpserver/p0_478_streamable_http_test.go pins the
// Anthropic MCP Streamable HTTP transport handler at /mcp (#478
// Wave M2). The shape these tests assert is the one live-verified
// against claude-code 2.1.148 — see streamable_http.go's package
// comment for the empirical premise + the e2e in cmd/runner that
// drives the actual claude binary.
//
// Refs #478 V0.9.2-ARCHITECTURE.md §22.
package mcpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newStreamableTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := New(nil)
	// No auth so tests can POST without bearer setup.
	mux := srv.buildMux()
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return httpSrv
}

func TestWaveM2_StreamablePOST_ReturnsJSONRPCWithSessionID(t *testing.T) {
	t.Parallel()
	srv := newStreamableTestServer(t)
	body := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get(mcpSessionIDHeader) == "" {
		t.Errorf("missing Mcp-Session-Id header (spec requires it)")
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json prefix", ct)
	}
	out, _ := io.ReadAll(resp.Body)
	var env struct {
		JSONRPC string `json:"jsonrpc"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if env.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", env.JSONRPC)
	}
	if env.Result.ProtocolVersion == "" {
		t.Errorf("initialize result.protocolVersion empty: %s", out)
	}
}

func TestWaveM2_StreamablePOST_NotificationReturns202NoBody(t *testing.T) {
	t.Parallel()
	srv := newStreamableTestServer(t)
	body := bytes.NewBufferString(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (notifications/* per spec)", resp.StatusCode)
	}
	out, _ := io.ReadAll(resp.Body)
	if len(out) > 0 {
		t.Errorf("body should be empty on notification ack, got %q", out)
	}
}

func TestWaveM2_StreamablePOST_EchoesClientSessionID(t *testing.T) {
	t.Parallel()
	srv := newStreamableTestServer(t)
	body := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(mcpSessionIDHeader, "client-supplied-sid")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if got := resp.Header.Get(mcpSessionIDHeader); got != "client-supplied-sid" {
		t.Errorf("server should echo client Mcp-Session-Id, got %q", got)
	}
}

func TestWaveM2_StreamableGET_OpensSSEStream(t *testing.T) {
	t.Parallel()
	srv := newStreamableTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	// Read the opening comment frame so we know the stream is live.
	buf := make([]byte, 128)
	n, _ := resp.Body.Read(buf)
	if n == 0 || !strings.HasPrefix(string(buf[:n]), ":") {
		t.Errorf("expected leading comment frame, got %q", buf[:n])
	}
}

func TestWaveM2_StreamableDELETE_ReturnsNoContent(t *testing.T) {
	t.Parallel()
	srv := newStreamableTestServer(t)
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/mcp", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

func TestWaveM2_StreamableRejectsUnknownMethod(t *testing.T) {
	t.Parallel()
	srv := newStreamableTestServer(t)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/mcp", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestWaveM2_AddHTTPListener_BindsAdditionalAddr(t *testing.T) {
	t.Parallel()
	s := New(nil)
	// Bind two ephemeral TCP listeners; second is the addition.
	if err := s.StartHTTP("127.0.0.1:0"); err != nil {
		t.Fatalf("StartHTTP: %v", err)
	}
	defer s.stopHTTP()
	if err := s.AddHTTPListener("127.0.0.1:0"); err != nil {
		t.Fatalf("AddHTTPListener: %v", err)
	}
	addrs := s.ExtraListenerAddrs()
	if len(addrs) != 1 {
		t.Fatalf("ExtraListenerAddrs len = %d, want 1", len(addrs))
	}
	// Hit /mcp/healthz via the EXTRA listener to prove it serves
	// the same handler.
	resp, err := http.Get("http://" + addrs[0] + "/mcp/healthz")
	if err != nil {
		t.Fatalf("GET /mcp/healthz on extra listener: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status on extra listener = %d, want 200", resp.StatusCode)
	}
}

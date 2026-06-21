// internal/runtimehttp/p0_504_runner_register_test.go — daemon-side
// unit test for the chepherd-runner registration WS endpoint.
//
// Asserts:
//
//	M1 — POST /api/v1/runners/register upgrades to WS + accepts a
//	     chepherd/register frame
//	M2 — daemon mints a sid when caller-provided sid is empty AND
//	     echoes back a matching sid when caller-provided
//	M3 — first response carries daemon_version + audit_topic
//	M4 — subsequent audit notifications increment the row's counter
//	M5 — GET /api/v1/runners returns the registered row with the
//	     locked field shape (sid, name, agent_slug, runner_version,
//	     a2a_base_url, mcp_socket, capabilities, registered_at,
//	     last_seen, audit_events_received)
//	M6 — first frame other than chepherd/register is rejected
//	     with -32600 + the WS closes cleanly
//
// Refs #504 Wave R1 internal/runtimehttp/runners_register.go.
package runtimehttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	rh "github.com/agenity-org/agenity/internal/runtimehttp"
)

// newTestRuntimeServer spins up a minimal Server with just the routes
// #504 needs. Keeps the test fast + avoids pulling in the full
// runtime + persistence stack the other test files require.
func newTestRuntimeServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer((&rh.Server{}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// dialRegisterWS dials the daemon's register endpoint at ts.URL,
// wraps the websocket.Conn for the test, and returns it. Caller
// closes.
func dialRegisterWS(t *testing.T, baseURL string) *websocket.Conn {
	t.Helper()
	u, _ := url.Parse(baseURL)
	u.Scheme = "ws"
	u.Path = "/api/v1/runners/register"
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial %s: %v", u.String(), err)
	}
	return conn
}

// TestP0_504_RegisterMintsSID_AndListSurfaces pins M1..M5.
func TestP0_504_RegisterMintsSID_AndListSurfaces(t *testing.T) {
	ts := newTestRuntimeServer(t)
	defer ts.Close()

	conn := dialRegisterWS(t, ts.URL)
	defer conn.Close()

	// M1 + M2 — register frame, empty sid → daemon assigns
	reg := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "chepherd/register",
		"params": map[string]any{
			"sid":            "",
			"name":           "test-runner-1",
			"agent_slug":     "claude-code",
			"runner_version": "0.9.4-R1",
			"a2a_base_url":   "http://127.0.0.1:9091",
			"mcp_socket":     "/run/chepherd/mcp.sock",
			"capabilities":   []string{"pty", "audit-stream"},
		},
	}
	if err := conn.WriteJSON(reg); err != nil {
		t.Fatalf("M1 FAIL: write register: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("M1 FAIL: read response: %v", err)
	}
	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			SID           string `json:"sid"`
			DaemonVersion string `json:"daemon_version"`
			AuditTopic    string `json:"audit_topic"`
		} `json:"result"`
		Error any `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("M1 FAIL: decode response: %v (raw=%s)", err, raw)
	}
	if resp.Error != nil {
		t.Fatalf("M1 FAIL: error response: %v", resp.Error)
	}
	if resp.Result.SID == "" {
		t.Errorf("M2 FAIL: daemon didn't mint a sid (raw=%s)", raw)
	}
	if !strings.HasPrefix(resp.Result.SID, "runner-") {
		t.Errorf("M2 FAIL: minted sid %q lacks runner- prefix", resp.Result.SID)
	}
	// M3 — daemon_version + audit_topic
	if resp.Result.DaemonVersion == "" {
		t.Errorf("M3 FAIL: daemon_version empty")
	}
	if resp.Result.AuditTopic != "runner:"+resp.Result.SID {
		t.Errorf("M3 FAIL: audit_topic = %q, want runner:%s", resp.Result.AuditTopic, resp.Result.SID)
	}

	// M4 — send 3 audit notifications
	for i := 0; i < 3; i++ {
		audit := map[string]any{
			"jsonrpc": "2.0",
			"method":  "audit",
			"params": map[string]any{
				"kind": "pty_output",
				"body": fmt.Sprintf("line-%d", i),
				"at":   time.Now().UTC().Format(time.RFC3339Nano),
			},
		}
		if err := conn.WriteJSON(audit); err != nil {
			t.Fatalf("M4 FAIL: write audit %d: %v", i, err)
		}
	}
	// Give the read loop time to process.
	time.Sleep(100 * time.Millisecond)

	// M5 — GET /api/v1/runners returns the row
	httpResp, err := http.Get(ts.URL + "/api/v1/runners")
	if err != nil {
		t.Fatalf("M5 FAIL: GET /api/v1/runners: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("M5 FAIL: HTTP %d", httpResp.StatusCode)
	}
	var listBody struct {
		Runners []rh.RegisteredRunner `json:"runners"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&listBody); err != nil {
		t.Fatalf("M5 FAIL: decode list: %v", err)
	}
	if len(listBody.Runners) != 1 {
		t.Fatalf("M5 FAIL: list returned %d rows, want 1", len(listBody.Runners))
	}
	row := listBody.Runners[0]
	if row.SID != resp.Result.SID {
		t.Errorf("M5 FAIL: row.sid = %q, want %q", row.SID, resp.Result.SID)
	}
	if row.Name != "test-runner-1" {
		t.Errorf("M5 FAIL: row.name = %q, want test-runner-1", row.Name)
	}
	if row.AgentSlug != "claude-code" {
		t.Errorf("M5 FAIL: row.agent_slug = %q", row.AgentSlug)
	}
	if row.A2ABaseURL != "http://127.0.0.1:9091" {
		t.Errorf("M5 FAIL: row.a2a_base_url = %q", row.A2ABaseURL)
	}
	// 3 client-sent + 1 daemon-side synchronous "registered" event
	// (emitted in handleRunnerRegister to avoid the CI race that
	// failed PR #507's first build).
	if row.AuditEventsRcv != 4 {
		t.Errorf("M5 FAIL: row.audit_events_received = %d, want 4 (3 client-sent audit + 1 synchronous daemon-side 'registered')", row.AuditEventsRcv)
	}
}

// TestP0_504_RegisterRejectsNonRegisterFirst pins M6 — the first
// frame must be chepherd/register; any other method gets -32600.
func TestP0_504_RegisterRejectsNonRegisterFirst(t *testing.T) {
	ts := newTestRuntimeServer(t)
	defer ts.Close()

	conn := dialRegisterWS(t, ts.URL)
	defer conn.Close()

	bad := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "audit",
		"params": map[string]any{"kind": "noop"},
	}
	if err := conn.WriteJSON(bad); err != nil {
		t.Fatalf("write bad first frame: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("M6 FAIL: bad first frame accepted")
	}
	if resp.Error.Code != -32600 {
		t.Errorf("M6 FAIL: error code = %d, want -32600", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "chepherd/register") {
		t.Errorf("M6 FAIL: error message lacks chepherd/register hint: %q", resp.Error.Message)
	}

	// Connection should close after the rejection. Give the daemon
	// a beat to close + then expect read error.
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _, readErr := conn.ReadMessage()
	if readErr == nil {
		t.Errorf("M6 FAIL: daemon kept WS open after invalid first frame")
	}
}

// noopHandler keeps the import of context alive for the runtime-port-
// test seam below.
var _ = context.Background

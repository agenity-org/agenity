// cmd/runner/daemon_ws.go — outbound WS from chepherd-runner to
// chepherd-daemon for registration + audit upload.
//
// #504 Wave R1 scope: dial daemon, send chepherd/register, read back
// the assigned SID + audit_topic, then keep the WS open for
// subsequent audit notifications. Subsequent Waves (R2+) layer per-
// session A2A endpoint / Agent Card discovery on top.
//
// Refs #504 Wave R1 internal/runtimehttp/runners_register.go.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/agenity-org/agenity/internal/runtime"
)

// daemonClient wraps the WS connection to chepherd-daemon.
type daemonClient struct {
	conn    *websocket.Conn
	closed  chan struct{}
	writeMu sync.Mutex // serialise concurrent WriteJSON from audit emitters + pty pump
}

// registerWithDaemon dials the daemon's /api/v1/runners/register
// endpoint, sends the registration frame, decodes the response, and
// returns a client the caller can use to send subsequent audit
// notifications. Caller must call Close when done.
//
// daemonURL accepts:
//   - "ws://host:port"   (already ws scheme)
//   - "wss://host:port"  (TLS ws)
//   - "http://host:port" (rewritten to ws://)
//   - "https://host:port" (rewritten to wss://)
//
// authToken populates Authorization: Bearer if non-empty.
func registerWithDaemon(daemonURL, authToken string, req runnerRegisterReq) (*daemonClient, runnerRegisterResp, error) {
	url := strings.TrimRight(daemonURL, "/")
	if strings.HasPrefix(url, "http://") {
		url = "ws://" + strings.TrimPrefix(url, "http://")
	} else if strings.HasPrefix(url, "https://") {
		url = "wss://" + strings.TrimPrefix(url, "https://")
	}
	url += "/api/v1/runners/register"

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   64 * 1024,
		WriteBufferSize:  64 * 1024,
	}
	hdr := http.Header{}
	if authToken != "" {
		hdr.Set("Authorization", "Bearer "+authToken)
	}
	conn, _, err := dialer.Dial(url, hdr)
	if err != nil {
		return nil, runnerRegisterResp{}, fmt.Errorf("daemon WS dial %s: %w", url, err)
	}

	rawParams, _ := json.Marshal(req)
	regFrame := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "chepherd/register",
		"params":  json.RawMessage(rawParams),
	}
	if err := conn.WriteJSON(regFrame); err != nil {
		_ = conn.Close()
		return nil, runnerRegisterResp{}, fmt.Errorf("daemon WS write register: %w", err)
	}

	_, raw, err := conn.ReadMessage()
	if err != nil {
		_ = conn.Close()
		return nil, runnerRegisterResp{}, fmt.Errorf("daemon WS read register response: %w", err)
	}
	var resp struct {
		Result runnerRegisterResp `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		_ = conn.Close()
		return nil, runnerRegisterResp{}, fmt.Errorf("daemon WS decode register response: %w (raw=%s)", err, raw)
	}
	if resp.Error != nil {
		_ = conn.Close()
		return nil, runnerRegisterResp{}, fmt.Errorf("daemon WS register rejected: %d %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Result.SID == "" {
		_ = conn.Close()
		return nil, runnerRegisterResp{}, fmt.Errorf("daemon WS register response missing sid (raw=%s)", raw)
	}
	return &daemonClient{conn: conn, closed: make(chan struct{})}, resp.Result, nil
}

// SendAudit pushes a notification frame to the daemon. R1 contract
// — used for PTY-output stream + freeform diagnostic events. AU1
// adds SendAuditEvent for structured §10-step-24 events.
func (c *daemonClient) SendAudit(kind, body string) error {
	if c == nil || c.conn == nil {
		return nil
	}
	frame := map[string]any{
		"jsonrpc": "2.0",
		"method":  "audit",
		"params": map[string]any{
			"kind": kind,
			"body": body,
			"at":   time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteJSON(frame)
}

// SendAuditEvent pushes a structured §10-step-24 audit event to the
// daemon as a JSON-RPC notification with method="audit.event". This
// is AU1's wire transport for the audit.sent / audit.received events
// the runner emits on A2A call boundaries.
//
// Fire-and-forget at the caller: writes errors are returned for the
// caller to log if they want, but the A2A response path SHOULD NOT
// block on this (caller wraps in a goroutine).
func (c *daemonClient) SendAuditEvent(e runtime.AuditEvent) error {
	if c == nil || c.conn == nil {
		return nil
	}
	frame := map[string]any{
		"jsonrpc": "2.0",
		"method":  "audit.event",
		"params":  e,
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteJSON(frame)
}

// EmitAuditEvent satisfies the runtime.AuditEmitter interface so the
// runner's inbound A2A middleware + outbound client wrapper can push
// events without coupling to daemonClient. Fire-and-forget — errors
// are logged to stderr but never returned.
func (c *daemonClient) EmitAuditEvent(e runtime.AuditEvent) {
	if err := c.SendAuditEvent(e); err != nil {
		fmt.Fprintf(os.Stderr, "[chepherd-runner] audit.event write failed: %v\n", err)
	}
}

// Close shuts down the underlying WS conn. Idempotent.
//
// #588 — send a graceful WebSocket close frame before tearing down
// the TCP connection so the daemon sees CloseNormalClosure (1000)
// instead of "close 1006 (abnormal closure): unexpected EOF". The
// abnormal-close was cosmetic (didn't break functionality) but
// noisy in operator logs + masked real disconnect anomalies.
//
// The control-frame WriteControl honors a deadline so a flaky
// network doesn't block shutdown; 2s is well under the runner's
// 5s SIGTERM-to-exit budget.
func (c *daemonClient) Close() {
	if c == nil || c.conn == nil {
		return
	}
	select {
	case <-c.closed:
		return
	default:
		close(c.closed)
	}
	// Try graceful close; if the deadline fires or the conn is
	// already broken, fall through to the TCP-level Close below.
	_ = c.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "runner shutdown"),
		time.Now().Add(2*time.Second),
	)
	_ = c.conn.Close()
}

// runnerRegisterReq + runnerRegisterResp mirror the daemon-side types
// in internal/runtimehttp/runners_register.go. Kept locally so
// cmd/runner doesn't import internal/runtimehttp (would conflate the
// daemon vs runner split). Field tags MUST match the daemon's
// verbatim.
//
// Locked with chepherd-worker2 (Wave D1) 2026-05-31: Name + A2ABaseURL
// added so the daemon's §12.2 directory consumer can template the
// well-known Agent Card URI without re-deriving / guessing scheme.
type runnerRegisterReq struct {
	SID           string   `json:"sid"`
	Name          string   `json:"name"`
	AgentSlug     string   `json:"agent_slug"`
	RunnerVersion string   `json:"runner_version"`
	A2ABaseURL    string   `json:"a2a_base_url"`
	MCPSocket     string   `json:"mcp_socket"`
	Capabilities  []string `json:"capabilities"`
}

type runnerRegisterResp struct {
	SID           string `json:"sid"`
	DaemonVersion string `json:"daemon_version"`
	AuditTopic    string `json:"audit_topic"`
}

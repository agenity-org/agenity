// cmd/runner/mcp_proxy.go implements the v0.9.4 §22 chepherd-runner
// MCP upstream proxy (#477 Wave M1). The runner's Unix-socket MCP
// server (StartHTTP on `unix://...`) receives JSON-RPC requests from
// the agent inside its container; the proxy forwards each request to
// chepherd-daemon's authoritative MCP HTTP endpoint so the runner
// doesn't need to host its own tool catalog or maintain a Runtime
// just for tool dispatch.
//
// Why HTTP-to-HTTP and not the existing register-WS channel:
//
//   - Synchronous request/response. tools/call is fundamentally a
//     blocking RPC; the daemon-bound WS is currently a one-shot
//     register frame + audit notifications and would need full
//     request/response correlation to carry MCP traffic. A direct
//     HTTP POST is simpler, isolates failures (one bad tool call
//     doesn't disturb the WS), and matches the daemon's existing
//     /mcp/rpc surface as the canonical inbound MCP path.
//   - Same auth as register. The runner already presents the same
//     CHEPHERD_TOKEN Bearer to the daemon; proxy requests reuse it.
//
// Failure semantics: transport errors (HTTP dial, timeout, non-200
// response) become JSON-RPC `error.code = -32000` with the actual
// error in `error.message`. The daemon's own JSON-RPC error
// envelope (e.g. -32601 "unknown tool") is propagated verbatim.
//
// Refs #477 V0.9.2-ARCHITECTURE.md §22.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/mcpserver"
)

const proxyHTTPTimeout = 30 * time.Second

// makeMCPProxy returns an mcpserver.UpstreamProxyFn that forwards
// each JSON-RPC request to the daemon's /mcp/rpc endpoint. The
// daemon URL accepts the same schemes runner registration does
// (http://, https://, ws://, wss://); ws→http rewrite keeps a
// single config knob covering both auth-and-MCP and just-MCP
// deployments.
func makeMCPProxy(daemonURL, authToken string) mcpserver.UpstreamProxyFn {
	endpoint := mcpProxyEndpoint(daemonURL)
	client := &http.Client{Timeout: proxyHTTPTimeout}
	return func(method string, params json.RawMessage, agent string) (any, int, string) {
		body, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  method,
			"params":  params,
		})
		if err != nil {
			return nil, -32603, "proxy marshal: " + err.Error()
		}
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, -32000, "proxy build request: " + err.Error()
		}
		req.Header.Set("Content-Type", "application/json")
		if authToken != "" {
			req.Header.Set("Authorization", "Bearer "+authToken)
		}
		if agent != "" {
			req.Header.Set("X-Chepherd-Agent", agent)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, -32000, "proxy dial: " + err.Error()
		}
		defer resp.Body.Close()
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
		if err != nil {
			return nil, -32000, "proxy read: " + err.Error()
		}
		if resp.StatusCode != http.StatusOK {
			return nil, -32000, fmt.Sprintf("proxy HTTP %d: %s", resp.StatusCode, truncateForError(respBody))
		}
		var envelope struct {
			Result any `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(respBody, &envelope); err != nil {
			return nil, -32000, "proxy decode: " + err.Error()
		}
		if envelope.Error != nil {
			return nil, envelope.Error.Code, envelope.Error.Message
		}
		return envelope.Result, 0, ""
	}
}

// mcpProxyEndpoint normalizes daemonURL to the /mcp/rpc HTTP path
// the daemon's mcpserver.StartHTTP exposes. Accepts ws:// + wss://
// for symmetry with daemon registration's URL handling so operators
// only configure ONE daemon-url knob.
func mcpProxyEndpoint(daemonURL string) string {
	u := strings.TrimRight(daemonURL, "/")
	if strings.HasPrefix(u, "ws://") {
		u = "http://" + strings.TrimPrefix(u, "ws://")
	} else if strings.HasPrefix(u, "wss://") {
		u = "https://" + strings.TrimPrefix(u, "wss://")
	}
	return u + "/mcp/rpc"
}

// truncateForError trims long error bodies so JSON-RPC error
// messages don't carry kilobytes of HTML. Leaves a hint when the
// body was truncated.
func truncateForError(body []byte) string {
	const max = 256
	if len(body) <= max {
		return string(body)
	}
	return string(body[:max]) + "...[truncated]"
}

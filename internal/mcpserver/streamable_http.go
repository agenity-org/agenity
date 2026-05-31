// internal/mcpserver/streamable_http.go implements Anthropic's
// MCP Streamable HTTP transport per
// https://modelcontextprotocol.io/specification/server (#478 Wave
// M2). Replaces the M1 /mcp alias of /mcp/rpc with the
// spec-compliant handler claude-code's HTTP transport requires.
//
// Live-premise check (recorded 2026-05-31 against claude-code
// 2.1.148): claude mcp list against a probe server at
// http://127.0.0.1:PORT/mcp returns ✓ Connected when the handler:
//
//   1. Accepts POST /mcp with JSON-RPC body
//   2. Returns Content-Type: application/json + JSON-RPC response
//   3. Issues Mcp-Session-Id header on first response (claude
//      stores the session id + replays on subsequent requests)
//   4. Accepts notifications/* by responding 202 with no body
//   5. Accepts GET /mcp for the SSE keep-alive channel (claude
//      may open it during long-running flows; minimum-viable
//      implementation just keeps the connection open until the
//      request context cancels)
//
// The same probe with `http+unix://...` URL form returns ✗ Failed
// to connect — claude-code's HTTP transport DOES NOT support
// http-over-unix-socket URLs. M2 therefore exposes BOTH a Unix
// socket (R1+M1 canonical / non-agent consumers) AND a
// localhost-only TCP listener (the agent-facing transport, since
// that's what claude can dial). Both wear the same handler.
//
// Refs #478 V0.9.2-ARCHITECTURE.md §22.
package mcpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
)

// mcpSessionIDHeader is the spec-defined header name for the
// MCP session-id round-trip.
const mcpSessionIDHeader = "Mcp-Session-Id"

// handleStreamableHTTP serves the canonical /mcp endpoint per
// Anthropic's MCP Streamable HTTP transport spec. Method dispatch:
//
//   - POST → JSON-RPC body, JSON-RPC response. Notification
//     messages (method starts with `notifications/` or jsonrpc
//     "id" omitted) respond 202 No Body.
//   - GET → SSE stream for server-initiated messages. v0.9.4 ships
//     keep-alive only; future Waves push notifications through it.
//
// Auth + agent identity come from the same headers as /mcp/rpc:
// Authorization Bearer + X-Chepherd-Agent.
func (s *Server) handleStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleStreamablePOST(w, r)
	case http.MethodGet:
		s.handleStreamableGET(w, r)
	case http.MethodDelete:
		// Spec allows DELETE for explicit session termination.
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "POST/GET/DELETE only", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleStreamablePOST(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.requireAuth(r); code != 0 {
		fmt.Fprintf(os.Stderr, "[chepherd-mcp] streamable POST auth REJECTED from %s: %s\n", r.RemoteAddr, msg)
		http.Error(w, msg, code)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var req rpcReq
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Session-id: echo the client's if present, otherwise issue a
	// fresh UUID. claude-code stores it after the first response
	// and replays on subsequent requests; we don't currently use it
	// to discriminate state but emitting it is part of the spec.
	sessionID := r.Header.Get(mcpSessionIDHeader)
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	w.Header().Set(mcpSessionIDHeader, sessionID)

	// Notifications (no id OR method starts with "notifications/")
	// receive 202 with no body per the spec. rpcReq.ID is `any`;
	// nil indicates the field was absent in the inbound JSON.
	if req.ID == nil || strings.HasPrefix(req.Method, "notifications/") {
		fmt.Fprintf(os.Stderr, "[chepherd-mcp] streamable notification %s → 202\n", req.Method)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	agent := r.Header.Get("X-Chepherd-Agent")
	resp := s.dispatchWithAgent(&req, agent)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleStreamableGET opens the server-initiated SSE channel. v0.9.4
// ships a minimum-viable keep-alive: the connection stays open until
// the client cancels OR the server tears down. Future Waves push
// task-progress / log events through it; today's chepherd MCP server
// is response-only so the GET path emits a single comment frame and
// holds.
func (s *Server) handleStreamableGET(w http.ResponseWriter, r *http.Request) {
	if code, msg := s.requireAuth(r); code != 0 {
		http.Error(w, msg, code)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	sessionID := r.Header.Get(mcpSessionIDHeader)
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	w.Header().Set(mcpSessionIDHeader, sessionID)
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		_, _ = fmt.Fprintf(w, ": chepherd streamable session %s\n\n", sessionID)
		flusher.Flush()
	}
	<-r.Context().Done()
}

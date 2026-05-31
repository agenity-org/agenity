// HTTP/WebSocket transport for chepherd MCP — replaces the legacy Unix-socket
// transport so the server works identically on Podman (local container DNS)
// and Kubernetes (Service DNS), enabling multi-cluster deployments. The wire
// protocol is unchanged: JSON-RPC 2.0 frames, identify-handshake first.
//
// Endpoints:
//
//	GET  /mcp/ws       — WebSocket upgrade; one frame per JSON-RPC msg
//	POST /mcp/rpc      — single-shot JSON-RPC over HTTP (testing / curl)
//	GET  /mcp/healthz  — liveness probe
//	GET  /mcp/info     — version + capability summary

package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// upgrader allows cross-origin WS so a containerized agent on any network
// can connect. Auth — once #128 lands — happens via Bearer header.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 64 * 1024,
	CheckOrigin:     func(_ *http.Request) bool { return true },
}

// StartHTTP binds the MCP HTTP listener on `addr` and serves WS+REST. Runs
// the accept loop in a goroutine; returns once `net.Listen` succeeds. The
// listener and HTTP server are stored on s so Stop() can close both.
func (s *Server) StartHTTP(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("mcp http: listen %s: %w", addr, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/ws", s.handleWS)
	mux.HandleFunc("/mcp/rpc", s.handleRPC)
	mux.HandleFunc("/mcp/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/mcp/info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":            "chepherd",
			"version":         "0.8.0",
			"protocolVersion": "2024-11-05",
			"transport":       "http+ws",
		})
	})
	s.httpListener = ln
	s.httpServer = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.httpServer.Serve(ln) }()
	return nil
}

// stopHTTP closes the HTTP listener. Idempotent.
func (s *Server) stopHTTP() {
	if s.httpServer != nil {
		_ = s.httpServer.Close()
		s.httpServer = nil
	}
	if s.httpListener != nil {
		_ = s.httpListener.Close()
		s.httpListener = nil
	}
}

// handleWS upgrades the connection to a WebSocket and runs the same
// JSON-RPC dispatch loop the legacy Unix-socket transport used. One frame
// per message; ping/pong handled by gorilla.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// #414 P0 — log every WS connection attempt so operator can grep
	// for handshake failures / auth rejections / agent identity. The
	// agent's `/mcp` only sees "-32000" or "disconnected"; the
	// server-side log shows exactly which gate the request hit.
	if code, msg := s.requireAuth(r); code != 0 {
		fmt.Fprintf(os.Stderr, "[chepherd-mcp] WS auth REJECTED from %s: %s\n", r.RemoteAddr, msg)
		http.Error(w, msg, code)
		return
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[chepherd-mcp] WS upgrade FAILED from %s: %v\n", r.RemoteAddr, err)
		http.Error(w, "ws upgrade failed", http.StatusBadRequest)
		return
	}
	defer c.Close()

	// Agent name can be passed as ?agent=<name> query param OR via the
	// initial $/chepherd/identify frame. Both are supported so existing
	// bridge code works.
	connAgent := r.URL.Query().Get("agent")
	fmt.Fprintf(os.Stderr, "[chepherd-mcp] WS connected from %s (agent=%q)\n", r.RemoteAddr, connAgent)

	// #447 PR1 — register peer in the conn registry so chepherd.send_to_
	// session can push notifications/peer-message to this conn. Re-
	// register inside the identify branch if the name is set via
	// $/chepherd/identify instead of query param.
	unreg := s.registerPeer(connAgent, c)
	defer unreg()

	c.SetReadLimit(4 * 1024 * 1024)
	for {
		_, frame, err := c.ReadMessage()
		if err != nil {
			return
		}
		var req rpcReq
		if err := json.Unmarshal(frame, &req); err != nil {
			_ = c.WriteJSON(rpcResp{JSONRPC: "2.0", ID: nil, Error: &rpcErr{Code: -32700, Message: "parse error: " + err.Error()}})
			continue
		}
		if req.Method == "$/chepherd/identify" {
			var p struct {
				Agent string `json:"agent"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Agent != "" && p.Agent != connAgent {
				// Name changed (or set after ?agent= empty). Re-
				// register under the new name. Old unreg fn becomes
				// no-op for stale name; new defer runs on conn close.
				unreg()
				connAgent = p.Agent
				unreg = s.registerPeer(connAgent, c)
			}
			_ = c.WriteJSON(rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"ok": true, "agent": connAgent}})
			continue
		}
		resp := s.dispatchWithAgent(&req, connAgent)
		if err := c.WriteJSON(resp); err != nil {
			return
		}
	}
}

// handleRPC accepts a single JSON-RPC request via POST and returns its
// response. Useful for ad-hoc testing with curl + for Streamable-HTTP MCP
// clients that don't open a long-lived connection. The agent identity
// comes from the X-Chepherd-Agent header (Authorization carries the
// bearer token, not the agent name).
func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if code, msg := s.requireAuth(r); code != 0 {
		fmt.Fprintf(os.Stderr, "[chepherd-mcp] RPC auth REJECTED from %s: %s\n", r.RemoteAddr, msg)
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
	agent := r.Header.Get("X-Chepherd-Agent")
	resp := s.dispatchWithAgent(&req, agent)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// BridgeStdioToHTTP is the new HTTP/WS bridge: replaces BridgeStdioToSocket.
// The agent's MCP config points at `chepherd mcp --url ws://chepherd:9090/mcp/ws`;
// claude-code spawns this as a stdio subprocess; the subprocess opens a WS
// to the chepherd daemon and shuttles JSON-RPC frames between agent stdio
// (newline-delimited) and WS messages (one frame per JSON-RPC msg).
//
// On connect, sends $/chepherd/identify with CHEPHERD_AGENT_NAME so the
// server attributes events to the right agent (#89).
func BridgeStdioToHTTP(url string) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		ReadBufferSize:   64 * 1024,
		WriteBufferSize:  64 * 1024,
	}
	// Bearer-token auth (#139). The chepherd daemon injects CHEPHERD_TOKEN
	// into every agent's env at spawn time; the bridge subprocess inherits
	// it and presents it on the WS upgrade.
	hdr := http.Header{}
	if tok := os.Getenv("CHEPHERD_TOKEN"); tok != "" {
		hdr.Set("Authorization", "Bearer "+tok)
	}

	// #422 P0 — retry the WS dial with exponential backoff. Operator's
	// agent showed `/mcp ✘ failed` with -32000 after #419 instrumented
	// the server-side dispatch (which showed no failures because no
	// connect ever reached dispatch — the bridge was failing AT THE
	// DIAL). Most common cause: chepherd container is up + listening
	// but the FIRST agent spawn happens before the listener fully
	// accepts new connections, OR a transient slirp4netns DNS resolve
	// fails. The bridge was a single-shot dial that surfaced any
	// transient failure as a permanent -32000.
	//
	// Retry sequence: 5 attempts at 0s, 1s, 2s, 4s, 8s (total ~15s).
	// Each attempt logs to stderr so `podman logs chepherd-agent-...`
	// shows the WS dial diagnostic trail. After 5 failures we surface
	// the last error so claude-code's /mcp shows a real reason.
	var c *websocket.Conn
	var lastErr error
	backoff := time.Duration(0)
	const maxAttempts = 5
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if backoff > 0 {
			fmt.Fprintf(os.Stderr, "[chepherd-mcp-bridge] attempt %d/%d backing off %s before dial %s\n",
				attempt, maxAttempts, backoff, url)
			time.Sleep(backoff)
		}
		conn, resp, err := dialer.Dial(url, hdr)
		if err == nil {
			fmt.Fprintf(os.Stderr, "[chepherd-mcp-bridge] dial %s OK on attempt %d/%d\n", url, attempt, maxAttempts)
			c = conn
			break
		}
		lastErr = err
		status := ""
		if resp != nil {
			status = fmt.Sprintf(" (HTTP %d)", resp.StatusCode)
		}
		fmt.Fprintf(os.Stderr, "[chepherd-mcp-bridge] dial %s FAILED attempt %d/%d: %v%s\n",
			url, attempt, maxAttempts, err, status)
		// Exponential backoff: 1s, 2s, 4s, 8s
		if backoff == 0 {
			backoff = 1 * time.Second
		} else {
			backoff *= 2
		}
	}
	if c == nil {
		return fmt.Errorf("mcp bridge: dial %s failed after %d attempts: %w", url, maxAttempts, lastErr)
	}
	defer c.Close()
	c.SetReadLimit(4 * 1024 * 1024)

	// Identify frame — eat its reply so it doesn't leak to Claude.
	agent := os.Getenv("CHEPHERD_AGENT_NAME")
	if agent != "" {
		idFrame := fmt.Sprintf(`{"jsonrpc":"2.0","id":"$id","method":"$/chepherd/identify","params":{"agent":%q}}`, agent)
		if err := c.WriteMessage(websocket.TextMessage, []byte(idFrame)); err == nil {
			_, _, _ = c.ReadMessage() // discard identify reply
		}
	}

	// Two goroutines: stdin → ws, ws → stdout. Either exiting kills the
	// bridge. Stdin reads are line-buffered because claude-code emits one
	// JSON-RPC request per line.
	var wg sync.WaitGroup
	wg.Add(2)
	done := make(chan struct{})
	closeOnce := sync.Once{}
	closeAll := func() {
		closeOnce.Do(func() { close(done) })
	}

	// stdin → ws
	go func() {
		defer wg.Done()
		defer closeAll()
		dec := json.NewDecoder(os.Stdin)
		for {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return
			}
			// Compact the raw frame so the server sees one JSON object
			// per WS message regardless of how the agent pretty-printed it.
			var buf bytes.Buffer
			if err := json.Compact(&buf, raw); err != nil {
				continue
			}
			if err := c.WriteMessage(websocket.TextMessage, buf.Bytes()); err != nil {
				return
			}
		}
	}()

	// ws → stdout
	go func() {
		defer wg.Done()
		defer closeAll()
		for {
			_, frame, err := c.ReadMessage()
			if err != nil {
				return
			}
			// Each WS frame is one JSON-RPC response; emit newline-terminated
			// so the agent's MCP client splits them correctly.
			if _, err := os.Stdout.Write(append(frame, '\n')); err != nil {
				return
			}
		}
	}()

	<-done
	_ = c.Close()
	_ = os.Stdin.Close()
	wg.Wait()
	_ = io.Discard // silence unused import on builds that strip dead code
	return nil
}

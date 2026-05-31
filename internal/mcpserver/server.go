// Package mcpserver implements chepherd's MCP server — the control-plane
// tool surface every chepherd-hosted agent calls into.
//
// Wire: MCP over JSON-RPC 2.0 carried by HTTP + WebSocket on TCP :9090.
// The chepherd binary's `mcp` subcommand is the stdio→WS bridge:
// claude-code spawns it as a stdio subprocess; the subprocess opens a
// WebSocket back to chepherd's daemon (CHEPHERD_MCP_URL env or --url
// flag) and shuttles frames between agent stdio and the WS connection.
//
// The Unix-socket transport (v0.5–v0.7) has been retired — it didn't
// survive Kubernetes node boundaries.
//
// Tool set (chepherd.* namespace; reserved by agreement with openova):
//
//	chepherd.spawn              spawn a peer session
//	chepherd.assign             change a session's tribe/role
//	chepherd.grant_channel      authorize cross-tribe routing
//	chepherd.list               enumerate sessions/tribes/grants
//	chepherd.read_pane          observe a session's recent output
//	chepherd.send_to_session    direct write (for shepherd's advise_adam)
//	chepherd.pause              pause/unpause a session
//	chepherd.alert_human        push to dashboard inbox
package mcpserver

import (

	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/google/uuid"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/runtime"
)

// Server hosts the chepherd MCP JSON-RPC surface. Constructed once per
// chepherd-run process. The HTTP/WebSocket listener is started via
// StartHTTP() and torn down via Stop().
type Server struct {
	rt           *runtime.Runtime
	deliverer    a2a.Deliverer // v0.9.2: backs the chepherd.send_to_session shim onto A2A SendMessage. Removed in v1.0.
	httpListener net.Listener
	httpServer   *http.Server
	// AuthToken (#139/#153) is the shared-secret bearer token clients
	// must present. Set via SetAuthToken before StartHTTP. Empty string
	// disables auth (CHEPHERD_AUTH_REQUIRE=false) — only safe on
	// 127.0.0.1 deployments.
	authToken string
	// lastCaller is the most-recently-identified agent name. Set per
	// dispatch in dispatchWithAgent — handlers read it to attribute
	// events. NOT thread-safe across concurrent dispatches; serveConn
	// is per-connection sequential (one agent per conn), which makes
	// this safe under the current invariant. The v0.9.2 runner-split
	// (#208) makes this per-runner-process and passes through
	// context.Context.
	lastCaller string

	// #447 PR1 — agent-name → live WS conn registry. Indexed by the
	// connAgent set in handleWS (either ?agent= query OR $/chepherd/
	// identify). chepherd.send_to_session uses this to push
	// notifications/peer-message frames to the recipient out-of-band
	// of the sender's tools/call response. NOT touched by PTY writes —
	// the body delivery NEVER reaches stdin.
	peersMu sync.Mutex
	peers   map[string]peerConn
}

// peerConn is a registered agent's MCP WS connection + the write mutex
// guarding gorilla/websocket's single-writer requirement. Multiple
// goroutines (dispatch responses, server-initiated notifications) can
// race on the same conn; the mutex serializes WriteJSON calls.
type peerConn struct {
	c  *websocket.Conn
	mu *sync.Mutex
}

// SetAuthToken configures the bearer token required on every WS upgrade
// and /mcp/rpc request. Empty string disables enforcement.
func (s *Server) SetAuthToken(tok string) { s.authToken = tok }

// requireAuth returns nil if the request carries the right bearer token
// or auth is disabled. Otherwise returns the http status + error string
// the caller should write back. Accepts the token via:
//
//   - Authorization: Bearer <tok>
//   - ?token=<tok> query param  (WS clients that can't set headers)
//   - X-Chepherd-Token: <tok>   (older clients)
func (s *Server) requireAuth(r *http.Request) (int, string) {
	if s.authToken == "" {
		return 0, ""
	}
	got := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = strings.TrimPrefix(h, "Bearer ")
	}
	if got == "" {
		got = r.Header.Get("X-Chepherd-Token")
	}
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	if got == "" || !constantTimeEq(got, s.authToken) {
		return http.StatusUnauthorized, "missing or invalid Bearer token"
	}
	return 0, ""
}

func constantTimeEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// CurrentCaller returns the name of the agent that made the most-recent
// MCP call on the current serveConn goroutine. Handlers in toolCallDirect
// use this to set actor= on emitted events.
func (s *Server) CurrentCaller() string {
	if s.lastCaller == "" {
		return "shepherd"
	}
	return s.lastCaller
}

// taskIDOrEmpty returns task.ID or empty string when task is nil.
// Used by the chepherd.send_to_session shim to surface the auto-
// generated A2A task ID back to legacy MCP callers.
func taskIDOrEmpty(task *a2a.Task) string {
	if task == nil {
		return ""
	}
	return task.ID
}

// New constructs a Server bound to the runtime. Call StartHTTP(addr) to
// begin accepting connections.
//
// Deprecated: use NewWithDeliverer for v0.9.2 callers; the legacy
// chepherd.send_to_session MCP tool requires an a2a.Deliverer to bridge
// onto A2A SendMessage. Calling New produces a Server whose
// send_to_session handler returns -32000 with a descriptive error.
// Removed in v1.0.
func New(rt *runtime.Runtime) *Server {
	return &Server{rt: rt}
}

// NewWithDeliverer constructs a Server bound to the runtime AND an
// a2a.Deliverer. The Deliverer backs the chepherd.send_to_session MCP
// tool — the legacy v0.9.1 tool now translates calls onto A2A
// SendMessage rather than writing directly to the target session's PTY.
//
// chepherd.send_to_session is DEPRECATED. v0.9.2 callers should migrate
// to A2A SendMessage directly. The shim is removed in v1.0. Per
// architect 2026-05-29.
//
// Refs #208.
func NewWithDeliverer(rt *runtime.Runtime, deliverer a2a.Deliverer) *Server {
	return &Server{rt: rt, deliverer: deliverer}
}

// DefaultListenAddr is the canonical MCP HTTP bind address. v0.8+ defaults
// to 127.0.0.1 so the control plane is not exposed to the LAN by default
// (#154). Operators running chepherd inside a container that needs to be
// reached by sibling containers on a bridge network must override via
// CHEPHERD_MCP_LISTEN=0.0.0.0:9090 explicitly — scripts/start.sh does
// this since the in-pod network is already isolated by podman.
const DefaultListenAddr = "127.0.0.1:9090"

// Stop closes the HTTP listener. Idempotent.
func (s *Server) Stop() {
	s.stopHTTP()
}

// dispatchWithAgent wraps dispatch with caller identity context.
// Tools that record actor in events (e.g. SetScorecard, RecordVerdict,
// HumanInbox) now have a real caller name instead of hardcoded "shepherd".
func (s *Server) dispatchWithAgent(req *rpcReq, agent string) rpcResp {
	if agent == "" {
		agent = "anonymous"
	}
	s.lastCaller = agent
	return s.dispatch(req)
}

// dispatch routes one request to its handler.
//
// #414 P0 — every dispatch logs a one-line audit entry to stderr
// (caller + method + outcome) so when the agent's `/mcp` shows
// "-32000" or "disconnected", the operator can grep
// `journalctl -u chepherd | grep '\[chepherd-mcp\]'` for the exact
// failure point. Pre-#414 the only signal was the agent-side
// error, with no server-side visibility into auth/dispatch/error
// emission.
func (s *Server) dispatch(req *rpcReq) rpcResp {
	caller := s.CurrentCaller()
	if req.Method == "" {
		fmt.Fprintf(os.Stderr, "[chepherd-mcp] %s: empty method → -32600\n", caller)
		return rpcResp{JSONRPC: "2.0", ID: req.ID, Error: &rpcErr{Code: -32600, Message: "invalid request"}}
	}
	// Standard MCP discovery
	if req.Method == "initialize" {
		fmt.Fprintf(os.Stderr, "[chepherd-mcp] %s: initialize → OK\n", caller)
		return rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "chepherd", "version": "0.5.0"},
		}}
	}
	if req.Method == "tools/list" {
		tools := s.toolList()
		fmt.Fprintf(os.Stderr, "[chepherd-mcp] %s: tools/list → OK (%d tools)\n", caller, len(tools))
		return rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": tools}}
	}
	if req.Method == "tools/call" {
		resp := s.toolCall(req)
		if resp.Error != nil {
			fmt.Fprintf(os.Stderr, "[chepherd-mcp] %s: tools/call → ERROR %d: %s\n", caller, resp.Error.Code, resp.Error.Message)
		} else {
			fmt.Fprintf(os.Stderr, "[chepherd-mcp] %s: tools/call → OK\n", caller)
		}
		return resp
	}

	// Direct chepherd.* method calls (non-MCP test path)
	if strings.HasPrefix(req.Method, "chepherd.") {
		resp := s.toolCallDirect(req.ID, strings.TrimPrefix(req.Method, "chepherd."), req.Params)
		if resp.Error != nil {
			fmt.Fprintf(os.Stderr, "[chepherd-mcp] %s: %s → ERROR %d: %s\n", caller, req.Method, resp.Error.Code, resp.Error.Message)
		}
		return resp
	}
	fmt.Fprintf(os.Stderr, "[chepherd-mcp] %s: %s → -32601 method not found\n", caller, req.Method)
	return rpcResp{JSONRPC: "2.0", ID: req.ID, Error: &rpcErr{Code: -32601, Message: "method not found: " + req.Method}}
}

// toolList returns the JSON-Schema descriptors for every tool.
func (s *Server) toolList() []map[string]any {
	return []map[string]any{
		{"name": "chepherd.spawn", "description": "Spawn a new peer session. Args: name (string), agent (string), team (string, optional), role (string, optional 'worker'|'shepherd'), cwd (string, optional).", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name", "agent"},
			"properties": map[string]any{
				"name":  map[string]any{"type": "string"},
				"agent": map[string]any{"type": "string"},
				"team":  map[string]any{"type": "string"},
				"role":  map[string]any{"type": "string", "enum": []string{"worker", "shepherd"}},
				"cwd":   map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.assign", "description": "Change a session's team and role. Args: name, team, role.", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name", "team", "role"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
				"team": map[string]any{"type": "string"},
				"role": map[string]any{"type": "string", "enum": []string{"worker", "shepherd"}},
			},
		}},
		{"name": "chepherd.grant_channel", "description": "Authorize a cross-team channel. Args: from_team, to_team, scope ('read'|'write'|'both').", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"from_team", "to_team", "scope"},
			"properties": map[string]any{
				"from_team": map[string]any{"type": "string"},
				"to_team":   map[string]any{"type": "string"},
				"scope":     map[string]any{"type": "string", "enum": []string{"read", "write", "both"}},
			},
		}},
		{"name": "chepherd.list", "description": "Enumerate sessions. Args: team (string, optional filter).", "inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"team": map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.set_scorecard", "description": "ScrumMaster-only: record a 0..10 score for each axis of a worker. Args: name, G, V, F, E, D, note (optional). G=Goal clarity, V=Velocity, F=Focus, E=End-state proximity, D=Discipline (CLAUDE.md compliance).", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name", "G", "V", "F", "E", "D"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
				"G":    map[string]any{"type": "number"},
				"V":    map[string]any{"type": "number"},
				"F":    map[string]any{"type": "number"},
				"E":    map[string]any{"type": "number"},
				"D":    map[string]any{"type": "number"},
				"note": map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.record_verdict", "description": "ScrumMaster-only: record a per-tick verdict for a worker. Args: name, verdict ('silent'|'praise'|'coach'|'intervene'), message (optional). Increments intervention count on coach/intervene.", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name", "verdict"},
			"properties": map[string]any{
				"name":    map[string]any{"type": "string"},
				"verdict": map[string]any{"type": "string", "enum": []string{"silent", "praise", "coach", "intervene"}},
				"message": map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.read_pane", "description": "Read the recent output (ring buffer snapshot) of a session. Args: name (string), lines (int, optional, default 50).", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name":  map[string]any{"type": "string"},
				"lines": map[string]any{"type": "integer"},
			},
		}},
		{"name": "chepherd.get_peer_card", "description": "Fetch a sibling agent's per-session AgentCard — role, capabilities, skills, current state, scorecard. Use this to discover what a peer can do BEFORE engaging them. The card complements chepherd.list_sessions (which only lists names+roles). Args: name (the peer's @-address).", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.peer_status", "description": "Fetch a sibling agent's LIVE status: alive/paused/exited, last activity timestamp, idle seconds, recent PTY output excerpt. Use this to answer 'what is peer X doing right now' without reading their pane via chepherd.read_pane. Args: name (the peer's @-address).", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.send_to_session", "description": "Write a message directly into a session's PTY stdin. Used by the Scrum Master to advise Adam (prefer @target relay for normal conversation). Args: name, body.", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name", "body"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
				"body": map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.pause", "description": "Pause or unpause a session. Args: name, paused (bool).", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name", "paused"},
			"properties": map[string]any{
				"name":   map[string]any{"type": "string"},
				"paused": map[string]any{"type": "boolean"},
			},
		}},
		{"name": "chepherd.alert_human", "description": "Surface a high-signal message in the human's inbox. ONLY for: accomplishment (major win), failure (something broke), stuck (agent blocked despite intervention), or question (operator decision needed). Routine observations go to chepherd.note or chepherd.record_event instead. Args: body, kind, urgency.", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"body", "kind"},
			"properties": map[string]any{
				"body":    map[string]any{"type": "string"},
				"kind":    map[string]any{"type": "string", "enum": []string{"accomplishment", "failure", "stuck", "question"}},
				"urgency": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
			},
		}},
		// v0.6 team / membership tools
		{"name": "chepherd.create_team", "description": "Create a team. Args: name, canon_path (optional), topology ('hub'|'mesh'|'custom', default 'hub').", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name":       map[string]any{"type": "string"},
				"canon_path": map[string]any{"type": "string"},
				"topology":   map[string]any{"type": "string", "enum": []string{"hub", "mesh", "custom"}},
			},
		}},
		{"name": "chepherd.join_team", "description": "Add an agent to a team with a role. Idempotent on (agent, team) — re-call to update role. Args: agent, team, role ('worker'|'shepherd'|'reviewer'|'reviewer-discipline'|'reviewer-architect'|'reviewer-economics'|'tester'|'architect'), brief_override (optional).", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"agent", "team", "role"},
			"properties": map[string]any{
				"agent":          map[string]any{"type": "string"},
				"team":           map[string]any{"type": "string"},
				"role":           map[string]any{"type": "string"},
				"brief_override": map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.leave_team", "description": "Remove an agent's membership from a team. Args: agent, team.", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"agent", "team"},
			"properties": map[string]any{
				"agent": map[string]any{"type": "string"},
				"team":  map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.list_teams", "description": "Enumerate all teams.", "inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}},
		{"name": "chepherd.list_memberships", "description": "List memberships, optionally filtered by agent or team (pass empty to skip a filter). Args: agent (optional), team (optional).", "inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent": map[string]any{"type": "string"},
				"team":  map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.set_review_axis", "description": "Reviewer-only: record a per-axis assessment of a target worker. Used in the council pattern (v0.6). Args: target, axis (e.g., 'G','V','F','E','D','quality'), score (0..10), note (optional).", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"target", "axis", "score"},
			"properties": map[string]any{
				"target": map[string]any{"type": "string"},
				"axis":   map[string]any{"type": "string"},
				"score":  map[string]any{"type": "number"},
				"note":   map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.note", "description": "ScrumMaster-only: attach a per-worker observation note (lightweight, goes to the worker's scorecard.note field — NEVER to the inbox). Use this for routine 'I saw X happen' commentary. Args: target, body.", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"target", "body"},
			"properties": map[string]any{
				"target": map[string]any{"type": "string"},
				"body":   map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.record_event", "description": "Append an event to the runtime audit log (events strip). Use for any structured observation that doesn't warrant the inbox. Args: kind (free-form, e.g. 'observation'|'milestone'|'warning'), body, actor (optional, defaults to caller agent).", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"kind", "body"},
			"properties": map[string]any{
				"kind":  map[string]any{"type": "string"},
				"body":  map[string]any{"type": "string"},
				"actor": map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.read_canon", "description": "Read the current canon (CLAUDE.md / team-specific rules) for a team. Returns the canon text. Scrum Masters should call this every tick to re-ground their judgment against the live canon (which can be edited mid-run via the dashboard's canon-viewer widget). Args: team.", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"team"},
			"properties": map[string]any{
				"team": map[string]any{"type": "string"},
			},
		}},
		// v0.8 orchestrator tools — require caller role=orchestrator or role=shepherd
		{"name": "chepherd.spawn_worker", "description": "Orchestrator-only: spawn a new worker session in the caller's team. Simpler alias for chepherd.spawn with role=worker. Args: name (string), agent (string, optional, default claude-code), cwd (string, optional), brief_override (string, optional).", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name":           map[string]any{"type": "string"},
				"agent":          map[string]any{"type": "string"},
				"cwd":            map[string]any{"type": "string"},
				"brief_override": map[string]any{"type": "string"},
			},
		}},
		{"name": "chepherd.stop_session", "description": "Orchestrator-only: permanently stop (terminate) a session. The session is removed from the registry. Use chepherd.pause to temporarily pause instead. Args: name (string).", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		}},
	}
}

// toolCall handles MCP-style tools/call requests. Per the MCP spec the
// result must be `{ content: [{type:"text", text:"..."}], isError: bool }`,
// not the raw tool-output JSON — Claude's MCP client silently drops
// non-conformant responses, which is why shepherd reported "no sessions"
// even though the backend list was correct (issue: tools/call shape).
func (s *Server) toolCall(req *rpcReq) rpcResp {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return rpcResp{JSONRPC: "2.0", ID: req.ID, Error: &rpcErr{Code: -32602, Message: "invalid params: " + err.Error()}}
	}
	if !strings.HasPrefix(p.Name, "chepherd.") {
		return rpcResp{JSONRPC: "2.0", ID: req.ID, Error: &rpcErr{Code: -32601, Message: "unknown tool: " + p.Name}}
	}
	inner := s.toolCallDirect(req.ID, strings.TrimPrefix(p.Name, "chepherd."), p.Arguments)
	// Wrap the raw result in the MCP content envelope.
	if inner.Error != nil {
		txt, _ := json.Marshal(map[string]any{"error": inner.Error.Message})
		return rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(txt)}},
			"isError": true,
		}}
	}
	body, err := json.Marshal(inner.Result)
	if err != nil {
		body = []byte(`{"error":"marshal failed"}`)
	}
	return rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(body)}},
		"isError": false,
	}}
}

func (s *Server) toolCallDirect(id any, name string, args json.RawMessage) rpcResp {
	resp := rpcResp{JSONRPC: "2.0", ID: id}
	switch name {
	case "spawn":
		var a struct {
			Name, Agent, Team, Role, Cwd string
		}
		if err := json.Unmarshal(args, &a); err != nil {
			resp.Error = &rpcErr{Code: -32602, Message: "invalid args: " + err.Error()}
			return resp
		}
		role := runtime.Role(a.Role)
		if role == "" {
			role = runtime.RoleWorker
		}
		info, _, err := s.rt.Spawn(runtime.SpawnSpec{
			Name:      a.Name,
			AgentSlug: a.Agent,
			Team:      a.Team,
			Role:      role,
			Cwd:       a.Cwd,
		})
		if err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]any{"id": info.ID, "name": info.Name}
	case "assign":
		var a struct{ Name, Team, Role string }
		_ = json.Unmarshal(args, &a)
		if err := s.rt.Assign(a.Name, a.Team, runtime.Role(a.Role)); err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]any{"ok": true}
	case "grant_channel":
		var a struct{ FromTeam, ToTeam, Scope string }
		_ = json.Unmarshal(args, &a)
		s.rt.GrantChannel(a.FromTeam, a.ToTeam, a.Scope)
		resp.Result = map[string]any{"ok": true}
	case "list":
		infos := s.rt.List()
		var out []map[string]any
		for _, info := range infos {
			out = append(out, map[string]any{
				"name": info.Name, "agent": info.AgentSlug, "team": info.Team,
				"role": info.Role, "paused": info.Paused,
				"shepherding": info.Shepherding, "created_at": info.CreatedAt,
			})
		}
		resp.Result = map[string]any{"sessions": out}
	case "set_scorecard":
		var a struct {
			Name           string
			G, V, F, E, D  float64
			Note           string
		}
		if err := json.Unmarshal(args, &a); err != nil {
			resp.Error = &rpcErr{Code: -32602, Message: "invalid args: " + err.Error()}
			return resp
		}
		if err := s.rt.SetScorecard(s.CurrentCaller(), a.Name, runtime.Scorecard{
			Goal: a.G, Velocity: a.V, Focus: a.F, EndState: a.E, Discipline: a.D, Note: a.Note,
		}); err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]any{"ok": true}
	case "record_verdict":
		var a struct {
			Name, Verdict, Message string
		}
		_ = json.Unmarshal(args, &a)
		if err := s.rt.RecordVerdict(s.CurrentCaller(), a.Name, a.Verdict, a.Message); err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]any{"ok": true}
	case "get_peer_card":
		// #404 P0.1 — return a sibling's PeerAgentCard. Builds from the
		// live runtime registry; same shape as the HTTP endpoint
		// /api/v1/sessions/<name>/agent-card so the two sources of
		// truth agree.
		var a struct{ Name string }
		if err := json.Unmarshal(args, &a); err != nil {
			resp.Error = &rpcErr{Code: -32602, Message: "invalid args: " + err.Error()}
			return resp
		}
		if a.Name == "" {
			resp.Error = &rpcErr{Code: -32602, Message: "name is required"}
			return resp
		}
		_, info := s.rt.Get(a.Name)
		if info == nil {
			resp.Error = &rpcErr{Code: -32000, Message: "no such session: " + a.Name}
			return resp
		}
		resp.Result = runtime.BuildPeerAgentCard(info)
	case "peer_status":
		// #404 P0.2 — return a sibling's PeerStatus (live activity +
		// ring excerpt). Same shape as
		// GET /api/v1/sessions/<name>/peer-status.
		var a struct{ Name string }
		if err := json.Unmarshal(args, &a); err != nil {
			resp.Error = &rpcErr{Code: -32602, Message: "invalid args: " + err.Error()}
			return resp
		}
		if a.Name == "" {
			resp.Error = &rpcErr{Code: -32602, Message: "name is required"}
			return resp
		}
		status := s.rt.BuildPeerStatus(a.Name)
		if status == nil {
			resp.Error = &rpcErr{Code: -32000, Message: "no such session: " + a.Name}
			return resp
		}
		resp.Result = status
	case "read_pane":
		var a struct {
			Name  string
			Lines int
		}
		_ = json.Unmarshal(args, &a)
		if a.Lines <= 0 {
			a.Lines = 50
		}
		sess, info := s.rt.Get(a.Name)
		if sess == nil {
			resp.Error = &rpcErr{Code: -32000, Message: "no such session: " + a.Name}
			return resp
		}
		// Subscribe + immediately unsubscribe to grab the ring buffer snapshot.
		sub, replay, err := sess.Subscribe(1)
		if err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: err.Error()}
			return resp
		}
		sess.Unsubscribe(sub)
		lines := splitLines(string(replay))
		if len(lines) > a.Lines {
			lines = lines[len(lines)-a.Lines:]
		}
		resp.Result = map[string]any{
			"name": info.Name, "agent": info.AgentSlug,
			"lines": lines, "total_lines": len(lines),
		}
	case "send_to_session":
		// v0.9.2 shim: chepherd.send_to_session translates onto A2A
		// SendMessage via the injected Deliverer. DEPRECATED — callers
		// should migrate to A2A SendMessage directly; the shim removes
		// in v1.0. Per architect 2026-05-29.
		//
		// Behavior carry-over from v0.9.1:
		//   - typing-skip delay (operator-wrote-within-15s) preserved
		//     here in the shim layer so the Deliverer stays substrate-
		//     agnostic
		//   - early-bail "no such session" -32000 preserved for caller
		//     compatibility (Deliverer would return its own error but
		//     legacy callers expect this exact code)
		//
		// v0.9.1 features intentionally dropped (acceptable for a
		// soon-to-be-removed shim):
		//   - no_submit flag (no external callers exercise it; A2A
		//     SendMessage always submits)
		//   - Inject-not-Write distinction (lastOperatorWrite bumping)
		//     — the A2A Deliverer uses session.Write
		var a struct {
			Name, Body string
			NoSubmit   bool `json:"no_submit"`
		}
		_ = json.Unmarshal(args, &a)
		sess, _ := s.rt.Get(a.Name)
		if sess == nil {
			resp.Error = &rpcErr{Code: -32000, Message: "no such session: " + a.Name}
			return resp
		}
		// #447 PR1 — two-channel delivery (operator-confirmed agreement
		// 2026-05-31). Body travels OUT-OF-BAND via MCP notifications/
		// peer-message; PTY receives ONLY a status-line observability
		// ping in PR2. NEVER writes body to PTY stdin (that's what
		// pre-#447 did + raced with operator typing + did not actually
		// submit because claude TUI doesn't treat bare CR as submit).
		body := strings.TrimRight(a.Body, "\r\n")
		taskID := uuid.NewString()
		from := s.CurrentCaller()
		if err := s.PushPeerMessage(a.Name, from, body, taskID); err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: "peer-message push failed: " + err.Error()}
			return resp
		}
		resp.Result = map[string]any{
			"ok":        true,
			"taskId":    taskID,
			"delivered": "mcp",
		}
		_ = sess // PR2 will use sess.WriteStatusLine here for the operator-visible ping
	case "pause":
		var a struct {
			Name   string
			Paused bool
		}
		_ = json.Unmarshal(args, &a)
		if err := s.rt.Pause(a.Name, a.Paused); err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]any{"ok": true}
	case "alert_human":
		var a struct{ Body, Kind, Urgency, From string }
		_ = json.Unmarshal(args, &a)
		from := a.From
		if from == "" {
			from = s.CurrentCaller()
		}
		// v0.6: include kind in the inbox body so dashboard can render
		// per-kind treatment. Default kind = "alert" for legacy callers
		// that don't pass it.
		body := a.Body
		if a.Kind != "" {
			body = "[" + a.Kind + "] " + body
		}
		s.rt.HumanInbox(from, body)
		resp.Result = map[string]any{"ok": true}
	case "create_team":
		var a struct {
			Name, CanonPath, Topology string
		}
		_ = json.Unmarshal(args, &a)
		if a.Name == "" {
			resp.Error = &rpcErr{Code: -32602, Message: "name required"}
			return resp
		}
		t, created := s.rt.CreateTeam(a.Name, a.CanonPath, runtime.Topology(a.Topology))
		resp.Result = map[string]any{"team": t, "created": created}
	case "join_team":
		var a struct {
			Agent, Team, Role, BriefOverride string
		}
		_ = json.Unmarshal(args, &a)
		m, err := s.rt.JoinTeam(a.Agent, a.Team, runtime.MembershipRole(a.Role), a.BriefOverride)
		if err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]any{"membership": m}
	case "leave_team":
		var a struct{ Agent, Team string }
		_ = json.Unmarshal(args, &a)
		ok := s.rt.LeaveTeam(a.Agent, a.Team)
		resp.Result = map[string]any{"ok": ok}
	case "list_teams":
		teams := s.rt.ListTeams()
		resp.Result = map[string]any{"teams": teams}
	case "list_memberships":
		var a struct{ Agent, Team string }
		_ = json.Unmarshal(args, &a)
		m := s.rt.ListMemberships(a.Agent, a.Team)
		resp.Result = map[string]any{"memberships": m}
	case "set_review_axis":
		var a struct {
			Reviewer, Target, Axis, Note string
			Score                        float64
		}
		_ = json.Unmarshal(args, &a)
		// Default reviewer to the caller's name from MCP connection
		// identity (#89). Callers can still override by passing reviewer:
		// explicitly (e.g., orchestrator forwarding on behalf of another).
		if a.Reviewer == "" {
			a.Reviewer = s.CurrentCaller()
		}
		if err := s.rt.SetReviewAxis(a.Reviewer, a.Target, a.Axis, a.Score, a.Note); err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]any{"ok": true}
	case "note":
		var a struct{ Target, Body string }
		_ = json.Unmarshal(args, &a)
		// Lightweight observation — append to the target's scorecard note
		// without disturbing the scores. If no scorecard exists yet, write
		// to events log only.
		_, info := s.rt.Get(a.Target)
		if info != nil && info.Scorecard != nil {
			info.Scorecard.Note = info.Scorecard.Note + "\n" + a.Body
		}
		s.rt.RecordEvent(runtime.Event{
			Kind: "note", Actor: "shepherd",
			Body: a.Target + ": " + a.Body,
			Meta: map[string]any{"target": a.Target, "body": a.Body},
		})
		resp.Result = map[string]any{"ok": true}
	case "record_event":
		var a struct{ Kind, Body, Actor string }
		_ = json.Unmarshal(args, &a)
		if a.Actor == "" {
			a.Actor = "mcp"
		}
		s.rt.RecordEvent(runtime.Event{
			Kind: a.Kind, Actor: a.Actor, Body: a.Body,
		})
		resp.Result = map[string]any{"ok": true}
	case "read_canon":
		var a struct{ Team string }
		_ = json.Unmarshal(args, &a)
		teams := s.rt.ListTeams()
		var canon string
		for _, t := range teams {
			if t.Name == a.Team {
				if b, err := os.ReadFile(t.CanonPath); err == nil {
					canon = string(b)
				}
				break
			}
		}
		resp.Result = map[string]any{"team": a.Team, "canon": canon}
	case "spawn_worker":
		// Orchestrator-only: spawn a worker in the caller's team.
		if !s.callerHasAuthority() {
			resp.Error = &rpcErr{Code: -32000, Message: "authority denied: caller role must be orchestrator or shepherd to spawn workers"}
			return resp
		}
		var a struct {
			Name          string `json:"name,omitempty"`
			Agent         string `json:"agent,omitempty"`
			Cwd           string `json:"cwd,omitempty"`
			BriefOverride string `json:"brief_override,omitempty"`
		}
		_ = json.Unmarshal(args, &a)
		if a.Name == "" {
			resp.Error = &rpcErr{Code: -32602, Message: "name required"}
			return resp
		}
		agent := a.Agent
		if agent == "" {
			agent = "claude-code"
		}
		// Inherit team from caller's first membership, or "default"
		callerTeam := "default"
		for _, m := range s.rt.ListMemberships(s.CurrentCaller(), "") {
			callerTeam = m.TeamName
			break
		}
		spec := runtime.SpawnSpec{
			Name:         a.Name,
			AgentSlug:    agent,
			Team:         callerTeam,
			Role:         runtime.RoleWorker,
			Cwd:          a.Cwd,
			SystemPrompt: a.BriefOverride,
		}
		_, _, err := s.rt.Spawn(spec)
		if err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: "spawn_worker: " + err.Error()}
			return resp
		}
		resp.Result = map[string]any{"ok": true, "name": a.Name, "team": callerTeam}
	case "stop_session":
		// Orchestrator-only: terminate a session.
		if !s.callerHasAuthority() {
			resp.Error = &rpcErr{Code: -32000, Message: "authority denied: caller role must be orchestrator or shepherd to stop sessions"}
			return resp
		}
		var a struct{ Name string }
		_ = json.Unmarshal(args, &a)
		if err := s.rt.Stop(a.Name); err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: "stop_session: " + err.Error()}
			return resp
		}
		s.rt.RecordEvent(runtime.Event{
			Kind:  "session_stopped",
			Actor: s.CurrentCaller(),
			Body:  fmt.Sprintf("orchestrator %q stopped session %q", s.CurrentCaller(), a.Name),
		})
		resp.Result = map[string]any{"ok": true, "stopped": a.Name}
	default:
		resp.Error = &rpcErr{Code: -32601, Message: "unknown chepherd tool: " + name}
	}
	return resp
}

// callerHasAuthority returns true if the current MCP caller has orchestration
// authority — i.e., their role in any team is "orchestrator" or "shepherd".
func (s *Server) callerHasAuthority() bool {
	caller := s.CurrentCaller()
	if caller == "" || caller == "anonymous" {
		return false
	}
	for _, m := range s.rt.ListMemberships(caller, "") {
		if m.Role == runtime.RoleMemberOrchestrator || m.Role == runtime.RoleMemberShepherd {
			return true
		}
	}
	// Also check the session's own role field (set at spawn time)
	_, si := s.rt.Get(caller)
	if si != nil && (si.Role == "orchestrator" || si.Role == "shepherd") {
		return true
	}
	return false
}

// ----- JSON-RPC types -----

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResp struct {
	JSONRPC string  `json:"jsonrpc"`
	ID      any     `json:"id,omitempty"`
	Result  any     `json:"result,omitempty"`
	Error   *rpcErr `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// #447 PR1 — registerPeer adds a live WS conn to the peer registry.
// Called from handleWS once connAgent is identified (either at upgrade
// via ?agent= or via $/chepherd/identify). Returns an unregister fn
// the caller defers.
func (s *Server) registerPeer(name string, c *websocket.Conn) func() {
	if name == "" {
		return func() {}
	}
	if s.peers == nil {
		s.peersMu.Lock()
		if s.peers == nil {
			s.peers = make(map[string]peerConn)
		}
		s.peersMu.Unlock()
	}
	mu := &sync.Mutex{}
	s.peersMu.Lock()
	s.peers[name] = peerConn{c: c, mu: mu}
	s.peersMu.Unlock()
	fmt.Fprintf(os.Stderr, "[chepherd-mcp] peer registered: %q\n", name)
	return func() {
		s.peersMu.Lock()
		// Only unregister if we're still the same conn (handles rapid
		// reconnect where new registration arrived before our defer).
		if pc, ok := s.peers[name]; ok && pc.c == c {
			delete(s.peers, name)
			fmt.Fprintf(os.Stderr, "[chepherd-mcp] peer unregistered: %q\n", name)
		}
		s.peersMu.Unlock()
	}
}

// PushPeerMessage sends an MCP notifications/peer-message frame to the
// named recipient's live WS conn. Returns nil if delivered, error if
// the peer isn't currently connected OR the WS write fails. Body
// travels OUT-OF-BAND of the recipient's stdin — claude-code's MCP
// client renders it inline without touching the input prompt.
//
// Notification envelope:
//
//	{"jsonrpc":"2.0","method":"notifications/peer-message",
//	 "params":{"from":"A","body":"X","taskID":"…","timestamp":"…"}}
//
// JSON-RPC 2.0 notifications have NO "id" field per spec; the
// recipient doesn't reply.
func (s *Server) PushPeerMessage(toName, fromName, body, taskID string) error {
	if s.peers == nil {
		return fmt.Errorf("peer registry empty (recipient %q not connected)", toName)
	}
	s.peersMu.Lock()
	pc, ok := s.peers[toName]
	s.peersMu.Unlock()
	if !ok {
		return fmt.Errorf("peer %q not connected (no live MCP WS conn)", toName)
	}
	notification := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/peer-message",
		"params": map[string]any{
			"from":      fromName,
			"body":      body,
			"taskID":    taskID,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if err := pc.c.WriteJSON(notification); err != nil {
		return fmt.Errorf("WS write to peer %q: %w", toName, err)
	}
	fmt.Fprintf(os.Stderr, "[chepherd-mcp] peer-message pushed: from=%q to=%q taskID=%q bytes=%d\n",
		fromName, toName, taskID, len(body))
	return nil
}

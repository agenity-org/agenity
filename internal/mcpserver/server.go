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
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/graphify"
	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/runtime"
)

// SetTaskStore wires the runner's R2-owned TaskRepository so the
// chepherd.get_task MCP tool (#473 Wave K2) can fetch persisted
// tasks recipient-scoped. nil disables the tool.
func (s *Server) SetTaskStore(store persistence.TaskRepository) {
	s.taskStore = store
}

// taskFetchMarker is the optional #79 seam: an a2a.Deliverer that wants
// to know when a recipient called chepherd.get_task implements
// MarkTaskFetched. runtime.A2ADeliverer does; federation deliverers and
// test fakes don't (the get_task handler's type-assert is a no-op for
// them). Kept here (not in a2a) so the deliverer interface stays minimal.
type taskFetchMarker interface {
	MarkTaskFetched(taskID string)
}

// Server hosts the chepherd MCP JSON-RPC surface. Constructed once per
// chepherd-run process. The HTTP/WebSocket listener is started via
// StartHTTP() and torn down via Stop().
type Server struct {
	rt           *runtime.Runtime
	deliverer    a2a.Deliverer // v0.9.2: backs the chepherd.send_to_session shim onto A2A SendMessage. Removed in v1.0.
	httpListener net.Listener
	httpServer   *http.Server
	// extraListeners + extraServers hold additional listeners bound
	// by AddHTTPListener (#478 Wave M2). chepherd-runner uses this
	// to expose the same MCP handler on BOTH the canonical Unix
	// socket AND a localhost TCP port (the agent-facing transport,
	// since claude-code's HTTP transport requires a TCP URL).
	extraListeners []net.Listener
	extraServers   []*http.Server
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

	// a2aBaseURL is the daemon's externally-reachable base URL
	// (scheme://host[:port]) used by chepherd.list_peers to template
	// absolute §12.1 agent_card_urls. Empty leaves URLs relative.
	// Set via SetA2ABaseURL — wired from cmd/run.go. #474 Wave K3.
	a2aBaseURL string

	// taskStore backs the chepherd.get_task MCP tool (#473 Wave K2).
	// Runner's MCP server gets this set to its R2-owned sqlite store
	// so agents can fetch tasks emitted via knock markers. nil
	// disables the tool (returns -32000 "task store not wired").
	taskStore persistence.TaskRepository

	// keepAliveInterval overrides the streamable-HTTP SSE keep-alive
	// cadence. Zero (the default in both constructors) means use the
	// package const sseKeepAliveInterval; tests inject a short interval
	// to assert keep-alive frames flow without waiting 15s. Read via
	// sseKeepAlive().
	keepAliveInterval time.Duration
}

// sseKeepAlive returns the effective SSE keep-alive interval: the
// per-server override when set (>0), otherwise the package default.
func (s *Server) sseKeepAlive() time.Duration {
	if s.keepAliveInterval > 0 {
		return s.keepAliveInterval
	}
	return sseKeepAliveInterval
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
		summary := toolCallSummary(req.Params) // #741 — log tool name + peer target for comms visibility
		resp := s.toolCall(req)
		if resp.Error != nil {
			fmt.Fprintf(os.Stderr, "[chepherd-mcp] %s: tools/call %s → ERROR %d: %s\n", caller, summary, resp.Error.Code, resp.Error.Message)
		} else {
			fmt.Fprintf(os.Stderr, "[chepherd-mcp] %s: tools/call %s → OK\n", caller, summary)
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
	// #743 — notifications/* are JSON-RPC notifications; the MCP spec expects
	// NO error response. The old -32601 fall-through made gemini-cli treat the
	// server as broken ("MCP issues detected") and re-handshake in a loop
	// (claude tolerated it; gemini does not). Ack with a non-error empty result.
	if strings.HasPrefix(req.Method, "notifications/") {
		return rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	}
	// #744 follow-up — gemini-cli (unlike claude-code) probes the full MCP
	// discovery surface after tools/list: prompts/list, resources/list, and
	// resources/templates/list. chepherd is a tools-only server, so the old
	// -32601 fall-through on these made gemini flag "MCP issues detected" even
	// though initialize + tools/list succeeded. Answer with empty collections
	// (a valid, spec-compliant "this server has none") so gemini sees a clean
	// server. Operator-observed live on a gemini-cli agent stuck after a knock.
	switch req.Method {
	case "prompts/list":
		return rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"prompts": []any{}}}
	case "resources/list":
		return rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"resources": []any{}}}
	case "resources/templates/list":
		return rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"resourceTemplates": []any{}}}
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
		// #474 Wave K3 — V0.9.2-ARCHITECTURE §10 Pattern 1 step 1.
		// Returns the §12.2 Agent Card directory shape {sid, name,
		// agent_card_url} for peers in the CALLER's team (or the
		// `team` arg override). Distinct from chepherd.list which
		// returns session metadata for ALL teams.
		{"name": "chepherd.list_peers", "description": "Enumerate Agent Cards for runners in your team. Returns {sid, name, agent_card_url} per peer (excludes yourself). The agent_card_url points at the §12.1 well-known URI; resolve with A2A's SendMessage / GetTask. Args: team (string, optional — overrides caller's own team scope). Distinct from chepherd.list which returns session metadata for ALL teams globally.", "inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"team": map[string]any{"type": "string"},
			},
		}},
		// #473 Wave K2 — V0.9.2-ARCH §10 Pattern 1 step 14-15.
		// Recipient-scoped: caller's name MUST match task.contextID
		// or the call returns isError=true (-32004 forbidden). This
		// prevents cross-task task-data leakage between agents
		// hosted by the same daemon / sharing the runner's MCP.
		{"name": "chepherd.get_task", "description": "Fetch an A2A task envelope by taskID. Recipient-scoped: caller's @-handle MUST match the task's contextID (the recipient runner). Call this when you see a knock marker `[chepherd-knock taskID=<uuid> from=<name>]` in your PTY — the marker tells you a task arrived; this tool returns the body. Args: taskID (string, required).", "inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"taskID"},
			"properties": map[string]any{
				"taskID": map[string]any{"type": "string"},
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
		{"name": "chepherd.graph_explain", "description": "Query YOUR repo's code knowledge graph (#725): a plain-language explanation of a code node (function/class/symbol) and its neighbors — calls, dependencies, definitions. Prefer this over grepping/reading files to understand structure (far fewer tokens). Scoped to your own session's repo. Args: node (string, required — e.g. a function or symbol name).", "inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"node": map[string]any{"type": "string"}},
			"required":   []string{"node"},
		}},
		{"name": "chepherd.graph_path", "description": "Query YOUR repo's code knowledge graph (#725): the shortest dependency/call path between two code nodes. Scoped to your own session's repo. Args: from (string, required), to (string, required).", "inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"from": map[string]any{"type": "string"}, "to": map[string]any{"type": "string"}},
			"required":   []string{"from", "to"},
		}},
	}
}

// toolCallSummary extracts the tool name (+ peer target for send-style tools)
// from a tools/call params blob so the daemon log makes agent→agent comms
// visible — e.g. "tools/call chepherd.send_to_session→m-coder → OK". (#741)
func toolCallSummary(params json.RawMessage) string {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return "?"
	}
	out := p.Name
	if strings.Contains(p.Name, "send_to_session") || strings.Contains(p.Name, "get_peer_card") || strings.Contains(p.Name, "peer_status") || strings.Contains(p.Name, "get_task") {
		var a struct {
			Name    string `json:"name"`
			Session string `json:"session"`
			To      string `json:"to"`
		}
		if json.Unmarshal(p.Arguments, &a) == nil {
			tgt := a.Name
			if tgt == "" {
				tgt = a.Session
			}
			if tgt == "" {
				tgt = a.To
			}
			if tgt != "" {
				out += "→" + tgt
			}
		}
	}
	return out
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
		// #671 — merge in #669 externally-registered A2A peers so
		// discovery surfaces the full team membership (not just
		// chepherd-managed PTY sessions). External entries carry
		// external=true so callers can distinguish; role is empty
		// since the peer self-describes via its AgentCard.
		if s.rt.Peers() != nil {
			for _, p := range s.rt.Peers().List() {
				out = append(out, map[string]any{
					"name": p.Name, "agent": "external-a2a", "team": p.Team,
					"role": "", "paused": false,
					"shepherding": "", "created_at": p.JoinedAt,
					"external": true,
				})
			}
		}
		resp.Result = map[string]any{"sessions": out}
	case "list_peers":
		// #474 Wave K3 — V0.9.2-ARCHITECTURE §10 Pattern 1 step 1.
		// Returns the §12.2 Agent Card directory shape
		// {sid, name, agent_card_url} for peers in the CALLER's team
		// (or the explicit `team` arg if set).
		//
		// Source: rt.List() filtered by team membership + caller
		// exclusion. Same data path as the daemon's
		// /api/v1/agents/ directory handler (#467 Wave D1
		// agentsDirectory) — wire shape matched verbatim. When Wave
		// R6/R7 lands the runner-WS registration table, the data
		// source switches server-side without changing the shape.
		var a struct {
			Team string `json:"team"`
		}
		_ = json.Unmarshal(args, &a)
		caller := s.CurrentCaller()
		teamFilter := a.Team
		if teamFilter == "" {
			// Resolve caller's own team from the runtime registry.
			// Empty team → empty result, NOT global — list_peers is
			// scoped by design (chepherd.list is the global tool).
			_, callerInfo := s.rt.Get(caller)
			if callerInfo != nil {
				teamFilter = callerInfo.Team
			}
		}
		peers := buildListPeersEntries(s.rt, caller, teamFilter, s.a2aBaseURL)
		resp.Result = map[string]any{"peers": peers, "team": teamFilter}
	case "get_task":
		// #473 Wave K2 — V0.9.2-ARCH §10 Pattern 1 steps 14-15.
		// Recipient-scoped: caller's @-handle must match the task's
		// ContextID (the recipient runner). Errors:
		//   -32000 task store not wired (runner config gap)
		//   -32004 forbidden (caller != recipient)
		//   -32602 invalid params (missing taskID)
		//   -32603 internal (store error other than not-found)
		// Standard tools/call -32603 path for not-found taskID
		// surfaces as isError=true with a "task not found" message.
		var a struct {
			TaskID string `json:"taskID"`
		}
		_ = json.Unmarshal(args, &a)
		if a.TaskID == "" {
			resp.Error = &rpcErr{Code: -32602, Message: "chepherd.get_task: taskID required"}
			break
		}
		if s.taskStore == nil {
			resp.Error = &rpcErr{Code: -32000, Message: "chepherd.get_task: task store not wired (runner SetTaskStore missing)"}
			break
		}
		callerName := s.CurrentCaller()
		rec, err := s.taskStore.Get(context.Background(), a.TaskID)
		if err != nil || rec == nil {
			resp.Error = &rpcErr{Code: -32603, Message: fmt.Sprintf("chepherd.get_task: task %q not found", a.TaskID)}
			break
		}
		// Decode InputBlob to recover the original Message (which
		// carries ContextID = recipient runner's sid). RecipientCheck:
		// callerName must equal that ContextID.
		var inputMsg a2a.Message
		_ = json.Unmarshal(rec.InputBlob, &inputMsg)
		if callerName != "" && inputMsg.ContextID != "" && callerName != inputMsg.ContextID {
			resp.Error = &rpcErr{Code: -32004, Message: fmt.Sprintf("chepherd.get_task: forbidden (caller %q is not task recipient %q)", callerName, inputMsg.ContextID)}
			break
		}
		// #79 — tell the re-knock watchdog the recipient actually fetched
		// this task, so it won't re-inject the knock. Recorded only after
		// the recipient-scope check above passes (a forbidden caller's
		// get_task must NOT count as the real recipient acting). Best-
		// effort: deliverers that don't implement the marker (federation /
		// tests) are a no-op.
		if m, ok := s.deliverer.(taskFetchMarker); ok {
			m.MarkTaskFetched(a.TaskID)
		}
		// Return the canonical A2A task envelope. OutputBlob already
		// carries it (runnerDeliverer marshals task there on Save).
		var taskEnv map[string]any
		_ = json.Unmarshal(rec.OutputBlob, &taskEnv)
		resp.Result = map[string]any{
			"task":  taskEnv,
			"input": inputMsg,
		}
	case "set_scorecard":
		var a struct {
			Name          string
			G, V, F, E, D float64
			Note          string
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
		// "operator"/"human" is the human, NOT an agent PTY session, so
		// s.rt.Get returns nil and the shim used to reply "no such session:
		// operator" — silently dropping every agent reply addressed to the
		// operator. An operator message arrives with from="operator", so when
		// the agent replies via send_to_session to that handle (exactly as the
		// knock-pattern briefing instructs) it hit this dead path and the
		// operator saw nothing in Talk (operator-reported 2026-06-20: messaged
		// all 5 agents, the MCP log showed send_to_session→operator → OK, but
		// the transcript had zero replies). Route operator-addressed messages
		// into the HumanInbox — the same sink alert_human uses — so they
		// surface in the Talk transcript (collectTranscriptRows section 3) AND
		// the dashboard inbox. No Deliverer/PTY session needed for this path.
		if name := strings.ToLower(strings.TrimSpace(a.Name)); name == "operator" || name == "human" {
			s.rt.HumanInbox(s.CurrentCaller(), strings.TrimRight(a.Body, "\r\n"))
			resp.Result = map[string]any{"ok": true, "routed": "operator-inbox"}
			return resp
		}
		if s.deliverer == nil {
			resp.Error = &rpcErr{Code: -32000, Message: "send_to_session shim unavailable: mcpserver constructed without a2a.Deliverer (use mcpserver.NewWithDeliverer)"}
			return resp
		}
		sess, _ := s.rt.Get(a.Name)
		if sess == nil {
			resp.Error = &rpcErr{Code: -32000, Message: "no such session: " + a.Name}
			return resp
		}
		// Typing-skip preserved at the shim layer (operator-vs-shepherd
		// race condition was a v0.9.1 chepherd-specific concern).
		const founderTypingSkipSec = 15
		if last := sess.LastOperatorWrite(); !last.IsZero() && time.Since(last) < founderTypingSkipSec*time.Second {
			remaining := founderTypingSkipSec*time.Second - time.Since(last)
			time.Sleep(remaining)
		}
		body := strings.TrimRight(a.Body, "\r\n")
		task, err := s.deliverer.Deliver(context.Background(), a2a.Message{
			Role:      "user",
			Kind:      "message",
			ContextID: a.Name,
			Parts:     []a2a.Part{{Kind: "text", Text: body}},
			From:      s.CurrentCaller(),
		})
		if err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]any{
			"ok":     task != nil && task.Status.State == a2a.TaskStateWorking,
			"taskId": taskIDOrEmpty(task),
		}
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
	case "graph_explain":
		var a struct {
			Node string `json:"node"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.Node == "" {
			resp.Error = &rpcErr{Code: -32602, Message: "graph_explain: 'node' required"}
			return resp
		}
		gp, gerr := s.graphPathForCaller()
		if gerr != nil {
			resp.Error = &rpcErr{Code: -32000, Message: gerr.Error()}
			return resp
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := graphify.New().Explain(ctx, gp, a.Node)
		if err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]any{"node": a.Node, "explanation": out}
	case "graph_path":
		var a struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.From == "" || a.To == "" {
			resp.Error = &rpcErr{Code: -32602, Message: "graph_path: 'from' and 'to' required"}
			return resp
		}
		gp, gerr := s.graphPathForCaller()
		if gerr != nil {
			resp.Error = &rpcErr{Code: -32000, Message: gerr.Error()}
			return resp
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := graphify.New().ShortestPath(ctx, gp, a.From, a.To)
		if err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]any{"from": a.From, "to": a.To, "path": out}
	default:
		resp.Error = &rpcErr{Code: -32601, Message: "unknown chepherd tool: " + name}
	}
	return resp
}

// graphPathForCaller resolves the calling agent's OWN session working dir and
// returns the path to its code knowledge graph (#725). This is the per-agent
// scoping primitive: an agent can only query the graph for the repo it is
// assigned, because the path is derived from its own session — never an
// arbitrary one. Errors when there is no caller identity, no workspace, or
// the graph has not been built yet (it builds asynchronously on spawn).
func (s *Server) graphPathForCaller() (string, error) {
	caller := s.CurrentCaller()
	if caller == "" {
		return "", fmt.Errorf("graph query: no caller identity")
	}
	_, info := s.rt.Get(caller)
	if info == nil || info.Cwd == "" {
		return "", fmt.Errorf("graph query: caller %q has no workspace", caller)
	}
	gp := graphify.New().GraphPath(info.Cwd)
	if _, err := os.Stat(gp); err != nil {
		return "", fmt.Errorf("graph not built yet for %q (it builds on spawn)", caller)
	}
	return gp, nil
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

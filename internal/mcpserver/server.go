// Package mcpserver implements chepherd's MCP server — the control-plane
// tool surface every chepherd-hosted agent calls into.
//
// Wire: MCP over stdio JSON-RPC 2.0. The chepherd binary's `mcp` subcommand
// is the stdio bridge — it connects to chepherd's main runtime via a Unix
// socket at $XDG_STATE/chepherd-v05/runtime.sock, then proxies the agent's
// stdio JSON-RPC over that socket.
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
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chepherd/chepherd/internal/runtime"
)

// Server hosts the JSON-RPC over a Unix socket. Constructed once per
// chepherd-run process.
type Server struct {
	rt       *runtime.Runtime
	sockPath string
	listener net.Listener
	// lastCaller is the most-recently-identified agent name. Set per
	// dispatch in dispatchWithAgent — handlers read it to attribute
	// events. NOT thread-safe across concurrent dispatches; serveConn
	// is per-connection sequential so this works for now (one agent
	// per conn). Future: pass through context.Context if we need
	// concurrent calls per conn.
	lastCaller string
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

// New constructs a Server bound to the runtime + a Unix socket at sockPath.
func New(rt *runtime.Runtime, sockPath string) *Server {
	return &Server{rt: rt, sockPath: sockPath}
}

// DefaultSockPath returns the canonical chepherd runtime socket location.
func DefaultSockPath(stateDir string) string {
	return filepath.Join(stateDir, "runtime.sock")
}

// Start binds the Unix socket + serves connections in goroutines.
// Idempotent across Stop()→Start(). Returns once listen is established;
// the accept loop runs in a goroutine.
func (s *Server) Start() error {
	_ = os.MkdirAll(filepath.Dir(s.sockPath), 0o700)
	_ = os.Remove(s.sockPath) // stale socket from prior unclean exit
	l, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return fmt.Errorf("mcp: listen %s: %w", s.sockPath, err)
	}
	_ = os.Chmod(s.sockPath, 0o600)
	s.listener = l
	go s.acceptLoop()
	return nil
}

// Stop closes the listener; any in-flight connections see EOF.
func (s *Server) Stop() {
	if s.listener != nil {
		_ = s.listener.Close()
		_ = os.Remove(s.sockPath)
	}
}

func (s *Server) acceptLoop() {
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go s.serveConn(c)
	}
}

// serveConn handles one MCP client (one chepherd-mcp subprocess bridging
// one agent). Reads newline-delimited JSON-RPC requests, dispatches to
// tool handlers, writes responses.
//
// Identity: the bridge sends a non-MCP "$/chepherd/identify" first frame
// with {agent: "<name>"} so the server can attribute every subsequent
// tool call to the correct agent. Closes #89.
func (s *Server) serveConn(c net.Conn) {
	defer c.Close()
	scanner := bufio.NewScanner(c)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	w := bufio.NewWriter(c)
	var connAgent string
	for scanner.Scan() {
		var req rpcReq
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			s.writeErr(w, nil, -32700, "parse error: "+err.Error())
			continue
		}
		// Identity frame — capture caller name + skip dispatch.
		if req.Method == "$/chepherd/identify" {
			var p struct {
				Agent string `json:"agent"`
			}
			_ = json.Unmarshal(req.Params, &p)
			connAgent = p.Agent
			_ = writeJSON(w, rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"ok": true, "agent": connAgent}})
			continue
		}
		resp := s.dispatchWithAgent(&req, connAgent)
		if err := writeJSON(w, resp); err != nil {
			return
		}
	}
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
func (s *Server) dispatch(req *rpcReq) rpcResp {
	if req.Method == "" {
		return rpcResp{JSONRPC: "2.0", ID: req.ID, Error: &rpcErr{Code: -32600, Message: "invalid request"}}
	}
	// Standard MCP discovery
	if req.Method == "initialize" {
		return rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "chepherd", "version": "0.5.0"},
		}}
	}
	if req.Method == "tools/list" {
		return rpcResp{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": s.toolList()}}
	}
	if req.Method == "tools/call" {
		return s.toolCall(req)
	}

	// Direct chepherd.* method calls (non-MCP test path)
	if strings.HasPrefix(req.Method, "chepherd.") {
		return s.toolCallDirect(req.ID, strings.TrimPrefix(req.Method, "chepherd."), req.Params)
	}
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
		{"name": "chepherd.set_scorecard", "description": "Shepherd-only: record a 0..10 score for each axis of a worker. Args: name, G, V, F, E, D, note (optional). G=Goal clarity, V=Velocity, F=Focus, E=End-state proximity, D=Discipline (CLAUDE.md compliance).", "inputSchema": map[string]any{
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
		{"name": "chepherd.record_verdict", "description": "Shepherd-only: record a per-tick verdict for a worker. Args: name, verdict ('silent'|'praise'|'coach'|'intervene'), message (optional). Increments intervention count on coach/intervene.", "inputSchema": map[string]any{
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
		{"name": "chepherd.send_to_session", "description": "Write a message directly into a session's PTY stdin. Used by the shepherd to advise Adam (prefer @target relay for normal conversation). Args: name, body.", "inputSchema": map[string]any{
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
		{"name": "chepherd.note", "description": "Shepherd-only: attach a per-worker observation note (lightweight, goes to the worker's scorecard.note field — NEVER to the inbox). Use this for routine 'I saw X happen' commentary. Args: target, body.", "inputSchema": map[string]any{
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
		{"name": "chepherd.read_canon", "description": "Read the current canon (CLAUDE.md / team-specific rules) for a team. Returns the canon text. Shepherds should call this every tick to re-ground their judgment against the live canon (which can be edited mid-run via the dashboard's canon-viewer widget). Args: team.", "inputSchema": map[string]any{
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
		// Typing-skip: if the operator wrote keystrokes within the last 15s,
		// delay the inject so it doesn't collide with an in-progress thought.
		const founderTypingSkipSec = 15
		if last := sess.LastOperatorWrite(); !last.IsZero() && time.Since(last) < founderTypingSkipSec*time.Second {
			remaining := founderTypingSkipSec*time.Second - time.Since(last)
			time.Sleep(remaining)
		}

		// Use Inject (not Write) so lastOperatorWrite is not bumped and the
		// writeMu serializes this against any concurrent operator keystrokes.
		body := strings.TrimRight(a.Body, "\r\n")
		if _, err := sess.Inject([]byte(body)); err != nil {
			resp.Error = &rpcErr{Code: -32000, Message: err.Error()}
			return resp
		}
		// Submit by default — send Enter as a SEPARATE PTY write so kitty /
		// modifyOtherKeys-mode TUIs (Claude Code 2.1.148+) treat it as a
		// distinct keypress event rather than part of the input buffer.
		// Tested against claude-code, qwen-code, sovereign-shell. Use
		// no_submit:true if you only want to deposit text into the input.
		if !a.NoSubmit {
			// Brief pause lets the receiver's input editor process the
			// body before the Enter event arrives.
			time.Sleep(120 * time.Millisecond)
			if _, err := sess.Inject([]byte("\r")); err != nil {
				resp.Error = &rpcErr{Code: -32000, Message: err.Error()}
				return resp
			}
		}
		resp.Result = map[string]any{"ok": true}
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
			Name, Agent, Cwd, BriefOverride string `json:"name,omitempty"`
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

func (s *Server) writeErr(w *bufio.Writer, id any, code int, msg string) {
	_ = writeJSON(w, rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcErr{Code: code, Message: msg}})
}

func writeJSON(w *bufio.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		return err
	}
	return w.Flush()
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

// BridgeStdioToSocket is the implementation of the `chepherd mcp` subcommand:
// it bridges agent stdio (in/out) ↔ a runtime Unix socket.
//
// On connect, the bridge sends a non-MCP "$/chepherd/identify" frame with
// the agent's name (read from CHEPHERD_AGENT_NAME env var set by runtime
// at spawn). Server uses this to attribute events to the correct agent
// instead of hardcoding "shepherd" (#89). Server eats the identify frame
// — Claude never sees it.
func BridgeStdioToSocket(sockPath string) error {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return fmt.Errorf("mcp bridge: dial %s: %w", sockPath, err)
	}
	defer conn.Close()

	// Send identify frame first, eat its reply so it doesn't leak to Claude.
	agent := os.Getenv("CHEPHERD_AGENT_NAME")
	if agent != "" {
		idFrame := fmt.Sprintf(`{"jsonrpc":"2.0","id":"$id","method":"$/chepherd/identify","params":{"agent":%q}}`+"\n", agent)
		if _, err := conn.Write([]byte(idFrame)); err == nil {
			// Read one line (the reply) and discard.
			rd := bufio.NewReader(conn)
			_, _ = rd.ReadString('\n')
		}
	}

	errCh := make(chan error, 2)
	go func() { _, err := io.Copy(conn, os.Stdin); errCh <- err }()
	go func() { _, err := io.Copy(os.Stdout, conn); errCh <- err }()
	<-errCh
	return nil
}

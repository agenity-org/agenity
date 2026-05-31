// internal/runtimehttp/runners_register.go — #504 Wave R1.
//
// Daemon-side WS endpoint accepting chepherd-runner registrations.
// chepherd-runner (cmd/runner) dials this endpoint at boot, sends one
// chepherd/register frame to claim or be assigned a session ID, and
// then streams subsequent notifications (audit, pty_output) over the
// same WS until the runner exits.
//
// Wire format (operator-confirmed shape, draft sent to chepherd-
// worker2 #467 for the /api/v1/agents/ directory consumer side):
//
//	# Inbound frames (JSON-RPC 2.0 from runner)
//	{ "jsonrpc":"2.0", "id":1, "method":"chepherd/register",
//	  "params": { "sid":"", "agent_slug":"...", "runner_version":"...",
//	              "a2a_listen":"host:port", "mcp_socket":"...",
//	              "capabilities":["pty","audit-stream"] } }
//
//	{ "jsonrpc":"2.0", "method":"audit",
//	  "params": { "kind":"pty_output"|"event"|...,
//	              "body":"<base64 or text>", "at":"<RFC3339>" } }
//
//	# Outbound (daemon → runner) — only the register response today
//	{ "jsonrpc":"2.0", "id":1, "result": {
//	    "sid":"runner-<uuid>", "daemon_version":"...",
//	    "audit_topic":"runner:<sid>" } }
//
// Wave R1 SCOPE: accept registration + record runner metadata + audit
// stream sink. The runner-MANAGED A2A endpoint, per-session Agent
// Card, PTY-cutover etc. are Waves R2-R5 in their own PRs. This
// endpoint coexists with the daemon's existing in-process Runtime
// until R5 ships the cutover.
//
// Refs #504 Wave R1 #453 (epic) V0.9.2-ARCHITECTURE.md §22.
package runtimehttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/chepherd/chepherd/internal/persistence"
)

// decodeAuditEvent parses a runner-uploaded audit.event params blob
// into a persistence.AuditEventRecord ready for Save. Returns nil
// when the params can't be decoded (audit emission is best-effort;
// daemon shouldn't 5xx on malformed runner uploads).
//
// org_id is stamped from the receiver-daemon's DaemonOrgID at ingest
// — cross-org events from federation peers stay scoped to the
// receiver, not the origin daemon's org.
//
// Refs #489 #488.
func decodeAuditEvent(raw json.RawMessage, orgID string) *persistence.AuditEventRecord {
	if orgID == "" {
		orgID = "default"
	}
	var ev struct {
		EventType string    `json:"event_type"`
		Timestamp time.Time `json:"timestamp"`
		Caller    string    `json:"caller"`
		Callee    string    `json:"callee"`
		Method    string    `json:"method"`
		LatencyMS int64     `json:"latency_ms"`
		JTI       string    `json:"jti"`
		Status    string    `json:"status"`
		Error     string    `json:"error"`
		TaskID    string    `json:"task_id"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil
	}
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	ts := ev.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return &persistence.AuditEventRecord{
		ID:        id.String(),
		OrgID:     orgID,
		EventType: ev.EventType,
		Timestamp: ts,
		Caller:    ev.Caller,
		Callee:    ev.Callee,
		Method:    ev.Method,
		LatencyMS: ev.LatencyMS,
		JTI:       ev.JTI,
		Status:    ev.Status,
		Error:     ev.Error,
		TaskID:    ev.TaskID,
		RawJSON:   append([]byte(nil), raw...),
	}
}

// daemonRunnerVersion is reported back to chepherd-runner clients in
// the register response. Kept as a const here (rather than threading
// the cmd/version.go string through) so the runners_register.go file
// stays self-contained for #504 Wave R1; later Waves can wire a
// build-time version via ldflags.
const daemonRunnerVersion = "0.9.4-R1"

// RegisteredRunner is the daemon's view of one chepherd-runner that
// has dialed in via /api/v1/runners/register. Populated by
// handleRunnerRegister; readable via the directory APIs that Wave D1
// (#467) lands.
//
// Field shape locked with chepherd-worker2 (Wave D1) 2026-05-31:
//   - Name is the operator-visible @-handle (e.g. "iogrid-1"); empty
//     at register-time is fine, daemon may echo back from spawn intent
//     in a future Wave.
//   - A2ABaseURL is scheme://host:port (NOT host:port) so D1's
//     §12.1 well-known URI builder can template
//     `<a2a_base_url>/a2a/<sid>/.well-known/agent-card.json` without
//     guessing scheme. Empty fine for R1 (D1 falls back to a stub
//     templated off the daemon's own request host).
//   - SID matches SessionInfo.ID semantics (daemon-assigned UUID).
//     Re-registration semantics (Wave R5 cutover) MUST be documented
//     so D1's Agent Card cache (Wave A) knows when to invalidate.
//     R1: every fresh /api/v1/runners/register WS dial mints a new
//     SID; restarted runners get new SIDs. Persistence + re-use is a
//     Wave R5 concern.
type RegisteredRunner struct {
	SID            string    `json:"sid"`
	Name           string    `json:"name"`
	AgentSlug      string    `json:"agent_slug"`
	RunnerVersion  string    `json:"runner_version"`
	A2ABaseURL     string    `json:"a2a_base_url"`
	MCPSocket      string    `json:"mcp_socket"`
	Capabilities   []string  `json:"capabilities"`
	RegisteredAt   time.Time `json:"registered_at"`
	LastSeen       time.Time `json:"last_seen"`
	AuditEventsRcv int64     `json:"audit_events_received"`
}

// runnerRegistry is the daemon-process-local map of live runner
// registrations. Wave R5 cutover may persist this to the agent
// registry in the persistence layer; for R1 in-memory is sufficient
// (re-registers re-create the row on daemon restart).
//
// Indexed by SID. Threadsafe via mu.
type runnerRegistry struct {
	mu      sync.Mutex
	rows    map[string]*RegisteredRunner
	auditCh map[string]chan auditEvent // per-SID fan-out for the (R2+) audit consumers
}

type auditEvent struct {
	SID  string    `json:"sid"`
	Kind string    `json:"kind"`
	Body string    `json:"body"`
	At   time.Time `json:"at"`
}

func newRunnerRegistry() *runnerRegistry {
	return &runnerRegistry{
		rows:    map[string]*RegisteredRunner{},
		auditCh: map[string]chan auditEvent{},
	}
}

// upsert adds or refreshes a row. Returns the canonical SID (either
// caller-provided or newly minted).
func (rr *runnerRegistry) upsert(req runnerRegisterReq) *RegisteredRunner {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	sid := req.SID
	if sid == "" {
		sid = "runner-" + uuid.NewString()
	}
	now := time.Now().UTC()
	row, ok := rr.rows[sid]
	if !ok {
		row = &RegisteredRunner{SID: sid, RegisteredAt: now}
		rr.rows[sid] = row
	}
	row.Name = req.Name
	row.AgentSlug = req.AgentSlug
	row.RunnerVersion = req.RunnerVersion
	row.A2ABaseURL = req.A2ABaseURL
	row.MCPSocket = req.MCPSocket
	row.Capabilities = req.Capabilities
	row.LastSeen = now
	return row
}

// recordAudit bumps the per-SID counter + non-blocking-fans-out on
// the audit channel (if any subscriber).
func (rr *runnerRegistry) recordAudit(sid string, ev auditEvent) {
	rr.mu.Lock()
	row := rr.rows[sid]
	if row != nil {
		row.AuditEventsRcv++
		row.LastSeen = time.Now().UTC()
	}
	ch := rr.auditCh[sid]
	rr.mu.Unlock()
	if ch != nil {
		select {
		case ch <- ev:
		default:
			// drop on slow consumer; this is R1 minimum, R2+ adds
			// a ring buffer if needed
		}
	}
}

// list returns a snapshot copy of all registered rows. Wave D1
// (#467) directory endpoint reads via this method.
func (rr *runnerRegistry) list() []*RegisteredRunner {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	out := make([]*RegisteredRunner, 0, len(rr.rows))
	for _, r := range rr.rows {
		cp := *r
		out = append(out, &cp)
	}
	return out
}

// runnerRegisterReq is the params shape of the chepherd/register
// JSON-RPC frame.
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
	SID            string `json:"sid"`
	DaemonVersion  string `json:"daemon_version"`
	AuditTopic     string `json:"audit_topic"`
}

type rpcFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcFrameError  `json:"error,omitempty"`
}

type rpcFrameError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// handleRunnerRegister upgrades the request to WebSocket + runs the
// per-runner read loop. First frame must be chepherd/register;
// subsequent frames are audit notifications.
func (s *Server) handleRunnerRegister(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// First frame — registration.
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return
	}
	var req rpcFrame
	if err := json.Unmarshal(raw, &req); err != nil || req.Method != "chepherd/register" {
		_ = conn.WriteJSON(rpcFrame{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcFrameError{Code: -32600, Message: "first frame must be chepherd/register"},
		})
		return
	}
	var params runnerRegisterReq
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = conn.WriteJSON(rpcFrame{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcFrameError{Code: -32602, Message: "bad register params: " + err.Error()},
		})
		return
	}
	row := s.runnerReg().upsert(params)
	fmt.Fprintf(os.Stderr, "[chepherd-daemon] runner registered: sid=%s agent_slug=%s version=%s\n",
		row.SID, row.AgentSlug, row.RunnerVersion)
	// #504 — emit the "registered" audit event SYNCHRONOUSLY here on
	// the daemon side, BEFORE writing the register response. Pre-fix
	// the runner emitted this via a fire-and-forget SendAudit call
	// after the response read, which raced SIGTERM in CI: the runner
	// process exited before the write goroutine flushed → daemon row
	// AuditEventsRcv stayed 0 → e2e test failed with row.audit_events
	// _received=0.
	//
	// Per V0.9.2-ARCH §5 #8 the audit log store lives inside
	// chepherd-daemon — "registered" is a daemon-side observation,
	// not a runner-uploaded event. Wave AU (later) wires runner-
	// uploaded audits for SendMessage call events where the runner
	// is the authoritative emitter; that direction stays runner-
	// driven. "registered" is daemon-driven.
	//
	// 4-layer RCA closure:
	//   TRIGGER: client-side fire-and-forget SendAudit raced SIGTERM
	//   INCIDENT-MGMT: 1006 abnormal close at daemon-side meant runner
	//                  didn't quiesce cleanly post-register
	//   DEFENSE: this synchronous-emit-on-daemon path removes the race
	//            structurally (no client-side audit-flush needed for
	//            "registered"); CI green proves it
	//   CONTAINMENT: this commit
	s.runnerReg().recordAudit(row.SID, auditEvent{
		SID:  row.SID,
		Kind: "event",
		Body: fmt.Sprintf("registered sid=%s agent_slug=%s version=%s", row.SID, row.AgentSlug, row.RunnerVersion),
		At:   row.RegisteredAt,
	})
	_ = conn.WriteJSON(rpcFrame{
		JSONRPC: "2.0", ID: req.ID,
		Result: runnerRegisterResp{
			SID:           row.SID,
			DaemonVersion: daemonRunnerVersion,
			AuditTopic:    "runner:" + row.SID,
		},
	})

	// Subsequent frames — audit notifications. Loop until conn
	// closes; recordAudit updates the per-SID counter on the
	// registry row + fans out to any audit subscriber.
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[chepherd-daemon] runner %s WS closed: %v\n", row.SID, err)
			return
		}
		var f rpcFrame
		if err := json.Unmarshal(raw, &f); err != nil {
			continue
		}
		switch f.Method {
		case "audit":
			// R1 legacy: freeform {kind, body, at}. Used for the
			// PTY-output stream + boot/shutdown diagnostic events.
			var p struct {
				Kind string    `json:"kind"`
				Body string    `json:"body"`
				At   time.Time `json:"at"`
			}
			if err := json.Unmarshal(f.Params, &p); err != nil {
				continue
			}
			if p.At.IsZero() {
				p.At = time.Now().UTC()
			}
			s.runnerReg().recordAudit(row.SID, auditEvent{
				SID: row.SID, Kind: p.Kind, Body: p.Body, At: p.At,
			})
		case "audit.event":
			// AU1 #488 + AU2 #489 — structured A2A call-boundary event.
			// AU2 swaps AU1's stub-log path for real persistence via
			// AuditEventStore (per-org partitioned at ingest). When
			// AuditEventStore is nil (dev / unit-test), the AU1 stub
			// log path stays active so the receiver doesn't black-hole
			// events.
			s.runnerReg().recordAudit(row.SID, auditEvent{
				SID:  row.SID,
				Kind: "a2a-event",
				Body: string(f.Params),
				At:   time.Now().UTC(),
			})
			if s.AuditEventStore != nil {
				rec := decodeAuditEvent(f.Params, s.DaemonOrgID)
				if rec != nil {
					// Fire-and-forget per AU1 requirement #4 — don't
					// block the WS read loop on persistence.
					go func(r *persistence.AuditEventRecord) {
						if err := s.AuditEventStore.Save(context.Background(), r); err != nil {
							fmt.Fprintf(os.Stderr, "[chepherd-daemon AU2] audit_events.Save %s: %v\n", r.ID, err)
						}
					}(rec)
				}
			} else {
				fmt.Fprintf(os.Stderr, "[chepherd-daemon AU1] runner %s audit.event: %s\n",
					row.SID, string(f.Params))
			}
		default:
			continue
		}
	}
}

// runnerReg lazily-initialises the per-Server registry. Threadsafe via
// the registry's own mu.
func (s *Server) runnerReg() *runnerRegistry {
	s.runnerRegMu.Lock()
	defer s.runnerRegMu.Unlock()
	if s.runnerRegistry == nil {
		s.runnerRegistry = newRunnerRegistry()
	}
	return s.runnerRegistry
}

// handleRunnersList — GET /api/v1/runners returns the snapshot of
// registered runners. Wave D1 (#467) builds the /api/v1/agents/
// directory atop this; for R1 alone the bare list is enough for the
// e2e test to assert the registration round-tripped.
func (s *Server) handleRunnersList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rows := s.runnerReg().list()
	writeJSON(w, http.StatusOK, map[string]any{"runners": rows})
}

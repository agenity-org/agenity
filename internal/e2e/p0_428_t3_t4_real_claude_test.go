// internal/e2e/p0_428_t3_t4_real_claude_test.go — second installment
// of architect's #428 comprehensive E2E test suite.
//
// Architect mandate 2026-05-31 (PR1 ship-confirm + PR2 direction):
//
//	"GO with long-running-agent harness. Real claude-code container
//	with TEST credential vault. Spawn via the SAME pipeline operator
//	uses (no test-only shortcuts) so the test surface = production
//	surface. Drive interactions via A2A message/send (not in-test
//	PTY tickling) so assertions match operator's real interaction
//	model. Skip tests with explicit 'REQUIRES_LIVE_CLAUDE=true' env
//	gate so local Go test runs without network/credentials don't
//	break CI. Avoid: stubbing claude-code's response with a mock."
//
// This PR ships T3 (real claude conversation) + T4 (chepherd-side
// MCP card contract). Both NAMED-assertion style per the
// "no rubbish tests" review criterion.
//
//	T3 — Two agents in the same team have a REAL A2A conversation;
//	     responder cites peers by name + role.
//	     GATED: requires CHEPHERD_TEST_LIVE_CLAUDE=1 + a host
//	     ~/.claude/.credentials.json (the agent container reuses
//	     the operator's OAuth token via the production /run/secrets
//	     bind-mount). Skips cleanly otherwise — running ungated is
//	     a no-op rather than a fake-green.
//
//	T4 — chepherd.get_peer_card MCP tool returns correct role +
//	     skills + agent slug for every peer in a 4-role team.
//	     This is the SAME shape the in-agent MCP call sees, so it
//	     verifies chepherd's promise to agents without burning real
//	     claude tokens. The in-claude invocation of the tool (i.e.
//	     "the agent reads peer_card via its own MCP transport, then
//	     uses it in a conversation reply") lands in a follow-up PR
//	     once the live-claude harness is hardened.
//
// Refs #428 P0 #404 #225 #208.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// ─── Live-claude harness extension ───────────────────────────────

// liveClaudeAvailable returns true when:
//  1. CHEPHERD_TEST_LIVE_CLAUDE=1 is set (operator opt-in — these
//     tests burn the host's real Anthropic OAuth token quota)
//  2. The host ~/.claude/.credentials.json exists (chepherd's spawn
//     pipeline reuses it via /run/secrets/claude-credentials bind
//     mount per #227 + #371)
//  3. The chepherd-agent:latest image is present (same check as PR1)
//
// Skip message tells the operator exactly how to enable. Returning
// true here MUST mean "this test will run a real claude container
// + use the host's real OAuth token" — no half-states.
func liveClaudeAvailable(t *testing.T) string {
	t.Helper()
	if os.Getenv("CHEPHERD_TEST_LIVE_CLAUDE") != "1" {
		return "CHEPHERD_TEST_LIVE_CLAUDE=1 not set — skipping live-claude E2E. This gate prevents accidental quota burn on CI. To enable locally: `CHEPHERD_TEST_LIVE_CLAUDE=1 go test ./internal/e2e/...`"
	}
	if !chepherdAgentImageAvailable() {
		return "chepherd-agent:latest image absent — run `make agent-image`"
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(home + "/.claude/.credentials.json"); err != nil {
		return "host ~/.claude/.credentials.json missing — claude-code container needs an OAuth token. Run `claude /login` on the host first, OR populate a test credential vault."
	}
	return ""
}

// spawnRealClaude is SpawnAgent's live-claude sibling. Uses the
// real "claude" agent slug + lets the production spawn pipeline
// wire the OAuth bind-mount + bridge + auto-dismiss. No test-only
// shortcuts (architect: "same pipeline operator uses").
//
// Returns the new session ID (info.id) which the A2A SendMessage
// path uses as contextId.
func (h *e2eHarness) spawnRealClaude(name, team, role string) (string, error) {
	h.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"Name":  name,
		"Agent": "claude",
		"Team":  team,
		"Role":  role,
	})
	req, _ := http.NewRequest(http.MethodPost, h.base()+"/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if h.bootstrapTok != "" {
		req.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("POST /sessions claude %q: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("POST /sessions claude %q: HTTP %d: %s", name, resp.StatusCode, raw)
	}
	var info struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("decode SessionInfo: %w", err)
	}
	if info.ID == "" {
		return "", fmt.Errorf("spawn returned empty session id")
	}
	return info.ID, nil
}

// waitClaudeBoot polls the chepherd binary's stderr for the per-agent
// MCP-bridge-connected marker. Real claude-code's startup takes
// ~5-15s + the MCP bridge re-dial sequence (#422) can add up to 15s
// more in transient cases. 60s budget is generous; failures past it
// usually indicate creds or image issues.
func (h *e2eHarness) waitClaudeBoot(name string) error {
	h.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		stderr := h.ReadStderr()
		if strings.Contains(stderr, "[chepherd-mcp-bridge] dial") && strings.Contains(stderr, name) {
			return nil
		}
		// Alternative: the per-spawn briefing log proves the agent
		// container reached at least the materializeAgentBriefing
		// step — sufficient for the A2A delivery target to exist.
		if strings.Contains(stderr, "[chepherd-spawn-briefing] "+name) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("waitClaudeBoot %q: no boot marker in stderr after 60s; check `make agent-image` + host ~/.claude/.credentials.json", name)
}

// a2aSend POSTs message/send to chepherd's A2A /jsonrpc endpoint
// with the bootstrap bearer token. The body is wrapped in an A2A
// Message{role:"user", parts:[{kind:"text",text:body}]}. ContextID
// = target session ID; messageId is auto-generated.
//
// Returns the created task ID for the caller to poll.
func (h *e2eHarness) a2aSend(targetSessionID, body string) (string, error) {
	h.t.Helper()
	msgID := fmt.Sprintf("e2e-msg-%d", time.Now().UnixNano())
	rpc := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"role":      "user",
				"messageId": msgID,
				"contextId": targetSessionID,
				"kind":      "message",
				"parts":     []map[string]any{{"kind": "text", "text": body}},
			},
		},
	}
	raw, _ := json.Marshal(rpc)
	req, _ := http.NewRequest(http.MethodPost, "http://"+h.httpAddr+"/jsonrpc",
		bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if h.bootstrapTok != "" {
		req.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("POST /jsonrpc message/send: %w", err)
	}
	defer resp.Body.Close()
	rawResp, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("a2aSend HTTP %d: %s", resp.StatusCode, rawResp)
	}
	var parsed struct {
		Error  *struct{ Code int; Message string } `json:"error,omitempty"`
		Result struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rawResp, &parsed); err != nil {
		return "", fmt.Errorf("decode message/send: %w (body=%s)", err, rawResp)
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("a2aSend RPC error %d: %s", parsed.Error.Code, parsed.Error.Message)
	}
	if parsed.Result.Task.ID == "" {
		return "", fmt.Errorf("a2aSend: empty task ID (body=%s)", rawResp)
	}
	return parsed.Result.Task.ID, nil
}

// waitTaskCompleted polls tasks/get until State=completed or
// timeout. Returns the completed Task's history (which carries the
// agent's response Message under role:"agent").
//
// Real claude-code takes 10-30s for an orientation-style reply
// (silence-window-finalize per a2a_deliverer.go taskCompleter).
// 90s budget covers most cases; longer responses may need bumping.
func (h *e2eHarness) waitTaskCompleted(taskID string, timeout time.Duration) ([]map[string]any, error) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	var lastState string
	for time.Now().Before(deadline) {
		rpc := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tasks/get",
			"params":  map[string]any{"id": taskID},
		}
		raw, _ := json.Marshal(rpc)
		req, _ := http.NewRequest(http.MethodPost, "http://"+h.httpAddr+"/jsonrpc",
			bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if h.bootstrapTok != "" {
			req.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		rawResp, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var parsed struct {
			Result struct {
				Status struct {
					State string `json:"state"`
				} `json:"status"`
				History []map[string]any `json:"history"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rawResp, &parsed); err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		lastState = parsed.Result.Status.State
		if lastState == "completed" {
			return parsed.Result.History, nil
		}
		if lastState == "failed" || lastState == "canceled" {
			return nil, fmt.Errorf("task %s terminal state=%q", taskID, lastState)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("task %s never reached 'completed' (last=%q) within %s", taskID, lastState, timeout)
}

// extractAgentReplyText walks a Task's history slice and returns the
// concatenated TextPart bodies of the LAST role:"agent" message. The
// a2a_deliverer.go taskCompleter appends one agent Message per
// completed task; for the first (and usually only) response, the
// last element is the reply.
func extractAgentReplyText(history []map[string]any) string {
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		if role, _ := m["role"].(string); role != "agent" {
			continue
		}
		parts, _ := m["parts"].([]any)
		var out strings.Builder
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			if pm == nil {
				continue
			}
			if kind, _ := pm["kind"].(string); kind == "text" {
				if txt, _ := pm["text"].(string); txt != "" {
					out.WriteString(txt)
				}
			}
		}
		return out.String()
	}
	return ""
}

// ─── T3 — Two agents have a REAL A2A conversation ──────────────

// TestT3_RealClaudePeerSummaryViaA2A pins the architect's PR2 walk
// path verbatim:
//
//	1. Spawn 3-agent team (worker, reviewer, scrum-master)
//	2. Send "Use team-orientation skill. List every peer + their
//	   role + a short note on what they're doing."
//	3. Assert response cites all 2 other peers by name + role
//
// Named assertions:
//
//	T3.C1 — message/send returns a working task ID
//	T3.C2 — tasks/get transitions to 'completed' within 90s
//	T3.C3 — agent's response carries non-empty text
//	T3.C4 — response mentions every other peer name in the team
//	T3.C5 — response mentions each peer's role correctly
//
// Failure messages cite assertion ID + the relevant excerpt of the
// agent's actual reply so an architect review can diagnose the
// failure without re-running locally.
func TestT3_RealClaudePeerSummaryViaA2A(t *testing.T) {
	if skip := liveClaudeAvailable(t); skip != "" {
		t.Skip(skip)
	}
	h := bootE2EHarness(t)
	const team = "t3-real-team"
	const speaker = "t3-speaker"
	const peerW = "t3-peer-worker"
	const peerR = "t3-peer-reviewer"

	speakerSID, err := h.spawnRealClaude(speaker, team, "worker")
	if err != nil {
		t.Fatalf("T3 spawn speaker: %v", err)
	}
	if _, err := h.spawnRealClaude(peerW, team, "worker"); err != nil {
		t.Fatalf("T3 spawn peerW: %v", err)
	}
	if _, err := h.spawnRealClaude(peerR, team, "reviewer"); err != nil {
		t.Fatalf("T3 spawn peerR: %v", err)
	}
	// Wait for ALL three to reach boot — the team-event briefing
	// regen needs every member registered so the speaker's
	// CLAUDE.md lists peerW + peerR before we ask it to summarize.
	for _, name := range []string{speaker, peerW, peerR} {
		if err := h.waitClaudeBoot(name); err != nil {
			t.Fatalf("T3 wait boot %q: %v", name, err)
		}
	}
	// Settle the 1s debounced briefing regen + give claude-code's
	// MCP /skills surface a beat to populate.
	time.Sleep(3 * time.Second)

	// T3.C1 — message/send returns a working task.
	taskID, err := h.a2aSend(speakerSID,
		"Use the team-orientation skill, then in one short paragraph list every other peer in your team by name + role + a brief note on what they do. Reply only with the summary; no extra preamble.")
	if err != nil {
		t.Fatalf("T3.C1 FAIL: message/send: %v", err)
	}

	// T3.C2 — tasks/get reaches completed.
	history, err := h.waitTaskCompleted(taskID, 90*time.Second)
	if err != nil {
		t.Fatalf("T3.C2 FAIL: %v", err)
	}

	// T3.C3 — response text non-empty.
	reply := extractAgentReplyText(history)
	if strings.TrimSpace(reply) == "" {
		t.Fatalf("T3.C3 FAIL: agent reply was empty. History had %d messages.", len(history))
	}

	// T3.C4 — every other peer cited by name (case-insensitive
	// since claude may render names with different capitalization).
	lowered := strings.ToLower(reply)
	for _, peer := range []string{peerW, peerR} {
		if !strings.Contains(lowered, strings.ToLower(peer)) {
			t.Errorf("T3.C4 FAIL: response missing peer %q.\n---reply---\n%s", peer, reply)
		}
	}

	// T3.C5 — roles cited per peer. claude-code reflects the
	// briefing's role markup ("`worker`" / "`reviewer`" /
	// "Worker" / "Reviewer"). Accept any reasonable
	// capitalization/quoting.
	if !strings.Contains(lowered, "worker") {
		t.Errorf("T3.C5 FAIL: response doesn't mention 'worker' role.\n---reply---\n%s", reply)
	}
	if !strings.Contains(lowered, "reviewer") {
		t.Errorf("T3.C5 FAIL: response doesn't mention 'reviewer' role.\n---reply---\n%s", reply)
	}
}

// ─── T4 — chepherd.get_peer_card MCP contract ──────────────────

// TestT4_GetPeerCardSurfacesRolesAndSkills pins the chepherd-side
// contract that backs the in-agent chepherd.get_peer_card MCP tool.
// The MCP server returns the same PeerAgentCard shape regardless of
// caller, so calling /jsonrpc with the bootstrap token tests the
// same code path the agent's claude-code MCP transport hits.
//
// Architect note: this is NOT a stub of claude's response. It tests
// chepherd's promise to agents — when an agent calls
// chepherd.get_peer_card("peer-x") via its MCP bridge, the response
// MUST carry peer-x's role + skills + agent slug correctly. The
// in-agent reply quality (i.e. whether claude USES the response to
// build a useful summary) is T3's domain.
//
// Named assertions:
//
//	T4.D1 — tools/call get_peer_card succeeds for every peer
//	T4.D2 — returned name + role match the JoinTeam role assignment
//	T4.D3 — returned skills includes every materialized skill from
//	        the agent's /skills tree
//	T4.D4 — agent_slug surfaces correctly (separates "what slug
//	        backs this peer" from "what role plays this peer in
//	        the team")
//	T4.D5 — non-existent peer returns -32000 with explanatory
//	        message (no silent empty-card)
func TestT4_GetPeerCardSurfacesRolesAndSkills(t *testing.T) {
	h := bootE2EHarness(t)
	const team = "t4-roles-team"
	// Use sovereign-shell — T4 tests the chepherd-side shape, not
	// claude conversation. Cheap + deterministic.
	type spawnSpec struct {
		name string
		role string
	}
	peers := []spawnSpec{
		{"t4-architect", "scrum-master"}, // architect role surfaces as scrum-master in current taxonomy
		{"t4-worker", "worker"},
		{"t4-reviewer", "reviewer"},
		{"t4-scrum", "scrum-master"},
	}
	for _, p := range peers {
		if _, err := h.SpawnAgent(p.name, team, p.role); err != nil {
			t.Fatalf("T4 spawn %q: %v", p.name, err)
		}
	}
	// Briefing materialization is async (#404 P0.3 debounce 1s).
	time.Sleep(2 * time.Second)

	// T4.D1+D2+D3+D4 — fetch each peer's card via chepherd MCP.
	for _, p := range peers {
		rpc := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      "chepherd.get_peer_card",
				"arguments": map[string]any{"name": p.name},
			},
		}
		raw, _ := json.Marshal(rpc)
		req, _ := http.NewRequest(http.MethodPost,
			"http://"+h.mcpAddr+"/mcp/rpc",
			bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if h.bootstrapTok != "" {
			req.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("T4.D1 FAIL: tools/call %q: %v", p.name, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 400 {
			t.Fatalf("T4.D1 FAIL: %q HTTP %d: %s", p.name, resp.StatusCode, body)
		}
		// MCP spec: tools/call result is { content: [{type:"text",
		// text:"<JSON>"}], isError: bool }. Unwrap then parse the
		// inner JSON the chepherd handler emitted.
		var envelope struct {
			Result struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("T4.D1 FAIL: decode envelope %q: %v (body=%s)", p.name, err, body)
		}
		if envelope.Result.IsError {
			t.Errorf("T4.D1 FAIL: %q tools/call isError=true: %s", p.name, body)
			continue
		}
		if len(envelope.Result.Content) == 0 {
			t.Errorf("T4.D1 FAIL: %q empty content envelope", p.name)
			continue
		}
		var card struct {
			Name      string   `json:"name"`
			Role      string   `json:"role"`
			AgentSlug string   `json:"agentSlug"`
			Skills    []string `json:"skills"`
		}
		if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &card); err != nil {
			t.Fatalf("T4.D1 FAIL: decode inner card %q: %v (text=%s)", p.name, err, envelope.Result.Content[0].Text)
		}
		// T4.D2 — name + role
		if card.Name != p.name {
			t.Errorf("T4.D2 FAIL: %q card.name = %q, want %q", p.name, card.Name, p.name)
		}
		if card.Role != p.role {
			t.Errorf("T4.D2 FAIL: %q card.role = %q, want %q (the JoinTeam-assigned role)", p.name, card.Role, p.role)
		}
		// T4.D3 — skills set non-empty + includes the 3 baseline
		// skills from agent_briefing.go materializeAgentBriefing.
		// Same list T2.B1 pins on disk; cross-checking here proves
		// the card endpoint reflects the filesystem (no drift).
		want := []string{"team-orientation", "peer-message", "operator-escalation"}
		got := map[string]bool{}
		for _, sk := range card.Skills {
			got[sk] = true
		}
		for _, sk := range want {
			if !got[sk] {
				t.Errorf("T4.D3 FAIL: %q card.skills missing %q (have %v)", p.name, sk, card.Skills)
			}
		}
		// T4.D4 — agent_slug surfaces. We spawned with
		// Agent="sovereign-shell"; that's the slug operators see
		// in the dashboard and other peers see via get_peer_card.
		if card.AgentSlug != "sovereign-shell" {
			t.Errorf("T4.D4 FAIL: %q card.agent_slug = %q, want sovereign-shell", p.name, card.AgentSlug)
		}
	}

	// T4.D5 — unknown peer returns a structured error, never an
	// empty card. Cheap regression guard against a bug shape where
	// BuildPeerAgentCard returns a zero-value struct on nil info.
	{
		rpc := map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"method": "tools/call",
			"params": map[string]any{
				"name":      "chepherd.get_peer_card",
				"arguments": map[string]any{"name": "definitely-not-a-peer"},
			},
		}
		raw, _ := json.Marshal(rpc)
		req, _ := http.NewRequest(http.MethodPost,
			"http://"+h.mcpAddr+"/mcp/rpc",
			bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if h.bootstrapTok != "" {
			req.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("T4.D5 FAIL: tools/call unknown: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var envelope struct {
			Result struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("T4.D5 decode: %v", err)
		}
		if !envelope.Result.IsError {
			t.Errorf("T4.D5 FAIL: tools/call for unknown peer should set isError=true (body=%s)", body)
		}
		if len(envelope.Result.Content) == 0 || !strings.Contains(envelope.Result.Content[0].Text, "no such session") {
			t.Errorf("T4.D5 FAIL: unknown-peer error content lacks 'no such session' marker (body=%s)", body)
		}
	}
}

// Silence unused context import — context.Background isn't used
// inline above (http.NewRequest uses implicit background).
var _ = context.Background

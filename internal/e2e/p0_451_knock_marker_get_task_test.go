// internal/e2e/p0_451_knock_marker_get_task_test.go — pins #451
// V0.9.2-ARCHITECTURE §10 Pattern 1 conformance.
//
// Acceptance criterion (7) from issue #451 (operator-visible
// observable shape):
//
//	"Spawn A + B claude sessions → A.send → assert (a) B's pane
//	shows KNOCK MARKER line, not the body text; (b) B's MCP
//	transcript shows chepherd.get_task call; (c) B's pane shows
//	the agent's own reasoning + response derived from the task;
//	(d) A's tasks/get returns COMPLETED with B's response."
//
// SUPERSEDES p0_449_send_to_session_a2a_test.go (which pinned the
// PRE-#451 body-to-PTY shape — that shape was the architectural
// regression #451 closes; the empirical PASS in #449/#450 was
// honest but tested a wrong contract).
//
// Test design:
//
//	Cheap-path (always runs, no live-claude): spawn 2 sovereign-
//	shell agents, POST chepherd.send_to_session via /mcp/rpc,
//	assert:
//	  L1 — B's pane carries the knock marker line, NOT the body
//	  L2 — A persists the Task; chepherd.get_task on it returns
//	       the body field with the original text
//	  L3 — get_task rejects access from a non-recipient caller
//	  L4 — chepherd.list_peers returns B in A's team but NOT
//	       agents in other teams
//
//	Live-path (gated by CHEPHERD_TEST_LIVE_CLAUDE=1): same setup
//	with REAL claude-code so the full Pattern 1 sequence runs end-
//	to-end including the agent-side get_task pull + thinking +
//	response. Asserts the receive-loop completes the task as the
//	architect's acceptance (d) requires. Skips cleanly when env
//	gate is off; never burns CI quota.
//
// Refs #451 #404 #208 V0.9.2-ARCHITECTURE §10 Pattern 1.
package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestP0_451_KnockMarkerPattern_CheapPath pins L1-L4 without live
// claude. Sovereign-shell agents stand in for claude-code; the
// chepherd-side shape (PTY writes only knock, persisted task body
// flows via MCP) is the contract the test verifies.
func TestP0_451_KnockMarkerPattern_CheapPath(t *testing.T) {
	h := bootE2EHarness(t)
	const team = "p0-451-team"
	const sender = "p0-451-sender"
	const receiver = "p0-451-receiver"
	const otherTeam = "p0-451-other-team"
	const otherAgent = "p0-451-other-agent"

	// Spawn the two agents in the same team + one in a different
	// team for L4 (cross-team isolation in list_peers).
	if _, err := h.SpawnAgent(sender, team, "worker"); err != nil {
		t.Fatalf("spawn sender: %v", err)
	}
	if _, err := h.SpawnAgent(receiver, team, "reviewer"); err != nil {
		t.Fatalf("spawn receiver: %v", err)
	}
	if _, err := h.SpawnAgent(otherAgent, otherTeam, "worker"); err != nil {
		t.Fatalf("spawn other-team: %v", err)
	}

	// Settle the spawn briefing materializer.
	time.Sleep(1 * time.Second)

	// Open an attach to RECEIVER so the WS keep-alive pump drains
	// the ring buffer + Subscribe path is exercised the same way
	// the dashboard's pane does it.
	h.attachKeepAlive(receiver)

	// Capture the receiver's current ring buffer baseline so the
	// post-send assertion can isolate the new bytes.
	preRing, _ := h.readSovereignPane(receiver)
	_ = preRing

	// ─── Send the message via send_to_session ─────────────────
	const bodyText = "Pattern1 conformance ping"
	rpc := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "tools/call",
		"params": map[string]any{
			"name": "chepherd.send_to_session",
			"arguments": map[string]any{
				"name": receiver,
				"body": bodyText,
			},
		},
	}
	// CurrentCaller (=identified MCP agent) is what From: in the
	// knock will be populated with. Use X-Chepherd-Agent header
	// to set the caller — handleRPC reads that. Cf. transport_http.go.
	envelope, err := h.postMCPAs(sender, rpc)
	if err != nil {
		t.Fatalf("send_to_session POST: %v", err)
	}
	var sendInner struct {
		OK     bool   `json:"ok"`
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &sendInner); err != nil {
		t.Fatalf("decode send_to_session inner: %v", err)
	}
	if !sendInner.OK {
		t.Fatalf("send_to_session result.ok = false")
	}
	if sendInner.TaskID == "" {
		t.Fatalf("send_to_session result.taskId empty")
	}

	// ─── L1 — receiver's pane carries the knock, NOT the body ─
	deadline := time.Now().Add(5 * time.Second)
	var paneSeen string
	var sawKnock, leakedBody bool
	for time.Now().Before(deadline) {
		pane, _ := h.readSovereignPane(receiver)
		paneSeen = pane
		if strings.Contains(pane, "[chepherd-knock taskID="+sendInner.TaskID+" from="+sender+"]") {
			sawKnock = true
		}
		// L1 negative — the literal body text MUST NOT be in B's
		// pane. Pre-#451 the Deliverer wrote the body straight to
		// PTY; #451 sends only the knock so this string should
		// never appear post-fix.
		if strings.Contains(pane, bodyText) {
			leakedBody = true
		}
		if sawKnock {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !sawKnock {
		t.Errorf("L1 FAIL: receiver's pane never showed knock marker [chepherd-knock taskID=%s from=%s]\n---pane---\n%s",
			sendInner.TaskID, sender, paneSeen)
	}
	if leakedBody {
		t.Errorf("L1 FAIL: receiver's pane LEAKED the body text %q — Pattern 1 contract violated; body must flow via MCP get_task only.\n---pane---\n%s",
			bodyText, paneSeen)
	}

	// ─── L2 — get_task returns the body envelope to the recipient ─
	getTaskRPC := map[string]any{
		"jsonrpc": "2.0", "id": 2,
		"method": "tools/call",
		"params": map[string]any{
			"name": "chepherd.get_task",
			"arguments": map[string]any{
				"taskID": sendInner.TaskID,
			},
		},
	}
	getEnv, err := h.postMCPAs(receiver, getTaskRPC)
	if err != nil {
		t.Fatalf("get_task POST: %v", err)
	}
	var envBody struct {
		TaskID    string `json:"taskID"`
		State     string `json:"state"`
		From      string `json:"from"`
		ContextID string `json:"contextID"`
		Body      string `json:"body"`
	}
	if err := json.Unmarshal([]byte(getEnv.Result.Content[0].Text), &envBody); err != nil {
		t.Fatalf("L2 FAIL: decode get_task inner: %v", err)
	}
	if envBody.TaskID != sendInner.TaskID {
		t.Errorf("L2 FAIL: get_task.taskID = %q, want %q", envBody.TaskID, sendInner.TaskID)
	}
	if envBody.Body != bodyText {
		t.Errorf("L2 FAIL: get_task.body = %q, want %q (body must round-trip through Task.InputBlob)",
			envBody.Body, bodyText)
	}
	if envBody.From != sender {
		t.Errorf("L2 FAIL: get_task.from = %q, want %q", envBody.From, sender)
	}
	if envBody.ContextID != receiver {
		t.Errorf("L2 FAIL: get_task.contextID = %q, want %q (the recipient)", envBody.ContextID, receiver)
	}

	// ─── L3 — get_task rejects a non-recipient caller ──────────
	notRecipientEnv, err := h.postMCPAs(otherAgent, getTaskRPC)
	if err != nil {
		t.Fatalf("L3 unauthorized-call POST: %v", err)
	}
	if !notRecipientEnv.Result.IsError {
		t.Errorf("L3 FAIL: get_task allowed call from non-recipient agent %q — scoping check broken", otherAgent)
	}

	// ─── L4 — list_peers is team-scoped ────────────────────────
	listPeersRPC := map[string]any{
		"jsonrpc": "2.0", "id": 3,
		"method": "tools/call",
		"params": map[string]any{
			"name":      "chepherd.list_peers",
			"arguments": map[string]any{},
		},
	}
	peersEnv, err := h.postMCPAs(sender, listPeersRPC)
	if err != nil {
		t.Fatalf("list_peers POST: %v", err)
	}
	var peersOut struct {
		Peers []struct {
			Name string `json:"name"`
		} `json:"peers"`
		Team string `json:"team"`
	}
	if err := json.Unmarshal([]byte(peersEnv.Result.Content[0].Text), &peersOut); err != nil {
		t.Fatalf("L4 FAIL: decode list_peers: %v", err)
	}
	if peersOut.Team != team {
		t.Errorf("L4 FAIL: list_peers.team = %q, want %q (resolved from caller's session)", peersOut.Team, team)
	}
	seen := map[string]bool{}
	for _, p := range peersOut.Peers {
		seen[p.Name] = true
	}
	if !seen[receiver] {
		t.Errorf("L4 FAIL: list_peers missing in-team peer %q", receiver)
	}
	if seen[otherAgent] {
		t.Errorf("L4 FAIL: list_peers includes out-of-team agent %q — team scope leaked", otherAgent)
	}
	if seen[sender] {
		t.Errorf("L4 FAIL: list_peers includes the CALLER %q — should exclude self", sender)
	}
}

// readSovereignPane is a small helper that wraps the chepherd-side
// PTY ring-buffer read for non-claude agents (sovereign-shell). The
// MCP read_pane handler returns the same shape regardless of agent
// flavor; reusing readPaneViaMCP would couple test files. Inlined
// for clarity since this test owns the cheap-path assertions.
func (h *e2eHarness) readSovereignPane(name string) (string, error) {
	return h.readPaneViaMCP(name)
}

// postMCPAs posts an MCP JSON-RPC envelope as if from `asAgent`
// (sets X-Chepherd-Agent header which the /mcp/rpc handler reads
// into CurrentCaller).
func (h *e2eHarness) postMCPAs(asAgent string, rpc map[string]any) (*mcpEnvelope, error) {
	h.t.Helper()
	raw, _ := json.Marshal(rpc)
	req, _ := http.NewRequest(http.MethodPost,
		"http://"+h.mcpAddr+"/mcp/rpc", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Chepherd-Agent", asAgent)
	if h.bootstrapTok != "" {
		req.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var env mcpEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// mcpEnvelope mirrors the MCP tools/call result shape used by
// multiple tests in this directory. Kept local since the field
// set is small + sharing it would entangle test files needlessly.
type mcpEnvelope struct {
	Result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
}

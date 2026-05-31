// internal/e2e/p0_474_list_peers_mcp_test.go — e2e walk pinning the
// #474 Wave K3 chepherd.list_peers MCP tool against the real harness
// (real chepherd binary + real /mcp/rpc surface).
//
// Acceptance per #474:
//
//	"Spawn 2 agents in a team, agent-A calls list_peers, asserts
//	agent-B's card returned."
//
// Cheap path (sovereign-shell, runs in CI): all chepherd-side
// mechanics — Server.CurrentCaller resolution from X-Chepherd-Agent
// header, rt registry filter, JSON envelope shape — are exercised
// without needing live claude. Real claude isn't needed because the
// MCP tool is a chepherd-side projection; agent-side invocation is
// a separate concern (lives in #474's sibling Wave K4 briefing).
//
// Named assertions:
//
//	N1 — tools/call chepherd.list_peers returns isError=false
//	N2 — response carries {peers, team} fields
//	N3 — peers[*] shape is {sid, name, agent_card_url} (D1 §12.2)
//	N4 — out-of-team peer NOT in result
//	N5 — caller itself NOT in result
//	N6 — agent_card_url ends with /a2a/<sid>/.well-known/agent-card.json
//	N7 — empty-team caller → empty peers + empty team field
//
// Refs #474 #467 V0.9.2-ARCHITECTURE §10 Pattern 1 step 1.
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

func TestP0_474_ListPeersMCP_TeamScope(t *testing.T) {
	h := bootE2EHarness(t)
	const team = "p0-474-team"
	const caller = "p0-474-caller"
	const inTeamPeer = "p0-474-peer"
	const otherTeam = "p0-474-other-team"
	const outOfTeamPeer = "p0-474-out"

	if _, err := h.SpawnAgent(caller, team, "worker"); err != nil {
		t.Fatalf("spawn caller: %v", err)
	}
	if _, err := h.SpawnAgent(inTeamPeer, team, "reviewer"); err != nil {
		t.Fatalf("spawn in-team peer: %v", err)
	}
	if _, err := h.SpawnAgent(outOfTeamPeer, otherTeam, "worker"); err != nil {
		t.Fatalf("spawn out-of-team peer: %v", err)
	}
	// Settle so the runtime registry has all three.
	time.Sleep(1 * time.Second)

	// Build + POST the MCP envelope. X-Chepherd-Agent header drives
	// CurrentCaller resolution on the daemon side.
	rpc := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "tools/call",
		"params": map[string]any{
			"name":      "chepherd.list_peers",
			"arguments": map[string]any{},
		},
	}
	raw, _ := json.Marshal(rpc)
	req, _ := http.NewRequest(http.MethodPost,
		"http://"+h.mcpAddr+"/mcp/rpc", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Chepherd-Agent", caller)
	if h.bootstrapTok != "" {
		req.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp/rpc: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// MCP envelope.
	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, body)
	}
	// N1 — isError=false
	if envelope.Result.IsError {
		t.Fatalf("N1 FAIL: tools/call isError=true: %s", body)
	}
	if len(envelope.Result.Content) == 0 {
		t.Fatalf("N1 FAIL: empty content envelope (body=%s)", body)
	}

	// N2 + N3 — inner shape.
	var inner struct {
		Peers []struct {
			SID          string `json:"sid"`
			Name         string `json:"name"`
			AgentCardURL string `json:"agent_card_url"`
		} `json:"peers"`
		Team string `json:"team"`
	}
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &inner); err != nil {
		t.Fatalf("N2 FAIL: decode inner: %v (text=%s)", err, envelope.Result.Content[0].Text)
	}
	if inner.Team != team {
		t.Errorf("N2 FAIL: team field = %q, want %q (resolved from caller's session)", inner.Team, team)
	}

	// N4 — out-of-team peer NOT in result
	seen := map[string]string{}
	for _, p := range inner.Peers {
		seen[p.Name] = p.SID
		if p.SID == "" {
			t.Errorf("N3 FAIL: peer %q has empty sid", p.Name)
		}
		if p.AgentCardURL == "" {
			t.Errorf("N3 FAIL: peer %q has empty agent_card_url", p.Name)
		}
	}
	if _, leaked := seen[outOfTeamPeer]; leaked {
		t.Errorf("N4 FAIL: out-of-team peer %q in result (team filter broken)", outOfTeamPeer)
	}
	// N5 — caller NOT in result
	if _, self := seen[caller]; self {
		t.Errorf("N5 FAIL: caller %q in own list_peers result", caller)
	}
	// in-team peer present
	gotSID, ok := seen[inTeamPeer]
	if !ok {
		t.Fatalf("N3 FAIL: in-team peer %q missing from result. Seen: %+v", inTeamPeer, seen)
	}

	// N6 — agent_card_url shape
	var inTeamCardURL string
	for _, p := range inner.Peers {
		if p.Name == inTeamPeer {
			inTeamCardURL = p.AgentCardURL
			break
		}
	}
	wantSuffix := "/a2a/" + gotSID + "/.well-known/agent-card.json"
	if !strings.HasSuffix(inTeamCardURL, wantSuffix) {
		t.Errorf("N6 FAIL: in-team peer agent_card_url = %q, want suffix %q", inTeamCardURL, wantSuffix)
	}
}

// TestP0_474_ListPeersMCP_NoPeersInTeam pins N7 — a caller in a
// team with no other members gets an empty peers list (caller is
// excluded from own result). The team field still reflects the
// caller's team — list_peers is team-scoped, not session-scoped.
//
// NOTE: SpawnAgent's empty-team value gets coerced to "default" by
// the spawn handler's JoinTeam call; the test asserts the
// real-world result rather than the un-reachable empty-team path.
func TestP0_474_ListPeersMCP_NoPeersInTeam(t *testing.T) {
	h := bootE2EHarness(t)
	const caller = "p0-474-lonely"
	const teamWithOnlyCaller = "p0-474-solo-team"
	if _, err := h.SpawnAgent(caller, teamWithOnlyCaller, "worker"); err != nil {
		t.Fatalf("spawn caller: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	rpc := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "tools/call",
		"params": map[string]any{
			"name":      "chepherd.list_peers",
			"arguments": map[string]any{},
		},
	}
	raw, _ := json.Marshal(rpc)
	req, _ := http.NewRequest(http.MethodPost,
		"http://"+h.mcpAddr+"/mcp/rpc", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Chepherd-Agent", caller)
	if h.bootstrapTok != "" {
		req.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp/rpc: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var inner struct {
		Peers []any  `json:"peers"`
		Team  string `json:"team"`
	}
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &inner); err != nil {
		t.Fatalf("decode inner: %v", err)
	}
	if len(inner.Peers) != 0 {
		t.Errorf("N7 FAIL: solo-in-team caller returned %d peers, want 0 (caller is excluded from own list_peers)", len(inner.Peers))
	}
	if inner.Team != teamWithOnlyCaller {
		t.Errorf("N7 FAIL: caller's team field = %q, want %q (list_peers carries caller's team in the response)", inner.Team, teamWithOnlyCaller)
	}
}

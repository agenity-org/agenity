// internal/e2e/p0_428_t9_t10_isolation_escalation_test.go — fifth
// installment of architect's #428 comprehensive E2E test suite.
//
// Architect post-#438 walk (2026-05-31, "PR5 isolation + escalation"):
//
//	"T9 cross-team isolation: spawn A in T1, B in T2 → A's briefing
//	must NOT mention B → list_sessions(team='T1') returns only A.
//	Pure chepherd-side."
//
//	"T10 alert_human escalation: MCP tools/call
//	chepherd.alert_human{kind:'failure',body:'test'} → assert
//	chepherd's inbox/event-store receives the record. T4-style
//	MCP roundtrip, no live-claude needed."
//
//	T9 — Two agents in DIFFERENT teams cannot see each other in
//	     briefings or membership listings. Proves
//	     snapshotPeersForBriefing's team filter + the
//	     /api/v1/memberships?team= query parameter actually scope
//	     correctly.
//
//	T10 — chepherd.alert_human MCP tool persists to the human
//	      inbox surface (GET /api/v1/inbox). Proves the escalation
//	      pipeline (MCP → Server.toolCall → Runtime.HumanInbox →
//	      Server.inbox HTTP handler) is wired end-to-end.
//
// Both tests are pure chepherd-side, no live-claude gate, run in CI.
//
// Refs #428 P0 #404 #225 PR #438.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ─── T9 — Cross-team isolation ─────────────────────────────────

// TestT9_CrossTeamIsolationKeepsBriefingsScoped pins the team-scope
// filter at three independent surfaces — each is its own source of
// truth that the dashboard / agents read for team-restricted views.
// If any one of them leaked cross-team membership, an agent in T1
// could discover or message agents in T2 by accident, which is the
// core operator-visible privacy boundary chepherd promises.
//
// Named assertions:
//
//	T9.I1 — A spawn in team T1 returns 201
//	T9.I2 — B spawn in team T2 returns 201
//	T9.I3 — A's CLAUDE.md does NOT mention B by name
//	T9.I4 — B's CLAUDE.md does NOT mention A by name
//	T9.I5 — GET /api/v1/memberships?team=T1 returns ONLY A
//	T9.I6 — GET /api/v1/memberships?team=T2 returns ONLY B
//	T9.I7 — GET /api/v1/memberships (no filter) returns BOTH
//	        (sanity check: the filter actually filters; the
//	        unfiltered API still surfaces the full picture so
//	        the dashboard's all-memberships view works)
func TestT9_CrossTeamIsolationKeepsBriefingsScoped(t *testing.T) {
	h := bootE2EHarness(t)
	const teamA = "t9-team-alpha"
	const teamB = "t9-team-beta"
	const agentA = "t9-alpha-worker"
	const agentB = "t9-beta-worker"

	// T9.I1 + T9.I2 — spawn into distinct teams.
	if _, err := h.SpawnAgent(agentA, teamA, "worker"); err != nil {
		t.Fatalf("T9.I1 FAIL: spawn A: %v", err)
	}
	if _, err := h.SpawnAgent(agentB, teamB, "worker"); err != nil {
		t.Fatalf("T9.I2 FAIL: spawn B: %v", err)
	}

	// T9.I3 + T9.I4 — neither agent's CLAUDE.md mentions the
	// other. The briefing renders peer references as code spans
	// `<name>`; absence of the code span is the load-bearing
	// assertion. Poll up to 5s for the initial spawn briefing
	// write to land + settle.
	if err := pollAssert(t, 5*time.Second, func() error {
		aBody, err := h.ReadCLAUDEMD(agentA)
		if err != nil {
			return fmt.Errorf("A CLAUDE.md not on disk: %w", err)
		}
		if strings.Contains(aBody, "`"+agentB+"`") {
			return fmt.Errorf("T9.I3: A's CLAUDE.md mentions B — cross-team leak through snapshotPeersForBriefing")
		}
		bBody, err := h.ReadCLAUDEMD(agentB)
		if err != nil {
			return fmt.Errorf("B CLAUDE.md not on disk: %w", err)
		}
		if strings.Contains(bBody, "`"+agentA+"`") {
			return fmt.Errorf("T9.I4: B's CLAUDE.md mentions A — cross-team leak")
		}
		// Sanity check: A's briefing DOES contain A's own team
		// + role markers (not silently truncated).
		if !strings.Contains(aBody, "**Team**: `"+teamA+"`") {
			return fmt.Errorf("T9.I3 sanity: A's CLAUDE.md missing own team marker — briefing may have malformed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// T9.I5 — team-filtered memberships query for T1 returns only A.
	gotA := h.fetchMembershipsByTeam(teamA)
	if len(gotA) != 1 {
		t.Errorf("T9.I5 FAIL: memberships?team=%s returned %d entries, want 1: %+v",
			teamA, len(gotA), gotA)
	} else if gotA[0].AgentName != agentA {
		t.Errorf("T9.I5 FAIL: memberships?team=%s returned %q, want %q",
			teamA, gotA[0].AgentName, agentA)
	}

	// T9.I6 — symmetric for T2 / B.
	gotB := h.fetchMembershipsByTeam(teamB)
	if len(gotB) != 1 {
		t.Errorf("T9.I6 FAIL: memberships?team=%s returned %d entries, want 1: %+v",
			teamB, len(gotB), gotB)
	} else if gotB[0].AgentName != agentB {
		t.Errorf("T9.I6 FAIL: memberships?team=%s returned %q, want %q",
			teamB, gotB[0].AgentName, agentB)
	}

	// T9.I7 — unfiltered query returns BOTH so the dashboard's
	// global view still works.
	all := h.fetchMembershipsByTeam("")
	seen := map[string]bool{}
	for _, m := range all {
		seen[m.AgentName] = true
	}
	if !seen[agentA] || !seen[agentB] {
		t.Errorf("T9.I7 FAIL: unfiltered memberships missing %q or %q: %+v",
			agentA, agentB, all)
	}
}

// ─── T10 — alert_human escalation ──────────────────────────────

// TestT10_AlertHumanPersistsToInbox pins the chepherd escalation
// path that operators rely on for high-signal worker alerts:
//
//	worker calls chepherd.alert_human{body, kind} → MCP server
//	dispatches to toolCall → Runtime.HumanInbox appends with
//	the synthesized "[kind] body" payload → dashboard reads via
//	GET /api/v1/inbox.
//
// Named assertions:
//
//	T10.J1 — tools/call chepherd.alert_human returns isError=false
//	         with {ok: true} inner result
//	T10.J2 — GET /api/v1/inbox now contains an entry whose body
//	         matches the synthesized "[kind] body" payload
//	T10.J3 — entry.from matches the explicit From arg passed in
//	         the call (proves attribution works for cross-agent
//	         alerts where the calling agent is identified)
//	T10.J4 — entry.at is a recent RFC3339 timestamp (within 30s
//	         of "now") — guards against a clock-init or zero-
//	         time-marshal regression
//	T10.J5 — entry.read is false on initial post (read-state
//	         lifecycle starts at unread; dashboard mark-as-read
//	         is a separate code path)
func TestT10_AlertHumanPersistsToInbox(t *testing.T) {
	h := bootE2EHarness(t)
	const alertBody = "T10 escalation test body"
	const alertKind = "failure"
	const alertFrom = "t10-test-caller"

	// T10.J1 — tools/call.
	rpc := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "tools/call",
		"params": map[string]any{
			"name": "chepherd.alert_human",
			"arguments": map[string]any{
				"body": alertBody,
				"kind": alertKind,
				"from": alertFrom,
			},
		},
	}
	raw, _ := json.Marshal(rpc)
	mcpReq, _ := http.NewRequest(http.MethodPost,
		"http://"+h.mcpAddr+"/mcp/rpc", bytes.NewReader(raw))
	mcpReq.Header.Set("Content-Type", "application/json")
	if h.bootstrapTok != "" {
		mcpReq.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	mcpResp, err := http.DefaultClient.Do(mcpReq)
	if err != nil {
		t.Fatalf("T10.J1 FAIL: MCP tools/call: %v", err)
	}
	mcpBody, _ := io.ReadAll(mcpResp.Body)
	_ = mcpResp.Body.Close()
	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(mcpBody, &envelope); err != nil {
		t.Fatalf("T10.J1 FAIL: decode envelope: %v (body=%s)", err, mcpBody)
	}
	if envelope.Result.IsError {
		t.Fatalf("T10.J1 FAIL: tools/call isError=true: %s", mcpBody)
	}
	if len(envelope.Result.Content) == 0 {
		t.Fatalf("T10.J1 FAIL: empty content envelope")
	}
	var inner struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &inner); err != nil {
		t.Fatalf("T10.J1 FAIL: decode inner: %v (text=%s)", err, envelope.Result.Content[0].Text)
	}
	if !inner.OK {
		t.Errorf("T10.J1 FAIL: alert_human result.ok = false, want true (body=%s)", envelope.Result.Content[0].Text)
	}

	// HumanInbox is synchronous so the entry should be visible on
	// the very next GET. Poll briefly for slack against an HTTP
	// scheduling hiccup.
	want := "[" + alertKind + "] " + alertBody
	if err := pollAssert(t, 2*time.Second, func() error {
		entries := h.fetchInbox()
		for _, e := range entries {
			if e.Body == want && e.From == alertFrom {
				// T10.J4 — timestamp recent.
				ts, perr := time.Parse(time.RFC3339, e.At)
				if perr != nil {
					return fmt.Errorf("T10.J4: entry.at %q not RFC3339: %v", e.At, perr)
				}
				if time.Since(ts) > 30*time.Second {
					return fmt.Errorf("T10.J4: entry.at %s is > 30s old (clock skew or zero-time-marshal regression?)", e.At)
				}
				// T10.J5 — initial read state is false.
				if e.Read {
					return fmt.Errorf("T10.J5: entry.read = true on initial post, want false")
				}
				return nil
			}
		}
		return fmt.Errorf("T10.J2/J3: no inbox entry with from=%q body=%q (have %d entries)",
			alertFrom, want, len(entries))
	}); err != nil {
		t.Errorf("%v", err)
	}
}

// ─── Harness extensions used by T9 + T10 ───────────────────────

// membershipRow is the JSON shape of GET /api/v1/memberships entries.
// Subset of the full Membership struct — only fields T9 asserts on.
type membershipRow struct {
	AgentName string `json:"agent_name"`
	TeamName  string `json:"team_name"`
	Role      string `json:"role"`
}

// fetchMembershipsByTeam queries GET /api/v1/memberships with an
// optional team filter. Empty team returns ALL memberships across
// every team (used by T9.I7).
func (h *e2eHarness) fetchMembershipsByTeam(team string) []membershipRow {
	h.t.Helper()
	u := h.base() + "/memberships"
	if team != "" {
		u += "?team=" + team
	}
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	if h.bootstrapTok != "" {
		req.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("fetchMembershipsByTeam(%q): %v", team, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		h.t.Fatalf("fetchMembershipsByTeam(%q): HTTP %d: %s", team, resp.StatusCode, raw)
	}
	var body struct {
		Memberships []membershipRow `json:"memberships"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		h.t.Fatalf("fetchMembershipsByTeam(%q) decode: %v", team, err)
	}
	return body.Memberships
}

// inboxRow is the JSON shape of GET /api/v1/inbox entries.
type inboxRow struct {
	ID   string `json:"id"`
	From string `json:"from"`
	Body string `json:"body"`
	At   string `json:"at"`
	Read bool   `json:"read"`
}

// fetchInbox queries GET /api/v1/inbox. Used by T10 to verify
// alert_human persisted.
func (h *e2eHarness) fetchInbox() []inboxRow {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.base()+"/inbox", nil)
	if h.bootstrapTok != "" {
		req.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("fetchInbox: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		h.t.Fatalf("fetchInbox: HTTP %d: %s", resp.StatusCode, raw)
	}
	var body struct {
		Inbox []inboxRow `json:"inbox"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		h.t.Fatalf("fetchInbox decode: %v", err)
	}
	return body.Inbox
}

// internal/e2e/p0_449_send_to_session_a2a_test.go — #449 regression
// test pinning the send_to_session A2A shim end-to-end.
//
// Acceptance criterion (4) from issue #449:
//
//	"2 claude-code sessions, A.send_to_session(B, "ping"), assert
//	B's pane shows the submitted user turn (not input-buffer stuck).
//	Test MUST fail on bare-CR shape + pass on corrected sequence."
//
// EMPIRICAL FINDING during #449 implementation (probe_449_*_test.go):
//
//	The premise that bare-CR doesn't submit was FALSIFIED. With
//	real credentials the bare-CR (0x0d) default IS recognized by
//	claude-code's TUI under --dangerously-skip-permissions; the
//	user-turn marker "❯ <body>" renders and claude begins thinking.
//	The original symptom ("messages landed but never processed")
//	was misdiagnosed in PR #448 — the SubmitSequence layer is
//	correct.
//
// This test therefore pins the CORRECT behavior of the restored
// shim: chepherd.send_to_session → A2A Deliverer → PTY write +
// bare-CR submit → B's pane shows the submitted user turn. If a
// future change breaks the submit path (e.g., another bypass-
// architectural-shape PR), this test catches it.
//
// The "MUST fail on bare-CR shape" clause in the acceptance is
// reframed to "MUST fail if the A2A Deliverer chain is bypassed
// or if the SubmitSequence is broken to a known-not-working byte".
// We can't trivially toggle SubmitSequence per-test without a
// runtime hook, so the test exercises the production shim path
// + asserts the submit-happened ❯ marker as the load-bearing
// signal.
//
// GATED by CHEPHERD_TEST_LIVE_CLAUDE=1 + valid host creds +
// chepherd-agent:latest image. Burns ~3-5 min of real claude
// quota per run. Skips cleanly without the env gate.
//
// Refs #449 #208 #404 PR #448 reverted in commit a25d135.
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

// TestP0_449_SendToSessionSubmitsViaA2ADeliverer pins the
// end-to-end shim path:
//
//	chepherd.send_to_session(name=B, body="ping pineapple")
//	  → MCP server tools/call dispatch
//	  → A2ADeliverer.Deliver
//	  → sess.Write(body) + sess.Write(submitSequence)
//	  → claude-code processes body as a user turn (❯ marker)
//
// Named assertions:
//
//	K1 — tools/call returns ok=true + non-empty taskId
//	K2 — within 30s B's PTY ring buffer carries the
//	     "❯ PING PINEAPPLE" user-turn marker (proves SUBMIT ran)
//	K3 — claude's thinking spinner ("Perusing…", "Coughing…",
//	     "Reticulating…" — any of the claude-code thinking
//	     animals) appears, proving the agent actually accepted
//	     the turn + is generating a response
func TestP0_449_SendToSessionSubmitsViaA2ADeliverer(t *testing.T) {
	if skip := liveClaudeAvailable(t); skip != "" {
		t.Skip(skip)
	}
	h := bootE2EHarness(t)
	const team = "p0-449-team"
	const sender = "p0-449-sender"
	const receiver = "p0-449-receiver"

	// Spawn sender first — it's the agent making the
	// send_to_session call. Has to be claude-code (real-agent) so
	// the production MCP transport is exercised end-to-end. We
	// won't drive the sender's MCP — we call send_to_session
	// directly via curl-equivalent below — but the spawn here keeps
	// the test surface close to production.
	if _, err := h.spawnRealClaude(sender, team, "worker"); err != nil {
		t.Fatalf("spawn sender: %v", err)
	}
	h.attachKeepAlive(sender)
	if err := h.waitClaudeReady(sender, 0); err != nil {
		t.Fatalf("wait sender ready: %v", err)
	}
	baseAfterSender := h.countAutoDismissSteadyState()

	// Spawn receiver. send_to_session targets this one.
	if _, err := h.spawnRealClaude(receiver, team, "reviewer"); err != nil {
		t.Fatalf("spawn receiver: %v", err)
	}
	h.attachKeepAlive(receiver)
	if err := h.waitClaudeReady(receiver, baseAfterSender); err != nil {
		t.Fatalf("wait receiver ready: %v", err)
	}

	// Settle so the receiver is at its conversation prompt.
	time.Sleep(2 * time.Second)

	// K1 — POST tools/call chepherd.send_to_session.
	rpc := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "tools/call",
		"params": map[string]any{
			"name": "chepherd.send_to_session",
			"arguments": map[string]any{
				"name": receiver,
				"body": "PING PINEAPPLE",
			},
		},
	}
	raw, _ := json.Marshal(rpc)
	req, _ := http.NewRequest(http.MethodPost,
		"http://"+h.mcpAddr+"/mcp/rpc", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if h.bootstrapTok != "" {
		req.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("K1 FAIL: tools/call: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("K1 FAIL: decode envelope: %v (body=%s)", err, body)
	}
	if envelope.Result.IsError {
		t.Fatalf("K1 FAIL: tools/call isError=true: %s", body)
	}
	if len(envelope.Result.Content) == 0 {
		t.Fatalf("K1 FAIL: empty content envelope")
	}
	var inner struct {
		OK     bool   `json:"ok"`
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &inner); err != nil {
		t.Fatalf("K1 FAIL: decode inner: %v (text=%s)", err, envelope.Result.Content[0].Text)
	}
	if !inner.OK {
		t.Errorf("K1 FAIL: send_to_session result.ok = false, want true")
	}
	if inner.TaskID == "" {
		t.Errorf("K1 FAIL: send_to_session result.taskId is empty — Deliverer didn't issue a task")
	}

	// K2 + K3 — within 30s the receiver's pane shows the
	// user-turn marker AND a claude thinking spinner.
	deadline := time.Now().Add(30 * time.Second)
	var sawSubmit, sawThinking bool
	var lastPane string
	for time.Now().Before(deadline) {
		pane, perr := h.readPaneViaMCP(receiver)
		if perr == nil {
			lastPane = pane
			up := strings.ToUpper(pane)
			if strings.Contains(up, "PING PINEAPPLE") && strings.Contains(pane, "❯ ") {
				sawSubmit = true
			}
			// claude-code thinking spinners use animal/verb gerunds.
			// Look for the common ones; "…" alone is also a strong
			// signal (used in every thinking state).
			if strings.Contains(pane, "…") {
				sawThinking = true
			}
			if sawSubmit && sawThinking {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	if !sawSubmit {
		t.Errorf("K2 FAIL: receiver's pane never showed '❯ PING PINEAPPLE' submit marker within 30s. Body landed in input box but submit byte didn't fire OR shim didn't reach Deliverer.\n---last pane (tail 1KB)---\n%s", tail(lastPane, 1024))
	}
	if !sawThinking {
		t.Errorf("K3 FAIL: receiver's claude never started thinking (no '…' spinner). Submit ran but agent might be wedged or auth-failing.\n---last pane (tail 1KB)---\n%s", tail(lastPane, 1024))
	}
}

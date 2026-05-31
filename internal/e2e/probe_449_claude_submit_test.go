// internal/e2e/probe_449_claude_submit_test.go — empirical probe
// for #449 claude-code submit-sequence determination.
//
// Operator-surfaced 2026-05-31: chepherd.send_to_session messages
// landed in claude-code's prompt buffer but were NEVER submitted as
// user turns. Architect issue #449 directs: empirically determine
// the correct claude-code SubmitSequence under
// --dangerously-skip-permissions.
//
// This file is GATED by CHEPHERD_TEST_449_PROBE=1. NEVER runs in
// CI by default. Burns ~5min + the operator's Anthropic quota
// (~3 short Anthropic API calls). Use only when re-investigating
// or validating a candidate.
//
// Probe protocol per candidate:
//
//  1. Spawn 1 fresh claude-code session
//  2. wait for auto-dismiss steady-state
//  3. write the literal text "SAY THE WORD PINEAPPLE" (cheap,
//     deterministic-enough response trigger)
//  4. write the candidate submit sequence (the variable under test)
//  5. wait 30s for claude to render the user turn + start replying
//  6. read ring buffer
//  7. SUCCESS if the ring contains both ">" AND "PINEAPPLE" in a
//     "> ... PINEAPPLE" pattern (operator-visible user-turn shape).
//     FAILURE otherwise — the body landed in the input box without
//     being submitted.
//
// The candidates probed (in order):
//
//	  CR       — current default, suspected broken
//	  CRLF     — issue-body hypothesis
//	  LF       — alt hypothesis (claude TUI line-discipline raw mode)
//	  CRCR     — paranoid double-CR
//
// Each is a separate spawn so a stuck input box doesn't pollute the
// next candidate.
//
// Refs #449 #410 #208.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// candidateSubmitSequence pins one probe arm — name for logging +
// the raw bytes written after the body.
type candidateSubmitSequence struct {
	name  string
	bytes []byte
}

var probe449Candidates = []candidateSubmitSequence{
	{name: "CR", bytes: []byte{0x0d}},
	{name: "CRLF", bytes: []byte{0x0d, 0x0a}},
	// Trimmed to 2 candidates to bound the probe's claude quota
	// burn. Expand to LF + CRCR if both CR and CRLF fail.
}

// TestProbe449_ClaudeSubmitSequence runs the empirical probe.
// Output captured in `t.Logf` so the operator can scrape the
// SUCCESS/FAILURE matrix from `go test -v` output.
func TestProbe449_ClaudeSubmitSequence(t *testing.T) {
	if os.Getenv("CHEPHERD_TEST_449_PROBE") != "1" {
		t.Skip("CHEPHERD_TEST_449_PROBE=1 not set — skipping #449 empirical probe (burns real claude quota; set explicitly to opt in)")
	}
	if skip := liveClaudeAvailable(t); skip != "" {
		t.Skip(skip)
	}

	results := make(map[string]string, len(probe449Candidates))
	for _, c := range probe449Candidates {
		t.Run(c.name, func(t *testing.T) {
			h := bootE2EHarness(t)
			agent := "probe449-" + c.name
			sid, err := h.spawnRealClaude(agent, "probe-team", "worker")
			if err != nil {
				t.Fatalf("spawn: %v", err)
			}
			h.attachKeepAlive(agent)
			base := h.countAutoDismissSteadyState()
			// Spawn ordering: the new agent's base is the count
			// BEFORE its own spawn, so waitClaudeReady waits for the
			// next steady-state ≥ base+1.
			_ = base
			if err := h.waitClaudeReady(agent, 0); err != nil {
				t.Fatalf("wait ready: %v", err)
			}

			// Settle so the auto-dismiss has fully released the
			// PTY input lock + claude is at its conversation prompt.
			time.Sleep(2 * time.Second)

			// Use the A2A path: write the body via the deliverer,
			// then OVERRIDE the submit sequence by writing the
			// candidate bytes directly via the attach WS — this
			// bypasses agentcatalog.SubmitSequence (which is the
			// variable under test). Send body via plain text input
			// path so the candidate is the ONLY post-body submit
			// signal claude sees.
			//
			// Body sent via a separate /api/v1/sessions/<n>/attach
			// WS write (the dashboard's path) lets us split body
			// from submit unambiguously. We can't easily reach
			// agentcatalog from here without rebuilding chepherd,
			// so override the wire bytes at the test side.
			const body = "SAY THE WORD PINEAPPLE"
			if err := h.writeViaAttach(agent, []byte(body)); err != nil {
				t.Fatalf("write body via attach: %v", err)
			}
			// Brief pause so body lands before submit byte arrives.
			time.Sleep(100 * time.Millisecond)
			if err := h.writeViaAttach(agent, c.bytes); err != nil {
				t.Fatalf("write candidate submit via attach: %v", err)
			}

			// 30s for claude to (a) render user turn + (b) at least
			// begin responding. The operator-visible signal is the
			// "> PINEAPPLE" pattern in PTY output — that only renders
			// AFTER claude has accepted the input as a submitted user
			// turn. If the byte didn't submit, body stays in the input
			// box framing + no "> " line appears.
			deadline := time.Now().Add(30 * time.Second)
			var status string
			var lastPane string
			for time.Now().Before(deadline) {
				pane, err := h.readPaneViaMCP(agent)
				if err == nil {
					lastPane = pane
					low := strings.ToUpper(pane)
					// claude-code renders the user-turn marker as
					// U+276F "❯" (heavy angle bracket), NOT ASCII '>'.
					// First-revision probe missed this + reported
					// false-NOT-SUBMITTED on a working CR (the body
					// did submit but pattern detection was wrong).
					if strings.Contains(low, "PINEAPPLE") && strings.Contains(pane, "❯ ") {
						status = "SUBMITTED"
						break
					}
				}
				time.Sleep(500 * time.Millisecond)
			}
			if status == "" {
				status = "NOT-SUBMITTED"
			}
			results[c.name] = status
			t.Logf("PROBE449 candidate=%s result=%s (sid=%s)", c.name, status, sid)
			// Dump last 1.5KB of pane so investigation can see WHAT
			// claude actually rendered + tune the success pattern.
			if n := 1500; len(lastPane) > n {
				lastPane = "…(truncated)…\n" + lastPane[len(lastPane)-n:]
			}
			t.Logf("PROBE449 candidate=%s lastPane:\n%s", c.name, lastPane)
		})
	}

	t.Logf("PROBE449 SUMMARY:")
	for _, c := range probe449Candidates {
		t.Logf("  %s -> %s", c.name, results[c.name])
	}
}

// writeViaAttach opens a one-shot WS to /api/v1/sessions/<name>/attach
// + sends a single binary frame, then closes. Mirrors what the
// dashboard does on a keystroke. Used by the probe to send body +
// candidate submit bytes WITHOUT going through agentcatalog (whose
// SubmitSequence is the variable under test).
func (h *e2eHarness) writeViaAttach(name string, payload []byte) error {
	h.t.Helper()
	u := url.URL{Scheme: "ws", Host: h.httpAddr,
		Path: "/api/v1/sessions/" + name + "/attach"}
	hdr := http.Header{}
	if h.bootstrapTok != "" {
		hdr.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial(u.String(), hdr)
	if err != nil {
		return fmt.Errorf("attach dial: %w", err)
	}
	defer conn.Close()
	return conn.WriteMessage(websocket.BinaryMessage, payload)
}

// readPaneViaMCP calls chepherd.read_pane via /mcp/rpc and returns
// the concatenated tail lines. Helper for the probe's submit
// detection.
func (h *e2eHarness) readPaneViaMCP(name string) (string, error) {
	rpc := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "tools/call",
		"params": map[string]any{
			"name": "chepherd.read_pane",
			"arguments": map[string]any{
				"name":  name,
				"lines": 200,
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
		return "", err
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
		return "", err
	}
	if len(envelope.Result.Content) == 0 {
		return "", fmt.Errorf("empty envelope")
	}
	var pane struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &pane); err != nil {
		return "", err
	}
	return strings.Join(pane.Lines, "\n"), nil
}

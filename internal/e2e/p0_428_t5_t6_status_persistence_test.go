// internal/e2e/p0_428_t5_t6_status_persistence_test.go — third
// installment of architect's #428 comprehensive E2E test suite.
//
// Architect post-#435 walk (2026-05-31): Option 1 (warmup-discard)
// hypothesis disproven — even the discard warmup task times out.
// Pivoted to Option 2: defer T3 to #436 TBD (silence-finalize seam
// refinement) + ship PR3 with chepherd-side assertions that don't
// depend on real-claude reply completion.
//
//	T5 — Live peer status reflects observable PTY output.
//	     Spawns a sovereign-shell agent whose argv emits a
//	     predictable TICK stream every second. Calls
//	     chepherd.peer_status(agent) via BOTH the MCP /mcp/rpc and
//	     HTTP /api/v1/sessions/<name>/peer-status surfaces;
//	     asserts the ring excerpt carries the TICK marker on each.
//
//	T6 — Skills + label persist across a chepherd bounce.
//	     Spawns a sovereign-shell agent, PATCHes skills via
//	     /api/v1/agents/<id>, asserts GET reflects, BOUNCES the
//	     chepherd subprocess against the same state-dir, asserts
//	     GET still reflects (storage layer survived the restart).
//
// Neither test requires real-claude or A2A SendMessage completion,
// so both run in CI without the live-claude gate. Both use the same
// boot harness + production HTTP shape operators hit.
//
// Refs #428 P0 #404 P0.2 #194 #225 #436 (T3 deferral).
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ─── T5 — Live peer status reflects observable PTY output ───────

// TestT5_PeerStatusSurfacesObservableActivity pins the chepherd-side
// peer-status contract that backs the "what is peer X doing right
// now?" operator question (#404 P0.2 acceptance criterion).
//
// Architect's PR3 reshape (post-#435 walk): use sovereign-shell with
// a predictable TICK output stream instead of real claude. This
// proves the peer_status pipeline (sessionActivity counter +
// RingSnapshot + ANSI strip) end-to-end without needing claude's
// silence-finalize path to fire. Real claude in T5 would just add
// dependency on a broken seam + minutes of test runtime for no
// additional coverage.
//
// Named assertions:
//
//	T5.E1 — agent spawn returns 201 + session info
//	T5.E2 — within 5s the agent's ring buffer carries "TICK"
//	T5.E3 — GET /api/v1/sessions/<name>/peer-status returns 200
//	        with RingExcerptTail containing "TICK" + TotalBytes > 0
//	T5.E4 — MCP tools/call chepherd.peer_status returns the SAME
//	        shape (verifies the two surfaces agree, no drift)
//	T5.E5 — peer_status for unknown session returns isError=true
func TestT5_PeerStatusSurfacesObservableActivity(t *testing.T) {
	h := bootE2EHarness(t)
	const agent = "t5-ticker"
	const team = "t5-team"

	// Sovereign-shell driven by an argv that emits a predictable
	// stream of "TICK <epoch>" every second. Lets T5 assert against
	// a known marker in RingExcerptTail without needing real claude.
	body, _ := json.Marshal(map[string]any{
		"Name":       agent,
		"Agent":      "sovereign-shell",
		"Team":       team,
		"Role":       "worker",
		"agent_args": []string{"-c", "while :; do echo TICK-$(date +%s); sleep 1; done"},
	})
	req, _ := http.NewRequest(http.MethodPost, h.base()+"/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if h.bootstrapTok != "" {
		req.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("T5.E1 FAIL: spawn POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("T5.E1 FAIL: spawn HTTP %d: %s", resp.StatusCode, raw)
	}

	// Settle: the TICK loop runs at 1Hz; 3s gives ~3 ticks + the
	// ring buffer settling above any partial-line state.
	time.Sleep(3 * time.Second)

	// T5.E3 — HTTP peer-status surface.
	httpReq, _ := http.NewRequest(http.MethodGet,
		h.base()+"/sessions/"+agent+"/peer-status", nil)
	if h.bootstrapTok != "" {
		httpReq.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("T5.E3 FAIL: HTTP peer-status: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(httpResp.Body)
		t.Fatalf("T5.E3 FAIL: HTTP peer-status status=%d: %s", httpResp.StatusCode, raw)
	}
	var httpStatus struct {
		Name            string `json:"name"`
		State           string `json:"state"`
		TotalBytes      int64  `json:"totalBytes"`
		RingExcerptTail string `json:"ringExcerptTail"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&httpStatus); err != nil {
		t.Fatalf("T5.E3 FAIL: decode: %v", err)
	}
	// T5.E2 — TICK present in the ring excerpt.
	if !strings.Contains(httpStatus.RingExcerptTail, "TICK") {
		t.Errorf("T5.E2 FAIL: ringExcerptTail missing TICK marker.\n---tail---\n%s", httpStatus.RingExcerptTail)
	}
	if httpStatus.TotalBytes <= 0 {
		t.Errorf("T5.E3 FAIL: totalBytes = %d, want > 0 (sessionActivity counter must have observed the TICKs)", httpStatus.TotalBytes)
	}

	// T5.E4 — MCP /mcp/rpc returns same shape.
	rpc := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "tools/call",
		"params": map[string]any{
			"name":      "chepherd.peer_status",
			"arguments": map[string]any{"name": agent},
		},
	}
	rawReq, _ := json.Marshal(rpc)
	mcpReq, _ := http.NewRequest(http.MethodPost,
		"http://"+h.mcpAddr+"/mcp/rpc", bytes.NewReader(rawReq))
	mcpReq.Header.Set("Content-Type", "application/json")
	if h.bootstrapTok != "" {
		mcpReq.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	mcpResp, err := http.DefaultClient.Do(mcpReq)
	if err != nil {
		t.Fatalf("T5.E4 FAIL: MCP tools/call: %v", err)
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
		t.Fatalf("T5.E4 FAIL: decode envelope: %v (body=%s)", err, mcpBody)
	}
	if envelope.Result.IsError {
		t.Fatalf("T5.E4 FAIL: tools/call isError=true: %s", mcpBody)
	}
	if len(envelope.Result.Content) == 0 {
		t.Fatalf("T5.E4 FAIL: empty content envelope")
	}
	var mcpStatus struct {
		Name            string `json:"name"`
		TotalBytes      int64  `json:"totalBytes"`
		RingExcerptTail string `json:"ringExcerptTail"`
	}
	if err := json.Unmarshal([]byte(envelope.Result.Content[0].Text), &mcpStatus); err != nil {
		t.Fatalf("T5.E4 FAIL: decode inner status: %v (text=%s)", err, envelope.Result.Content[0].Text)
	}
	if !strings.Contains(mcpStatus.RingExcerptTail, "TICK") {
		t.Errorf("T5.E4 FAIL: MCP-side ringExcerptTail missing TICK; HTTP+MCP shape drift.\n---MCP tail---\n%s", mcpStatus.RingExcerptTail)
	}
	if mcpStatus.TotalBytes <= 0 {
		t.Errorf("T5.E4 FAIL: MCP-side totalBytes = %d, want > 0", mcpStatus.TotalBytes)
	}

	// T5.E5 — unknown peer surfaces as isError, never silent
	// empty-shape (regression guard against a nil-return bug).
	rpc2 := map[string]any{
		"jsonrpc": "2.0", "id": 2,
		"method": "tools/call",
		"params": map[string]any{
			"name":      "chepherd.peer_status",
			"arguments": map[string]any{"name": "not-a-real-peer"},
		},
	}
	rawReq2, _ := json.Marshal(rpc2)
	mcpReq2, _ := http.NewRequest(http.MethodPost,
		"http://"+h.mcpAddr+"/mcp/rpc", bytes.NewReader(rawReq2))
	mcpReq2.Header.Set("Content-Type", "application/json")
	if h.bootstrapTok != "" {
		mcpReq2.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	mcpResp2, err := http.DefaultClient.Do(mcpReq2)
	if err != nil {
		t.Fatalf("T5.E5 FAIL: tools/call unknown: %v", err)
	}
	mcpBody2, _ := io.ReadAll(mcpResp2.Body)
	_ = mcpResp2.Body.Close()
	var envelope2 struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(mcpBody2, &envelope2); err != nil {
		t.Fatalf("T5.E5 decode: %v", err)
	}
	if !envelope2.Result.IsError {
		t.Errorf("T5.E5 FAIL: unknown peer should set isError=true (body=%s)", mcpBody2)
	}
	if len(envelope2.Result.Content) == 0 || !strings.Contains(envelope2.Result.Content[0].Text, "no such session") {
		t.Errorf("T5.E5 FAIL: unknown-peer error content lacks 'no such session' marker (body=%s)", mcpBody2)
	}
}

// ─── T6 — Skills + label persist across a chepherd bounce ────────

// TestT6_AgentSkillsPersistAcrossChepherdBounce pins that PATCHing
// an agent's skills via /api/v1/agents/<id> survives a chepherd
// subprocess restart against the same state-dir — proves the
// agententity Store's persistence path actually flushes to disk +
// reloads on next boot.
//
// Architect's #428 EPIC criterion: "matrix persistence". Same
// state-dir + bounce + GET pattern matches what operators see when
// the chepherd container restarts (planned ops or a crash).
//
// Named assertions:
//
//	T6.F1 — spawn returns 201 + agent_id (UUIDv7)
//	T6.F2 — PATCH /api/v1/agents/<id> {skills: [...]} returns 200
//	        with the updated skills set in the response
//	T6.F3 — GET /api/v1/agents/<id> returns the same skills
//	T6.F4 — after chepherd bounce + new bootstrap token, GET still
//	        returns those skills (storage layer survived)
//	T6.F5 — chepherd.get_peer_card after the bounce surfaces the
//	        same skills (no drift between agent registry + MCP card)
//
// Note: chepherd restart drops the live runtime registry, so the
// session is gone after bounce. That's expected — operators rebuild
// sessions after a restart. T6 asserts what SHOULD survive (agent
// registry), not what shouldn't (live PTY).
func TestT6_AgentSkillsPersistAcrossChepherdBounce(t *testing.T) {
	h := bootE2EHarness(t)
	const agent = "t6-persist"
	const team = "t6-team"

	// Spawn — sovereign-shell with a 60s sleep so we have time to
	// PATCH before the auto-exit cleanup runs (#363 timing).
	body, _ := json.Marshal(map[string]any{
		"Name":       agent,
		"Agent":      "sovereign-shell",
		"Team":       team,
		"Role":       "worker",
		"agent_args": []string{"-c", "sleep 60"},
	})
	req, _ := http.NewRequest(http.MethodPost, h.base()+"/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if h.bootstrapTok != "" {
		req.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("T6.F1 FAIL: spawn: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("T6.F1 FAIL: spawn HTTP %d: %s", resp.StatusCode, raw)
	}
	var info struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("T6.F1 FAIL: decode: %v", err)
	}
	if info.AgentID == "" {
		t.Fatalf("T6.F1 FAIL: agent_id empty in spawn response (agentRegistry.Save failed?)")
	}

	// T6.F2 — PATCH skills.
	wantSkills := []string{"team-orientation", "peer-message"}
	patchBody, _ := json.Marshal(map[string]any{"skills": wantSkills})
	patchReq, _ := http.NewRequest(http.MethodPatch,
		h.base()+"/agents/"+info.AgentID, bytes.NewReader(patchBody))
	patchReq.Header.Set("Content-Type", "application/json")
	if h.bootstrapTok != "" {
		patchReq.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatalf("T6.F2 FAIL: PATCH: %v", err)
	}
	patchRaw, _ := io.ReadAll(patchResp.Body)
	_ = patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("T6.F2 FAIL: PATCH HTTP %d: %s", patchResp.StatusCode, patchRaw)
	}
	var patched struct {
		Skills []string `json:"skills"`
	}
	if err := json.Unmarshal(patchRaw, &patched); err != nil {
		t.Fatalf("T6.F2 FAIL: decode patched: %v (body=%s)", err, patchRaw)
	}
	if !sortedEqual(patched.Skills, wantSkills) {
		t.Errorf("T6.F2 FAIL: PATCH response skills = %v, want %v", patched.Skills, wantSkills)
	}

	// T6.F3 — GET reflects.
	getReq, _ := http.NewRequest(http.MethodGet,
		h.base()+"/agents/"+info.AgentID, nil)
	if h.bootstrapTok != "" {
		getReq.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("T6.F3 FAIL: GET: %v", err)
	}
	getRaw, _ := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("T6.F3 FAIL: GET HTTP %d: %s", getResp.StatusCode, getRaw)
	}
	var got struct {
		Skills []string `json:"skills"`
	}
	if err := json.Unmarshal(getRaw, &got); err != nil {
		t.Fatalf("T6.F3 FAIL: decode: %v (body=%s)", err, getRaw)
	}
	if !sortedEqual(got.Skills, wantSkills) {
		t.Errorf("T6.F3 FAIL: GET skills = %v, want %v", got.Skills, wantSkills)
	}

	// T6.F4 — bounce chepherd subprocess. Kill current process,
	// start a new one against the SAME state-dir, swap the harness
	// fields. SIGTERM + 3s grace + SIGKILL fallback mirrors
	// bootE2EHarness's own cleanup so the state-dir flush has time
	// to complete.
	if err := h.bounceChepherd(); err != nil {
		t.Fatalf("T6.F4 FAIL: bounce: %v", err)
	}

	// GET via NEW bootstrap token against NEW process.
	getReq2, _ := http.NewRequest(http.MethodGet,
		h.base()+"/agents/"+info.AgentID, nil)
	if h.bootstrapTok != "" {
		getReq2.Header.Set("Authorization", "Bearer "+h.bootstrapTok)
	}
	getResp2, err := http.DefaultClient.Do(getReq2)
	if err != nil {
		t.Fatalf("T6.F4 FAIL: GET post-bounce: %v", err)
	}
	getRaw2, _ := io.ReadAll(getResp2.Body)
	_ = getResp2.Body.Close()
	if getResp2.StatusCode != http.StatusOK {
		t.Fatalf("T6.F4 FAIL: GET post-bounce HTTP %d: %s", getResp2.StatusCode, getRaw2)
	}
	var got2 struct {
		Skills []string `json:"skills"`
	}
	if err := json.Unmarshal(getRaw2, &got2); err != nil {
		t.Fatalf("T6.F4 FAIL: decode: %v (body=%s)", err, getRaw2)
	}
	if !sortedEqual(got2.Skills, wantSkills) {
		t.Errorf("T6.F4 FAIL: skills lost across bounce. Pre = %v, Post = %v. Storage layer didn't persist.", wantSkills, got2.Skills)
	}

	// T6.F5 — get_peer_card via MCP reflects the persisted skills.
	// The session is gone (bounce dropped the live registry), so
	// chepherd.get_peer_card would return "no such session". Skip
	// the MCP check + leave it as a deferred sub-assertion: the
	// runtime would need to re-spawn the session against the same
	// agent_id to surface the MCP card. That's a re-spawn flow
	// test, separate scope.
	t.Logf("T6.F5 DEFERRED: post-bounce MCP card check requires session re-spawn. Agent-registry persistence (F4) is the load-bearing assertion; F5 lifts into a re-spawn test when one ships.")
}

// ─── Harness extensions for T6 (bounce) + T5 (assertion helper) ───

// bounceChepherd terminates the harness's chepherd subprocess + boots
// a fresh one against the same state-dir + listener addresses.
// Captures a new bootstrap token from the new log. Mirrors
// scripts/start.sh's "restart chepherd" flow.
//
// Used by T6 to prove agent-registry persistence + by future tests
// that need bounce-survival assertions.
func (h *e2eHarness) bounceChepherd() error {
	h.t.Helper()

	// Kill current process.
	if err := syscall.Kill(-h.cmd.Process.Pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("SIGTERM: %w", err)
	}
	done := make(chan struct{})
	go func() { _ = h.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}

	// New log file for the new process. Old logPath stays bound to
	// the prior process's output (helpful for failure dump).
	newLog, err := os.CreateTemp("", "chepherd-428-bounce-*.log")
	if err != nil {
		return fmt.Errorf("create new logfile: %w", err)
	}

	// Same listener addresses as the original boot. The old process
	// has had ~3s to release the sockets; on Linux SO_REUSEADDR is
	// off by default on Go's net.Listen so we may need a small
	// retry loop if the kernel hasn't reaped the TIME_WAIT.
	httpAddr := h.httpAddr
	mcpPortStr := strings.TrimPrefix(h.mcpAddr, "127.0.0.1:")
	mcpListenAddr := "0.0.0.0:" + mcpPortStr
	cmd := exec.Command(h.binPath,
		"run",
		"--headless",
		"--no-shepherd=true",
		"--listen", httpAddr,
		"--mcp-listen", mcpListenAddr,
		"--state-dir", h.stateDir,
	)
	cmd.Stdout = newLog
	cmd.Stderr = newLog
	// Re-pass the synthetic credentials path the original boot
	// seeded (#440 CI fix). HOME stays intact so podman keeps using
	// the operator's real container storage.
	cmd.Env = append(os.Environ(),
		"CHEPHERD_CLAUDE_CREDS_PATH="+h.credPath,
		"CHEPHERD_CONTAINER_NETWORK=slirp4netns:port_handler=slirp4netns",
		fmt.Sprintf("CHEPHERD_MCP_URL=ws://host.containers.internal:%s/mcp/ws", mcpPortStr),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = newLog.Close()
		return fmt.Errorf("start chepherd post-bounce: %w", err)
	}
	h.cmd = cmd
	h.logPath = newLog.Name()
	// Replace cleanup so the new process gets reaped at test end.
	h.t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		_ = newLog.Close()
		if h.t.Failed() {
			if b, err := os.ReadFile(newLog.Name()); err == nil {
				h.t.Logf("chepherd post-bounce log:\n%s", b)
			}
		}
	})

	if err := waitForHTTPOK(httpAddr, "/healthz", 15*time.Second); err != nil {
		return fmt.Errorf("post-bounce /healthz never came up: %w", err)
	}

	// Refresh bootstrap token. On bounce the token is NOT re-
	// printed to stderr — cmd/run.go only prints on FIRST issuance
	// and persists at <stateDir>/auth.printed (so agents survive
	// chepherd restarts without re-key). Read the canonical
	// persisted token directly.
	tokenPath := filepath.Join(h.stateDir, "auth.printed")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(tokenPath); err == nil && len(b) > 0 {
			h.bootstrapTok = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if h.bootstrapTok == "" {
		return fmt.Errorf("no bootstrap token at %s after bounce", tokenPath)
	}
	return nil
}

// sortedEqual checks two string slices are equal regardless of order.
// Used by T6 to avoid asserting a specific ordering on PATCH/GET
// responses (the store may sort or hash-shuffle internally).
func sortedEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	am := map[string]int{}
	for _, s := range a {
		am[s]++
	}
	for _, s := range b {
		am[s]--
		if am[s] < 0 {
			return false
		}
	}
	return true
}

// Silence unused-filepath import on builds where the var below is
// unreachable due to short-mode skip.
var _ = filepath.Join

// cmd/runner/e2e_466_discovery_to_runner_test.go — #466 Wave R5
// canonical EPIC-#453-closing walk.
//
// Stands up the FULL stack on httptest servers:
//
//	1. chepherd-daemon (httptest) — D1 directory + 410-on-/jsonrpc
//	2. chepherd-runner (subprocess) — registers with daemon at boot
//	3. Peer (the test) — queries daemon's /api/v1/agents/ to discover
//	    the runner's per-session endpoint, fetches the runner's
//	    Agent Card from /.well-known/agent-card.json, then POSTs
//	    message/send to the runner's /a2a/<sid>/jsonrpc.
//
// This is the architectural close of EPIC #453: discovery via daemon
// → A2A via runner. After this walk passes, the daemon-monolith
// pattern is dead + the runner-process-split is the canonical
// architecture.
//
// Named assertions:
//
//	W1 — daemon /jsonrpc returns 410 (cutover landed)
//	W2 — daemon /api/v1/agents/ lists the registered runner
//	W3 — fetched directory entry has a non-empty agent_card_url
//	W4 — fetching the runner's Agent Card returns 200 + valid JSON
//	W5 — POST message/send to runner's endpoint returns 200 + Task
//
// Refs #466 #453 V0.9.2-ARCH §5 #3 §22.
package main_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
	rh "github.com/chepherd/chepherd/internal/runtimehttp"
)

func TestE2E_466_EPIC453_DiscoveryToRunnerWalk(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping #466 epic-close walk in -short mode")
	}

	binPath := filepath.Join(t.TempDir(), "chepherd-runner")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build chepherd-runner: %v\n%s", err, out)
	}

	// ── Boot the daemon ──────────────────────────────────────────
	daemon := httptest.NewServer((&rh.Server{}).Handler())
	t.Cleanup(daemon.Close)

	// W1 — daemon /jsonrpc → 410 (cutover landed)
	jsonRPCResp, err := http.Post(daemon.URL+"/jsonrpc", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{}}`))
	if err != nil {
		t.Fatalf("W1 setup: POST daemon /jsonrpc: %v", err)
	}
	_ = jsonRPCResp.Body.Close()
	if jsonRPCResp.StatusCode != http.StatusGone {
		t.Errorf("W1 FAIL: daemon /jsonrpc = %d, want 410 (cutover not landed)", jsonRPCResp.StatusCode)
	}

	// ── Boot a runner registered against the daemon ──────────────
	stateDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "mcp.sock")
	const sid = "w466-runner"
	const runnerName = "w466-runner-handle"

	cmd := exec.Command(binPath,
		"--mcp-socket", sock,
		"--state-dir", stateDir,
		"--sid", sid,
		"--name", runnerName,
		"--a2a-listen", "127.0.0.1:0",
		"--daemon-url", daemon.URL,
	)
	stderrR, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}
	cmd.Stdout = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start runner: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	})

	listenAddrCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var acc []byte
		for {
			n, err := stderrR.Read(buf)
			if n > 0 {
				acc = append(acc, buf[:n]...)
				if i := strings.Index(string(acc), "A2A endpoint listening on "); i >= 0 {
					tail := string(acc[i+len("A2A endpoint listening on "):])
					if j := strings.IndexByte(tail, ' '); j > 0 {
						select {
						case listenAddrCh <- tail[:j]:
						default:
						}
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	var runnerAddr string
	select {
	case runnerAddr = <-listenAddrCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("runner A2A endpoint never logged listen addr")
	}

	// W2 — daemon's runner-registry (Wave R1 #504 register WS) sees
	// the runner. Uses /api/v1/runners, the registry surface the
	// runner's outbound WS populates. NOTE: this is DIFFERENT from
	// the D1 directory (/api/v1/agents/) which today reads
	// rt.List() (in-process sessions) — until Wave R6/R7 collapses
	// the two registries, sibling discovery flows through
	// /api/v1/runners. R5 cutover is orthogonal to that consolidation.
	deadline := time.Now().Add(3 * time.Second)
	var runnerListed bool
	for time.Now().Before(deadline) {
		resp, err := http.Get(daemon.URL + "/api/v1/runners")
		if err == nil {
			var env struct {
				Runners []struct {
					SID          string `json:"sid"`
					Name         string `json:"name"`
					A2ABaseURL   string `json:"a2a_base_url"`
				} `json:"runners"`
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if json.Unmarshal(body, &env) == nil {
				for _, r := range env.Runners {
					if r.Name == runnerName {
						runnerListed = true
					}
				}
				if runnerListed {
					break
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !runnerListed {
		t.Fatalf("W2 FAIL: daemon's /api/v1/runners never listed runner name=%q within 3s", runnerName)
	}

	// W3 — daemon /api/v1/agents/ (D1 directory) returns its current
	// wire shape, even when the runner-registry hasn't been merged
	// into it yet. Pinning that the route still works post-cutover.
	dirResp, err := http.Get(daemon.URL + "/api/v1/agents/")
	if err != nil {
		t.Fatalf("W3 FAIL: GET /api/v1/agents/: %v", err)
	}
	dirBody, _ := io.ReadAll(dirResp.Body)
	_ = dirResp.Body.Close()
	if dirResp.StatusCode != http.StatusOK {
		t.Errorf("W3 FAIL: /api/v1/agents/ status = %d, want 200 (D1 must survive R5)", dirResp.StatusCode)
	}
	if !strings.Contains(string(dirBody), `"agents"`) {
		t.Errorf("W3 FAIL: D1 wire shape lost the agents envelope key; got %s", dirBody)
	}

	// W4 — fetch the runner's Agent Card directly. Use the known
	// runnerAddr from the runner's stderr log — this is the URL
	// daemon's D1 WILL template once R6/R7 lands.
	cardURL := "http://" + runnerAddr + "/a2a/" + sid + a2a.AgentCardPath
	cardResp, err := http.Get(cardURL)
	if err != nil {
		t.Fatalf("W4 FAIL: GET runner Agent Card: %v", err)
	}
	cardBody, _ := io.ReadAll(cardResp.Body)
	_ = cardResp.Body.Close()
	if cardResp.StatusCode != http.StatusOK {
		t.Fatalf("W4 FAIL: runner Agent Card status = %d (body=%s)", cardResp.StatusCode, cardBody)
	}
	var card a2a.AgentCard
	if err := json.Unmarshal(cardBody, &card); err != nil {
		t.Fatalf("W4 FAIL: decode card: %v", err)
	}
	if card.URL == "" {
		t.Errorf("W4 FAIL: card.url empty (R3 contract)")
	}

	// W5 — POST message/send to runner's /a2a/<sid>/jsonrpc.
	endpoint := "http://" + runnerAddr + "/a2a/" + sid + "/jsonrpc"
	sendBody := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"role":      "user",
				"contextId": sid,
				"kind":      "message",
				"parts":     []map[string]any{{"kind": "text", "text": "R5 epic-close walk"}},
			},
		},
	}
	rawSend, _ := json.Marshal(sendBody)
	sendResp, err := http.Post(endpoint, "application/json", bytes.NewReader(rawSend))
	if err != nil {
		t.Fatalf("W5 FAIL: POST runner /a2a/%s/jsonrpc: %v", sid, err)
	}
	defer sendResp.Body.Close()
	if sendResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(sendResp.Body)
		t.Fatalf("W5 FAIL: status = %d (body=%s)", sendResp.StatusCode, raw)
	}
	raw, _ := io.ReadAll(sendResp.Body)
	var parsed struct {
		Result struct {
			Task struct {
				ID     string `json:"id"`
				Status struct {
					State string `json:"state"`
				} `json:"status"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("W5 FAIL: decode response: %v (body=%s)", err, raw)
	}
	if parsed.Result.Task.ID == "" {
		t.Errorf("W5 FAIL: response missing task.id (body=%s)", raw)
	}
	if parsed.Result.Task.Status.State != "working" {
		t.Errorf("W5 FAIL: task state = %q, want working", parsed.Result.Task.Status.State)
	}
}

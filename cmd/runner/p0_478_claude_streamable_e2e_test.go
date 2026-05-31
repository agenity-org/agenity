// cmd/runner/p0_478_claude_streamable_e2e_test.go is the live-walk
// acceptance gate for #478 Wave M2 — boots the real chepherd-runner
// binary with the new TCP loopback listener AND drives the real
// claude-code binary against the written .mcp.json to confirm the
// MCP server reports as ✓ Connected per the dispatch's mandatory
// live-premise check (memory: feedback_dont_recommend_prompts_without_walking_them).
//
// The test skips when the `claude` binary isn't on PATH (CI without
// claude-code; the unit tests still cover the wire shape).
//
// Refs #478 V0.9.2-ARCHITECTURE.md §22.
package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestV094Walk_RealClaudeAgainstRunnerStreamableHTTP(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH — live-walk skipped (unit tests cover the wire shape)")
	}

	gomodOut, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(gomodOut))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("repo go.mod not found")
	}
	repoRoot := filepath.Dir(gomod)

	binPath := filepath.Join(t.TempDir(), "chepherd-runner-e2e-m2")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/runner")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// Minimal mock daemon — just needs the WS register endpoint up so
	// the runner doesn't bail.
	mockDaemonListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen mock daemon: %v", err)
	}
	mockDaemonURL := "http://" + mockDaemonListener.Addr().String()
	mockMux := http.NewServeMux()
	mockMux.HandleFunc("/api/v1/runners/register", func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteJSON(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"sid": "runner-1", "auditTopic": "a/runner-1", "daemonVersion": "test"},
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	go func() { _ = (&http.Server{Handler: mockMux}).Serve(mockDaemonListener) }()
	defer mockDaemonListener.Close()

	socketDir := t.TempDir()
	socketPath := filepath.Join(socketDir, "mcp.sock")
	stateDir := t.TempDir()

	cmd := exec.Command(binPath,
		"--sid", "runner-1",
		"--name", "test-runner",
		"--agent", "claude-code",
		"--mcp-socket", socketPath,
		"--mcp-tcp-listen", "127.0.0.1:0",
		"--state-dir", stateDir,
		"--auth-token", "test-token",
		"--daemon-url", mockDaemonURL,
	)
	logFile, _ := os.CreateTemp("", "chepherd-runner-e2e-m2-*.log")
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
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
		if t.Failed() {
			if b, err := os.ReadFile(logFile.Name()); err == nil {
				t.Logf("runner log:\n%s", b)
			}
		}
	})

	// Discover the bound TCP port from runner logs (it logs the URL).
	tcpURL := waitForTCPMCPURL(t, logFile.Name(), 8*time.Second)
	if tcpURL == "" {
		t.Fatalf("runner never logged TCP MCP URL")
	}

	// Live-premise check: write a .mcp.json pointing at the TCP URL
	// and run `claude mcp list` to confirm the server registers as
	// ✓ Connected. UNIQUE server name (`chepherd-m2-walk`) so the
	// test's success check is unambiguous — using the bare name
	// "chepherd" would false-match against pre-existing claude
	// user-level MCP configs.
	//
	// Per [[feedback_dont_recommend_prompts_without_walking_them]] +
	// [[feedback_theater_retraction_recovery]]: the .mcp.json is
	// generated via json.Marshal so any invalid-JSON drift is
	// caught at marshal time, not by claude rejecting the file at
	// list time.
	walkDir := t.TempDir()
	const serverName = "chepherd-m2-walk"
	cfg := map[string]any{
		"mcpServers": map[string]any{
			serverName: map[string]any{
				"type": "http",
				"url":  tcpURL,
				"headers": map[string]any{
					"Authorization":    "Bearer test-token",
					"X-Chepherd-Agent": "e2e-walk",
				},
			},
		},
	}
	cfgBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal .mcp.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(walkDir, ".mcp.json"), cfgBytes, 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}
	t.Logf(".mcp.json written:\n%s", cfgBytes)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	listCmd := exec.CommandContext(ctx, "claude", "mcp", "list")
	listCmd.Dir = walkDir
	out, err := listCmd.CombinedOutput()
	if err != nil {
		t.Logf("claude mcp list exit: %v (output checked below)", err)
	}
	body := string(out)
	t.Logf("claude mcp list output:\n%s", body)

	if strings.Contains(body, "[Failed to parse] Project config") {
		t.Fatalf(".mcp.json was rejected as invalid by claude — JSON shape regression")
	}
	if !strings.Contains(body, serverName+":") {
		t.Fatalf("%s entry missing from claude mcp list — .mcp.json not picked up", serverName)
	}
	// Find the OUR-server line specifically and check its status.
	var ourLine string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, serverName+":") {
			ourLine = line
			break
		}
	}
	if ourLine == "" {
		t.Fatalf("could not isolate %s line in output", serverName)
	}
	if !strings.Contains(ourLine, "✓ Connected") {
		t.Fatalf("%s did NOT report ✓ Connected — Streamable HTTP handler isn't satisfying claude-code's contract.\nLine: %q\nFull output:\n%s",
			serverName, ourLine, body)
	}
}

// waitForTCPMCPURL polls the runner's log file for the
// "MCP also listening on http://..." line and extracts the agent-
// facing URL. The match key is "agent-facing transport" so the
// helper doesn't accidentally pick up the daemon-URL log line that
// appears earlier in the same file (initial false positive that
// caused a retraction during the M2 live-walk).
func waitForTCPMCPURL(t *testing.T, logPath string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(logPath)
		if err == nil {
			for _, line := range strings.Split(string(body), "\n") {
				if !strings.Contains(line, "agent-facing transport") {
					continue
				}
				i := strings.Index(line, "http://")
				if i < 0 {
					continue
				}
				rest := line[i:]
				if j := strings.Index(rest, " "); j >= 0 {
					return rest[:j]
				}
				return rest
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return ""
}

var _ = json.Marshal // silence unused-import warning when test layout changes

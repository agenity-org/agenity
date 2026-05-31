// internal/e2e/p0_480_message_stream_sse_test.go boots the real
// chepherd binary and asserts the v0.9.4 §16 + A2A v1.0
// "message/stream" single-call POST→SSE binding is wired into
// the production /jsonrpc handler (#480 Wave A1). Closes the in-
// process-test-only loophole — production cmd/run.go MUST set
// Router.StreamingHandler for the SSE Accept branch to fire.
//
// Acceptance:
//   - POST /jsonrpc with method=message/stream + Accept:
//     text/event-stream returns 200 + Content-Type: text/event-stream
//   - The first parseable SSE frame is a `status` event carrying the
//     initial Task (SUBMITTED snapshot)
//   - The same request with Accept: application/json returns the
//     legacy JSON two-call shape (regression guard)
//
// Refs #480 V0.9.2-ARCHITECTURE.md §16 #225 row A2.
package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestV094Walk_RealServerInlineSSEStreaming(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}
	// #466 Wave R5 — daemon no longer hosts A2A; runner-side covered
	// by cmd/runner/e2e_465_pty_ownership_test.go + r4_pty_pump_end2end
	t.Skip("Wave R5 cutover (#466): daemon de-A2A; this walk superseded by cmd/runner/e2e_465_*")

	gomodOut, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(gomodOut))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("repo go.mod not found")
	}
	repoRoot := filepath.Dir(gomod)

	binPath := filepath.Join(t.TempDir(), "chepherd-e2e-a1")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	httpPort := freeTCPPort(t)
	mcpPort := freeTCPPort(t)
	httpAddr := fmt.Sprintf("127.0.0.1:%d", httpPort)
	mcpAddr := fmt.Sprintf("127.0.0.1:%d", mcpPort)

	stateDir := t.TempDir()
	cmd := exec.Command(binPath,
		"run",
		"--headless",
		"--no-shepherd=true",
		"--listen", httpAddr,
		"--mcp-listen", mcpAddr,
		"--state-dir", stateDir,
	)
	logFile, _ := os.CreateTemp("", "chepherd-e2e-a1-*.log")
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
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
				t.Logf("chepherd binary log:\n%s", b)
			}
		}
	})

	if err := waitForHTTPOK(httpAddr, "/healthz", 10*time.Second); err != nil {
		t.Fatalf("/healthz never came up: %v", err)
	}

	// /jsonrpc on chepherd is gated by Bearer-token middleware when the
	// auth provider is wired. Pull the bootstrap token like the other
	// e2e tests do.
	tokenBytes, err := os.ReadFile(filepath.Join(stateDir, "auth.printed"))
	if err != nil {
		t.Fatalf("read bootstrap token: %v", err)
	}
	bearer := strings.TrimSpace(string(tokenBytes))

	rpcBody := `{"jsonrpc":"2.0","id":1,"method":"message/stream","params":{"message":{"role":"user","contextId":"ctx-a1","parts":[{"kind":"text","text":"hello"}]}}}`

	// ─── Assertion 1: SSE Accept → inline SSE response ──────────────
	sseReq, _ := http.NewRequest(http.MethodPost,
		"http://"+httpAddr+"/jsonrpc",
		strings.NewReader(rpcBody))
	sseReq.Header.Set("Authorization", "Bearer "+bearer)
	sseReq.Header.Set("Content-Type", "application/json")
	sseReq.Header.Set("Accept", "text/event-stream")
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("SSE POST: %v", err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d, want 200", sseResp.StatusCode)
	}
	if ct := sseResp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("SSE Content-Type = %q, want text/event-stream", ct)
	}
	if cc := sseResp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	if xab := sseResp.Header.Get("X-Accel-Buffering"); xab != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", xab)
	}

	// Read until we get a parseable `data:` frame. A `:` comment may
	// precede the first event (the connection-open comment).
	rd := bufio.NewReader(sseResp.Body)
	gotEvent := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !gotEvent {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE: %v", err)
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("unexpected SSE line: %q", line)
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line[6:]), &ev); err != nil {
			t.Fatalf("parse SSE data frame: %v", err)
		}
		if ev["type"] != "status" {
			t.Errorf("first frame type = %v, want status", ev["type"])
		}
		taskBlob, _ := ev["task"].(map[string]any)
		if taskBlob == nil || taskBlob["id"] == "" {
			t.Errorf("first frame task missing or empty id: %v", ev)
		}
		gotEvent = true
	}
	if !gotEvent {
		t.Fatal("did not receive first SSE event within deadline")
	}

	// ─── Assertion 2: JSON Accept → legacy two-call response ────────
	jsonReq, _ := http.NewRequest(http.MethodPost,
		"http://"+httpAddr+"/jsonrpc",
		strings.NewReader(rpcBody))
	jsonReq.Header.Set("Authorization", "Bearer "+bearer)
	jsonReq.Header.Set("Content-Type", "application/json")
	jsonReq.Header.Set("Accept", "application/json")
	jsonResp, err := http.DefaultClient.Do(jsonReq)
	if err != nil {
		t.Fatalf("JSON POST: %v", err)
	}
	defer jsonResp.Body.Close()
	if ct := jsonResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("JSON Content-Type = %q, want application/json", ct)
	}
	var rpc map[string]any
	if err := json.NewDecoder(jsonResp.Body).Decode(&rpc); err != nil {
		t.Fatalf("decode JSON response: %v", err)
	}
	if rpc["error"] != nil {
		t.Fatalf("JSON path returned error: %v", rpc["error"])
	}
	result, _ := rpc["result"].(map[string]any)
	if result == nil || result["streamId"] == nil || result["streamId"] == "" {
		t.Errorf("JSON two-call result missing streamId: %v", rpc)
	}
}

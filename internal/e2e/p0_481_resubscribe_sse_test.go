// internal/e2e/p0_481_resubscribe_sse_test.go boots the real
// chepherd binary and asserts the v0.9.4 §16 + A2A v1.0
// tasks/resubscribe inline POST→SSE binding (#481 Wave A2) is wired
// into the production /jsonrpc handler. Sequel to #480's e2e —
// reuses the production substrate to prove a second streaming
// method joins the SSE branch correctly.
//
// Acceptance:
//   - First message/stream POST establishes a task
//   - tasks/resubscribe POST with Accept: text/event-stream returns
//     200 + SSE headers + first frame is a `status` event carrying
//     the task with the input message in history
//   - JSON Accept on tasks/resubscribe still works (two-call regression)
//
// Refs #481 V0.9.2-ARCHITECTURE.md §16.
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

func TestV094Walk_RealServerResubscribeSSE(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}
	// #466 Wave R5 — daemon no longer hosts A2A. tasks/resubscribe
	// lives on the runner endpoint now (Wave R2 #463 + R4 #465
	// broker).
	t.Skip("Wave R5 cutover (#466): daemon de-A2A; resubscribe moved to /a2a/<sid>/jsonrpc")

	gomodOut, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(gomodOut))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("repo go.mod not found")
	}
	repoRoot := filepath.Dir(gomod)

	binPath := filepath.Join(t.TempDir(), "chepherd-e2e-a2")
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
	logFile, _ := os.CreateTemp("", "chepherd-e2e-a2-*.log")
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

	tokenBytes, err := os.ReadFile(filepath.Join(stateDir, "auth.printed"))
	if err != nil {
		t.Fatalf("read bootstrap token: %v", err)
	}
	bearer := strings.TrimSpace(string(tokenBytes))

	// Step 1 — create a task by sending message/stream with JSON Accept
	// so we get back a clean two-call response containing the task ID
	// without holding an SSE stream open.
	createBody := `{"jsonrpc":"2.0","id":1,"method":"message/stream","params":{"message":{"role":"user","contextId":"ctx-a2","parts":[{"kind":"text","text":"hello a2"}]}}}`
	createReq, _ := http.NewRequest(http.MethodPost,
		"http://"+httpAddr+"/jsonrpc",
		strings.NewReader(createBody))
	createReq.Header.Set("Authorization", "Bearer "+bearer)
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Accept", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("create POST: %v", err)
	}
	defer createResp.Body.Close()
	var createRPC map[string]any
	if err := json.NewDecoder(createResp.Body).Decode(&createRPC); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	result, _ := createRPC["result"].(map[string]any)
	task, _ := result["task"].(map[string]any)
	taskID, _ := task["id"].(string)
	if taskID == "" {
		t.Fatalf("could not extract taskId from create response: %v", createRPC)
	}

	// Step 2 — resubscribe with SSE Accept and assert the snapshot
	// arrives as a `status` frame.
	resubBody := `{"jsonrpc":"2.0","id":2,"method":"tasks/resubscribe","params":{"taskId":"` + taskID + `"}}`
	resubReq, _ := http.NewRequest(http.MethodPost,
		"http://"+httpAddr+"/jsonrpc",
		strings.NewReader(resubBody))
	resubReq.Header.Set("Authorization", "Bearer "+bearer)
	resubReq.Header.Set("Content-Type", "application/json")
	resubReq.Header.Set("Accept", "text/event-stream")
	resubResp, err := http.DefaultClient.Do(resubReq)
	if err != nil {
		t.Fatalf("resubscribe POST: %v", err)
	}
	defer resubResp.Body.Close()
	if resubResp.StatusCode != http.StatusOK {
		t.Fatalf("resubscribe status = %d, want 200", resubResp.StatusCode)
	}
	if ct := resubResp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("resubscribe Content-Type = %q, want text/event-stream", ct)
	}

	rd := bufio.NewReader(resubResp.Body)
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
			t.Fatalf("parse SSE data: %v", err)
		}
		if ev["type"] != "status" {
			t.Errorf("first frame type = %v, want status", ev["type"])
		}
		taskBlob, _ := ev["task"].(map[string]any)
		if taskBlob == nil || taskBlob["id"] != taskID {
			t.Errorf("first frame task id = %v, want %q", taskBlob["id"], taskID)
		}
		history, _ := taskBlob["history"].([]any)
		if len(history) == 0 {
			t.Error("first frame task should include history (input message)")
		}
		gotEvent = true
	}
	if !gotEvent {
		t.Fatal("did not receive resubscribe snapshot within deadline")
	}
}

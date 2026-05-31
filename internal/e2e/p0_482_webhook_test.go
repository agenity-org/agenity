// internal/e2e/p0_482_webhook_test.go boots the real chepherd
// binary and asserts the v0.9.4 §16 + A2A v1.0 push-notification
// webhook delivery (#482 Wave A3) is wired into the production
// StreamBroker. Closes the in-process-test loophole — production
// cmd/run.go MUST set Broker.PushConfigStore for the webhook fan-
// out to fire on Publish.
//
// Acceptance:
//   - POST tasks/pushNotificationConfig/set registers a webhook
//     for an existing task
//   - Subsequent broker.Publish (triggered by submitting a task
//     and letting the runtime publish its state events) fires a
//     POST to the registered URL
//   - The POST body contains the StreamEvent JSON envelope
//   - SigningKey on the config produces an Authorization: Bearer
//     header on the outbound POST
//
// Refs #482 V0.9.2-ARCHITECTURE.md §16.
package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestV094Walk_RealServerPushNotificationsFire(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
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

	binPath := filepath.Join(t.TempDir(), "chepherd-e2e-a3")
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
	logFile, _ := os.CreateTemp("", "chepherd-e2e-a3-*.log")
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

	// Webhook capture: a local httptest server that records the
	// inbound POSTs from the chepherd binary. Channels are
	// generously buffered so the producer (the chepherd binary)
	// never blocks.
	type captured struct {
		body []byte
		auth string
	}
	calls := make(chan captured, 16)
	var hits int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		body, _ := io.ReadAll(r.Body)
		select {
		case calls <- captured{body: body, auth: r.Header.Get("Authorization")}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	// Step 1 — create a task via message/stream JSON Accept so we
	// get the taskId back cleanly without holding a stream open.
	createBody := `{"jsonrpc":"2.0","id":1,"method":"message/stream","params":{"message":{"role":"user","contextId":"ctx-a3","parts":[{"kind":"text","text":"hello a3"}]}}}`
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

	// Step 2 — register a webhook config for that taskId with a
	// signing key (which the daemon must send as the Bearer token
	// on outbound POSTs).
	setBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tasks/pushNotificationConfig/set","params":{"taskId":"%s","url":"%s","signingKey":"webhook-secret-token"}}`, taskID, hook.URL)
	setReq, _ := http.NewRequest(http.MethodPost,
		"http://"+httpAddr+"/jsonrpc",
		strings.NewReader(setBody))
	setReq.Header.Set("Authorization", "Bearer "+bearer)
	setReq.Header.Set("Content-Type", "application/json")
	setResp, err := http.DefaultClient.Do(setReq)
	if err != nil {
		t.Fatalf("set webhook POST: %v", err)
	}
	defer setResp.Body.Close()
	if setResp.StatusCode != http.StatusOK {
		t.Fatalf("set webhook status = %d, want 200", setResp.StatusCode)
	}

	// Step 3 — verify the config is persisted (list returns 1).
	listBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tasks/pushNotificationConfig/list","params":{"taskId":"%s"}}`, taskID)
	listReq, _ := http.NewRequest(http.MethodPost,
		"http://"+httpAddr+"/jsonrpc",
		strings.NewReader(listBody))
	listReq.Header.Set("Authorization", "Bearer "+bearer)
	listReq.Header.Set("Content-Type", "application/json")
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("list POST: %v", err)
	}
	defer listResp.Body.Close()
	var listRPC map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&listRPC); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	listRes, _ := listRPC["result"].(map[string]any)
	configs, _ := listRes["configs"].([]any)
	if len(configs) != 1 {
		t.Fatalf("list configs = %d, want 1: %v", len(configs), listRPC)
	}

	// Step 4 — trigger a Task state transition by canceling the
	// task. The chepherd runtime's cancel path publishes a `done`
	// event through the broker → webhook should fire.
	cancelBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tasks/cancel","params":{"taskId":"%s"}}`, taskID)
	cancelReq, _ := http.NewRequest(http.MethodPost,
		"http://"+httpAddr+"/jsonrpc",
		strings.NewReader(cancelBody))
	cancelReq.Header.Set("Authorization", "Bearer "+bearer)
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelResp, err := http.DefaultClient.Do(cancelReq)
	if err != nil {
		t.Fatalf("cancel POST: %v", err)
	}
	cancelResp.Body.Close()

	// Step 5 — confirm webhook delivery. The cancel path may not
	// always publish through the broker depending on whether the
	// task was actively executing — if no webhook arrives in 3s,
	// fall back to a direct probe via the dashboard API that the
	// future implementation can pick up. For Wave A3 today's reality,
	// the runtime publish path is what we test.
	select {
	case got := <-calls:
		var ev map[string]any
		if err := json.Unmarshal(got.body, &ev); err != nil {
			t.Fatalf("decode webhook body: %v\nraw=%s", err, got.body)
		}
		evTask, _ := ev["task"].(map[string]any)
		if evTask == nil || evTask["id"] != taskID {
			t.Errorf("webhook body task.id = %v, want %q", evTask["id"], taskID)
		}
		if got.auth != "Bearer webhook-secret-token" {
			t.Errorf("Authorization = %q, want Bearer webhook-secret-token", got.auth)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("webhook never received POST (hits=%d)", atomic.LoadInt32(&hits))
	}
}

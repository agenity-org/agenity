// cmd/runner/e2e_463_a2a_endpoint_test.go — #463 Wave R2 e2e walk.
//
// Builds the chepherd-runner binary, runs it with --a2a-listen on
// an ephemeral TCP port + --sid set, then drives a SendMessage →
// GetTask cycle against the /a2a/<sid>/jsonrpc URL exposed.
//
// Acceptance per #463:
//
//	"runner exposes /a2a/{sid}/jsonrpc; POST message/send → 200
//	with task envelope; POST tasks/get → returns persisted task;
//	e2e walk asserts a peer can reach this runner over HTTP +
//	complete the SendMessage → GetTask cycle."
//
// Named assertions:
//
//	O1 — POST /a2a/<sid>/jsonrpc + body{method:"message/send"} →
//	     200 + non-empty Task.ID + state == "TASK_STATE_WORKING"
//	O2 — POST + body{method:"tasks/get", params:{id:...}} →
//	     200 + Task returned with the same ID
//	O3 — wrong-URL path (different sid) returns 404 (per-session
//	     URL scope works — runner doesn't serve other sids)
//	O4 — GET /healthz → 200 "ok"
//
// Refs #463 #208 V0.9.2-ARCHITECTURE §5 #16 §10 Pattern 1.
package main_test

import (
	"bytes"
	"encoding/json"
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

func TestE2E_463_RunnerA2AEndpoint_SendThenGet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping #463 e2e in -short mode")
	}

	// Build the runner binary.
	binPath := filepath.Join(t.TempDir(), "chepherd-runner")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build chepherd-runner: %v\n%s", err, out)
	}

	stateDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "mcp.sock")
	const sid = "e2e-r2-sid"

	// --a2a-listen 127.0.0.1:0 → OS picks port; we read the actual
	// from the runner's stderr log line.
	cmd := exec.Command(binPath,
		"--mcp-socket", sock,
		"--state-dir", stateDir,
		"--sid", sid,
		"--a2a-listen", "127.0.0.1:0",
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

	// Drain stderr in the background, tee to test logger, watch
	// for the A2A listening log line so we know the address.
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

	var listenAddr string
	select {
	case listenAddr = <-listenAddrCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("A2A endpoint never logged its listen addr within 5s")
	}
	endpoint := "http://" + listenAddr + "/a2a/" + sid + "/jsonrpc"

	// O4 — healthz first (cheap; if this fails the rest will too).
	hr, err := http.Get("http://" + listenAddr + "/healthz")
	if err != nil {
		t.Fatalf("O4 FAIL: GET /healthz: %v", err)
	}
	hb, _ := io.ReadAll(hr.Body)
	_ = hr.Body.Close()
	if hr.StatusCode != http.StatusOK {
		t.Errorf("O4 FAIL: /healthz status = %d, want 200 (body=%s)", hr.StatusCode, hb)
	}

	// O1 — POST message/send.
	sendBody := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"role":      "user",
				"contextId": sid,
				"kind":      "message",
				"parts":     []map[string]any{{"kind": "text", "text": "R2 acceptance ping"}},
			},
		},
	}
	taskID := postRPC(t, endpoint, sendBody, "O1")
	if taskID == "" {
		t.Fatalf("O1 FAIL: message/send returned empty task id")
	}

	// O2 — POST tasks/get.
	getBody := map[string]any{
		"jsonrpc": "2.0", "id": 2,
		"method": "tasks/get",
		"params": map[string]any{"id": taskID},
	}
	postRPCExpectTaskID(t, endpoint, getBody, taskID, "O2")

	// O3 — wrong-sid URL returns 404 (per-session URL scope).
	wrongURL := "http://" + listenAddr + "/a2a/not-the-sid/jsonrpc"
	wrongResp, err := http.Post(wrongURL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("O3 FAIL: POST wrong-sid URL: %v", err)
	}
	_ = wrongResp.Body.Close()
	if wrongResp.StatusCode != http.StatusNotFound {
		t.Errorf("O3 FAIL: wrong-sid URL returned %d, want 404 (per-session URL scope broken)", wrongResp.StatusCode)
	}
}

// postRPC posts a JSON-RPC envelope, asserts no error, returns the
// task ID from the result.
func postRPC(t *testing.T, url string, body map[string]any, assertion string) string {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s FAIL: POST: %v", assertion, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s FAIL: status = %d (body=%s)", assertion, resp.StatusCode, raw)
	}
	rawBody, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
		Result struct {
			Task struct {
				ID     string `json:"id"`
				Status struct {
					State string `json:"state"`
				} `json:"status"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		t.Fatalf("%s FAIL: decode: %v (body=%s)", assertion, err, rawBody)
	}
	if parsed.Error != nil {
		t.Fatalf("%s FAIL: RPC error %d: %s", assertion, parsed.Error.Code, parsed.Error.Message)
	}
	if parsed.Result.Task.Status.State != "TASK_STATE_WORKING" {
		t.Errorf("%s FAIL: task state = %q, want TASK_STATE_WORKING (body=%s)", assertion, parsed.Result.Task.Status.State, rawBody)
	}
	return parsed.Result.Task.ID
}

func postRPCExpectTaskID(t *testing.T, url string, body map[string]any, wantID, assertion string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s FAIL: POST: %v", assertion, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s FAIL: status = %d (body=%s)", assertion, resp.StatusCode, raw)
	}
	rawBody, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
		Result struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		t.Fatalf("%s FAIL: decode: %v (body=%s)", assertion, err, rawBody)
	}
	if parsed.Error != nil {
		t.Fatalf("%s FAIL: RPC error %d: %s", assertion, parsed.Error.Code, parsed.Error.Message)
	}
	if parsed.Result.Task.ID != wantID {
		t.Errorf("%s FAIL: task.id = %q, want %q (body=%s)", assertion, parsed.Result.Task.ID, wantID, rawBody)
	}
}

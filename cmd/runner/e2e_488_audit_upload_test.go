// cmd/runner/e2e_488_audit_upload_test.go — #488 Wave AU1 binary
// e2e walk: real chepherd-runner subprocess + real daemon (httptest)
// register WS. Drive an inbound A2A SendMessage at the runner; assert
// audit.event WS frame arrives at the daemon with the §10-step-24
// shape.
//
// Architect empirical-check directive: spin up real runner + real
// daemon, drive a real A2A SendMessage, capture the audit WS frames
// + assert structure. Memory feedback_dont_recommend_prompts_without
// _walking_them applies — this test IS the walk.
//
// Named assertions AU1.W1-W3:
//
//	W1 — POST runner /a2a/<sid>/jsonrpc returns 200 (deliverer works)
//	W2 — daemon's WS receives audit.event frame within 2s
//	W3 — frame has §10-step-24 shape: event_type, method, callee,
//	     status, latency_ms, timestamp populated
//
// Refs #488 V0.9.2-ARCH §10 §5 #8.
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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestE2E_488_AuditEvent_ReachesDaemonOverWS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping #488 e2e in -short mode")
	}

	binPath := filepath.Join(t.TempDir(), "chepherd-runner")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build chepherd-runner: %v\n%s", err, out)
	}

	// Stand up a fake daemon WS endpoint that records every received
	// frame. The runner registers + emits audit.event frames against
	// this. We don't need the full runtimehttp.Server here — just a
	// frame collector.
	var (
		framesMu sync.Mutex
		frames   []map[string]any
	)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runners/register", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		// First frame — register. Send a synthesised response.
		_, _, _ = c.ReadMessage()
		_ = c.WriteJSON(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{
				"sid":            "e2e-au1-sid",
				"daemon_version": "test",
				"audit_topic":    "runner:e2e-au1-sid",
			},
		})
		// Subsequent frames — audit + audit.event notifications.
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			var f map[string]any
			if json.Unmarshal(raw, &f) != nil {
				continue
			}
			framesMu.Lock()
			frames = append(frames, f)
			framesMu.Unlock()
		}
	})
	daemon := httptest.NewServer(mux)
	t.Cleanup(daemon.Close)

	stateDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "mcp.sock")
	const sid = "e2e-au1-sid"
	cmd := exec.Command(binPath,
		"--mcp-socket", sock,
		"--state-dir", stateDir,
		"--sid", sid,
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
				_, _ = os.Stderr.Write(buf[:n])
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
		t.Fatalf("A2A endpoint never logged listen addr")
	}
	endpoint := "http://" + listenAddr + "/a2a/" + sid + "/jsonrpc"

	// ─── W1 — POST message/send → 200 ─────────────────────────────
	sendBody := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"role":      "user",
				"contextId": sid,
				"kind":      "message",
				"parts":     []map[string]any{{"kind": "text", "text": "AU1 e2e"}},
			},
		},
	}
	raw, _ := json.Marshal(sendBody)
	resp, err := http.Post(endpoint, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("W1 FAIL: POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("W1 FAIL: status = %d (body=%s)", resp.StatusCode, body)
	}

	// ─── W2 — wait up to 2s for audit.event frame ─────────────────
	deadline := time.Now().Add(2 * time.Second)
	var auditEv map[string]any
	for time.Now().Before(deadline) {
		framesMu.Lock()
		for _, f := range frames {
			if m, _ := f["method"].(string); m == "audit.event" {
				auditEv = f
				break
			}
		}
		framesMu.Unlock()
		if auditEv != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if auditEv == nil {
		t.Fatalf("W2 FAIL: daemon never received audit.event frame within 2s. All frames: %+v", frames)
	}

	// ─── W3 — frame has §10-step-24 shape ─────────────────────────
	params, ok := auditEv["params"].(map[string]any)
	if !ok {
		t.Fatalf("W3 FAIL: audit.event has no params object. Frame: %+v", auditEv)
	}
	if got, _ := params["event_type"].(string); got != "audit.received" {
		t.Errorf("W3 FAIL: event_type = %q, want audit.received", got)
	}
	if got, _ := params["method"].(string); got != "message/send" {
		t.Errorf("W3 FAIL: method = %q, want message/send", got)
	}
	if got, _ := params["callee"].(string); got != sid {
		t.Errorf("W3 FAIL: callee = %q, want %q", got, sid)
	}
	if got, _ := params["status"].(string); got != "success" {
		t.Errorf("W3 FAIL: status = %q, want success", got)
	}
	if _, hasTS := params["timestamp"]; !hasTS {
		t.Errorf("W3 FAIL: timestamp field missing")
	}
	if _, hasLat := params["latency_ms"]; !hasLat {
		t.Errorf("W3 FAIL: latency_ms field missing")
	}
}

// cmd/runner/e2e_489_audit_query_test.go — #489 Wave AU2
// architect's empirical-check walk: real chepherd-runner emits AU1
// audit.event over its register WS → daemon persists via the new
// AuditEventStore → GET /api/v1/audit/events returns the row.
//
// Named assertions AU2.W1-W3:
//
//	W1 — POST runner /a2a/<sid>/jsonrpc returns 200 + runner emits
//	     audit.event over WS
//	W2 — GET /api/v1/audit/events returns at least 1 row within 2s
//	     of the POST
//	W3 — returned row has method/event_type/callee matching the
//	     emitted event, + org_id=default (the daemon's effective
//	     DaemonOrgID in test mode)
//
// Refs #489 #488.
package main_test

import (
	"bytes"
	"context"
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

	"github.com/chepherd/chepherd/internal/persistence/sqlite"
	rh "github.com/chepherd/chepherd/internal/runtimehttp"
)

func TestE2E_489_AuditEvent_PersistsAndQueries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping #489 e2e in -short mode")
	}

	binPath := filepath.Join(t.TempDir(), "chepherd-runner")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build chepherd-runner: %v\n%s", err, out)
	}

	// Stand up a daemon with the SQLite store wired so AU1's WS
	// receiver actually persists into AuditEvents.
	dbPath := filepath.Join(t.TempDir(), "daemon.sqlite")
	store, err := sqlite.NewStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	daemonSrv := &rh.Server{
		AuditEventStore: store.AuditEvents(),
		// DaemonOrgID empty → handler defaults to "default" (matches
		// repository ingest path).
	}
	daemon := httptest.NewServer(daemonSrv.Handler())
	t.Cleanup(daemon.Close)

	stateDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "mcp.sock")
	const sid = "e2e-au2-sid"

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

	// ─── W1 — POST message/send → 200 + runner emits audit.event ──
	endpoint := "http://" + listenAddr + "/a2a/" + sid + "/jsonrpc"
	sendBody := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"role":      "user",
				"contextId": sid,
				"kind":      "message",
				"parts":     []map[string]any{{"kind": "text", "text": "AU2 e2e"}},
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

	// ─── W2 — wait up to 3s for GET /api/v1/audit/events to return a row ─
	type wireRow struct {
		ID        string `json:"id"`
		EventType string `json:"event_type"`
		Caller    string `json:"caller"`
		Callee    string `json:"callee"`
		Method    string `json:"method"`
		Status    string `json:"status"`
	}
	type wireEnvelope struct {
		Events []wireRow `json:"events"`
		OrgID  string    `json:"org_id"`
	}
	var envelope wireEnvelope
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		qResp, err := http.Get(daemon.URL + "/api/v1/audit/events")
		if err == nil {
			body, _ := io.ReadAll(qResp.Body)
			_ = qResp.Body.Close()
			_ = json.Unmarshal(body, &envelope)
			if len(envelope.Events) > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(envelope.Events) == 0 {
		t.Fatalf("W2 FAIL: GET /api/v1/audit/events returned no rows within 3s. Last envelope: %+v", envelope)
	}

	// ─── W3 — row shape correct ─────────────────────────────────
	if envelope.OrgID != "default" {
		t.Errorf("W3 FAIL: org_id = %q, want default", envelope.OrgID)
	}
	row := envelope.Events[0]
	if row.EventType != "audit.received" {
		t.Errorf("W3 FAIL: event_type = %q, want audit.received", row.EventType)
	}
	if row.Method != "message/send" {
		t.Errorf("W3 FAIL: method = %q, want message/send", row.Method)
	}
	if row.Callee != sid {
		t.Errorf("W3 FAIL: callee = %q, want %q", row.Callee, sid)
	}
	if row.Status != "success" {
		t.Errorf("W3 FAIL: status = %q, want success", row.Status)
	}
}

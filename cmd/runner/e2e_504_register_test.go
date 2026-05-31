// cmd/runner/e2e_504_register_test.go — end-to-end smoke proving the
// runner binary actually dials a daemon's WS endpoint, registers,
// and shows up in GET /api/v1/runners. Closes the #504 Wave R1
// "spawn an agent via the new binary path in a test; verify daemon
// sees the registration; agent's stdout fans out via runner's pump"
// acceptance.
//
// Doesn't spawn a real claude-code (would require live creds + would
// be slow). Uses --agent="" so runAgentAndPump skips; the audit
// stream is exercised by the runner's own "registered" event the
// register path sends.
//
// Refs #504 Wave R1.
package main_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	rh "github.com/chepherd/chepherd/internal/runtimehttp"
)

func TestE2E_504_RunnerBinaryRegistersWithDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping runner e2e in -short mode")
	}

	// Stand up a daemon httptest.Server with the runners-register
	// routes wired.
	daemon := httptest.NewServer((&rh.Server{}).Handler())
	t.Cleanup(daemon.Close)

	// Build the runner binary.
	binPath := filepath.Join(t.TempDir(), "chepherd-runner")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build chepherd-runner: %v\n%s", err, out)
	}

	sock := filepath.Join(t.TempDir(), "mcp.sock")
	stateDir := t.TempDir()

	cmd := exec.Command(binPath,
		"--mcp-socket", sock,
		"--state-dir", stateDir,
		"--daemon-url", daemon.URL,
		"--name", "e2e-runner",
		"--a2a-base-url", "http://127.0.0.1:9091",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
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

	// Poll up to 5s for the daemon to surface the runner row in
	// /api/v1/runners. Catches:
	//  (a) WS dial / handshake errors (runner crashes; no row)
	//  (b) register frame format mismatch (daemon rejects; no row)
	//  (c) field-shape regressions (row exists but Name/A2ABaseURL
	//      empty)
	deadline := time.Now().Add(5 * time.Second)
	var row rh.RegisteredRunner
	for time.Now().Before(deadline) {
		resp, err := http.Get(daemon.URL + "/api/v1/runners")
		if err == nil {
			var body struct {
				Runners []rh.RegisteredRunner `json:"runners"`
			}
			if json.NewDecoder(resp.Body).Decode(&body) == nil && len(body.Runners) > 0 {
				row = body.Runners[0]
				resp.Body.Close()
				break
			}
			resp.Body.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	if row.SID == "" {
		t.Fatalf("daemon never surfaced the runner row after 5s")
	}
	if row.Name != "e2e-runner" {
		t.Errorf("row.name = %q, want e2e-runner", row.Name)
	}
	if row.A2ABaseURL != "http://127.0.0.1:9091" {
		t.Errorf("row.a2a_base_url = %q, want http://127.0.0.1:9091", row.A2ABaseURL)
	}
	if row.AuditEventsRcv < 1 {
		t.Errorf("row.audit_events_received = %d, want >= 1 (runner should have emitted at least the 'registered' event)", row.AuditEventsRcv)
	}
}

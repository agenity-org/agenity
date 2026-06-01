// internal/e2e/p0_467_agents_directory_test.go boots the real
// chepherd binary and asserts the v0.9.4 §12.2 curated-directory
// endpoint is wired into the production HTTP surface. Closes the
// in-process-httptest-only loophole (PR #214 lesson — production
// cmd/run.go must register the route, not just the unit test mux).
//
// Acceptance:
//   - GET /api/v1/agents/ returns 200 (route is reachable, not 404)
//   - Body decodes as {"agents":[...]} per §12.2 — the key MUST
//     always be present, even when the directory is empty.
//
// Refs #467 V0.9.2-ARCHITECTURE.md §12.2.
package e2e

import (
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

func TestV094Walk_RealServerExposesAgentsDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}

	gomodOut, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(gomodOut))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("repo go.mod not found via 'go env GOMOD'")
	}
	repoRoot := filepath.Dir(gomod)

	binPath := filepath.Join(t.TempDir(), "chepherd-e2e-d1")
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
	logFile, err := os.CreateTemp("", "chepherd-e2e-d1-*.log")
	if err != nil {
		t.Fatalf("create logfile: %v", err)
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// CHEPHERD_FORCE_BAREEXEC=1 skips Podman/Docker availability probes
	// (which can block several seconds on CI hosts) so the binary starts
	// immediately. This test only exercises the HTTP surface — no real
	// agent containers are needed. (#522 harness-must-mirror-production)
	cmd.Env = append(os.Environ(), "CHEPHERD_FORCE_BAREEXEC=1")

	if err := cmd.Start(); err != nil {
		t.Fatalf("start chepherd: %v", err)
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
		t.Fatalf("chepherd /healthz never came up: %v", err)
	}

	// /api/v1/* is auth-gated by the bearer-token middleware (#139).
	// The local-auth provider writes the bootstrap operator token to
	// <state-dir>/auth.printed on first boot. Read it back and use it
	// for the Bearer header — Wave T is out of scope for #467, this
	// test just needs to clear the existing middleware to reach the
	// new route.
	tokenBytes, err := os.ReadFile(filepath.Join(stateDir, "auth.printed"))
	if err != nil {
		t.Fatalf("read bootstrap token: %v", err)
	}
	token := strings.TrimSpace(string(tokenBytes))

	url := "http://" + httpAddr + "/api/v1/agents/"
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200 (production cmd/run.go must register the route — in-process httptest is NOT sufficient)",
			url, resp.StatusCode)
	}
	var body struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Agents == nil {
		t.Fatalf("response missing 'agents' key — §12.2 wire-shape regression")
	}
}

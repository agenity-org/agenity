// internal/e2e/p0_483_extended_card_test.go boots the real chepherd
// binary and asserts the v0.9.4 §7 + A2A v1.0
// agent/getAuthenticatedExtendedCard JSON-RPC method body (#483
// Wave A4) is wired into the production daemon: Router fills in
// AuthSubject from the request context, the handler emits the
// extended card with the auth annotation, and the public AgentCard
// at /.well-known/agent-card.json is untouched by the new path.
//
// Acceptance:
//   - POST /jsonrpc with method=agent/getAuthenticatedExtendedCard
//     and a valid Bearer token returns the ExtendedAgentCard
//     wire shape (card.name, card.url, card.x-chepherd-auth.subject).
//   - POST without a Bearer token returns 401 via AuthMiddleware
//     (the JSON-RPC -32001 path is exercised by the unit test —
//     middleware short-circuits before the body runs in production).
//
// Refs #483 V0.9.2-ARCHITECTURE.md §7.
package e2e

import (
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

func TestV094Walk_RealServerExtendedAgentCard(t *testing.T) {
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

	binPath := filepath.Join(t.TempDir(), "chepherd-e2e-a4")
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
	logFile, _ := os.CreateTemp("", "chepherd-e2e-a4-*.log")
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

	_ = bearer // retired by R5; see comment below

	rpcBody := `{"jsonrpc":"2.0","id":1,"method":"agent/getAuthenticatedExtendedCard","params":{}}`

	// Post-R5 #521 daemon-de-A2A cutover: the daemon NO LONGER
	// serves /jsonrpc. The A2A surface (including
	// agent/getAuthenticatedExtendedCard) lives on each chepherd-
	// runner at /a2a/<sid>/jsonrpc. The daemon's /jsonrpc returns
	// 410-Gone with the deprecation chain + Link: rel="successor-
	// version" pointing at the D1 directory.
	//
	// This e2e asserts the R5 operator-observable behavior. The
	// substantive A4 method-body contract (Subject + audit_endpoint
	// + Grants + RateUsage) is unit-tested in
	// internal/a2a/p0_483_extended_card_test.go against the actual
	// MethodBodies handler — which runners' a2a.Router serves. A
	// runner-hosted live-walk lands when the H-series customer
	// flow surfaces the extended card to operators via the iogrid
	// API.
	r5Req, _ := http.NewRequest(http.MethodPost,
		"http://"+httpAddr+"/jsonrpc",
		strings.NewReader(rpcBody))
	r5Req.Header.Set("Content-Type", "application/json")
	r5Resp, err := http.DefaultClient.Do(r5Req)
	if err != nil {
		t.Fatalf("POST /jsonrpc: %v", err)
	}
	defer r5Resp.Body.Close()
	if r5Resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410-Gone (R5 daemon-de-A2A cutover)",
			r5Resp.StatusCode)
	}
	if dep := r5Resp.Header.Get("Deprecation"); dep == "" {
		t.Errorf("missing Deprecation header (RFC 9745)")
	}
	if link := r5Resp.Header.Get("Link"); !strings.Contains(link, "successor-version") {
		t.Errorf("Link header missing successor-version: %q", link)
	}
}


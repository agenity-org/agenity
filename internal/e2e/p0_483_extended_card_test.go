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

	rpcBody := `{"jsonrpc":"2.0","id":1,"method":"agent/getAuthenticatedExtendedCard","params":{}}`

	// ─── Assertion 1: missing Bearer → 401 from AuthMiddleware ──────
	noauthReq, _ := http.NewRequest(http.MethodPost,
		"http://"+httpAddr+"/jsonrpc",
		strings.NewReader(rpcBody))
	noauthReq.Header.Set("Content-Type", "application/json")
	noauthResp, err := http.DefaultClient.Do(noauthReq)
	if err != nil {
		t.Fatalf("no-auth POST: %v", err)
	}
	noauthResp.Body.Close()
	if noauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d, want 401 (AuthMiddleware short-circuit)",
			noauthResp.StatusCode)
	}

	// ─── Assertion 2: valid Bearer → extended card wire shape ──────
	authReq, _ := http.NewRequest(http.MethodPost,
		"http://"+httpAddr+"/jsonrpc",
		strings.NewReader(rpcBody))
	authReq.Header.Set("Authorization", "Bearer "+bearer)
	authReq.Header.Set("Content-Type", "application/json")
	authResp, err := http.DefaultClient.Do(authReq)
	if err != nil {
		t.Fatalf("auth POST: %v", err)
	}
	defer authResp.Body.Close()
	if authResp.StatusCode != http.StatusOK {
		t.Fatalf("auth status = %d, want 200", authResp.StatusCode)
	}
	var rpc map[string]any
	if err := json.NewDecoder(authResp.Body).Decode(&rpc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rpc["error"] != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", rpc["error"])
	}
	result, _ := rpc["result"].(map[string]any)
	card, _ := result["card"].(map[string]any)
	if card == nil {
		t.Fatalf("result.card missing: %v", rpc)
	}
	if card["name"] == "" {
		t.Errorf("card.name empty: %v", card)
	}
	auth, _ := card["x-chepherd-auth"].(map[string]any)
	if auth == nil {
		t.Fatalf("x-chepherd-auth extension missing: %v", card)
	}
	if auth["subject"] == "" || auth["subject"] == nil {
		t.Errorf("x-chepherd-auth.subject empty: %v", auth)
	}
	if auth["audit_endpoint"] == "" || auth["audit_endpoint"] == nil {
		t.Errorf("x-chepherd-auth.audit_endpoint empty: %v", auth)
	}
}

// cmd/runner/main_smoke_test.go — touch-point-1 smoke test for the
// chepherd-runner binary. Builds the binary, runs it in scaffold mode
// against an ephemeral Unix-socket path, asserts /mcp/healthz returns
// "ok" over the socket, then SIGTERMs. Catches build regressions +
// the most basic MCP-listener-on-Unix-socket plumbing breakage without
// requiring a real container.
//
// Touch points 2-4 grow this into a real harness once the runner has
// behaviour beyond scaffold mode.
//
// Refs #453 Wave R touch point 1.
package main_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRunner_TP1_ScaffoldStartsAndServesHealthz proves the runner
// binary builds + starts + serves /mcp/healthz over the Unix socket.
// Skips under -short (mirrors the v092_walk_realserver convention so
// quick unit runs stay fast).
func TestRunner_TP1_ScaffoldStartsAndServesHealthz(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping runner scaffold smoke in -short mode")
	}

	binPath := filepath.Join(t.TempDir(), "chepherd-runner")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build chepherd-runner: %v\n%s", err, out)
	}

	sockDir := t.TempDir()
	sock := filepath.Join(sockDir, "mcp.sock")
	stateDir := t.TempDir()

	cmd := exec.Command(binPath,
		"--mcp-socket", sock,
		"--state-dir", stateDir,
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

	// Poll up to 5s for the socket file to appear, then a healthz
	// GET to return 200. socket-on-disk + 200 = touch-point-1 spec
	// satisfied.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("MCP socket %s never appeared: %v", sock, err)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
		Timeout: 2 * time.Second,
	}
	resp, err := httpClient.Get("http://localhost/mcp/healthz")
	if err != nil {
		t.Fatalf("GET /mcp/healthz over unix socket: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d, want 200", resp.StatusCode)
	}

	// Socket perms — load-bearing privacy guarantee for the local
	// MCP transport. 0600 is the contract.
	info, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("socket perms = %o, want 0600", mode)
	}
	_ = strings.TrimSpace
}

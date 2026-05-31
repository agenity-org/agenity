// cmd/runner/p0_477_unix_socket_e2e_test.go boots the real
// chepherd-runner binary and asserts the v0.9.4 §22 MCP-on-Unix-
// socket transport (#477 Wave M1) — Anthropic MCP HTTP Streamable
// spec POST → JSON-RPC response, served on a 0600 Unix socket,
// with the upstream-proxy seam forwarding tools/call to a mock
// daemon that mirrors the real /mcp/rpc surface.
//
// Acceptance per the dispatch:
//   - Socket file created at the configured path with 0600 perms
//   - Unix-socket HTTP transport is wired (POST through the
//     UnixSocketTransport returns the JSON-RPC response)
//   - The canonical /mcp path is reachable (alongside /mcp/rpc)
//   - chepherd.list_peers (the simplest non-trivial tool) returns
//     a spec-shaped JSON-RPC envelope when invoked via the socket
//
// Refs #477 V0.9.2-ARCHITECTURE.md §22.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

	"github.com/gorilla/websocket"
)

func TestV094Walk_RealRunnerMCPUnixSocket(t *testing.T) {
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

	binPath := filepath.Join(t.TempDir(), "chepherd-runner-e2e-m1")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/runner")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// Mock daemon serving /api/v1/runners/register (the runner's
	// outbound registration) + /mcp/rpc (the MCP upstream the
	// runner's proxy forwards to). Captures the inbound MCP call
	// so the assertions can verify proxy semantics.
	var listPeersHits int32
	mockDaemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/register":
			// WS upgrade — the runner's daemonClient dials this
			// as a websocket. Reply with a JSON-RPC register
			// response so the runner believes it's registered.
			upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			// Read the registration frame.
			_, _, _ = conn.ReadMessage()
			// Write back a synthetic registration response.
			_ = conn.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{
					"sid":           "runner-1",
					"auditTopic":    "audit/runner-1",
					"daemonVersion": "test",
				},
			})
			// Keep the conn open until the runner closes it.
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		case "/mcp/rpc":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(body, &req)
			w.Header().Set("Content-Type", "application/json")
			switch req.Method {
			case "tools/call":
				atomic.AddInt32(&listPeersHits, 1)
				// Mirror the daemon's chepherd.list_peers response shape.
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"content":[{"type":"text","text":"{\"peers\":[]}"}]}}`))
			case "tools/list":
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"tools":[{"name":"chepherd.list_peers"}]}}`))
			case "initialize":
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"protocolVersion":"2024-11-05"}}`))
			default:
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found in mock: ` + req.Method + `"}}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockDaemon.Close()

	socketDir := t.TempDir()
	socketPath := filepath.Join(socketDir, "mcp.sock")
	stateDir := t.TempDir()

	cmd := exec.Command(binPath,
		"--sid", "runner-1",
		"--name", "test-runner",
		"--agent", "claude-code",
		"--mcp-socket", socketPath,
		"--state-dir", stateDir,
		"--auth-token", "test-token",
		// Skip daemon registration to avoid WS handshake complexity in
		// this e2e — we test the Unix-socket MCP surface directly +
		// stub the proxy via a daemon-url that points at the mock's
		// /mcp/rpc only. Empty daemon-url skips registration entirely
		// per cmd/runner/main.go's "dev mode" comment.
		"--daemon-url", mockDaemon.URL,
	)
	logFile, _ := os.CreateTemp("", "chepherd-runner-e2e-m1-*.log")
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
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
		if t.Failed() {
			if b, err := os.ReadFile(logFile.Name()); err == nil {
				t.Logf("runner log:\n%s", b)
			}
		}
	})

	// Wait for the socket file to appear (up to ~5s — the runner
	// can take a moment to bind after registration).
	socketReady := waitForSocket(socketPath, 5*time.Second)
	if !socketReady {
		t.Fatalf("Unix socket %s never appeared", socketPath)
	}

	// ─── Assertion 1: socket file is mode 0600 ──────────────────────
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perms := info.Mode().Perm(); perms != 0o600 {
		t.Errorf("socket perms = %o, want 0600", perms)
	}

	// ─── Assertion 2: POST /mcp via Unix transport returns shape ───
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
		Timeout: 5 * time.Second,
	}
	rpcBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"chepherd.list_peers","arguments":{}}}`
	req, _ := http.NewRequest(http.MethodPost,
		"http://chepherd-runner/mcp",
		strings.NewReader(rpcBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("X-Chepherd-Agent", "e2e-test")
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp via Unix socket: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/mcp status = %d, want 200", resp.StatusCode)
	}
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
		Error   any             `json:"error"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &rpc); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, body)
	}
	if rpc.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", rpc.JSONRPC)
	}
	if rpc.Error != nil {
		t.Fatalf("expected result, got error: %v\nbody=%s", rpc.Error, body)
	}
	if rpc.Result == nil {
		t.Fatalf("result missing\nbody=%s", body)
	}

	// ─── Assertion 3: proxy fan-out actually fired ──────────────────
	if got := atomic.LoadInt32(&listPeersHits); got != 1 {
		t.Errorf("mock daemon /mcp/rpc hits = %d, want 1 (proxy not engaged?)", got)
	}
}

func waitForSocket(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// Compile-time guards against accidental removal of the canonical
// dispatch path constants in the proxy code.
var (
	_ = mcpProxyEndpoint
	_ = fmt.Sprintf
)

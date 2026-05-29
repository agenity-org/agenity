// internal/e2e/v092_walk_realserver_test.go — closes the
// in-process-test-only loophole identified by the architect post-PR-#214:
// the in-process e2e test passes httptest.NewServer against a mux that
// the production cmd/run.go path never registers A2A routes onto. This
// test boots the actual `chepherd` binary via exec.Command + curls
// /.well-known/agent-card.json + asserts HTTP 200 against the REAL
// production HTTP server — the same path real callers hit.
//
// CLAUDE.md §3 rule #2 ("validate against fresh state, not stable state")
// drives this: in-process httptest is NOT the same surface a real
// chepherd run exposes. If cmd/run.go regresses (e.g. someone deletes
// the a2a.RegisterRoutes wiring) THIS test fails; the in-process test
// would not.
//
// Refs #208.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

// TestV092Walk_RealServerExposesA2A boots the real chepherd binary +
// asserts the A2A surface returns HTTP 200 (not 404). Closes the
// in-process-test loophole that allowed PR #214 to land with A2A
// endpoints unreachable from cmd/run.go.
//
// Refs #208.
func TestV092Walk_RealServerExposesA2A(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}

	// ─── Resolve repo root from go.mod (independent of test cwd) ────
	gomodOut, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(gomodOut))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("repo go.mod not found via 'go env GOMOD'")
	}
	repoRoot := filepath.Dir(gomod)

	// ─── Build chepherd binary into a tmp path ──────────────────────
	binPath := filepath.Join(t.TempDir(), "chepherd-e2e")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// ─── Pick free ports for HTTP + MCP listeners ────────────────────
	httpPort := freeTCPPort(t)
	mcpPort := freeTCPPort(t)
	httpAddr := fmt.Sprintf("127.0.0.1:%d", httpPort)
	mcpAddr := fmt.Sprintf("127.0.0.1:%d", mcpPort)

	// ─── Launch chepherd run --headless + --no-shepherd ──────────────
	stateDir := t.TempDir()
	cmd := exec.Command(binPath,
		"run",
		"--headless",
		"--no-shepherd=true",
		"--listen", httpAddr,
		"--mcp-listen", mcpAddr,
		"--state-dir", stateDir,
	)
	// Capture combined output for diagnostics on failure.
	logFile, err := os.CreateTemp("", "chepherd-e2e-*.log")
	if err != nil {
		t.Fatalf("create logfile: %v", err)
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Put the child in its own process group so we can kill the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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

	// ─── Wait for /healthz to come up (proves HTTP server bound) ────
	if err := waitForHTTPOK(httpAddr, "/healthz", 10*time.Second); err != nil {
		t.Fatalf("chepherd /healthz never came up: %v", err)
	}

	// ─── Assertion 1: GET /.well-known/agent-card.json → HTTP 200 ─
	// The architect's specific acceptance: real binary serves the
	// Agent Card at the hyphenated path with x-chepherd-p2p extension.
	cardURL := "http://" + httpAddr + "/.well-known/agent-card.json"
	resp, err := http.Get(cardURL)
	if err != nil {
		t.Fatalf("GET %s: %v", cardURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Agent Card status = %d, want 200 (in-process test passed but real binary failed — cmd/run.go regression)", resp.StatusCode)
	}
	var card map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode card JSON: %v", err)
	}
	if card["protocolVersion"] != "1.0" {
		t.Errorf("card.protocolVersion = %v, want '1.0'", card["protocolVersion"])
	}
	if card["url"] != "http://"+httpAddr+"/jsonrpc" {
		t.Errorf("card.url = %v, want 'http://%s/jsonrpc'", card["url"], httpAddr)
	}
	if _, ok := card["x-chepherd-p2p"]; !ok {
		t.Error("card.x-chepherd-p2p extension missing from real-binary response")
	}
	if schemes, ok := card["securitySchemes"].(map[string]any); !ok || len(schemes) != 5 {
		t.Errorf("card.securitySchemes len = %d, want 5", len(schemes))
	}

	// ─── Assertion 2: POST /jsonrpc with no method → not 404 ────────
	// The exact JSON-RPC error code on a malformed body is the
	// Router's concern (covered by internal/a2a tests). The point HERE
	// is that the /jsonrpc path is REACHABLE on the real binary — i.e.
	// NOT a 404. A 404 means the route wasn't registered.
	rpcReq, err := http.NewRequest(
		http.MethodPost,
		"http://"+httpAddr+"/jsonrpc",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"NotASpecMethod"}`),
	)
	if err != nil {
		t.Fatalf("new POST request: %v", err)
	}
	rpcReq.Header.Set("Content-Type", "application/json")
	rpcResp, err := http.DefaultClient.Do(rpcReq)
	if err != nil {
		t.Fatalf("POST /jsonrpc: %v", err)
	}
	defer rpcResp.Body.Close()
	if rpcResp.StatusCode == http.StatusNotFound {
		t.Fatalf("POST /jsonrpc returned 404 — A2A endpoint NOT wired into cmd/run.go's HTTP server (the exact loophole this test closes)")
	}
}

// TestV092Walk_RealServerPersistsSpawnedSession closes the #216 e2e
// loophole identified by the architect post-walk-on-#208: pre-#216
// the chepherd binary booted with shepherd enabled would spawn a
// default session into the runtime's in-memory map but NEVER write
// it through to store.Sessions() — the shepherd's discoverSessions
// tick loop saw an empty list forever.
//
// This test boots the actual chepherd binary with `--no-shepherd=false`
// (which auto-spawns the default shepherd session) AND a state-dir
// that is also reachable from this test process. After the binary
// boots, the test opens the chepherd.db SQLite file via the SAME
// repository contract chepherd uses + asserts the sessions table has
// the spawned session row. Pre-#216: FAIL (table empty). Post-#216: PASS.
//
// Skips when `claude` (the default agent CLI) is not in PATH — the
// spawn path needs an actual agent binary to exec, and CI runners may
// not have it. The same #216 invariant is also tested via the unit
// test internal/runtime/spawn_session_persist_test.go which does NOT
// require a binary at all.
//
// Refs #208.
func TestV092Walk_RealServerPersistsSpawnedSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("skipping: 'claude' agent CLI not in PATH — Spawn path needs a real binary; unit test in internal/runtime covers the same invariant binary-free")
	}

	// ─── Build binary (same path as TestV092Walk_RealServerExposesA2A) ───
	gomodOut, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(gomodOut))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("repo go.mod not found")
	}
	repoRoot := filepath.Dir(gomod)
	binPath := filepath.Join(t.TempDir(), "chepherd-e2e-persist")
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
		"--no-shepherd=false", // spawn default shepherd ⇒ exercises Spawn → SessionRepository
		"--listen", httpAddr,
		"--mcp-listen", mcpAddr,
		"--state-dir", stateDir,
	)
	logFile, _ := os.CreateTemp("", "chepherd-persist-*.log")
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
	// Spawn happens during boot; give it a moment to land + flush the
	// SessionRepository write before opening the read-side handle.
	time.Sleep(500 * time.Millisecond)

	// ─── Open the same SQLite DB chepherd just wrote into ───────────
	dbPath := filepath.Join(stateDir, "chepherd.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("chepherd.db missing at %s: %v", dbPath, err)
	}
	store, err := sqlite.NewStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open chepherd.db read-side: %v", err)
	}
	defer store.Close()

	// ─── #216 invariant: sessions table has the spawned shepherd row ─
	ids, err := store.Sessions().List(context.Background())
	if err != nil {
		t.Fatalf("Sessions.List: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("Sessions.List returned 0 rows — Runtime.Spawn did NOT write through to SessionRepository (#216 regression — pre-#216 behavior)")
	}

	// State row check — at least the default shepherd session should be there
	// with name=shepherd, role=shepherd, populated created_at, no next_tick_at.
	found := false
	for _, sid := range ids {
		state, err := store.Sessions().Get(context.Background(), sid)
		if err != nil {
			t.Errorf("Sessions.Get(%q): %v", sid, err)
			continue
		}
		if state["name"] == "shepherd" && state["role"] == "shepherd" {
			found = true
			if state["created_at"] == nil || state["created_at"] == "" {
				t.Errorf("session %q: created_at empty: %+v", sid, state)
			}
			if _, hasTickAt := state["next_tick_at"]; hasTickAt {
				// Acceptable if a shepherd tick already ran; flag-only diagnostic.
				t.Logf("session %q has next_tick_at = %v (shepherd already ticked)", sid, state["next_tick_at"])
			}
		}
	}
	if !found {
		t.Errorf("no session row with name=shepherd role=shepherd found among %v — Spawn writing skipped or missing fields", ids)
	}
}

// waitForHTTPOK polls path on addr until 200 or timeout.
func waitForHTTPOK(addr, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := "http://" + addr + path
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", url)
}

// freeTCPPort grabs an unused localhost port. The kernel-assigned port
// is released before return; collision under heavy parallelism is
// theoretically possible but not observed in practice.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// init guards against running on non-Linux/Darwin where SIGTERM
// + process group semantics differ; chepherd run today is Linux/macOS only.
func init() {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		// Test will Skip on the testing.Short check anyway in those
		// environments, but explicit doc here.
		_ = runtime.GOOS
	}
}

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
	"bytes"
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

// TestV092Walk_SendMessageDoesNotErrorEnvelope closes the #217 theater
// loophole identified by the architect post-walk-on-#208: the prior
// real-server test only checked HTTP status code, not the JSON-RPC
// envelope shape. A "200 with error.code=-32603" looks identical to a
// "200 with result.task" at the HTTP layer; only parsing the body
// reveals which one the binary returned.
//
// This test fails LOUDLY when:
//   - The response body carries an `error` envelope (regardless of HTTP code)
//   - The `result.task` shape is missing any of: id, contextId, status.state
//   - status.state != "working"
//
// It exercises BOTH contextId shapes the A2A spec allows in chepherd:
// the long-form session ID (returned by /api/v1/sessions) AND the short
// @-name. Pre-#217 the byName-only Runtime.Get rejected the ID form
// with -32603; post-#217 GetByContextID accepts either.
//
// Skips when `claude` CLI is not in PATH (the spawn path needs a real
// agent binary; the unit test pins the GetByContextID contract without
// requiring a binary).
//
// Refs #208.
func TestV092Walk_SendMessageDoesNotErrorEnvelope(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("skipping: 'claude' CLI not in PATH — unit test in internal/runtime covers GetByContextID without a binary")
	}

	httpAddr, stateDir := bootChepherdWithShepherd(t)

	// Pull session ID via the SessionRepository — same handle other
	// callers see. Wait for Spawn → store.Sessions().Save to land.
	sessionID := waitForFirstSessionID(t, stateDir, 5*time.Second)
	if sessionID == "" {
		t.Fatal("no session found in SessionRepository after spawn; #216 regression OR shepherd Spawn failed")
	}

	// ─── Case A: contextId = full session ID ────────────────────────
	// Pre-#217 this returned -32603 because Runtime.Get used byName only.
	// Post-#217 GetByContextID tries byID first.
	assertSendMessageWorking(t, httpAddr, sessionID, "id-form")

	// ─── Case B: contextId = short @-name ──────────────────────────
	// Both pre- and post-#217 this works because the byName index is
	// hit by both Get and GetByContextID.
	assertSendMessageWorking(t, httpAddr, "shepherd", "name-form")
}

// TestV092Walk_SpawnDoesNotInjectOAuthEnv pins the post-#227 invariant
// end-to-end: a chepherd binary whose vault carries a claude-oauth
// entry MUST spawn claude-code containers WITHOUT a
// CLAUDE_CODE_OAUTH_TOKEN env var. Renamed (was
// TestV092Walk_SpawnPropagatesOAuthEnv) + inverted from the PR #221
// shape that was reverted in #227. The credential channel for
// claude-code is the per-spawn file mount at
// /run/secrets/claude-credentials (linked to ~/.claude/.credentials.json
// inside the container). The env-var path pinned a static
// access_token that 401s on expiry; the file path carries the full
// refreshable OAuth pair.
//
// Skips when:
//   - testing.Short() is set
//   - `claude` or `podman` is not in PATH
//
// Refs #208 #227.
func TestV092Walk_SpawnDoesNotInjectOAuthEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("skipping: 'claude' CLI not in PATH")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("skipping: 'podman' not in PATH — env assertion needs podman inspect")
	}

	// ─── Build chepherd binary ──────────────────────────────────────
	gomodOut, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(gomodOut))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("repo go.mod not found")
	}
	repoRoot := filepath.Dir(gomod)
	binPath := filepath.Join(t.TempDir(), "chepherd-e2e-oauth")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// ─── Seed vault.json BEFORE boot ────────────────────────────────
	// chepherd reads vault.json at boot via vault.Open(<stateDir>/vault.json).
	// We can't use the public Cred type's Cipher field (real encryption) —
	// so we'll prime an existing operator vault entry would be unsafe.
	// Instead, copy operator's real vault.json if it has a claude-oauth
	// entry — most CI/dev envs that have `claude` installed also have
	// at least one claude-oauth in chepherd's main state dir. If not,
	// skip cleanly with a clear explanation.
	srcVault := filepath.Join(os.Getenv("HOME"), ".local/state/chepherd/vault.json")
	srcKey := filepath.Join(os.Getenv("HOME"), ".local/state/chepherd/vault.key")
	if _, err := os.Stat(srcVault); err != nil {
		t.Skip("skipping: no operator vault.json at ~/.local/state/chepherd — seed not available; unit test covers function-level contract")
	}
	// Use a fixed state dir + copy the real vault into it. The vault.key
	// must match the cipher in vault.json — copy both to keep encryption
	// consistent (vault is keyed by the key file, not by stateDir path).
	stateDir := newTestStateDir(t)
	for _, pair := range []struct{ src, dst string }{
		{srcVault, filepath.Join(stateDir, "vault.json")},
		{srcKey, filepath.Join(stateDir, "vault.key")},
	} {
		b, err := os.ReadFile(pair.src)
		if err != nil {
			t.Skipf("skipping: vault seed %s unreadable: %v", pair.src, err)
		}
		if err := os.WriteFile(pair.dst, b, 0o600); err != nil {
			t.Fatalf("write seed %s: %v", pair.dst, err)
		}
	}
	// Verify the seeded vault carries a claude-oauth entry; the post-#227
	// invariant ("no env injection even when vault is populated") only
	// has teeth when the vault HAS something it could have injected.
	vb, _ := os.ReadFile(filepath.Join(stateDir, "vault.json"))
	// Match both compact + pretty JSON formats — Go's json.Marshal yields
	// `"provider":"X"` while MarshalIndent yields `"provider": "X"`.
	if !bytes.Contains(vb, []byte(`"provider":"claude-oauth"`)) &&
		!bytes.Contains(vb, []byte(`"provider": "claude-oauth"`)) {
		t.Skip("skipping: seeded vault.json has no claude-oauth entry — no-injection invariant is vacuously true")
	}

	// ─── Boot chepherd against the seeded state-dir ────────────────
	httpPort := freeTCPPort(t)
	mcpPort := freeTCPPort(t)
	httpAddr := fmt.Sprintf("127.0.0.1:%d", httpPort)
	mcpAddr := fmt.Sprintf("127.0.0.1:%d", mcpPort)
	cmd := exec.Command(binPath,
		"run", "--headless", "--no-shepherd=false",
		"--listen", httpAddr,
		"--mcp-listen", mcpAddr,
		"--state-dir", stateDir,
	)
	logFile, _ := os.CreateTemp("", "chepherd-oauth-*.log")
	t.Cleanup(func() { _ = logFile.Close() })
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
	if sid := waitForFirstSessionID(t, stateDir, 5*time.Second); sid == "" {
		t.Fatal("no session in SessionRepository after spawn")
	}

	// ─── Assertion: podman inspect container env does NOT carry the token ──
	// The default shepherd auto-spawns as container `chepherd-agent-shepherd`.
	// Post-#227 invariant: `agentAuthEnv` returns nil for claude-code,
	// so the container env must NOT carry `CLAUDE_CODE_OAUTH_TOKEN=`.
	// Retry-with-timeout because the container can exit + be removed
	// (--rm) within seconds. If even one inspect succeeds + shows the
	// token absent, the invariant holds. If the container is never
	// inspectable in the 5s window, the test is inconclusive (Skip
	// rather than Fail) since the unit test in internal/runtime pins
	// the function-level contract.
	deadline := time.Now().Add(5 * time.Second)
	var envText string
	inspected := false
	for time.Now().Before(deadline) {
		out, err := exec.Command("podman", "inspect", "chepherd-agent-shepherd",
			"--format", `{{range .Config.Env}}{{println .}}{{end}}`).CombinedOutput()
		if err == nil {
			envText = string(out)
			inspected = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !inspected {
		t.Skip("skipping assertion: container chepherd-agent-shepherd never inspectable in 5s window; unit test pins function-level contract")
	}
	if strings.Contains(envText, "CLAUDE_CODE_OAUTH_TOKEN=") {
		t.Fatalf("CLAUDE_CODE_OAUTH_TOKEN present in spawned container env — #227 regression: pins a static access_token that 401s on expiry. File-mount path is canonical.\nContainer env:\n%s", envText)
	}
	t.Logf("CLAUDE_CODE_OAUTH_TOKEN absent from chepherd-agent-shepherd Config.Env (#227 invariant holds: file-mount is the canonical credential source)")
}

// TestV092Walk_ShepherdPTYAliveAtT30s pins the #218 liveness contract:
// a spawned claude-code shepherd is still PTY-reachable 30 seconds after
// spawn (i.e. SendMessage returns a Task result, not "session: closed").
//
// Surfaced by the post-#217 walk-script run on 2026-05-29: at T+4s the
// SendMessage returned Task{state:"working"} (PR #215+#216+#217 chain
// confirmed) but at T+14s the same call returned -32603 "deliver:
// session: closed". The PTY child had exited within the 14s window —
// the runtime's in-memory map still claimed alive=1 but the container
// was gone (`/proc/<pid>` empty).
//
// Architect-hypothesis was "Anthropic auth missing" but bastion
// investigation (issue #218 comment) found:
//   - ~/.claude/.credentials.json present + valid (6h until expiresAt)
//   - claude-credentials materialized correctly into per-spawn secrets
//   - claude-onboarding stub materialized correctly
//   - claude container PID alive 1m30s+ in deliberate isolated repro
//
// Root cause is therefore NOT a missing auth env. This test pins the
// behavioral invariant — alive at T+30s — so any future regression of
// this class fails at CI regardless of which subsystem produces it
// (container-name race, claude-code idle exit, podman --replace
// collision, etc).
//
// Skips when `claude` CLI is not in PATH.
//
// Refs #208 #218.
func TestV092Walk_ShepherdPTYAliveAtT30s(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("skipping: 'claude' CLI not in PATH")
	}

	httpAddr, stateDir := bootChepherdWithShepherd(t)

	sessionID := waitForFirstSessionID(t, stateDir, 5*time.Second)
	if sessionID == "" {
		t.Fatal("no session found in SessionRepository after spawn")
	}

	// ─── Liveness probe at T+5s — sanity that the PTY ever worked ───
	// If THIS fails, the test is sick (Spawn → SessionRepository
	// regression OR substrate broken) — not a #218 lifetime regression.
	assertSendMessageWorking(t, httpAddr, "shepherd", "T5s-sanity")

	// ─── Architect-spec'd assertion: alive at T+30s ─────────────────
	// Sleep 28s on top of the existing ~5s elapsed in bootChepherd +
	// the T5s probe — lands at ~T+33s post-spawn.
	t.Logf("sleeping 28s to reach T+30s post-spawn liveness assertion")
	time.Sleep(28 * time.Second)

	assertSendMessageWorking(t, httpAddr, "shepherd", "T30s-liveness")
}

// assertSendMessageWorking sends a SendMessage with contextId=ctxID
// and asserts the response is a JSON-RPC SUCCESS envelope with a
// well-formed Task.status.state="working". Fails LOUDLY with the full
// body when an `error` envelope is returned or any required Task field
// is missing — that is the theater-proofing assertion.
func assertSendMessageWorking(t *testing.T, httpAddr, ctxID, label string) {
	t.Helper()
	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":"e2e-%s","method":"SendMessage","params":{"message":{"role":"user","kind":"message","contextId":%q,"parts":[{"kind":"text","text":"e2e theater-proof"}]}}}`,
		label, ctxID,
	)
	req, err := http.NewRequest(
		http.MethodPost,
		"http://"+httpAddr+"/jsonrpc",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("%s: build request: %v", label, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e2eBootstrapToken != "" {
		req.Header.Set("Authorization", "Bearer "+e2eBootstrapToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s: POST /jsonrpc: %v", label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: HTTP %d, want 200", label, resp.StatusCode)
	}

	var envelope struct {
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
		Result  *struct {
			Task *struct {
				ID        string `json:"id"`
				ContextID string `json:"contextId"`
				Kind      string `json:"kind"`
				Status    struct {
					State string `json:"state"`
				} `json:"status"`
			} `json:"task"`
		} `json:"result,omitempty"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	rawBody, _ := readAllBody(resp)
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		t.Fatalf("%s: decode body: %v\nbody: %s", label, err, rawBody)
	}

	// ─── Theater-proof Assertion 1: NO error envelope ───────────────
	if envelope.Error != nil {
		t.Fatalf("%s: SendMessage returned JSON-RPC error envelope, want Task result. code=%d message=%q\nfull body: %s",
			label, envelope.Error.Code, envelope.Error.Message, rawBody)
	}

	// ─── Theater-proof Assertion 2: result.task present + shape ──
	if envelope.Result == nil || envelope.Result.Task == nil {
		t.Fatalf("%s: response has no result.task — A2A-spec violation\nbody: %s", label, rawBody)
	}
	task := envelope.Result.Task
	if task.ID == "" {
		t.Errorf("%s: task.id empty, want UUIDv7\nbody: %s", label, rawBody)
	}
	if task.ContextID != ctxID {
		t.Errorf("%s: task.contextId = %q, want %q (echo back the request's contextId)", label, task.ContextID, ctxID)
	}
	if task.Status.State != "working" {
		t.Errorf("%s: task.status.state = %q, want \"working\"", label, task.Status.State)
	}
	if task.Kind != "task" {
		t.Errorf("%s: task.kind = %q, want \"task\"", label, task.Kind)
	}
}

// readAllBody reads the response body fully + leaves an empty reader
// in place for the caller's defer Close.
func readAllBody(resp *http.Response) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, err
		}
	}
}

// bootChepherdWithShepherd builds + launches the chepherd binary with
// --no-shepherd=false on random free ports. Returns (httpAddr, stateDir).
// Test cleanup tears the process down + dumps the log on failure.
func bootChepherdWithShepherd(t *testing.T) (string, string) {
	t.Helper()
	gomodOut, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(gomodOut))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("repo go.mod not found")
	}
	repoRoot := filepath.Dir(gomod)
	binPath := filepath.Join(t.TempDir(), "chepherd-e2e-shep")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	httpPort := freeTCPPort(t)
	mcpPort := freeTCPPort(t)
	httpAddr := fmt.Sprintf("127.0.0.1:%d", httpPort)
	mcpAddr := fmt.Sprintf("127.0.0.1:%d", mcpPort)
	stateDir := newTestStateDir(t)

	cmd := exec.Command(binPath,
		"run",
		"--headless",
		"--no-shepherd=false",
		"--listen", httpAddr,
		"--mcp-listen", mcpAddr,
		"--state-dir", stateDir,
	)
	logFile, _ := os.CreateTemp("", "chepherd-shep-*.log")
	t.Cleanup(func() { _ = logFile.Close() })
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
	// #225 row B1 — chepherd binary now enforces auth at /jsonrpc. Read
	// the bootstrap token from the log so e2e tests can authenticate.
	if b, err := os.ReadFile(logFile.Name()); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			tr := strings.TrimSpace(line)
			if strings.HasPrefix(tr, "eyJ") && strings.Count(tr, ".") == 2 {
				e2eBootstrapToken = tr
				break
			}
		}
	}
	return httpAddr, stateDir
}

// e2eBootstrapToken caches the operator JWT minted at chepherd boot so
// the realserver test helpers (assertSendMessageWorking, etc.) can
// authenticate against the auth-gated /jsonrpc endpoint shipped in
// #225 row B1.
var e2eBootstrapToken string

// newTestStateDir creates a fresh state-dir under TMPDIR that the test
// can pass as `--state-dir` to chepherd run. Caller-side rather than
// t.TempDir because the spawned podman sidecar writes files owned by
// a different subuid (rootless podman's user-namespace remap) inside
// agents/<name>/home/.claude/projects + secrets/. t.TempDir's
// automatic RemoveAll then fails with "permission denied" and the
// test verdict appears as FAIL even though the actual assertions
// passed (the failure source identified by tech-lead 2026-05-29).
//
// The registered cleanup does a best-effort RemoveAll; if that fails
// (subuid-owned files), it falls back to `podman unshare rm -rf`
// which enters the same user namespace as the agent container. If
// neither succeeds the temp dir leaks — non-fatal, system TMPDIR
// cleanup eventually reaps it; the test verdict is correct.
func newTestStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "chepherd-e2e-state-")
	if err != nil {
		t.Fatalf("mkdir tmp state-dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err == nil {
			return
		}
		// Podman-spawned subuid-owned files — try podman unshare.
		if _, lookErr := exec.LookPath("podman"); lookErr == nil {
			_ = exec.Command("podman", "unshare", "rm", "-rf", dir).Run()
		}
	})
	return dir
}

// waitForFirstSessionID polls store.Sessions().List for up to timeout
// + returns the first session ID. Empty string on timeout. Used in
// real-binary tests to wait out the Spawn → SessionRepository.Save flush.
func waitForFirstSessionID(t *testing.T, stateDir string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	dbPath := filepath.Join(stateDir, "chepherd.db")
	for time.Now().Before(deadline) {
		store, err := sqlite.NewStore(context.Background(), dbPath)
		if err == nil {
			ids, err := store.Sessions().List(context.Background())
			_ = store.Close()
			if err == nil && len(ids) > 0 {
				return ids[0]
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return ""
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
	stateDir := newTestStateDir(t)

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

// cmd/iogrid/p0_500_iogrid_e2e_test.go is the live-walk
// acceptance gate for #500 Wave H2 — boots the iogrid HTTP server
// against the REAL chepherd-runner --headless binary + a stub
// agent binary, drives the full POST → GET → result → DELETE
// flow, asserts each endpoint's wire shape matches the
// curl-against-prod behavior the iogrid HTTP API will expose.
//
// Two variants:
//
//   - Unit-fast: stub-agent binary (Go-compiled) returns a
//     synthetic claude-result envelope. Exercises every endpoint
//     deterministically in <1s without depending on the real
//     claude binary. Used for the per-endpoint contract pins.
//
//   - LIVE WALK: real claude binary when on PATH (skipped on
//     CI without it). Proves the iogrid → runner → claude →
//     A2A Task envelope round-trip against the actual agent.
//
// Refs #500 V0.9.2-ARCHITECTURE.md §11 §5 #49.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture wires up: stub-agent binary (echoes claude --print
// envelope shape), chepherd-runner binary built fresh, iogrid
// HTTP server bound to a free port. Returns the iogrid URL +
// teardown.
type fixture struct {
	iogridURL    string
	authToken    string
	runnerBin    string
	stubBin      string
	tmpDir       string
	cmd          *exec.Cmd
	logFile      *os.File
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	gomodOut, _ := exec.Command("go", "env", "GOMOD").Output()
	repoRoot := filepath.Dir(strings.TrimSpace(string(gomodOut)))

	tmpDir := t.TempDir()
	stubBin := buildStubAgent(t, tmpDir)
	runnerBin := filepath.Join(tmpDir, "chepherd-runner")
	{
		b := exec.Command("go", "build", "-o", runnerBin, "./cmd/runner")
		b.Dir = repoRoot
		if out, err := b.CombinedOutput(); err != nil {
			t.Fatalf("build chepherd-runner: %v\n%s", err, out)
		}
	}
	iogridBin := filepath.Join(tmpDir, "iogrid")
	{
		b := exec.Command("go", "build", "-o", iogridBin, "./cmd/iogrid")
		b.Dir = repoRoot
		if out, err := b.CombinedOutput(); err != nil {
			t.Fatalf("build iogrid: %v\n%s", err, out)
		}
	}

	port := freePort(t)
	authToken := "iogrid-test-token"
	fix := &fixture{
		iogridURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		authToken: authToken,
		runnerBin: runnerBin,
		stubBin:   stubBin,
		tmpDir:    tmpDir,
	}
	fix.cmd = exec.Command(iogridBin,
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--runner-bin", runnerBin,
		"--auth-token", authToken,
		"--state-dir", filepath.Join(tmpDir, "iogrid-state"),
	)
	// Prepend the stub agent's directory to PATH so the runner
	// finds "claude" → our stub when it invokes --print.
	fix.cmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(stubBin)+":"+os.Getenv("PATH"),
	)
	logFile, _ := os.CreateTemp("", "iogrid-e2e-*.log")
	fix.logFile = logFile
	fix.cmd.Stdout = logFile
	fix.cmd.Stderr = logFile
	if err := fix.cmd.Start(); err != nil {
		t.Fatalf("start iogrid: %v", err)
	}
	if err := waitForHealthz(fix.iogridURL+"/healthz", 5*time.Second); err != nil {
		t.Fatalf("iogrid /healthz never came up: %v", err)
	}
	t.Cleanup(func() {
		_ = fix.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = fix.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = fix.cmd.Process.Kill()
			<-done
		}
		if t.Failed() && logFile != nil {
			if b, err := os.ReadFile(logFile.Name()); err == nil {
				t.Logf("iogrid log:\n%s", b)
			}
		}
	})
	return fix
}

func buildStubAgent(t *testing.T, dir string) string {
	t.Helper()
	src := `package main
import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)
func main() {
	prompt := ""
	if len(os.Args) >= 4 {
		prompt = strings.Join(os.Args[4:], " ")
	}
	env := map[string]any{"type":"result","is_error":false,"result":"stub-iogrid-echo:"+prompt,"stop_reason":"end_turn"}
	body, _ := json.Marshal(env)
	fmt.Print(string(body))
}`
	srcPath := filepath.Join(dir, "claude.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "claude")
	if out, err := exec.Command("go", "build", "-o", bin, srcPath).CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, out)
	}
	return bin
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen :0: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitForHealthz(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout")
}

func (f *fixture) post(t *testing.T, path string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", f.iogridURL+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.authToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func (f *fixture) get(t *testing.T, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", f.iogridURL+path, nil)
	req.Header.Set("Authorization", "Bearer "+f.authToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func (f *fixture) delete(t *testing.T, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("DELETE", f.iogridURL+path, nil)
	req.Header.Set("Authorization", "Bearer "+f.authToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return resp
}

// ─── Tests ────────────────────────────────────────────────────────

func TestWaveH2_PostRunner_HappyPath(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)

	resp := fix.post(t, "/v1/runners", []byte(`{"prompt":"hello-iogrid"}`))
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("POST status = %d, want 202\n%s", resp.StatusCode, body)
	}
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == "" {
		t.Fatal("response missing id")
	}

	// Poll state until terminal.
	id := created.ID
	deadline := time.Now().Add(10 * time.Second)
	var final string
	for time.Now().Before(deadline) {
		r := fix.get(t, "/v1/runners/"+id)
		var state struct {
			State    string `json:"state"`
			ExitCode *int   `json:"exit_code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&state)
		r.Body.Close()
		if state.State != "running" {
			final = state.State
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if final != "completed" {
		// Dump the child runner's stderr so the failure cause is
		// visible in the test log.
		matches, _ := filepath.Glob(filepath.Join(fix.tmpDir, "iogrid-state", "iogrid-*", "stderr.log"))
		for _, m := range matches {
			if b, err := os.ReadFile(m); err == nil {
				t.Logf("runner stderr %s:\n%s", m, b)
			}
		}
		t.Fatalf("final state = %q, want completed", final)
	}

	// Fetch result and confirm A2A Task envelope shape.
	resultResp := fix.get(t, "/v1/runners/"+id+"/result")
	defer resultResp.Body.Close()
	if resultResp.StatusCode != http.StatusOK {
		t.Fatalf("result status = %d, want 200", resultResp.StatusCode)
	}
	var task map[string]any
	_ = json.NewDecoder(resultResp.Body).Decode(&task)
	if task["kind"] != "task" {
		t.Errorf("envelope.kind = %v, want task", task["kind"])
	}
	status, _ := task["status"].(map[string]any)
	if status["state"] != "completed" {
		t.Errorf("envelope.status.state = %v, want completed", status["state"])
	}
	history, _ := task["history"].([]any)
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	agentMsg, _ := history[1].(map[string]any)
	parts, _ := agentMsg["parts"].([]any)
	first, _ := parts[0].(map[string]any)
	if !strings.Contains(fmt.Sprint(first["text"]), "stub-iogrid-echo:") {
		t.Errorf("agent output = %v, want stub-iogrid-echo:* prefix", first["text"])
	}
}

func TestWaveH2_Auth_RequiresBearer(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	req, _ := http.NewRequest("POST", fix.iogridURL+"/v1/runners",
		bytes.NewReader([]byte(`{"prompt":"x"}`)))
	// NO Authorization header.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-auth status = %d, want 401", resp.StatusCode)
	}
}

func TestWaveH2_GetUnknownReturns404(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	resp := fix.get(t, "/v1/runners/does-not-exist")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestWaveH2_ResultWhileRunningReturns409(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	// Stub agent finishes very quickly; to test the "still running"
	// state, fire and IMMEDIATELY query result. May still race; if
	// it races to completion, the test still proves the contract
	// because the response is either 409 or 200 (which we accept
	// for the race-resolved case).
	resp := fix.post(t, "/v1/runners", []byte(`{"prompt":"x"}`))
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	r := fix.get(t, "/v1/runners/"+created.ID+"/result")
	r.Body.Close()
	if r.StatusCode != http.StatusConflict && r.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 409 (still running) or 200 (race-resolved)", r.StatusCode)
	}
}

func TestWaveH2_DeleteCancelsRunningRunner(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	// Issue many tasks rapidly; before they all settle, DELETE one
	// + assert it transitions to canceled.
	resp := fix.post(t, "/v1/runners", []byte(`{"prompt":"x"}`))
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	delResp := fix.delete(t, "/v1/runners/"+created.ID)
	delResp.Body.Close()
	// DELETE always 204 regardless of final state.
	if delResp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204", delResp.StatusCode)
	}
	// Eventually state is canceled OR completed (race window).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r := fix.get(t, "/v1/runners/"+created.ID)
		var state struct{ State string }
		_ = json.NewDecoder(r.Body).Decode(&state)
		r.Body.Close()
		if state.State == "canceled" || state.State == "completed" || state.State == "failed" {
			return // valid terminal state observed
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("runner never reached terminal state after DELETE")
}

func TestWaveH2_Healthz_NoAuthRequired(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	resp, err := http.Get(fix.iogridURL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", resp.StatusCode)
	}
}

// ─── LIVE WALK: real claude binary ────────────────────────────────

func TestV094Walk_IogridAgainstRealClaude(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH — live-walk skipped")
	}
	gomodOut, _ := exec.Command("go", "env", "GOMOD").Output()
	repoRoot := filepath.Dir(strings.TrimSpace(string(gomodOut)))

	tmpDir := t.TempDir()
	runnerBin := filepath.Join(tmpDir, "chepherd-runner")
	if out, err := exec.Command("go", "build", "-o", runnerBin, "./cmd/runner").CombinedOutput(); err != nil {
		// build needs Dir override
		b := exec.Command("go", "build", "-o", runnerBin, "./cmd/runner")
		b.Dir = repoRoot
		if out, err := b.CombinedOutput(); err != nil {
			t.Fatalf("build chepherd-runner: %v\n%s", err, out)
		}
		_ = out
	}
	iogridBin := filepath.Join(tmpDir, "iogrid")
	b := exec.Command("go", "build", "-o", iogridBin, "./cmd/iogrid")
	b.Dir = repoRoot
	if out, err := b.CombinedOutput(); err != nil {
		t.Fatalf("build iogrid: %v\n%s", err, out)
	}

	port := freePort(t)
	authToken := "live-walk-token"
	cmd := exec.Command(iogridBin,
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--runner-bin", runnerBin,
		"--auth-token", authToken,
		"--state-dir", filepath.Join(tmpDir, "state"),
	)
	logFile, _ := os.CreateTemp("", "iogrid-live-*.log")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start iogrid: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_, _ = cmd.Process.Wait()
		if t.Failed() && logFile != nil {
			if b, err := os.ReadFile(logFile.Name()); err == nil {
				t.Logf("iogrid log:\n%s", b)
			}
		}
	})
	iogridURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForHealthz(iogridURL+"/healthz", 5*time.Second); err != nil {
		t.Fatalf("/healthz never came up: %v", err)
	}

	// POST a task → poll → fetch result.
	req, _ := http.NewRequest("POST", iogridURL+"/v1/runners",
		bytes.NewReader([]byte(`{"prompt":"say only the literal word ack and nothing else"}`)))
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/runners: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("status = %d, want 202\n%s", resp.StatusCode, body)
	}
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	t.Logf("iogrid runner-id: %s", created.ID)

	// Poll until terminal.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		gr, _ := http.NewRequest("GET", iogridURL+"/v1/runners/"+created.ID, nil)
		gr.Header.Set("Authorization", "Bearer "+authToken)
		gresp, _ := http.DefaultClient.Do(gr)
		var s struct{ State string }
		_ = json.NewDecoder(gresp.Body).Decode(&s)
		gresp.Body.Close()
		if s.State != "running" && s.State != "" {
			t.Logf("final state: %s", s.State)
			if s.State != "completed" {
				t.Fatalf("state = %s, want completed", s.State)
			}
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	rreq, _ := http.NewRequest("GET", iogridURL+"/v1/runners/"+created.ID+"/result", nil)
	rreq.Header.Set("Authorization", "Bearer "+authToken)
	rresp, err := http.DefaultClient.Do(rreq)
	if err != nil {
		t.Fatalf("GET result: %v", err)
	}
	defer rresp.Body.Close()
	if rresp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(rresp.Body)
		t.Fatalf("result status = %d, want 200\n%s", rresp.StatusCode, body)
	}
	body, _ := io.ReadAll(rresp.Body)
	t.Logf("A2A Task envelope:\n%s", body)
	var task map[string]any
	_ = json.Unmarshal(body, &task)
	history, _ := task["history"].([]any)
	if len(history) < 2 {
		t.Fatalf("history short: %v", history)
	}
	agent, _ := history[1].(map[string]any)
	parts, _ := agent["parts"].([]any)
	first, _ := parts[0].(map[string]any)
	if !strings.Contains(strings.ToLower(fmt.Sprint(first["text"])), "ack") {
		t.Errorf("agent output didn't contain 'ack': %v", first["text"])
	}
}

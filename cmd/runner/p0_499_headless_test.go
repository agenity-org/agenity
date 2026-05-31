// cmd/runner/p0_499_headless_test.go pins the v0.9.4 §4 + §5 #49
// chepherd-runner --headless per-task ephemeral lifecycle (#499
// Wave H1). Asserts:
//
//   - Task input parsing matrix: --task-json inline > --task-file >
//     stdin precedence; bare `{"prompt":"..."}` convenience shape
//     translates into a one-Part Message; empty input errors.
//   - Result envelope shape: COMPLETED Task with history [user
//     message, agent response] when the agent returns successfully;
//     FAILED Task with Status.Message when the agent exits non-zero
//     or returns an error envelope; auto-generated taskId +
//     contextId when caller omits them.
//   - Stub agent binary mode: agentBinPath override drives a
//     test-fixture binary (a tiny Go program that echoes a fake
//     `claude --print --output-format json` envelope back to
//     stdout) so the unit suite exercises the FULL runHeadless
//     code path without depending on the real claude binary.
//   - Live-walk e2e: real chepherd-runner --headless against real
//     `claude --print --output-format json` — proves the
//     iogrid-equivalent integration contract that Wave H2 will
//     HTTP-wrap. Skipped when `claude` isn't on PATH (CI without
//     the agent binary).
//
// Refs #499 V0.9.2-ARCHITECTURE.md §4 §5 #49.
package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
)

// buildAgentStub compiles a tiny Go program into binPath that, when
// invoked with `--print --output-format json <prompt>`, echoes a
// canonical claude-result-shape envelope back to stdout. Reusable
// across multiple unit tests without paying claude's cost.
func buildAgentStub(t *testing.T, behavior string) string {
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
	if len(os.Args) >= 4 && os.Args[1] == "--print" && os.Args[2] == "--output-format" && os.Args[3] == "json" {
		prompt = strings.Join(os.Args[4:], " ")
	}
	behavior := ` + behaviorLiteral(behavior) + `
	if behavior == "fail" {
		fmt.Fprintln(os.Stderr, "stub agent: simulated failure")
		os.Exit(2)
	}
	if behavior == "error-envelope" {
		envelope := map[string]any{"type": "result", "is_error": true, "result": "stub error", "stop_reason": "end_turn"}
		body, _ := json.Marshal(envelope)
		fmt.Print(string(body))
		return
	}
	if behavior == "garbage" {
		fmt.Print("not-json")
		return
	}
	envelope := map[string]any{"type": "result", "is_error": false, "result": "echo:" + prompt, "stop_reason": "end_turn"}
	body, _ := json.Marshal(envelope)
	fmt.Print(string(body))
}`
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "stub.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write stub src: %v", err)
	}
	binPath := filepath.Join(dir, "stub")
	build := exec.Command("go", "build", "-o", binPath, srcPath)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, out)
	}
	return binPath
}

func behaviorLiteral(b string) string {
	return "\"" + b + "\""
}

func TestWaveH1_RunHeadless_HappyPathProducesCompletedTask(t *testing.T) {
	t.Parallel()
	stub := buildAgentStub(t, "ok")
	resultFile := filepath.Join(t.TempDir(), "result.json")
	hc := &headlessConfig{
		enabled:      true,
		taskJSON:     `{"role":"user","kind":"message","contextId":"ctx-1","parts":[{"kind":"text","text":"hi"}]}`,
		resultFile:   resultFile,
		agentBinPath: stub,
		timeout:      10 * time.Second,
	}
	code, err := runHeadless(context.Background(), hc)
	if err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	body, _ := os.ReadFile(resultFile)
	var task a2a.Task
	if err := json.Unmarshal(body, &task); err != nil {
		t.Fatalf("decode task envelope: %v\n%s", err, body)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("state = %q, want completed", task.Status.State)
	}
	if task.ContextID != "ctx-1" {
		t.Errorf("contextId = %q, want ctx-1", task.ContextID)
	}
	if len(task.History) != 2 {
		t.Fatalf("history len = %d, want 2 (input + output)", len(task.History))
	}
	if got := task.History[1].Parts[0].Text; got != "echo:hi" {
		t.Errorf("output text = %q, want echo:hi", got)
	}
}

func TestWaveH1_RunHeadless_AgentFailureProducesFailedTaskExit2(t *testing.T) {
	t.Parallel()
	stub := buildAgentStub(t, "fail")
	hc := &headlessConfig{
		enabled:      true,
		taskJSON:     `{"role":"user","kind":"message","parts":[{"kind":"text","text":"hi"}]}`,
		agentBinPath: stub,
		timeout:      10 * time.Second,
	}
	// Redirect stdout so the test doesn't pollute its own output.
	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w
	code, _ := runHeadless(context.Background(), hc)
	_ = w.Close()
	os.Stdout = origStdout
	body, _ := io.ReadAll(r)

	if code != 2 {
		t.Errorf("exit code = %d, want 2 (FAILED task)", code)
	}
	var task a2a.Task
	if err := json.Unmarshal(body, &task); err != nil {
		t.Fatalf("decode task envelope: %v\n%s", err, body)
	}
	if task.Status.State != a2a.TaskStateFailed {
		t.Errorf("state = %q, want failed", task.Status.State)
	}
	if task.Status.Message == nil ||
		len(task.Status.Message.Parts) == 0 ||
		!strings.Contains(task.Status.Message.Parts[0].Text, "agent process failed") {
		t.Errorf("failure reason not surfaced in Status.Message: %+v", task.Status.Message)
	}
}

func TestWaveH1_RunHeadless_AgentErrorEnvelopeProducesFailedTask(t *testing.T) {
	t.Parallel()
	stub := buildAgentStub(t, "error-envelope")
	resultFile := filepath.Join(t.TempDir(), "result.json")
	hc := &headlessConfig{
		enabled:      true,
		taskJSON:     `{"role":"user","kind":"message","parts":[{"kind":"text","text":"x"}]}`,
		resultFile:   resultFile,
		agentBinPath: stub,
		timeout:      10 * time.Second,
	}
	code, _ := runHeadless(context.Background(), hc)
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (FAILED)", code)
	}
	body, _ := os.ReadFile(resultFile)
	var task a2a.Task
	_ = json.Unmarshal(body, &task)
	if task.Status.State != a2a.TaskStateFailed {
		t.Errorf("state = %q, want failed", task.Status.State)
	}
}

func TestWaveH1_RunHeadless_GarbageAgentOutputProducesFailedTask(t *testing.T) {
	t.Parallel()
	stub := buildAgentStub(t, "garbage")
	resultFile := filepath.Join(t.TempDir(), "result.json")
	hc := &headlessConfig{
		enabled:      true,
		taskJSON:     `{"role":"user","kind":"message","parts":[{"kind":"text","text":"x"}]}`,
		resultFile:   resultFile,
		agentBinPath: stub,
		timeout:      10 * time.Second,
	}
	code, _ := runHeadless(context.Background(), hc)
	if code != 2 {
		t.Errorf("exit code = %d, want 2 on un-decodable agent output", code)
	}
}

func TestWaveH1_RunHeadless_BarePromptShapeAccepted(t *testing.T) {
	t.Parallel()
	stub := buildAgentStub(t, "ok")
	resultFile := filepath.Join(t.TempDir(), "result.json")
	hc := &headlessConfig{
		enabled:      true,
		taskJSON:     `{"prompt":"hello-via-bare-shape"}`,
		resultFile:   resultFile,
		agentBinPath: stub,
		timeout:      10 * time.Second,
	}
	code, err := runHeadless(context.Background(), hc)
	if err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	body, _ := os.ReadFile(resultFile)
	var task a2a.Task
	_ = json.Unmarshal(body, &task)
	if task.ContextID == "" || !strings.HasPrefix(task.ContextID, "headless-") {
		t.Errorf("auto-generated contextId not set: %q", task.ContextID)
	}
	if got := task.History[1].Parts[0].Text; got != "echo:hello-via-bare-shape" {
		t.Errorf("bare-shape prompt didn't reach agent: %q", got)
	}
}

func TestWaveH1_RunHeadless_TaskFilePrecedence(t *testing.T) {
	t.Parallel()
	stub := buildAgentStub(t, "ok")
	dir := t.TempDir()
	taskFile := filepath.Join(dir, "task.json")
	if err := os.WriteFile(taskFile, []byte(`{"prompt":"from-file"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	resultFile := filepath.Join(dir, "result.json")
	hc := &headlessConfig{
		enabled:      true,
		taskFile:     taskFile,
		resultFile:   resultFile,
		agentBinPath: stub,
		timeout:      10 * time.Second,
	}
	code, _ := runHeadless(context.Background(), hc)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	body, _ := os.ReadFile(resultFile)
	if !strings.Contains(string(body), "echo:from-file") {
		t.Errorf("file path didn't reach agent: %s", body)
	}
}

func TestWaveH1_RunHeadless_EmptyInputErrors(t *testing.T) {
	t.Parallel()
	hc := &headlessConfig{enabled: true, taskJSON: ""}
	// Replace stdin with EOF so the read returns 0 bytes.
	origStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	_ = w.Close()
	defer func() { os.Stdin = origStdin }()

	code, err := runHeadless(context.Background(), hc)
	if err == nil {
		t.Error("expected error for empty input")
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3 (malformed input)", code)
	}
}

// ─── LIVE WALK: real chepherd-runner binary + real claude ─────────

func TestV094Walk_HeadlessRunnerAgainstRealClaude(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH — live-walk skipped (unit tests cover the wire shape)")
	}

	gomodOut, _ := exec.Command("go", "env", "GOMOD").Output()
	repoRoot := filepath.Dir(strings.TrimSpace(string(gomodOut)))

	binPath := filepath.Join(t.TempDir(), "chepherd-runner-h1")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/runner")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	resultFile := filepath.Join(t.TempDir(), "result.json")
	cmd := exec.Command(binPath,
		"--headless",
		"--task-json", `{"prompt":"say only the literal word ack and nothing else"}`,
		"--result-file", resultFile,
		"--task-timeout", "30s",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runner --headless: %v\nstdout/err: %s", err, out)
	}

	body, err := os.ReadFile(resultFile)
	if err != nil {
		t.Fatalf("read result: %v\nrunner output: %s", err, out)
	}
	t.Logf("A2A Task envelope:\n%s", body)

	var task a2a.Task
	if err := json.Unmarshal(body, &task); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, body)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("state = %q, want completed (live claude returned: %+v)", task.Status.State, task)
	}
	if task.ID == "" {
		t.Error("task.id empty")
	}
	if task.ContextID == "" || !strings.HasPrefix(task.ContextID, "headless-") {
		t.Errorf("auto-generated contextId not set: %q", task.ContextID)
	}
	if len(task.History) != 2 {
		t.Fatalf("history len = %d, want 2", len(task.History))
	}
	if task.History[1].Role != "agent" {
		t.Errorf("output role = %q, want agent", task.History[1].Role)
	}
	resultText := task.History[1].Parts[0].Text
	if !strings.Contains(strings.ToLower(resultText), "ack") {
		t.Errorf("agent output didn't mention 'ack': %q", resultText)
	}
}

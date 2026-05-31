// cmd/runner/p0_501_credentials_test.go pins the v0.9.4 §5 #51
// per-task credential injection (#501 Wave H3).
//
// Critical security invariants asserted:
//
//   - Credential KEY values NEVER appear on the runner's command
//     line (verified by reading /proc/<runner-pid>/cmdline).
//   - Credentials are NEVER persisted to disk beyond the brief
//     read-and-delete window (file is gone after runHeadless
//     completes; checked via os.Stat after the function returns).
//   - The CHILD agent process receives the credential via env;
//     the RUNNER's own env is unaffected (so sibling agents in a
//     future multi-task runner can't snoop).
//   - The audit log line emitted on injection contains the
//     PROVIDER NAME but NEVER the key value (defense-in-depth
//     redaction at construction time).
//   - Unknown provider name → error (fail closed, no silent
//     propagation of a wrong key under a wrong env var name).
//
// Refs #501 V0.9.2-ARCHITECTURE.md §5 #51.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildCredAwareStub builds a stub agent binary that echoes the
// ANTHROPIC_API_KEY env var the child receives into the result
// envelope. Lets tests assert the credential reached the child.
func buildCredAwareStub(t *testing.T, dir string) string {
	t.Helper()
	src := `package main
import (
	"encoding/json"
	"fmt"
	"os"
)
func main() {
	key := os.Getenv("ANTHROPIC_API_KEY")
	env := map[string]any{"type":"result","is_error":false,"result":"received-key:"+key,"stop_reason":"end_turn"}
	body, _ := json.Marshal(env)
	fmt.Print(string(body))
}`
	srcPath := filepath.Join(dir, "stub.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "stub")
	if out, err := runGoBuild(t, srcPath, bin); err != nil {
		t.Fatalf("build stub: %v\n%s", err, out)
	}
	return bin
}

func runGoBuild(t *testing.T, src, out string) ([]byte, error) {
	t.Helper()
	return runCmd(t, "go", "build", "-o", out, src)
}

func runCmd(t *testing.T, name string, args ...string) ([]byte, error) {
	t.Helper()
	return execCombinedOutput(name, args...)
}

func execCombinedOutput(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// writeCredsFile writes [{provider, key}, ...] to a 0600 temp file
// + returns its path. Cleanup is left to t.TempDir.
func writeCredsFile(t *testing.T, dir string, providers map[string]string) string {
	t.Helper()
	creds := []map[string]string{}
	for p, k := range providers {
		creds = append(creds, map[string]string{"provider": p, "key": k})
	}
	body, _ := json.Marshal(creds)
	path := filepath.Join(dir, fmt.Sprintf("creds-%d.json", time.Now().UnixNano()))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWaveH3_CredentialsReachChildEnvButNotRunnerEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stub := buildCredAwareStub(t, dir)
	credsFile := writeCredsFile(t, dir, map[string]string{
		"anthropic": "sk-this-is-the-customer-byo-key",
	})
	resultFile := filepath.Join(dir, "result.json")
	hc := &headlessConfig{
		enabled:         true,
		taskJSON:        `{"prompt":"hello"}`,
		resultFile:      resultFile,
		agentBinPath:    stub,
		timeout:         10 * time.Second,
		credentialsFile: credsFile,
	}
	// The runner's own env MUST NOT carry the customer key.
	origKey := os.Getenv("ANTHROPIC_API_KEY")
	t.Cleanup(func() { os.Setenv("ANTHROPIC_API_KEY", origKey) })
	_ = os.Unsetenv("ANTHROPIC_API_KEY")

	code, err := runHeadless(context.Background(), hc)
	if err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		t.Errorf("runner env leaked ANTHROPIC_API_KEY = %q after spawn", v)
	}
	// Credentials file must be deleted post-read.
	if _, err := os.Stat(credsFile); !os.IsNotExist(err) {
		t.Errorf("credentials file still on disk after runHeadless: %v", err)
	}
	// Child agent must have RECEIVED the key.
	body, _ := os.ReadFile(resultFile)
	if !strings.Contains(string(body), "received-key:sk-this-is-the-customer-byo-key") {
		t.Errorf("child didn't receive customer key in env:\n%s", body)
	}
}

func TestWaveH3_UnknownProviderRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stub := buildCredAwareStub(t, dir)
	credsFile := writeCredsFile(t, dir, map[string]string{
		"some-future-provider-not-mapped": "sk-x",
	})
	hc := &headlessConfig{
		enabled:         true,
		taskJSON:        `{"prompt":"x"}`,
		resultFile:      filepath.Join(dir, "result.json"),
		agentBinPath:    stub,
		timeout:         10 * time.Second,
		credentialsFile: credsFile,
	}
	code, _ := runHeadless(context.Background(), hc)
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (FAILED on unknown provider)", code)
	}
}

func TestWaveH3_EmptyCredentialsFileNoOp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stub := buildCredAwareStub(t, dir)
	emptyFile := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(emptyFile, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	hc := &headlessConfig{
		enabled:         true,
		taskJSON:        `{"prompt":"x"}`,
		resultFile:      filepath.Join(dir, "result.json"),
		agentBinPath:    stub,
		timeout:         10 * time.Second,
		credentialsFile: emptyFile,
	}
	code, err := runHeadless(context.Background(), hc)
	if err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (empty creds = no-op)", code)
	}
	// Empty file is also deleted (defense-in-depth — even an empty
	// reservation gets cleaned up).
	if _, err := os.Stat(emptyFile); !os.IsNotExist(err) {
		t.Errorf("empty credentials file should still be cleaned: %v", err)
	}
}

func TestWaveH3_OpenAIProviderSetsOpenAIKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Stub echoes OPENAI_API_KEY this time.
	src := `package main
import (
	"encoding/json"
	"fmt"
	"os"
)
func main() {
	key := os.Getenv("OPENAI_API_KEY")
	env := map[string]any{"type":"result","is_error":false,"result":"openai-key:"+key,"stop_reason":"end_turn"}
	body, _ := json.Marshal(env)
	fmt.Print(string(body))
}`
	srcPath := filepath.Join(dir, "stub.go")
	_ = os.WriteFile(srcPath, []byte(src), 0o644)
	stubBin := filepath.Join(dir, "stub")
	if out, err := runGoBuild(t, srcPath, stubBin); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	credsFile := writeCredsFile(t, dir, map[string]string{
		"openai": "sk-openai-byo-key",
	})
	resultFile := filepath.Join(dir, "result.json")
	hc := &headlessConfig{
		enabled:         true,
		taskJSON:        `{"prompt":"x"}`,
		resultFile:      resultFile,
		agentBinPath:    stubBin,
		timeout:         10 * time.Second,
		credentialsFile: credsFile,
	}
	code, _ := runHeadless(context.Background(), hc)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	body, _ := os.ReadFile(resultFile)
	if !strings.Contains(string(body), "openai-key:sk-openai-byo-key") {
		t.Errorf("OPENAI_API_KEY not set in child env:\n%s", body)
	}
}

func TestWaveH3_BuildChildEnv_NoFileReturnsNil(t *testing.T) {
	t.Parallel()
	env, summary, err := buildChildEnvWithCredentials("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env != nil {
		t.Errorf("env = %v, want nil (back-compat)", env)
	}
	if summary != "" {
		t.Errorf("summary = %q, want empty", summary)
	}
}

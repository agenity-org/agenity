// cmd/runner/p0_503_auth_required_test.go pins the v0.9.4 §15.3
// AUTH_REQUIRED detection path in --headless mode (#503 Wave H5).
//
// When the agent's --print --output-format json output triggers
// the per-flavor IsAuthRequired detector, runHeadless:
//   - exits with code 4 (headlessAuthRequiredExitCode)
//   - writes a Task envelope with Status.State = "auth-required"
//   - populates Status.Details with AuthProvider / AuthMessage
//     (and AuthURL when available)
//
// The test uses a stub claude that echoes the canonical headless
// prose pattern empirically captured from real claude 2.1.148.
//
// Refs #503 V0.9.2-ARCHITECTURE.md §15.3.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWaveH5_HeadlessAuthRequired_PopulatesStatusDetails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Stub agent that emits a result envelope containing the
	// canonical headless prose pattern claude 2.1.148 emits for
	// OAuth-needing connectors.
	src := `package main
import (
	"encoding/json"
	"fmt"
)
func main() {
	env := map[string]any{
		"type":"result",
		"is_error":false,
		"result":"The Google Drive MCP connector can't be authenticated from this side. Please run ` + "`" + `/mcp` + "`" + ` and select **claude.ai Google Drive** to complete the OAuth flow in your browser.",
		"stop_reason":"end_turn",
	}
	body, _ := json.Marshal(env)
	fmt.Print(string(body))
}`
	srcPath := filepath.Join(dir, "stub.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(dir, "stub")
	if out, err := exec.Command("go", "build", "-o", stub, srcPath).CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, out)
	}

	resultFile := filepath.Join(dir, "result.json")
	hc := &headlessConfig{
		enabled:      true,
		taskJSON:     `{"prompt":"please use Google Drive"}`,
		resultFile:   resultFile,
		agentBinPath: stub,
		agentSlug:    "claude-code",
		timeout:      10 * time.Second,
	}
	code, err := runHeadless(context.Background(), hc)
	if err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	if code != 4 {
		t.Fatalf("exit code = %d, want 4 (headlessAuthRequiredExitCode)", code)
	}

	body, _ := os.ReadFile(resultFile)
	var envelope struct {
		Status struct {
			State   string `json:"state"`
			Details *struct {
				AuthProvider string `json:"authProvider"`
				AuthMessage  string `json:"authMessage"`
				AuthURL      string `json:"authUrl"`
			} `json:"details"`
		} `json:"status"`
		History []map[string]any `json:"history"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode result: %v\n%s", err, body)
	}
	if envelope.Status.State != "auth-required" {
		t.Errorf("Status.State = %q, want auth-required\n%s", envelope.Status.State, body)
	}
	if envelope.Status.Details == nil {
		t.Fatalf("Status.Details = nil, want populated AuthChallenge\n%s", body)
	}
	d := envelope.Status.Details
	if !strings.Contains(strings.ToLower(d.AuthProvider), "google drive") &&
		!strings.Contains(strings.ToLower(d.AuthProvider), "claude.ai") {
		t.Errorf("AuthProvider = %q, want to identify Google Drive / claude.ai", d.AuthProvider)
	}
	if !strings.Contains(strings.ToLower(d.AuthMessage), "/mcp") {
		t.Errorf("AuthMessage = %q, want /mcp instruction", d.AuthMessage)
	}
	// Anthropic-managed connector — no in-band URL.
	if d.AuthURL != "" {
		t.Errorf("AuthURL = %q, want empty for claude.ai connector", d.AuthURL)
	}
}

func TestWaveH5_HeadlessNonAuthOutput_StaysCompleted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Stub emits a normal completed result — no auth pattern.
	src := `package main
import (
	"encoding/json"
	"fmt"
)
func main() {
	env := map[string]any{
		"type":"result",
		"is_error":false,
		"result":"hello world",
		"stop_reason":"end_turn",
	}
	body, _ := json.Marshal(env)
	fmt.Print(string(body))
}`
	srcPath := filepath.Join(dir, "stub.go")
	_ = os.WriteFile(srcPath, []byte(src), 0o644)
	stub := filepath.Join(dir, "stub")
	if out, err := exec.Command("go", "build", "-o", stub, srcPath).CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, out)
	}
	resultFile := filepath.Join(dir, "result.json")
	hc := &headlessConfig{
		enabled:      true,
		taskJSON:     `{"prompt":"hi"}`,
		resultFile:   resultFile,
		agentBinPath: stub,
		agentSlug:    "claude-code",
		timeout:      10 * time.Second,
	}
	code, err := runHeadless(context.Background(), hc)
	if err != nil {
		t.Fatalf("runHeadless: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (completed)", code)
	}
}

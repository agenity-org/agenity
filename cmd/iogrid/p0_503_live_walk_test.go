// cmd/iogrid/p0_503_live_walk_test.go is the live-walk acceptance
// gate for #503 Wave H5 — boots iogrid + real chepherd-runner +
// REAL claude 2.1.148, posts a task that exercises the OAuth-
// needing claude.ai Google Drive built-in MCP connector, asserts:
//
//   - runner state transitions to "auth-required"
//   - result envelope has Status.State="auth-required" +
//     Status.Details populated with the provider name
//   - inject-credentials endpoint returns 202 with new runner id
//
// Skipped when claude isn't on PATH (CI without the binary). When
// running locally the claude.ai Google Drive connector must be
// listed as needs-auth in `/mcp` output — the test fixture relies
// on the empirically-verified behavior that headless mode emits
// the OAuth-prose pattern when the user asks it to use the
// connector.
//
// Refs #503 V0.9.2-ARCHITECTURE.md §15.3.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestV094Walk_H5_AuthRequiredAgainstRealClaude(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH")
	}

	gomodOut, _ := exec.Command("go", "env", "GOMOD").Output()
	repoRoot := filepath.Dir(strings.TrimSpace(string(gomodOut)))

	tmpDir := t.TempDir()
	runnerBin := filepath.Join(tmpDir, "chepherd-runner")
	rb := exec.Command("go", "build", "-o", runnerBin, "./cmd/runner")
	rb.Dir = repoRoot
	if out, err := rb.CombinedOutput(); err != nil {
		t.Fatalf("build runner: %v\n%s", err, out)
	}
	iogridBin := filepath.Join(tmpDir, "iogrid")
	ib := exec.Command("go", "build", "-o", iogridBin, "./cmd/iogrid")
	ib.Dir = repoRoot
	if out, err := ib.CombinedOutput(); err != nil {
		t.Fatalf("build iogrid: %v\n%s", err, out)
	}

	port := freePort(t)
	authToken := "h5-live-walk-token"
	cmd := exec.Command(iogridBin,
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--runner-bin", runnerBin,
		"--auth-token", authToken,
		"--state-dir", filepath.Join(tmpDir, "state"),
		"--task-timeout", "30s",
	)
	logFile, _ := os.CreateTemp("", "iogrid-h5-live-*.log")
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
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForHealthz(url+"/healthz", 5*time.Second); err != nil {
		t.Fatalf("iogrid /healthz: %v", err)
	}

	post := func(path string, body []byte) *http.Response {
		req, _ := http.NewRequest("POST", url+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+authToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp
	}
	get := func(path string) *http.Response {
		req, _ := http.NewRequest("GET", url+path, nil)
		req.Header.Set("Authorization", "Bearer "+authToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return resp
	}

	// Drive real claude with a prompt that surfaces the OAuth-
	// needing connector. Empirically captured response: prose
	// pattern containing /mcp + claude.ai Google Drive + OAuth.
	body := []byte(`{"prompt":"Call the mcp__claude_ai_Google_Drive__authenticate tool to start auth"}`)
	resp := post("/v1/runners", body)
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("POST = %d, want 202\n%s", resp.StatusCode, b)
	}
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	// Poll for terminal state.
	deadline := time.Now().Add(45 * time.Second)
	var final string
	for time.Now().Before(deadline) {
		r := get("/v1/runners/" + created.ID)
		var s struct{ State string }
		_ = json.NewDecoder(r.Body).Decode(&s)
		r.Body.Close()
		if s.State != "running" && s.State != "" {
			final = s.State
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if final != "auth-required" {
		t.Fatalf("final runner state = %q, want auth-required (real claude empirically emits OAuth prose for this connector)", final)
	}

	r := get("/v1/runners/" + created.ID + "/result")
	defer r.Body.Close()
	envBody, _ := io.ReadAll(r.Body)
	var env struct {
		Status struct {
			State   string `json:"state"`
			Details *struct {
				AuthProvider string `json:"authProvider"`
				AuthMessage  string `json:"authMessage"`
				AuthURL      string `json:"authUrl"`
			} `json:"details"`
		} `json:"status"`
	}
	if err := json.Unmarshal(envBody, &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, envBody)
	}
	if env.Status.State != "auth-required" {
		t.Errorf("envelope Status.State = %q, want auth-required", env.Status.State)
	}
	if env.Status.Details == nil {
		t.Fatalf("Status.Details = nil from real claude — H5 detector failed on real bytes")
	}
	t.Logf("REAL claude AuthChallenge: Provider=%q Message=%q URL=%q",
		env.Status.Details.AuthProvider, env.Status.Details.AuthMessage,
		env.Status.Details.AuthURL)
	if env.Status.Details.AuthProvider == "" {
		t.Errorf("AuthProvider empty in real-claude envelope")
	}
	if env.Status.Details.AuthMessage == "" {
		t.Errorf("AuthMessage empty in real-claude envelope")
	}

	// Inject credentials → expect 202 + new runner id.
	inject := []byte(`{"credentials":[{"provider":"google","key":"mock-oauth-token"}]}`)
	ir := post("/v1/runners/"+created.ID+"/credentials/inject", inject)
	defer ir.Body.Close()
	if ir.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(ir.Body)
		t.Fatalf("inject = %d, want 202\n%s", ir.StatusCode, b)
	}
	var resumed struct {
		ResumedFrom string `json:"resumed_from"`
		ID          string `json:"id"`
	}
	_ = json.NewDecoder(ir.Body).Decode(&resumed)
	if resumed.ResumedFrom != created.ID {
		t.Errorf("resumed_from = %q, want %q", resumed.ResumedFrom, created.ID)
	}
	if resumed.ID == "" || resumed.ID == created.ID {
		t.Errorf("resumed id = %q, want fresh non-empty != %q", resumed.ID, created.ID)
	}
	t.Logf("H5 live walk: real claude auth-required + inject → resume id=%s", resumed.ID)
}

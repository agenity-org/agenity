// cmd/iogrid/p0_502_recipes_live_walk_test.go is the live-walk
// acceptance gate for #502 Wave H4 — registers a real recipe via
// the iogrid REST surface, executes by name against the REAL claude
// 2.1.148, asserts the recipe template expansion drove the real
// agent + the result envelope carries the expected output.
//
// Skipped when claude isn't on PATH (CI without the agent binary).
//
// Refs #502 V0.9.2-ARCHITECTURE.md §4 §11 #461.
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

func TestV094Walk_RecipeAgainstRealClaude(t *testing.T) {
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
	authToken := "h4-live-walk-token"
	cmd := exec.Command(iogridBin,
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--runner-bin", runnerBin,
		"--auth-token", authToken,
		"--state-dir", filepath.Join(tmpDir, "state"),
	)
	logFile, _ := os.CreateTemp("", "iogrid-h4-live-*.log")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
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
		t.Fatalf("/healthz: %v", err)
	}

	post := func(path string, body []byte) *http.Response {
		req, _ := http.NewRequest("POST", iogridURL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+authToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp
	}
	get := func(path string) *http.Response {
		req, _ := http.NewRequest("GET", iogridURL+path, nil)
		req.Header.Set("Authorization", "Bearer "+authToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return resp
	}

	// Register a recipe.
	recipe := TaskRecipe{
		Name:           "say-the-word",
		AgentSlug:      "claude-code",
		Description:    "Reply with exactly the supplied word",
		PromptTemplate: "Reply with exactly the single word \"{{.word}}\" and nothing else.",
		RequiredParams: []string{"word"},
	}
	body, _ := json.Marshal(recipe)
	cr := post("/api/v1/recipes", body)
	if cr.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(cr.Body)
		cr.Body.Close()
		t.Fatalf("POST recipe = %d, want 201\n%s", cr.StatusCode, b)
	}
	cr.Body.Close()

	// Execute by name.
	er := post("/v1/runners/recipe/say-the-word", []byte(`{"params":{"word":"ack"}}`))
	if er.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(er.Body)
		er.Body.Close()
		t.Fatalf("exec = %d, want 202\n%s", er.StatusCode, b)
	}
	var exec_ struct {
		ID     string `json:"id"`
		Prompt string `json:"prompt"`
	}
	_ = json.NewDecoder(er.Body).Decode(&exec_)
	er.Body.Close()
	t.Logf("expanded prompt: %s", exec_.Prompt)

	// Poll for terminal.
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		r := get("/v1/runners/" + exec_.ID)
		var s struct{ State string }
		_ = json.NewDecoder(r.Body).Decode(&s)
		r.Body.Close()
		if s.State != "running" && s.State != "" {
			if s.State != "completed" {
				t.Fatalf("state = %s, want completed", s.State)
			}
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Fetch result + assert agent said "ack".
	rr := get("/v1/runners/" + exec_.ID + "/result")
	defer rr.Body.Close()
	var task map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&task); err != nil {
		t.Fatalf("decode: %v", err)
	}
	history, _ := task["history"].([]any)
	if len(history) < 2 {
		t.Fatalf("history short: %v", history)
	}
	agent, _ := history[1].(map[string]any)
	parts, _ := agent["parts"].([]any)
	first, _ := parts[0].(map[string]any)
	text, _ := first["text"].(string)
	t.Logf("real claude returned: %q", text)
	if !strings.Contains(strings.ToLower(text), "ack") {
		t.Errorf("agent didn't say ack: %q", text)
	}
}

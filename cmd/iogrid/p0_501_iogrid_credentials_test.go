// cmd/iogrid/p0_501_iogrid_credentials_test.go pins the v0.9.4 §5
// #51 iogrid → runner credential-forwarding contract (#501 Wave
// H3). End-to-end:
//
//   iogrid POST /v1/runners {credentials:[...], prompt:"..."}
//     → iogrid strips credentials from task body
//     → writes them to 0600 file in the runner's workDir
//     → forks chepherd-runner --headless with --credentials-file <path>
//        (NEVER with the key value on argv — verified)
//     → runner reads credentials, sets ANTHROPIC_API_KEY in child env,
//        deletes the file
//     → child agent receives the key + echoes it in the result
//
// Security invariants asserted:
//
//   - The key value DOES NOT appear in /proc/<runner-pid>/cmdline
//     (would-be leak if iogrid forwarded it as a flag arg)
//   - The credentials file is gone after the runner exits
//   - The result envelope contains the customer's key as proof
//     the child received it
//
// Refs #501 V0.9.2-ARCHITECTURE.md §5 #51.
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

func TestWaveH3_IogridForwardsCredentialsToChildEnv(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	// Replace the default stub agent (built by newFixture) with a
	// CRED-AWARE one that echoes ANTHROPIC_API_KEY into the result.
	stubDir := filepath.Dir(fix.stubBin)
	src := `package main
import (
	"encoding/json"
	"fmt"
	"os"
)
func main() {
	key := os.Getenv("ANTHROPIC_API_KEY")
	env := map[string]any{"type":"result","is_error":false,"result":"recv:"+key,"stop_reason":"end_turn"}
	body, _ := json.Marshal(env)
	fmt.Print(string(body))
}`
	srcPath := filepath.Join(stubDir, "claude.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "build", "-o", fix.stubBin, srcPath).CombinedOutput(); err != nil {
		t.Fatalf("rebuild stub: %v\n%s", err, out)
	}

	const customerKey = "sk-customer-byo-h3-test-key"
	body := []byte(fmt.Sprintf(`{"prompt":"go","credentials":[{"provider":"anthropic","key":%q}]}`, customerKey))
	resp := fix.post(t, "/v1/runners", body)
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("POST status = %d, want 202\n%s", resp.StatusCode, b)
	}
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	// Poll until terminal.
	deadline := time.Now().Add(10 * time.Second)
	var final string
	for time.Now().Before(deadline) {
		r := fix.get(t, "/v1/runners/"+created.ID)
		var state struct{ State string }
		_ = json.NewDecoder(r.Body).Decode(&state)
		r.Body.Close()
		if state.State != "running" {
			final = state.State
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if final != "completed" {
		t.Fatalf("final state = %q, want completed", final)
	}

	// Fetch the A2A Task envelope and assert the customer key
	// reached the child agent.
	r := fix.get(t, "/v1/runners/"+created.ID+"/result")
	defer r.Body.Close()
	respBody, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(respBody), "recv:"+customerKey) {
		t.Errorf("customer key didn't reach child agent:\n%s", respBody)
	}

	// The credentials file must be gone (runner deletes it).
	matches, _ := filepath.Glob(filepath.Join(fix.tmpDir, "iogrid-state", "iogrid-*", "credentials.json"))
	for _, m := range matches {
		if _, err := os.Stat(m); err == nil {
			t.Errorf("credentials file still present: %s", m)
		}
	}
}

func TestWaveH3_KeyNotInCmdline(t *testing.T) {
	t.Parallel()
	// Sentinel-rebuild of stub so this subtest doesn't race with
	// the previous test's stub mutation.
	fix := newFixture(t)
	const sentinel = "sk-sentinel-this-must-not-appear-in-cmdline-99XYZ"
	body := []byte(fmt.Sprintf(`{"prompt":"x","credentials":[{"provider":"anthropic","key":%q}]}`, sentinel))

	// Watch /proc for any process whose cmdline contains the
	// sentinel while the task is in flight.
	seen := make(chan struct{}, 1)
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				matches, _ := filepath.Glob("/proc/*/cmdline")
				for _, p := range matches {
					b, err := os.ReadFile(p)
					if err == nil && bytes.Contains(b, []byte(sentinel)) {
						select {
						case seen <- struct{}{}:
						default:
						}
						return
					}
				}
			}
		}
	}()

	resp := fix.post(t, "/v1/runners", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status = %d, want 202", resp.StatusCode)
	}
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	// Wait for terminal so the proc scan has time to observe.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := fix.get(t, "/v1/runners/"+created.ID)
		var state struct{ State string }
		_ = json.NewDecoder(r.Body).Decode(&state)
		r.Body.Close()
		if state.State != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(stop)

	select {
	case <-seen:
		t.Fatal("SENTINEL KEY observed in /proc/*/cmdline — H3 security invariant violated")
	default:
		// No process exposed the sentinel — invariant holds.
	}
}

func TestWaveH3_NoCredentialsTaskStillWorks(t *testing.T) {
	t.Parallel()
	fix := newFixture(t)
	// No credentials in body — H1/H2 happy path still works.
	resp := fix.post(t, "/v1/runners", []byte(`{"prompt":"plain"}`))
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("status = %d, want 202\n%s", resp.StatusCode, b)
	}
	resp.Body.Close()
}

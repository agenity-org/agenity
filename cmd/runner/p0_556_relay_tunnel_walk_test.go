// cmd/runner/p0_556_relay_tunnel_walk_test.go is the v0.9.4 §10
// Pattern 4 LIVE WALK gate for #556 Wave F7.1 — boots the real
// chepherd-hub binary, dials it from the runner-side tunnel client,
// HTTP-POSTs from the hub's /v1/relay/{org}/{path} surface, and
// proves the request lands in the runner's handler + the response
// byte-exactly routes back.
//
// Refs #556 #497 V0.9.2-ARCHITECTURE.md §10 Pattern 4.
package main

import (
	"bytes"
	"context"
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

func TestV094Walk_F71_RunnerTunnel_ThroughRealHubBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live walk in -short")
	}

	gomodOut, _ := exec.Command("go", "env", "GOMOD").Output()
	repoRoot := filepath.Dir(strings.TrimSpace(string(gomodOut)))
	tmpDir := t.TempDir()
	hubBin := filepath.Join(tmpDir, "chepherd-hub")
	build := exec.Command("go", "build", "-o", hubBin, "./cmd/chepherd-hub")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build hub: %v\n%s", err, out)
	}
	port := freeTCPPortRunner(t)
	cmd := exec.Command(hubBin,
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--stun-listen", "",
		"--turn-listen", "",
		"--allowed-orgs", "alice.example,bob.example",
	)
	logFile, _ := os.CreateTemp("", "hub-f71-live-*.log")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start hub: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_, _ = cmd.Process.Wait()
		if t.Failed() && logFile != nil {
			if b, err := os.ReadFile(logFile.Name()); err == nil {
				t.Logf("hub log:\n%s", b)
			}
		}
	})
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForHubHealthz(hubURL+"/healthz", 5*time.Second); err != nil {
		t.Fatalf("hub healthz: %v", err)
	}

	// Runner's local A2A handler — echoes body + records what it saw.
	const sentinel = "opaque-DTLS-A2A-from-alice-XYZ-987"
	const sentinelHeader = "X-Bob-Handler"
	bobHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set(sentinelHeader, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	bobTunnel := newRelayTunnelClient(hubURL, "bob.example", "", bobHandler)
	if err := bobTunnel.Dial(context.Background()); err != nil {
		t.Fatalf("bob Dial: %v", err)
	}
	defer bobTunnel.Close()

	// Alice POSTs an opaque blob through the hub to bob.
	aliceReq, _ := http.NewRequest("POST", hubURL+"/v1/relay/bob.example/a2a/55/jsonrpc",
		bytes.NewReader([]byte(sentinel)))
	aliceReq.Header.Set("X-Chepherd-Org", "alice.example")
	aliceReq.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(aliceReq)
	if err != nil {
		t.Fatalf("alice POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, b)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, []byte(sentinel)) {
		t.Errorf("body mutated:\n got: %q\nwant: %q", body, sentinel)
	}
	if got := resp.Header.Get(sentinelHeader); got != "POST /a2a/55/jsonrpc" {
		t.Errorf("sentinel header = %q, want POST /a2a/55/jsonrpc", got)
	}
	if bobTunnel.TotalFrames() != 1 {
		t.Errorf("bob TotalFrames = %d, want 1", bobTunnel.TotalFrames())
	}
	if bobTunnel.TotalHandlerOK() != 1 {
		t.Errorf("bob TotalHandlerOK = %d, want 1", bobTunnel.TotalHandlerOK())
	}
	t.Logf("F7.1 live walk: alice → real hub binary → bob runner tunnel client; body byte-exact (%d bytes); path+method preserved",
		len(body))
}

// freeTCPPortRunner picks a free port for the live walk; named
// distinctly to avoid collision with other helpers in this package.
func freeTCPPortRunner(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitForHubHealthz(url string, timeout time.Duration) error {
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
	return fmt.Errorf("never got 200 from %s", url)
}

// Unused-import guard.
var _ = json.Marshal

// cmd/chepherd-hub/p0_497_tunnel_walk_test.go is the v0.9.4 §10
// Pattern 4 LIVE WALK gate for #497 Wave F7 — boots the real
// chepherd-hub binary, dials a stub runner over the WS tunnel, and
// proves an A2A request from alice (HTTP) reaches bob's stub runner
// through the hub tunnel and the response round-trips back.
//
// Body-blind invariant verified: the payload bytes alice sends
// equal the bytes bob's runner sees, byte-for-byte.
//
// Refs #497 V0.9.2-ARCHITECTURE.md §5 #28 §10 Pattern 4.
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
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestV094Walk_F7_TunnelRoundTrip_ThroughRealHubBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live walk in -short")
	}
	gomodOut, _ := exec.Command("go", "env", "GOMOD").Output()
	repoRoot := filepath.Dir(strings.TrimSpace(string(gomodOut)))
	tmpDir := t.TempDir()
	bin := filepath.Join(tmpDir, "chepherd-hub")
	build := exec.Command("go", "build", "-o", bin, "./cmd/chepherd-hub")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	port := freePort(t)
	cmd := exec.Command(bin,
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--stun-listen", "",
		"--turn-listen", "",
		"--allowed-orgs", "alice.example,bob.example",
	)
	logFile, _ := os.CreateTemp("", "hub-f7-live-*.log")
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
				t.Logf("hub log:\n%s", b)
			}
		}
	})
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/healthz")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			break
		}
		if err == nil {
			resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Bob's stub runner dials in.
	wsURL := strings.Replace(baseURL, "http://", "ws://", 1) + "/v1/relay/tunnel"
	bobHeader := http.Header{}
	bobHeader.Set("X-Chepherd-Org", "bob.example")
	bobConn, _, err := websocket.DefaultDialer.Dial(wsURL, bobHeader)
	if err != nil {
		t.Fatalf("bob dial: %v", err)
	}
	defer bobConn.Close()

	// Bob's stub: echo request body with a sentinel header.
	const sentinelHeader = "X-Bob-Saw-Request"
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			var frame relayFrame
			if err := bobConn.ReadJSON(&frame); err != nil {
				return
			}
			if frame.Direction != "to-runner" {
				continue
			}
			_ = bobConn.WriteJSON(&relayFrame{
				RequestID: frame.RequestID,
				Direction: "to-hub",
				Status:    http.StatusOK,
				Headers: map[string]string{
					sentinelHeader: frame.Path,
					"X-Echo-Method": frame.Method,
				},
				Body: frame.Body,
			})
		}
	}()

	// Confirm healthz reports the active tunnel.
	hResp, _ := http.Get(baseURL + "/healthz")
	var health map[string]any
	_ = json.NewDecoder(hResp.Body).Decode(&health)
	hResp.Body.Close()
	tunnels, _ := health["tunnels"].(map[string]any)
	if active, _ := tunnels["active"].(float64); active < 1 {
		t.Errorf("healthz.tunnels.active = %v, want >= 1", tunnels["active"])
	}

	// Alice POSTs an opaque blob through the hub.
	const opaqueBody = "this-is-encrypted-DTLS-wrapped-A2A-XYZ-987!@#"
	req, _ := http.NewRequest("POST", baseURL+"/v1/relay/bob.example/a2a/123/jsonrpc",
		bytes.NewReader([]byte(opaqueBody)))
	req.Header.Set("X-Chepherd-Org", "alice.example")
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("alice POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200\n%s", resp.StatusCode, b)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if string(respBody) != opaqueBody {
		t.Errorf("body mutated:\n got: %q\nwant: %q", respBody, opaqueBody)
	}
	if got := resp.Header.Get(sentinelHeader); got != "/a2a/123/jsonrpc" {
		t.Errorf("sentinel = %q, want path /a2a/123/jsonrpc", got)
	}
	if resp.Header.Get("X-Echo-Method") != "POST" {
		t.Errorf("method header missing or wrong: %q", resp.Header.Get("X-Echo-Method"))
	}
	t.Logf("F7 live walk: alice→bob A2A round-trip via real chepherd-hub tunnel; body byte-exact; path+method preserved")

	bobConn.Close()
	wg.Wait()
}

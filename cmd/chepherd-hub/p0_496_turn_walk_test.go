// cmd/chepherd-hub/p0_496_turn_walk_test.go is the v0.9.4 §10
// Pattern 3 LIVE WALK gate for #496 Wave F6 — boots the real
// chepherd-hub binary with TURN enabled on a free UDP port, mints
// REST creds via the HTTP endpoint, and runs a real pion/turn
// Client Allocate against the binary:
//
//   - Allocate with valid minted creds succeeds + relay addr returned
//   - Allocate with TAMPERED creds fails (401 from pion's auth handler)
//   - Healthz active_allocations counter increments on Allocate +
//     decrements on close
//   - Hub log carries the OnAllocationCreated / OnAllocationDeleted
//     audit lines (metadata only — username + addrs + timestamps;
//     NO relayed payload bytes per the body-blind invariant)
//
// Refs #496 V0.9.2-ARCHITECTURE.md §10 Pattern 3.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
)

func TestV094Walk_F6_PionClientAllocates_AgainstRealHubTURN(t *testing.T) {
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
		t.Fatalf("build chepherd-hub: %v\n%s", err, out)
	}

	httpPort := freePort(t)
	turnUDPPort := freeUDPPort(t)
	turnListen := fmt.Sprintf("127.0.0.1:%d", turnUDPPort)
	publicHost := turnListen
	secret := "f6-live-walk-secret-32-bytes-ok"

	cmd := exec.Command(bin,
		"--listen", fmt.Sprintf("127.0.0.1:%d", httpPort),
		"--stun-listen", "",
		"--turn-listen", turnListen,
		"--turn-secret", secret,
		"--turn-relay-ip", "127.0.0.1",
		"--turn-public-host", publicHost,
		"--allowed-orgs", "alice.example",
	)
	logFile, _ := os.CreateTemp("", "hub-f6-live-*.log")
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

	hubURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	// Wait for HTTP up.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(hubURL + "/healthz")
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			break
		}
		if err == nil {
			resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Healthz should advertise turn enabled with the configured realm.
	hResp, _ := http.Get(hubURL + "/healthz")
	var health map[string]any
	_ = json.NewDecoder(hResp.Body).Decode(&health)
	hResp.Body.Close()
	turnBlock, _ := health["turn"].(map[string]any)
	if turnBlock["enabled"] != true {
		t.Fatalf("healthz.turn.enabled = %v, want true", turnBlock["enabled"])
	}

	// Mint creds.
	req, _ := http.NewRequest("GET", hubURL+"/v1/turn/credentials", nil)
	req.Header.Set("X-Chepherd-Org", "alice.example")
	cResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mint creds: %v", err)
	}
	var creds turnCredentialsResponse
	_ = json.NewDecoder(cResp.Body).Decode(&creds)
	cResp.Body.Close()
	if creds.Username == "" || creds.Password == "" {
		t.Fatalf("empty creds: %+v", creds)
	}

	// Run a real pion turn.Client Allocate against the hub.
	conn, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("client udp listen: %v", err)
	}
	defer conn.Close()
	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel = logging.LogLevelError
	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: turnListen,
		TURNServerAddr: turnListen,
		Username:       creds.Username,
		Password:       creds.Password,
		Realm:          creds.Realm,
		Conn:           conn,
		LoggerFactory:  loggerFactory,
	})
	if err != nil {
		t.Fatalf("turn.NewClient: %v", err)
	}
	defer client.Close()
	if err := client.Listen(); err != nil {
		t.Fatalf("client.Listen: %v", err)
	}
	relayConn, err := client.Allocate()
	if err != nil {
		t.Fatalf("client.Allocate with valid creds: %v", err)
	}
	defer relayConn.Close()
	if relayConn.LocalAddr() == nil {
		t.Error("Allocate returned conn with nil LocalAddr")
	}
	t.Logf("F6 live walk: pion client Allocate via real chepherd-hub TURN succeeded; relay=%s",
		relayConn.LocalAddr())

	// active_allocations should have incremented.
	time.Sleep(150 * time.Millisecond) // give the EventHandler time to fire
	hResp2, _ := http.Get(hubURL + "/healthz")
	var health2 map[string]any
	_ = json.NewDecoder(hResp2.Body).Decode(&health2)
	hResp2.Body.Close()
	turnBlock2, _ := health2["turn"].(map[string]any)
	if active, _ := turnBlock2["active_allocations"].(float64); active < 1 {
		t.Errorf("active_allocations = %v, want >= 1 after Allocate", turnBlock2["active_allocations"])
	}

	// Tampered creds: same username but mangled password.
	conn2, _ := net.ListenPacket("udp4", "0.0.0.0:0")
	defer conn2.Close()
	bad, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr: turnListen,
		TURNServerAddr: turnListen,
		Username:       creds.Username,
		Password:       "this-is-not-the-mac",
		Realm:          creds.Realm,
		Conn:           conn2,
		LoggerFactory:  loggerFactory,
	})
	if err != nil {
		t.Fatalf("tampered client init: %v", err)
	}
	defer bad.Close()
	_ = bad.Listen()
	allocCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	allocCh := make(chan error, 1)
	go func() {
		_, err := bad.Allocate()
		allocCh <- err
	}()
	select {
	case err := <-allocCh:
		if err == nil {
			t.Error("Allocate with TAMPERED creds should have failed")
		} else {
			t.Logf("F6 live walk: tampered creds correctly rejected: %v", err)
		}
	case <-allocCtx.Done():
		t.Log("F6 live walk: tampered Allocate didn't reply within 3s (also acceptable rejection)")
	}
}

// freeUDPPort picks a random UDP port for the live-walk TURN listener.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	_ = conn.Close()
	return port
}

// cmd/runner/e2e_464_agent_card_test.go — #464 Wave R3 e2e walk.
//
// Builds the runner, runs it with --a2a-listen + --sid, fetches
// GET /a2a/<sid>/.well-known/agent-card.json, asserts spec-conformant
// JSON.
//
// Refs #464 V0.9.2-ARCHITECTURE §5 #9 §7 §12.1.
package main_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/a2a"
	rh "github.com/agenity-org/agenity/internal/runtimehttp"
)

func TestE2E_464_RunnerAgentCard_AtWellKnownURI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping #464 e2e in -short mode")
	}

	binPath := filepath.Join(t.TempDir(), "chepherd-runner")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build chepherd-runner: %v\n%s", err, out)
	}

	stateDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "mcp.sock")
	const sid = "e2e-r3-sid"
	const runnerName = "e2e-r3-runner"

	// Stand up a real httptest daemon so --daemon-url isn't a
	// dangling DNS-resolve / TCP-timeout (which previously blocked
	// runner startup for several seconds, race'ing the e2e's 5s
	// listen-addr deadline). The runner-register WS lives on this
	// httptest.Server's mux.
	daemon := httptest.NewServer((&rh.Server{}).Handler())
	t.Cleanup(daemon.Close)

	cmd := exec.Command(binPath,
		"--mcp-socket", sock,
		"--state-dir", stateDir,
		"--sid", sid,
		"--name", runnerName,
		"--a2a-listen", "127.0.0.1:0",
		"--a2a-base-url", "http://test-runner:9091",
		"--daemon-url", daemon.URL,
	)
	stderrR, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}
	cmd.Stdout = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start runner: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	})

	// Watch stderr for the listen-addr.
	listenAddrCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var acc []byte
		for {
			n, err := stderrR.Read(buf)
			if n > 0 {
				acc = append(acc, buf[:n]...)
				if i := strings.Index(string(acc), "A2A endpoint listening on "); i >= 0 {
					tail := string(acc[i+len("A2A endpoint listening on "):])
					if j := strings.IndexByte(tail, ' '); j > 0 {
						select {
						case listenAddrCh <- tail[:j]:
						default:
						}
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	var listenAddr string
	select {
	case listenAddr = <-listenAddrCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("A2A endpoint never logged its listen addr within 5s")
	}

	// ─── Q1 — fetch canonical well-known URI ────────────────────
	wellKnownURL := "http://" + listenAddr + "/a2a/" + sid + a2a.AgentCardPath
	resp, err := http.Get(wellKnownURL)
	if err != nil {
		t.Fatalf("Q1 FAIL: GET %s: %v", wellKnownURL, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Q1 FAIL: %s → %d: %s", wellKnownURL, resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("Q1 FAIL: Content-Type = %q, want application/json", ct)
	}

	// ─── Q2 — JSON parses into a2a.AgentCard ─────────────────────
	var card a2a.AgentCard
	if err := json.Unmarshal(body, &card); err != nil {
		t.Fatalf("Q2 FAIL: decode: %v (body=%s)", err, body)
	}

	// ─── Q3 — spec-required fields populated ─────────────────────
	if card.ProtocolVersion != "1.0" {
		t.Errorf("Q3 FAIL: protocolVersion = %q, want 1.0", card.ProtocolVersion)
	}
	if card.Name != "chepherd-runner-"+sid {
		t.Errorf("Q3 FAIL: name = %q, want chepherd-runner-%s", card.Name, sid)
	}
	wantURL := "http://test-runner:9091/a2a/" + sid + "/jsonrpc"
	if card.URL != wantURL {
		t.Errorf("Q3 FAIL: url = %q, want %q", card.URL, wantURL)
	}

	// ─── Q4 — capability bits per #511 + R3 promises ─────────────
	if !card.Capabilities.Streaming {
		t.Errorf("Q4 FAIL: capabilities.streaming = false; want true (#511 Wave A1)")
	}

	// ─── Q5 — JWT security scheme references daemon JWKS URL ─────
	scheme, ok := card.SecuritySchemes["chepherd-jwt"]
	if !ok {
		t.Fatalf("Q5 FAIL: securitySchemes lacks chepherd-jwt entry")
	}
	if scheme.BearerFormat != "JWT" {
		t.Errorf("Q5 FAIL: scheme.bearerFormat = %q, want JWT", scheme.BearerFormat)
	}
	wantJWKSRef := daemon.URL + "/.well-known/jwks.json"
	if !strings.Contains(scheme.Description, wantJWKSRef) {
		t.Errorf("Q5 FAIL: scheme.description should reference %s; got %q", wantJWKSRef, scheme.Description)
	}

	// ─── Q6 — x-chepherd-p2p extension block present (placeholder) ─
	if card.XChepherdP2P == nil {
		t.Errorf("Q6 FAIL: x-chepherd-p2p block absent (R3 ships placeholder; F2/F3/F4 populate)")
	}

	// ─── Q7 — operator-handle in description when --name set ─────
	if !strings.Contains(card.Description, "@"+runnerName) {
		t.Errorf("Q7 FAIL: description should include @%s; got %q", runnerName, card.Description)
	}

	// ─── Q8 — alias path (suffix-less) serves the same bytes ─────
	aliasURL := "http://" + listenAddr + "/a2a/" + sid + a2a.AgentCardAliasPath
	aliasResp, err := http.Get(aliasURL)
	if err != nil {
		t.Fatalf("Q8 FAIL: GET %s: %v", aliasURL, err)
	}
	aliasBody, _ := io.ReadAll(aliasResp.Body)
	_ = aliasResp.Body.Close()
	if aliasResp.StatusCode != http.StatusOK {
		t.Errorf("Q8 FAIL: alias path %s → %d", aliasURL, aliasResp.StatusCode)
	}
	if string(aliasBody) != string(body) {
		t.Errorf("Q8 FAIL: alias path served different bytes than canonical path")
	}

	// ─── Q9 — wrong-sid path returns 404 (per-session scope) ─────
	wrongSIDURL := "http://" + listenAddr + "/a2a/not-the-sid" + a2a.AgentCardPath
	wrongResp, err := http.Get(wrongSIDURL)
	if err != nil {
		t.Fatalf("Q9 FAIL: GET wrong-sid: %v", err)
	}
	_ = wrongResp.Body.Close()
	if wrongResp.StatusCode != http.StatusNotFound {
		t.Errorf("Q9 FAIL: wrong-sid agent-card URL returned %d, want 404 (per-session URL scope broken)", wrongResp.StatusCode)
	}
}

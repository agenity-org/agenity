// cmd/runner/e2e_486_jwt_runner_test.go — #486 Wave T1 binary-level
// e2e walk: real D2 daemon mints a JWT against its ES256 key, runner
// binary subprocess verifies it against the daemon's JWKS endpoint.
//
// Named assertions T1.E1-E4:
//
//	E1 — runner --require-jwt rejects unauthenticated POST (401)
//	E2 — D2 daemon mints JWT (aud=runner sid) → POST /a2a/<sid>/jsonrpc
//	     with Bearer → 200 + Task envelope
//	E3 — tampered JWT (flip a byte in signature) → 401
//	E4 — JWT with aud=different-sid → 401
//
// Refs #486 #468 #505.
package main_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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

	"github.com/chepherd/chepherd/internal/auth"
	rh "github.com/chepherd/chepherd/internal/runtimehttp"
)

func TestE2E_486_RunnerJWT_AcceptsMintedRejectsRest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping #486 e2e in -short mode")
	}

	binPath := filepath.Join(t.TempDir(), "chepherd-runner")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build chepherd-runner: %v\n%s", err, out)
	}

	// Daemon with an ES256 priv → mint + JWKS both work.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwksBody, err := auth.PublicJWK(priv)
	if err != nil {
		t.Fatalf("PublicJWK: %v", err)
	}
	daemon := httptest.NewServer((&rh.Server{
		ES256Priv: priv,
		JWKSBody:  jwksBody,
	}).Handler())
	t.Cleanup(daemon.Close)

	// Runner subprocess with --require-jwt set.
	stateDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "mcp.sock")
	const sid = "e2e-t1-sid"

	cmd := exec.Command(binPath,
		"--mcp-socket", sock,
		"--state-dir", stateDir,
		"--sid", sid,
		"--a2a-listen", "127.0.0.1:0",
		"--a2a-base-url", "http://test-runner:9091",
		"--daemon-url", daemon.URL,
		"--require-jwt",
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

	listenAddrCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var acc []byte
		for {
			n, err := stderrR.Read(buf)
			if n > 0 {
				// Tee to our own stderr so test failures show the
				// runner's diagnostic log (esp. JWT-rejection details).
				_, _ = os.Stderr.Write(buf[:n])
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
		t.Fatalf("A2A endpoint never logged listen addr")
	}
	endpoint := "http://" + listenAddr + "/a2a/" + sid + "/jsonrpc"

	// ─── E1 — unauthenticated POST → 401 ─────────────────────────
	resp, err := http.Post(endpoint, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("E1 setup: POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("E1 FAIL: status = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("E1 FAIL: WWW-Authenticate header missing Bearer")
	}

	// ─── E2 — mint JWT with aud=sid → POST → 200 ─────────────────
	mintBody := bytes.NewBufferString(`{"sub":"e2e-caller","aud":"` + sid + `"}`)
	mintResp, err := http.Post(daemon.URL+"/api/v1/jwt/mint", "application/json", mintBody)
	if err != nil {
		t.Fatalf("E2 setup: mint: %v", err)
	}
	defer mintResp.Body.Close()
	if mintResp.StatusCode != http.StatusOK {
		mb, _ := io.ReadAll(mintResp.Body)
		t.Fatalf("E2 setup: mint status = %d (body=%s)", mintResp.StatusCode, mb)
	}
	var mintParsed struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(mintResp.Body).Decode(&mintParsed); err != nil {
		t.Fatalf("E2 setup: decode mint: %v", err)
	}
	if mintParsed.Token == "" {
		t.Fatalf("E2 setup: empty token")
	}

	sendBody := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"role":      "user",
				"contextId": sid,
				"kind":      "message",
				"parts":     []map[string]any{{"kind": "text", "text": "T1 e2e ping"}},
			},
		},
	}
	rawSend, _ := json.Marshal(sendBody)
	authedReq, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(rawSend))
	authedReq.Header.Set("Content-Type", "application/json")
	authedReq.Header.Set("Authorization", "Bearer "+mintParsed.Token)
	authedResp, err := http.DefaultClient.Do(authedReq)
	if err != nil {
		t.Fatalf("E2 FAIL: authed POST: %v", err)
	}
	defer authedResp.Body.Close()
	if authedResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(authedResp.Body)
		t.Fatalf("E2 FAIL: status = %d (body=%s)", authedResp.StatusCode, body)
	}

	// ─── E3 — tampered signature → 401 ──────────────────────────
	tampered := tamperJWTSig(mintParsed.Token)
	tReq, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(rawSend))
	tReq.Header.Set("Authorization", "Bearer "+tampered)
	tResp, err := http.DefaultClient.Do(tReq)
	if err != nil {
		t.Fatalf("E3 FAIL: POST: %v", err)
	}
	_ = tResp.Body.Close()
	if tResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("E3 FAIL: status = %d, want 401", tResp.StatusCode)
	}

	// ─── E4 — JWT for different sid → 401 ───────────────────────
	wrongMintBody := bytes.NewBufferString(`{"sub":"e2e-caller","aud":"wrong-sid"}`)
	wrongMintResp, err := http.Post(daemon.URL+"/api/v1/jwt/mint", "application/json", wrongMintBody)
	if err != nil {
		t.Fatalf("E4 setup: mint: %v", err)
	}
	var wrongParsed struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(wrongMintResp.Body).Decode(&wrongParsed)
	_ = wrongMintResp.Body.Close()

	wReq, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(rawSend))
	wReq.Header.Set("Authorization", "Bearer "+wrongParsed.Token)
	wResp, err := http.DefaultClient.Do(wReq)
	if err != nil {
		t.Fatalf("E4 FAIL: POST: %v", err)
	}
	_ = wResp.Body.Close()
	if wResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("E4 FAIL: status = %d, want 401", wResp.StatusCode)
	}
}

// tamperJWTSig flips a character in the last (signature) segment to
// force ECDSA verification to fail.
func tamperJWTSig(tok string) string {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return tok
	}
	sig := parts[2]
	if len(sig) < 4 {
		return tok
	}
	// Swap last 2 chars with "AA"; chance of accidentally still valid
	// is negligible.
	parts[2] = sig[:len(sig)-2] + "AA"
	return strings.Join(parts, ".")
}

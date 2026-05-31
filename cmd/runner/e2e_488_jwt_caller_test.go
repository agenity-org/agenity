// cmd/runner/e2e_488_jwt_caller_test.go — Wave AU1 + T1 #486
// integration: when JWT verification is on, the JWT sub claim
// MUST propagate into the AuditEvent.Caller field (audit
// attribution closes the trust gap).
//
// Named assertion AU1.W4:
//
//	W4 — daemon receives audit.event with caller equal to the JWT
//	     sub claim that was set on the inbound POST
//
// Refs #488 #486.
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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chepherd/chepherd/internal/auth"
	rh "github.com/chepherd/chepherd/internal/runtimehttp"
)

func TestE2E_488_W4_JWTCallerPropagatesIntoAuditEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping #488/T1 integration e2e in -short mode")
	}

	binPath := filepath.Join(t.TempDir(), "chepherd-runner")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build chepherd-runner: %v\n%s", err, out)
	}

	// Stand up a daemon that BOTH:
	//   (a) accepts the runner's register WS + records audit frames
	//   (b) serves /api/v1/jwt/mint + /.well-known/jwks.json (T1 D2 #468 + T2 #505)
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwksBody, err := auth.PublicJWK(priv)
	if err != nil {
		t.Fatalf("PublicJWK: %v", err)
	}

	var (
		framesMu sync.Mutex
		frames   []map[string]any
	)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	// runtimehttp.Server gives us /api/v1/jwt/mint + /.well-known/jwks.json
	// for free. We layer the runners-register WS handler on top via a
	// composite handler.
	rhServer := (&rh.Server{ES256Priv: priv, JWKSBody: jwksBody}).Handler()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runners/register", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_, _, _ = c.ReadMessage()
		_ = c.WriteJSON(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{
				"sid":            "e2e-au1-w4-sid",
				"daemon_version": "test",
				"audit_topic":    "runner:e2e-au1-w4-sid",
			},
		})
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			var f map[string]any
			if json.Unmarshal(raw, &f) != nil {
				continue
			}
			framesMu.Lock()
			frames = append(frames, f)
			framesMu.Unlock()
		}
	})
	// Fall through everything else to runtimehttp (which serves mint +
	// jwks).
	mux.Handle("/", rhServer)
	daemon := httptest.NewServer(mux)
	t.Cleanup(daemon.Close)

	stateDir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "mcp.sock")
	const sid = "e2e-au1-w4-sid"

	cmd := exec.Command(binPath,
		"--mcp-socket", sock,
		"--state-dir", stateDir,
		"--sid", sid,
		"--a2a-listen", "127.0.0.1:0",
		"--daemon-url", daemon.URL,
		"--require-jwt", // T1 + AU1 integration path
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

	// Mint a JWT with sub="caller-X" + aud=<runner sid>.
	mintBody := bytes.NewBufferString(`{"sub":"caller-X","aud":"` + sid + `"}`)
	mintResp, err := http.Post(daemon.URL+"/api/v1/jwt/mint", "application/json", mintBody)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	defer mintResp.Body.Close()
	var mintParsed struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(mintResp.Body).Decode(&mintParsed); err != nil {
		t.Fatalf("decode mint: %v", err)
	}
	if mintParsed.Token == "" {
		t.Fatalf("empty token")
	}

	// POST with Bearer.
	sendBody := map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "message/send",
		"params": map[string]any{
			"message": map[string]any{
				"role":      "user",
				"contextId": sid,
				"kind":      "message",
				"parts":     []map[string]any{{"kind": "text", "text": "W4 e2e"}},
			},
		},
	}
	raw, _ := json.Marshal(sendBody)
	req, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mintParsed.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}

	// Wait for audit.event arrival + assert caller field == sub claim.
	deadline := time.Now().Add(2 * time.Second)
	var auditEv map[string]any
	for time.Now().Before(deadline) {
		framesMu.Lock()
		for _, f := range frames {
			if m, _ := f["method"].(string); m == "audit.event" {
				auditEv = f
				break
			}
		}
		framesMu.Unlock()
		if auditEv != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if auditEv == nil {
		t.Fatalf("W4 FAIL: daemon never received audit.event frame within 2s")
	}
	params, ok := auditEv["params"].(map[string]any)
	if !ok {
		t.Fatalf("W4 FAIL: audit.event has no params object")
	}
	if got, _ := params["caller"].(string); got != "caller-X" {
		t.Errorf("W4 FAIL: caller = %q, want caller-X (JWT sub claim must propagate via auth.SubjectFromRunnerContext)", got)
	}
}

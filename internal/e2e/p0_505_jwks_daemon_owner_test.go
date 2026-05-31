// internal/e2e/p0_505_jwks_daemon_owner_test.go boots the real
// chepherd binary and asserts the v0.9.4 §15.2 + §22 daemon-owned
// JWKS endpoint serves the multi-key wire shape produced by the
// new KeyStore (#505 Wave T2).
//
// Acceptance:
//   - GET /.well-known/jwks.json on the daemon returns 200
//   - The document is the canonical {keys:[...]} JWKS shape
//   - At least one entry is present, with kty/crv/alg/kid fields
//   - The mint endpoint (#508) produces a JWS whose header kid
//     matches one of the JWKS-published kids (proves the daemon
//     signs with a key the same daemon also publishes — runners
//     fetching the JWKS will verify against the active key)
//
// Refs #505 V0.9.2-ARCHITECTURE.md §15.2 §22 §23.
package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestV094Walk_RealServerDaemonOwnedJWKS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-binary boot in -short mode")
	}

	gomodOut, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	gomod := strings.TrimSpace(string(gomodOut))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("repo go.mod not found")
	}
	repoRoot := filepath.Dir(gomod)

	binPath := filepath.Join(t.TempDir(), "chepherd-e2e-t2")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	httpPort := freeTCPPort(t)
	mcpPort := freeTCPPort(t)
	httpAddr := fmt.Sprintf("127.0.0.1:%d", httpPort)
	mcpAddr := fmt.Sprintf("127.0.0.1:%d", mcpPort)

	stateDir := t.TempDir()
	cmd := exec.Command(binPath,
		"run",
		"--headless",
		"--no-shepherd=true",
		"--listen", httpAddr,
		"--mcp-listen", mcpAddr,
		"--state-dir", stateDir,
	)
	logFile, _ := os.CreateTemp("", "chepherd-e2e-t2-*.log")
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
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
		if t.Failed() {
			if b, err := os.ReadFile(logFile.Name()); err == nil {
				t.Logf("chepherd binary log:\n%s", b)
			}
		}
	})

	if err := waitForHTTPOK(httpAddr, "/healthz", 10*time.Second); err != nil {
		t.Fatalf("/healthz never came up: %v", err)
	}

	// Assertion 1 — daemon owns the JWKS endpoint, returns valid shape.
	jwksResp, err := http.Get("http://" + httpAddr + "/.well-known/jwks.json")
	if err != nil {
		t.Fatalf("GET jwks: %v", err)
	}
	defer jwksResp.Body.Close()
	if jwksResp.StatusCode != http.StatusOK {
		t.Fatalf("jwks status = %d, want 200", jwksResp.StatusCode)
	}
	if cc := jwksResp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (rotations must invalidate caches)", cc)
	}
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			Kid string `json:"kid"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(jwksResp.Body).Decode(&jwks); err != nil {
		t.Fatalf("decode jwks: %v", err)
	}
	if len(jwks.Keys) == 0 {
		t.Fatal("jwks empty — daemon failed to expose any key")
	}
	publishedKIDs := map[string]bool{}
	for _, k := range jwks.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || k.Alg != "ES256" || k.Use != "sig" {
			t.Errorf("JWKS entry shape wrong: %+v", k)
		}
		if k.Kid == "" {
			t.Error("JWKS entry has empty kid")
		}
		publishedKIDs[k.Kid] = true
	}

	// Assertion 2 — a minted JWT's JOSE header kid matches a kid the
	// daemon publishes. Together this proves the daemon signs with a
	// key in its own JWKS (the architectural invariant T2 enforces).
	//
	// The mint endpoint is gated by the #468 GrantCheck seam. In a
	// main where #469 (Wave D3) has merged the seam is wired to a
	// real check and an unseeded mint returns 403; in a main where
	// only D2 has merged the seam stays stub-allow-all and mint
	// returns 200 unconditionally. T2 must remain green in both
	// states — try mint first; if it 403s, seed a grant via the D3
	// CRUD surface and retry. The kid-match assertion is independent
	// of which gate fired.
	tokenBytes, err := os.ReadFile(filepath.Join(stateDir, "auth.printed"))
	if err != nil {
		t.Fatalf("read bootstrap token: %v", err)
	}
	bearer := strings.TrimSpace(string(tokenBytes))

	mintBodyStr := `{"sub":"caller","aud":"tgt"}`
	mintOnce := func() *http.Response {
		req, _ := http.NewRequest(http.MethodPost,
			"http://"+httpAddr+"/api/v1/jwt/mint",
			strings.NewReader(mintBodyStr))
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		return resp
	}
	mintResp := mintOnce()
	if mintResp.StatusCode == http.StatusForbidden {
		mintResp.Body.Close()
		grantBody := `{
			"granter_org":"org-X","grantee_org":"org-Y",
			"scope":{"type":"agent","agent_sid":"tgt"},
			"permissions":["call_agent"],"accepted":true,
			"created_by":"e2e-t2"
		}`
		grantReq, _ := http.NewRequest(http.MethodPost,
			"http://"+httpAddr+"/api/v1/grants",
			strings.NewReader(grantBody))
		grantReq.Header.Set("Authorization", "Bearer "+bearer)
		grantReq.Header.Set("Content-Type", "application/json")
		grantResp, err := http.DefaultClient.Do(grantReq)
		if err != nil {
			t.Fatalf("POST grant: %v", err)
		}
		grantResp.Body.Close()
		if grantResp.StatusCode != http.StatusCreated {
			t.Fatalf("post-403 grant seed status = %d, want 201 (D3 endpoint)", grantResp.StatusCode)
		}
		mintResp = mintOnce()
	}
	defer mintResp.Body.Close()
	if mintResp.StatusCode != http.StatusOK {
		t.Fatalf("mint status = %d, want 200", mintResp.StatusCode)
	}
	var mintBody struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(mintResp.Body).Decode(&mintBody); err != nil {
		t.Fatalf("decode mint: %v", err)
	}
	parts := strings.Split(mintBody.Token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}
	hdr, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header struct {
		KID string `json:"kid"`
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(hdr, &header); err != nil {
		t.Fatalf("parse header: %v", err)
	}
	if header.Alg != "ES256" {
		t.Errorf("token alg = %q, want ES256", header.Alg)
	}
	if header.KID == "" {
		t.Fatal("token kid empty")
	}
	if !publishedKIDs[header.KID] {
		t.Errorf("token kid %q not in published JWKS %v", header.KID, publishedKIDs)
	}
}

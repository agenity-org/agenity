// internal/e2e/p0_468_jwt_mint_test.go boots the real chepherd
// binary and asserts the v0.9.4 §15.2 JWT mint endpoint is wired
// into the production HTTP surface. Closes the in-process-test-
// only loophole (PR #214 lesson — cmd/run.go must register the
// route, not just the unit-test mux).
//
// Acceptance:
//   - POST /api/v1/jwt/mint with valid bearer + body returns 200 +
//     a non-empty token
//   - The token's signature verifies against the same daemon's
//     published JWKS (round-trip cryptographic proof)
//
// Refs #468 V0.9.2-ARCHITECTURE.md §15.2.
package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/auth"
)

func TestV094Walk_RealServerMintsJWT(t *testing.T) {
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

	binPath := filepath.Join(t.TempDir(), "chepherd-e2e-d2")
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
	logFile, _ := os.CreateTemp("", "chepherd-e2e-d2-*.log")
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

	tokenBytes, err := os.ReadFile(filepath.Join(stateDir, "auth.printed"))
	if err != nil {
		t.Fatalf("read bootstrap token: %v", err)
	}
	bearer := strings.TrimSpace(string(tokenBytes))

	// #469 Wave D3 wired Server.GrantCheck = PersistenceGrantCheck in
	// the production cmd/run.go path. The mint endpoint now enforces
	// grants. Assert BOTH the deny path (no grant pre-seeded) AND the
	// allow path (agent-scoped grant covers the target) before claiming
	// the mint pipeline is healthy end-to-end.

	// 1. Pre-grant — mint MUST return 403. Proves the production
	//    wiring is live and observable.
	preReq, _ := http.NewRequest(
		http.MethodPost,
		"http://"+httpAddr+"/api/v1/jwt/mint",
		strings.NewReader(`{"sub":"alpha","aud":"bravo"}`),
	)
	preReq.Header.Set("Authorization", "Bearer "+bearer)
	preReq.Header.Set("Content-Type", "application/json")
	preResp, err := http.DefaultClient.Do(preReq)
	if err != nil {
		t.Fatalf("pre-grant POST /api/v1/jwt/mint: %v", err)
	}
	preResp.Body.Close()
	if preResp.StatusCode != http.StatusForbidden {
		t.Fatalf("pre-grant mint status = %d, want 403 (PersistenceGrantCheck must reject)", preResp.StatusCode)
	}

	// 2. Seed an agent-scoped grant covering caller=alpha → target=bravo
	//    via POST /api/v1/grants (the #469 D3 CRUD surface).
	grantBody := `{
		"granter_org": "org-X",
		"grantee_org": "org-Y",
		"scope": {"type":"agent","agent_sid":"bravo"},
		"permissions": ["call_agent"],
		"accepted": true,
		"created_by": "e2e-test"
	}`
	grantReq, _ := http.NewRequest(
		http.MethodPost,
		"http://"+httpAddr+"/api/v1/grants",
		strings.NewReader(grantBody),
	)
	grantReq.Header.Set("Authorization", "Bearer "+bearer)
	grantReq.Header.Set("Content-Type", "application/json")
	grantResp, err := http.DefaultClient.Do(grantReq)
	if err != nil {
		t.Fatalf("POST /api/v1/grants: %v", err)
	}
	grantResp.Body.Close()
	if grantResp.StatusCode != http.StatusCreated {
		t.Fatalf("grant POST status = %d, want 201", grantResp.StatusCode)
	}

	// 3. Post-grant — mint MUST now return 200.
	mintReq, _ := http.NewRequest(
		http.MethodPost,
		"http://"+httpAddr+"/api/v1/jwt/mint",
		strings.NewReader(`{"sub":"alpha","aud":"bravo"}`),
	)
	mintReq.Header.Set("Authorization", "Bearer "+bearer)
	mintReq.Header.Set("Content-Type", "application/json")
	mintResp, err := http.DefaultClient.Do(mintReq)
	if err != nil {
		t.Fatalf("POST /api/v1/jwt/mint: %v", err)
	}
	defer mintResp.Body.Close()
	if mintResp.StatusCode != http.StatusOK {
		t.Fatalf("post-grant mint status = %d, want 200", mintResp.StatusCode)
	}
	var mintBody struct {
		Token        string `json:"token"`
		ExpInSeconds int    `json:"exp_in_seconds"`
	}
	if err := json.NewDecoder(mintResp.Body).Decode(&mintBody); err != nil {
		t.Fatalf("decode mint: %v", err)
	}
	if mintBody.Token == "" {
		t.Fatal("mint token empty")
	}
	if mintBody.ExpInSeconds != 60 {
		t.Errorf("exp_in_seconds = %d, want 60", mintBody.ExpInSeconds)
	}

	// Fetch the JWKS document (unauthenticated; spec-required public).
	jwksResp, err := http.Get("http://" + httpAddr + "/.well-known/jwks.json")
	if err != nil {
		t.Fatalf("GET /.well-known/jwks.json: %v", err)
	}
	defer jwksResp.Body.Close()
	if jwksResp.StatusCode != http.StatusOK {
		t.Fatalf("jwks status = %d, want 200", jwksResp.StatusCode)
	}
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
			Alg string `json:"alg"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(jwksResp.Body).Decode(&jwks); err != nil {
		t.Fatalf("decode jwks: %v", err)
	}
	if len(jwks.Keys) == 0 {
		t.Fatal("jwks has no keys")
	}
	pub, err := jwkToECDSA(jwks.Keys[0].X, jwks.Keys[0].Y)
	if err != nil {
		t.Fatalf("jwk → ecdsa: %v", err)
	}

	claims, err := auth.VerifyJWS(pub, mintBody.Token)
	if err != nil {
		t.Fatalf("VerifyJWS against daemon's published JWKS: %v", err)
	}
	if claims["sub"] != "alpha" {
		t.Errorf("sub = %v, want alpha", claims["sub"])
	}
	if claims["aud"] != "bravo" {
		t.Errorf("aud = %v, want bravo", claims["aud"])
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if int(exp-iat) != 60 {
		t.Errorf("exp-iat = %d, want 60", int(exp-iat))
	}
}

func jwkToECDSA(xB64, yB64 string) (*ecdsa.PublicKey, error) {
	xb, err := base64.RawURLEncoding.DecodeString(xB64)
	if err != nil {
		return nil, fmt.Errorf("decode x: %w", err)
	}
	yb, err := base64.RawURLEncoding.DecodeString(yB64)
	if err != nil {
		return nil, fmt.Errorf("decode y: %w", err)
	}
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(xb),
		Y:     new(big.Int).SetBytes(yb),
	}, nil
}

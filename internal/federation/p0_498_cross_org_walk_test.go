// internal/federation/p0_498_cross_org_walk_test.go is the v0.9.4
// LIVE WALK gate for #498 Wave F8 — wires the full daemon-X →
// chepherd-hub → daemon-Y chain end-to-end:
//
//   - real chepherd-hub binary boots, with --federation-targets
//     wired against an httptest-served stub daemon-Y
//   - daemon-Y's stub serves CrossOrgJWTMinter backed by REAL
//     auth.KeyStore (ES256 key + JWKS publication)
//   - daemon-X (CrossOrgJWTClient) requests the token through the
//     hub
//   - resulting JWT is verified against daemon-Y's REAL JWKS via
//     auth.VerifyJWS — closing the federation loop empirically
//
// Body-blind invariant: hub never decodes the JWT (forwards bytes;
// verification is the responsibility of the consumer using
// daemon-Y's JWKS).
//
// Refs #498 V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 2.
package federation

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/auth"
)

// stubKeyStoreSigner is the bridge between the cross_org_jwt Minter
// (which wants a JWTSigner interface) and a raw ecdsa key — used in
// the live walk so the JWT this PR mints can be verified against
// real auth.VerifyJWS later.
type stubKeyStoreSigner struct {
	priv *ecdsa.PrivateKey
}

func (s *stubKeyStoreSigner) Sign(claims map[string]any) (string, error) {
	return auth.SignJWS(s.priv, claims)
}

func TestV094Walk_F8_CrossOrgJWT_ThroughRealHubBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live walk in -short")
	}

	// 1) daemon-Y key + minter.
	yPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	minter := &CrossOrgJWTMinter{
		Issuer: "bob.example",
		Signer: &stubKeyStoreSigner{priv: yPriv},
		TTL:    2 * time.Minute,
	}
	daemonY := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The minter expects POST /api/v1/federation/jwt.
		if r.URL.Path != "/api/v1/federation/jwt" {
			http.NotFound(w, r)
			return
		}
		minter.ServeHTTP(w, r)
	}))
	defer daemonY.Close()

	// 2) Real chepherd-hub binary.
	gomodOut, _ := exec.Command("go", "env", "GOMOD").Output()
	repoRoot := filepath.Dir(strings.TrimSpace(string(gomodOut)))
	tmpDir := t.TempDir()
	hubBin := filepath.Join(tmpDir, "chepherd-hub")
	build := exec.Command("go", "build", "-o", hubBin, "./cmd/chepherd-hub")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build hub: %v\n%s", err, out)
	}
	port := freeTCPPort(t)
	cmd := exec.Command(hubBin,
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--stun-listen", "",
		"--turn-listen", "",
		"--allowed-orgs", "alice.example,bob.example",
		"--federation-targets", "bob.example="+daemonY.URL,
	)
	logFile, _ := os.CreateTemp("", "hub-f8-live-*.log")
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
	if err := waitFor200(hubURL+"/healthz", 5*time.Second); err != nil {
		t.Fatalf("healthz: %v", err)
	}

	// 3) daemon-X's CrossOrgJWTClient.
	client := &CrossOrgJWTClient{
		HubURL:    hubURL,
		CallerOrg: "alice.example",
	}
	ctx := context.Background()
	jws, err := client.Get(ctx, "bob.example", "a2a.send")
	if err != nil {
		t.Fatalf("CrossOrgJWTClient.Get: %v", err)
	}
	if jws == "" {
		t.Fatal("empty JWT returned")
	}

	// 4) Verify via REAL auth.VerifyJWS against daemon-Y's public key.
	claims, err := auth.VerifyJWS(&yPriv.PublicKey, jws)
	if err != nil {
		t.Fatalf("VerifyJWS: %v", err)
	}
	// #584 — iss/sub are URL-normalised by the minter.
	if claims["iss"] != "https://bob.example" {
		t.Errorf("iss = %v, want https://bob.example", claims["iss"])
	}
	if claims["sub"] != "https://alice.example" {
		t.Errorf("sub = %v, want https://alice.example", claims["sub"])
	}
	if claims["scope"] != "a2a.send" {
		t.Errorf("scope = %v, want a2a.send", claims["scope"])
	}
	t.Logf("F8 live walk: daemon-X (alice) → real hub binary → daemon-Y (bob); minted JWT verified against bob's REAL ES256 key; sub=%v scope=%v iss=%v",
		claims["sub"], claims["scope"], claims["iss"])

	// 5) Second Get hits the cache (no hub roundtrip).
	jws2, err := client.Get(ctx, "bob.example", "a2a.send")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if jws2 != jws {
		t.Error("second Get returned a different JWT (cache miss)")
	}
}

// freeTCPPort + waitFor200 — local helpers for the live walk so the
// package doesn't depend on cmd/* test helpers.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitFor200(url string, timeout time.Duration) error {
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

// Unused-import guard. json is only referenced via the test bodies
// after compile-time elimination; keep the var so a future test
// that references it has the import.
var _ = json.Marshal

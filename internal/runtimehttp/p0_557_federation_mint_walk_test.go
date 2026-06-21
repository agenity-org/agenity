// internal/runtimehttp/p0_557_federation_mint_walk_test.go is the
// v0.9.4 §10 Pattern 2 Phase 2 LIVE WALK gate for #557 Wave F8.1 —
// wires a REAL auth.KeyStore via the sqlite-backed AuthSecret
// repository, mounts the cross-org JWT mint endpoint on a real
// httptest server WITH production authMiddleware (#583), simulates
// the hub-attesting headers F8 #498 supplies, and verifies the
// minted JWT against the daemon's REAL JWKS — closing the trust
// loop empirically.
//
// #583 — added authMiddleware wrapper + auth probes (no-token → 401,
// valid Bearer → 200) so CI catches auth chain regressions.
//
// Refs #557 #498 #583 V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 2.
package runtimehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/auth"
	"github.com/agenity-org/agenity/internal/persistence/sqlite"
)

func TestV094Walk_F81_CrossOrgMint_RealKeyStore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live walk in -short")
	}

	// 1) Real sqlite-backed AuthSecret repo + KeyStore.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "f81-walk.db")
	store, err := sqlite.NewStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()
	ks, err := auth.LoadOrCreateKeyStore(context.Background(), store.AuthSecrets(), 0)
	if err != nil {
		t.Fatalf("LoadOrCreateKeyStore: %v", err)
	}

	// 2) Mount the cross-org JWT minter on a real mux WRAPPED IN
	//    authMiddleware (#583). The production Handler() always wraps
	//    the mux this way; tests that skip the wrapper miss auth
	//    chain regressions.
	const testAuthToken = "test-auth-token-f81-walk"
	srv := &Server{
		OrgID:     "bob.example",
		KeyStore:  ks,
		AuthToken: testAuthToken,
		// #639/#583 — wire an explicit allow-all check so the walk
		// test exercises the happy path without a real grant store.
		// Production cmd/run.go wires a store-backed check instead.
		CrossOrgGrantCheck: func(_, _ string) error { return nil },
	}
	mux := http.NewServeMux()
	srv.mountCrossOrgFederationMint(mux)
	// Wrap with production authMiddleware so /api/v1/* requires Bearer.
	hs := httptest.NewServer(srv.authMiddleware(mux))
	defer hs.Close()

	// 3a) #583 — probe auth chain: no Bearer → 401.
	reqNoAuth, _ := http.NewRequest("POST", hs.URL+"/api/v1/federation/jwt",
		strings.NewReader(`{"scope":"a2a.send","audience":"runner-7"}`))
	reqNoAuth.Header.Set("Content-Type", "application/json")
	reqNoAuth.Header.Set("X-Chepherd-Caller-Org", "alice.example")
	reqNoAuth.Header.Set("X-Chepherd-Hub-Attest", "true")
	respNoAuth, err := http.DefaultClient.Do(reqNoAuth)
	if err != nil {
		t.Fatalf("no-auth probe: %v", err)
	}
	respNoAuth.Body.Close()
	if respNoAuth.StatusCode != http.StatusUnauthorized {
		t.Errorf("#583 no-auth probe: status = %d, want 401 (authMiddleware not firing)", respNoAuth.StatusCode)
	}

	// 3b) Simulate the F8 hub forwarding a daemon-X request with auth.
	body := `{"scope":"a2a.send","audience":"runner-7"}`
	req, _ := http.NewRequest("POST", hs.URL+"/api/v1/federation/jwt",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAuthToken)
	req.Header.Set("X-Chepherd-Caller-Org", "alice.example")
	req.Header.Set("X-Chepherd-Hub-Attest", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var mintResp struct {
		JWT     string `json:"jwt"`
		Issuer  string `json:"iss"`
		Expires int64  `json:"exp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&mintResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if mintResp.JWT == "" {
		t.Fatal("empty JWT in mint response")
	}
	// #584 — iss must be a URL, not a bare org ID.
	if mintResp.Issuer != "https://bob.example" {
		t.Errorf("iss = %q, want https://bob.example", mintResp.Issuer)
	}
	if mintResp.Expires <= time.Now().Unix() {
		t.Errorf("exp = %d, want future", mintResp.Expires)
	}

	// 4) Verify the JWT against the REAL KeyStore via its own
	// Verify method (uses the kid in the JOSE header to pick the
	// right key — including across rotations).
	claims, err := ks.Verify(mintResp.JWT)
	if err != nil {
		t.Fatalf("KeyStore.Verify: %v", err)
	}
	// #584 — sub must be a URL form of the caller org ID.
	if claims["sub"] != "https://alice.example" {
		t.Errorf("sub = %v, want https://alice.example", claims["sub"])
	}
	if claims["scope"] != "a2a.send" {
		t.Errorf("scope = %v, want a2a.send", claims["scope"])
	}
	if claims["aud"] != "runner-7" {
		t.Errorf("aud = %v, want runner-7", claims["aud"])
	}
	t.Logf("F8.1 live walk: cross-org mint via real Server.KeyStore (sqlite-backed); minted JWT verified against daemon's REAL JWKS; sub=%v scope=%v iss=%v",
		claims["sub"], claims["scope"], claims["iss"])
}

// internal/runtimehttp/p0_557_federation_mint_walk_test.go is the
// v0.9.4 §10 Pattern 2 Phase 2 LIVE WALK gate for #557 Wave F8.1 —
// wires a REAL auth.KeyStore via the sqlite-backed AuthSecret
// repository, mounts the cross-org JWT mint endpoint on a real
// httptest server, simulates the hub-attesting headers F8 #498
// supplies, and verifies the minted JWT against the daemon's REAL
// JWKS — closing the trust loop empirically.
//
// Refs #557 #498 V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 2.
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

	"github.com/chepherd/chepherd/internal/auth"
	"github.com/chepherd/chepherd/internal/persistence/sqlite"
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

	// 2) Build the federation handler using the production path (FederationHandler,
	// not raw mux) with AuthToken set so authMiddleware would block if it were wired.
	// Guards against regression of #562/#583: a raw-mux test would pass even when
	// the federation listener incorrectly uses the auth-gated Handler().
	srv := &Server{
		OrgID:     "bob.example",
		KeyStore:  ks,
		AuthToken: "sentinel-bearer-token-that-hub-does-not-send",
	}
	hs := httptest.NewServer(srv.FederationHandler())
	defer hs.Close()

	// Auth-chain guard: same path on dashboard Handler() must return 401 (no Bearer).
	hsAuth := httptest.NewServer(srv.Handler())
	defer hsAuth.Close()
	authCheck, _ := http.NewRequest("POST", hsAuth.URL+"/api/v1/federation/jwt",
		strings.NewReader(`{}`))
	authCheck.Header.Set("Content-Type", "application/json")
	if ar, err2 := http.DefaultClient.Do(authCheck); err2 == nil {
		defer ar.Body.Close()
		if ar.StatusCode != http.StatusUnauthorized {
			t.Errorf("dashboard Handler returned %d for unauthenticated request; want 401 (#583 guard)", ar.StatusCode)
		}
	}

	// 3) Simulate the F8 hub forwarding a daemon-X request:
	//    POST /api/v1/federation/jwt with the attesting headers.
	body := `{"scope":"a2a.send","audience":"runner-7"}`
	req, _ := http.NewRequest("POST", hs.URL+"/api/v1/federation/jwt",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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
	if mintResp.Issuer != "bob.example" {
		t.Errorf("iss = %q, want bob.example", mintResp.Issuer)
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
	if claims["sub"] != "alice.example" {
		t.Errorf("sub = %v, want alice.example", claims["sub"])
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

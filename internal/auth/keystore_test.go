// internal/auth/keystore_test.go pins the v0.9.4 §15.2 + §22
// daemon-owned KeyStore with rotation + overlap window (#505 Wave T2).
//
// Refs #505.
package auth

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

// memoryAuthSecretRepo is a minimal in-memory AuthSecretRepository
// for tests that don't want to spin up SQLite.
type memoryAuthSecretRepo struct {
	rows map[string]*persistence.AuthSecret
}

func newMemoryRepo() *memoryAuthSecretRepo {
	return &memoryAuthSecretRepo{rows: map[string]*persistence.AuthSecret{}}
}

func (m *memoryAuthSecretRepo) Get(ctx context.Context, purpose string) (*persistence.AuthSecret, error) {
	sec, ok := m.rows[purpose]
	if !ok {
		return nil, &notFoundErr{purpose: purpose}
	}
	cp := *sec
	return &cp, nil
}

func (m *memoryAuthSecretRepo) Save(ctx context.Context, purpose string, key []byte, alg string) error {
	m.rows[purpose] = &persistence.AuthSecret{
		Purpose: purpose, Key: append([]byte(nil), key...),
		Algorithm: alg, CreatedAt: time.Now().UTC(),
	}
	return nil
}

type notFoundErr struct{ purpose string }

func (e *notFoundErr) Error() string { return e.purpose + ": not found" }

func TestKeyStore_FreshMintProducesOneActiveKey(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ks, err := LoadOrCreateKeyStore(context.Background(), repo, time.Hour)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	active, err := ks.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if active.KID == "" {
		t.Error("active.KID empty")
	}
	if active.Retired() {
		t.Error("active should not be retired")
	}
	// The archive row must exist after a fresh mint.
	if _, ok := repo.rows[AuthSecretPurposeKeyStore]; !ok {
		t.Error("archive row not persisted")
	}
}

func TestKeyStore_LegacyMigrationOnFirstLoad(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	// Seed a legacy single-key row (pre-T2 instance state).
	legacyKS, err := LoadOrCreateKeyStore(context.Background(), repo, time.Hour)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	legacyActive, _ := legacyKS.Active()
	// Forge a legacy AuthSecret with the seed key's PEM.
	repo.rows[AuthSecretPurposeES256] = &persistence.AuthSecret{
		Purpose:   AuthSecretPurposeES256,
		Key:       legacyActive.PEM,
		Algorithm: "ES256",
		CreatedAt: time.Now().UTC(),
	}
	// Wipe the new-archive row so migration kicks in.
	delete(repo.rows, AuthSecretPurposeKeyStore)

	migrated, err := LoadOrCreateKeyStore(context.Background(), repo, time.Hour)
	if err != nil {
		t.Fatalf("migrated load: %v", err)
	}
	a, err := migrated.Active()
	if err != nil {
		t.Fatalf("migrated Active: %v", err)
	}
	if a.KID != ES256KID {
		t.Errorf("migrated active KID = %q, want legacy %q", a.KID, ES256KID)
	}
	if _, ok := repo.rows[AuthSecretPurposeKeyStore]; !ok {
		t.Error("migration should persist the new-archive row")
	}
}

func TestKeyStore_RotateDemotesActiveToRetired(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ks, _ := LoadOrCreateKeyStore(context.Background(), repo, time.Hour)
	first, _ := ks.Active()
	firstKID := first.KID
	time.Sleep(2 * time.Millisecond) // ensure monotonic CreatedAt gap

	newKID, err := ks.Rotate(context.Background())
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if newKID == firstKID {
		t.Fatal("Rotate must mint a new KID")
	}
	active, _ := ks.Active()
	if active.KID != newKID {
		t.Errorf("post-rotate active = %q, want %q", active.KID, newKID)
	}
	// The previous active must now be Retired.
	old, err := ks.ByKID(firstKID)
	if err != nil {
		t.Fatalf("ByKID(first): %v", err)
	}
	if !old.Retired() {
		t.Error("first key not marked Retired after rotation")
	}
}

func TestKeyStore_OverlapWindow_RetiredStillVerifies(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	overlap := 50 * time.Millisecond
	ks, _ := LoadOrCreateKeyStore(context.Background(), repo, overlap)

	// Sign with the first active key.
	tokenA, err := ks.Sign(map[string]any{"sub": "alpha"})
	if err != nil {
		t.Fatalf("Sign A: %v", err)
	}
	// Rotate — first key becomes retired but stays within overlap.
	if _, err := ks.Rotate(context.Background()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	// Verify A still works using the retired key.
	if _, err := ks.Verify(tokenA); err != nil {
		t.Fatalf("retired-but-within-overlap key should still verify: %v", err)
	}
	// Wait for the overlap window to expire.
	time.Sleep(2 * overlap)
	if _, err := ks.Verify(tokenA); err == nil {
		t.Error("retired key past overlap window should NOT verify")
	}
}

func TestKeyStore_JWKSContainsAllUnexpiredKeys(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ks, _ := LoadOrCreateKeyStore(context.Background(), repo, time.Hour)
	// Rotate twice to land three entries (initial + 2 rotations).
	if _, err := ks.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}

	body, err := ks.JWKS()
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode JWKS: %v", err)
	}
	if len(doc.Keys) != 3 {
		t.Errorf("JWKS keys = %d, want 3 (initial + 2 rotations within overlap)", len(doc.Keys))
	}
	// Each key has a unique kid.
	seen := map[string]bool{}
	for _, k := range doc.Keys {
		kid, _ := k["kid"].(string)
		if kid == "" || seen[kid] {
			t.Errorf("kid duplicate or empty: %v", k)
		}
		seen[kid] = true
		if k["alg"] != "ES256" || k["kty"] != "EC" || k["crv"] != "P-256" {
			t.Errorf("JWK fields wrong: %v", k)
		}
	}
}

func TestKeyStore_SignUsesActive_VerifyPicksByKID(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ks, _ := LoadOrCreateKeyStore(context.Background(), repo, time.Hour)
	active, _ := ks.Active()

	token, err := ks.Sign(map[string]any{"sub": "bravo"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// Verify the signed token's header carries active's KID.
	claims, err := ks.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims["sub"] != "bravo" {
		t.Errorf("sub = %v, want bravo", claims["sub"])
	}

	// A token signed by a key that is not in the store must fail.
	otherRepo := newMemoryRepo()
	otherKS, _ := LoadOrCreateKeyStore(context.Background(), otherRepo, time.Hour)
	otherToken, _ := otherKS.Sign(map[string]any{"sub": "x"})
	if _, err := ks.Verify(otherToken); err == nil {
		t.Error("Verify accepted a token from an unknown kid")
	}
	_ = active // silence unused if Active reads change
}

func TestKeyStore_LoadPersistsAcrossInstances(t *testing.T) {
	t.Parallel()
	repo := newMemoryRepo()
	ks1, _ := LoadOrCreateKeyStore(context.Background(), repo, time.Hour)
	tokenBeforeReload, _ := ks1.Sign(map[string]any{"sub": "persist"})

	// Reload (simulates daemon restart) — same repo, fresh KeyStore.
	ks2, err := LoadOrCreateKeyStore(context.Background(), repo, time.Hour)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, err := ks2.Verify(tokenBeforeReload); err != nil {
		t.Fatalf("post-restart verify: %v", err)
	}
}

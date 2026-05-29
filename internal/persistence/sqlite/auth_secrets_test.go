package sqlite

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestAuthSecretRepository_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewAuthSecretRepository(openTestDB(t))

	// Get missing.
	if _, err := r.Get(ctx, "dashboard-hs256"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("Get missing err = %v, want 'not found'", err)
	}

	// Save HS256 dashboard token signer.
	hsKey := []byte("hs256-secret-bytes")
	if err := r.Save(ctx, "dashboard-hs256", hsKey, "HS256"); err != nil {
		t.Fatalf("Save HS256: %v", err)
	}

	// Save ES256 A2A JWT signer private key (v0.9.2 NEW).
	esKey := []byte("-----BEGIN EC PRIVATE KEY-----\nMHcCAQEE...\n-----END EC PRIVATE KEY-----")
	if err := r.Save(ctx, "a2a-es256-priv", esKey, "ES256"); err != nil {
		t.Fatalf("Save ES256: %v", err)
	}

	// Get back each — distinct rows.
	hs, err := r.Get(ctx, "dashboard-hs256")
	if err != nil {
		t.Fatalf("Get HS256: %v", err)
	}
	if !bytes.Equal(hs.Key, hsKey) || hs.Algorithm != "HS256" {
		t.Errorf("Get HS256 = %v, want key=%q alg=HS256", hs, hsKey)
	}

	es, err := r.Get(ctx, "a2a-es256-priv")
	if err != nil {
		t.Fatalf("Get ES256: %v", err)
	}
	if !bytes.Equal(es.Key, esKey) || es.Algorithm != "ES256" {
		t.Errorf("Get ES256 alg=%q key=%q, want ES256/PEM", es.Algorithm, es.Key)
	}
}

func TestAuthSecretRepository_EmptyArgs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewAuthSecretRepository(openTestDB(t))

	if _, err := r.Get(ctx, ""); err == nil {
		t.Error("Get(empty) = nil, want error")
	}
	if err := r.Save(ctx, "", []byte("x"), "HS256"); err == nil {
		t.Error("Save(empty purpose) = nil, want error")
	}
	if err := r.Save(ctx, "foo", []byte("x"), ""); err == nil {
		t.Error("Save(empty alg) = nil, want error")
	}
}

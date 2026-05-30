// internal/auth/es256_test.go — v0.9.3 #225 row B2.
// Pins ES256 keypair lifecycle (load-or-create, JWS sign+verify,
// JWKS public-key marshalling).
//
// Refs #225 row B2.
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

func TestLoadOrCreateES256_PersistsAcrossCalls(t *testing.T) {
	store, err := sqlite.NewStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	repo := store.AuthSecrets()
	priv1, err := LoadOrCreateES256(context.Background(), repo)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	priv2, err := LoadOrCreateES256(context.Background(), repo)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if priv1.D.Cmp(priv2.D) != 0 {
		t.Errorf("second load returned a DIFFERENT key — persistence broken")
	}
}

func TestSignVerifyJWS_RoundTrip(t *testing.T) {
	store, _ := sqlite.NewStore(context.Background(), ":memory:")
	defer store.Close()
	priv, _ := LoadOrCreateES256(context.Background(), store.AuthSecrets())
	claims := map[string]any{
		"sub":  "operator",
		"iss":  "chepherd-test",
		"exp":  time.Now().Add(time.Hour).Unix(),
		"peer": "uuid-B",
	}
	token, err := SignJWS(priv, claims)
	if err != nil {
		t.Fatalf("SignJWS: %v", err)
	}
	got, err := VerifyJWS(&priv.PublicKey, token)
	if err != nil {
		t.Fatalf("VerifyJWS: %v", err)
	}
	if got["sub"] != "operator" || got["peer"] != "uuid-B" {
		t.Errorf("decoded claims = %+v, want sub=operator peer=uuid-B", got)
	}
}

func TestVerifyJWS_RejectsTamperedSignature(t *testing.T) {
	store, _ := sqlite.NewStore(context.Background(), ":memory:")
	defer store.Close()
	priv, _ := LoadOrCreateES256(context.Background(), store.AuthSecrets())
	token, _ := SignJWS(priv, map[string]any{"sub": "x"})
	// Tamper by decoding the signature segment, flipping a high-order
	// byte, and re-encoding. Earlier "+_" / "+A" forms were flaky
	// because 64-byte ES256 sigs base64url-encode such that only 4
	// chars are valid as the LAST char (A/Q/g/w — coming from the
	// last byte's low 2 bits), giving a 25% collision rate.
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token shape: %d parts", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	sig[0] ^= 0xFF
	tampered := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(sig)
	if tampered == token {
		t.Fatal("tamper logic produced unchanged token")
	}
	if _, err := VerifyJWS(&priv.PublicKey, tampered); err == nil {
		t.Error("VerifyJWS accepted a tampered signature")
	}
}

func TestVerifyJWS_RejectsWrongAlgorithm(t *testing.T) {
	store, _ := sqlite.NewStore(context.Background(), ":memory:")
	defer store.Close()
	priv, _ := LoadOrCreateES256(context.Background(), store.AuthSecrets())
	// Forge a header with alg=none.
	noneToken := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." + // {"alg":"none","typ":"JWT"}
		"eyJzdWIiOiJ4In0." + // {"sub":"x"}
		""
	if _, err := VerifyJWS(&priv.PublicKey, noneToken); err == nil {
		t.Error("VerifyJWS accepted alg=none token")
	}
}

func TestPublicJWK_HasExpectedFields(t *testing.T) {
	store, _ := sqlite.NewStore(context.Background(), ":memory:")
	defer store.Close()
	priv, _ := LoadOrCreateES256(context.Background(), store.AuthSecrets())
	body, err := PublicJWK(priv)
	if err != nil {
		t.Fatalf("PublicJWK: %v", err)
	}
	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(doc.Keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(doc.Keys))
	}
	k := doc.Keys[0]
	for _, field := range []string{"kty", "crv", "x", "y", "kid", "use", "alg"} {
		if _, ok := k[field]; !ok {
			t.Errorf("JWKS missing %s field", field)
		}
	}
	if k["kty"] != "EC" || k["crv"] != "P-256" || k["alg"] != "ES256" {
		t.Errorf("JWKS metadata = %+v", k)
	}
}

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVerify_Valid(t *testing.T) {
	jwks, key := mockJWKS(t)
	defer jwks.Close()

	v := New(jwks.URL, "https://identity.test", "chepherd-rc")
	tok := mintToken(t, key, "test-kid", Claims{
		Subject:           "alice",
		UserID:            "alice@example.com",
		PermittedBastions: []string{"bastion-a", "bastion-b"},
		Expiry:            time.Now().Add(1 * time.Hour).Unix(),
		Issuer:            "https://identity.test",
		Audience:          "chepherd-rc",
	})

	claims, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != "alice@example.com" {
		t.Errorf("UserID mismatch: %s", claims.UserID)
	}
	if !claims.PermitsBastion("bastion-a") {
		t.Errorf("PermitsBastion(bastion-a) want true")
	}
	if claims.PermitsBastion("bastion-X") {
		t.Errorf("PermitsBastion(bastion-X) want false")
	}
}

func TestVerify_Expired(t *testing.T) {
	jwks, key := mockJWKS(t)
	defer jwks.Close()
	v := New(jwks.URL, "https://identity.test", "chepherd-rc")
	tok := mintToken(t, key, "test-kid", Claims{
		Subject:  "alice",
		Expiry:   time.Now().Add(-5 * time.Minute).Unix(),
		Issuer:   "https://identity.test",
		Audience: "chepherd-rc",
	})
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Errorf("expected expired-token rejection")
	}
}

func TestVerify_BadIssuer(t *testing.T) {
	jwks, key := mockJWKS(t)
	defer jwks.Close()
	v := New(jwks.URL, "https://identity.test", "chepherd-rc")
	tok := mintToken(t, key, "test-kid", Claims{
		Subject:  "alice",
		Expiry:   time.Now().Add(1 * time.Hour).Unix(),
		Issuer:   "https://attacker.example",
		Audience: "chepherd-rc",
	})
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Errorf("expected bad-issuer rejection")
	}
}

func TestMiddleware(t *testing.T) {
	jwks, key := mockJWKS(t)
	defer jwks.Close()
	v := New(jwks.URL, "https://identity.test", "chepherd-rc")
	tok := mintToken(t, key, "test-kid", Claims{
		Subject:           "alice",
		UserID:            "alice@example.com",
		Expiry:            time.Now().Add(1 * time.Hour).Unix(),
		Issuer:            "https://identity.test",
		Audience:          "chepherd-rc",
		PermittedBastions: []string{"b1"},
	})
	var captured *Claims
	handler := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// without token → 401
	resp, _ := http.Get(srv.URL)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token want 401 got %d", resp.StatusCode)
	}

	// with token → 200
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("with token want 200 got %d", resp.StatusCode)
	}
	if captured == nil || captured.UserID != "alice@example.com" {
		t.Errorf("middleware did not attach claims: %+v", captured)
	}
}

// ─── helpers ────────────────────────────────────────────────────────────

// mockJWKS spins up a small HTTP server that serves a single-key JWKS.
func mockJWKS(t *testing.T) (*httptest.Server, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nBytes := key.N.Bytes()
		eBytes := []byte{byte(key.E >> 16), byte(key.E >> 8), byte(key.E)}
		// strip leading zero on E if any
		for len(eBytes) > 1 && eBytes[0] == 0 {
			eBytes = eBytes[1:]
		}
		json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{
				{
					"kid": "test-kid",
					"kty": "RSA",
					"alg": "RS256",
					"use": "sig",
					"n":   base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(nBytes),
					"e":   base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(eBytes),
				},
			},
		})
	}))
	return srv, key
}

// mintToken builds a JWT signed with the given key.
func mintToken(t *testing.T, key *rsa.PrivateKey, kid string, claims Claims) string {
	t.Helper()
	hdr := map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"}
	hb, _ := json.Marshal(hdr)
	pb, _ := json.Marshal(claims)
	h64 := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(hb)
	p64 := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(pb)
	signing := h64 + "." + p64
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, 5, sum[:]) // 5 = crypto.SHA256
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(sig)
}

var _ = strings.Contains // keep imports tidy in case future tests add string checks
var _ = fmt.Sprintf

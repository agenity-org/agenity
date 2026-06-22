// internal/auth/es256.go — v0.9.3 #225 row B2. ES256 keypair lifecycle
// + JWS sign/verify + JWKS public-key publication. Substrate for B4
// mTLS inter-runner trust (peer fetches `/.well-known/jwks.json`,
// verifies inbound JWTs without out-of-band key sharing).
//
// Stores the PRIVATE key in AuthSecretRepository under purpose
// "a2a-es256-priv" (see internal/persistence/interface.go). PEM-
// encoded so existing Postgres + SQLite repositories can store it
// without schema changes.
//
// Refs #225 row B2.
package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

const (
	// AuthSecretPurposeES256 keys the AuthSecretRepository row that
	// holds this instance's PEM-encoded ECDSA P-256 private key.
	AuthSecretPurposeES256 = "a2a-es256-priv"
	// ES256KID is the kid (key id) emitted in JWS headers + the JWKS
	// document. v0.9.3 ships a single key per instance (rotation in a
	// follow-up sub-branch).
	ES256KID = "chepherd-a2a-es256"
)

// LoadOrCreateES256 returns the instance's ECDSA P-256 private key,
// generating + persisting a new one on first call. Idempotent across
// chepherd restarts because the AuthSecretRepository persists the PEM
// bytes. Safe for concurrent use — repository writes serialise via
// SQLite/Postgres row locks; a second concurrent caller will simply
// observe the first writer's persisted key.
func LoadOrCreateES256(ctx context.Context, repo persistence.AuthSecretRepository) (*ecdsa.PrivateKey, error) {
	if repo == nil {
		return nil, errors.New("LoadOrCreateES256: nil repository")
	}
	if sec, err := repo.Get(ctx, AuthSecretPurposeES256); err == nil && sec != nil && len(sec.Key) > 0 {
		return parseES256PEM(sec.Key)
	} else if err != nil && !strings.Contains(err.Error(), "not found") {
		return nil, fmt.Errorf("LoadOrCreateES256: repo get: %w", err)
	}
	// Mint a fresh key.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("LoadOrCreateES256: generate: %w", err)
	}
	pemBytes, err := encodeES256PEM(priv)
	if err != nil {
		return nil, fmt.Errorf("LoadOrCreateES256: encode: %w", err)
	}
	if err := repo.Save(ctx, AuthSecretPurposeES256, pemBytes, "ES256"); err != nil {
		return nil, fmt.Errorf("LoadOrCreateES256: persist: %w", err)
	}
	return priv, nil
}

func encodeES256PEM(priv *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func parseES256PEM(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("parseES256PEM: no PEM block")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

// PublicJWK returns the JWKS-formatted public key (single-key JWKS:
// `{"keys":[{"kty":"EC","crv":"P-256","x":"...","y":"...","kid":"...",
// "use":"sig","alg":"ES256"}]}`). Serve at /.well-known/jwks.json so
// peers can verify inbound JWTs signed by this key.
func PublicJWK(priv *ecdsa.PrivateKey) ([]byte, error) {
	if priv == nil || priv.PublicKey.X == nil || priv.PublicKey.Y == nil {
		return nil, errors.New("PublicJWK: nil or unset key")
	}
	// P-256 X and Y are 32 bytes; left-pad if math/big shortens them.
	xb := paddedBigEndian(priv.PublicKey.X, 32)
	yb := paddedBigEndian(priv.PublicKey.Y, 32)
	doc := map[string]any{
		"keys": []any{
			map[string]any{
				"kty": "EC",
				"crv": "P-256",
				"x":   base64.RawURLEncoding.EncodeToString(xb),
				"y":   base64.RawURLEncoding.EncodeToString(yb),
				"kid": ES256KID,
				"use": "sig",
				"alg": "ES256",
			},
		},
	}
	return json.Marshal(doc)
}

func paddedBigEndian(n *big.Int, length int) []byte {
	b := n.Bytes()
	if len(b) >= length {
		return b
	}
	out := make([]byte, length)
	copy(out[length-len(b):], b)
	return out
}

// SignJWS produces a compact JWS (`header.payload.signature`) using
// this instance's ES256 key over the given claims. The header carries
// `alg=ES256, typ=JWT, kid=<ES256KID>` so a verifier can pick the
// right JWKS entry on lookup.
func SignJWS(priv *ecdsa.PrivateKey, claims map[string]any) (string, error) {
	if priv == nil {
		return "", errors.New("SignJWS: nil key")
	}
	header := map[string]any{"alg": "ES256", "typ": "JWT", "kid": ES256KID}
	headerJSON, _ := json.Marshal(header)
	if claims == nil {
		claims = map[string]any{}
	}
	if _, ok := claims["iat"]; !ok {
		claims["iat"] = time.Now().Unix()
	}
	claimsJSON, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)
	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, priv, sum[:])
	if err != nil {
		return "", fmt.Errorf("SignJWS: sign: %w", err)
	}
	// JWS ES256 signature is (R || S) raw bytes, 64 total for P-256.
	rb := paddedBigEndian(r, 32)
	sb := paddedBigEndian(s, 32)
	sig := append(rb, sb...)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// VerifyJWS parses + verifies a compact JWS against pub. Returns the
// decoded claims map. Caller is responsible for asserting time-based
// claims (exp/nbf) — VerifyJWS only proves origin + integrity.
func VerifyJWS(pub *ecdsa.PublicKey, token string) (map[string]any, error) {
	if pub == nil {
		return nil, errors.New("VerifyJWS: nil key")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("VerifyJWS: token has != 3 parts")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("VerifyJWS: header decode: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("VerifyJWS: header parse: %w", err)
	}
	if header.Alg != "ES256" {
		return nil, fmt.Errorf("VerifyJWS: unsupported alg %q (want ES256)", header.Alg)
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("VerifyJWS: claims decode: %w", err)
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("VerifyJWS: sig decode: %w", err)
	}
	if len(sigBytes) != 64 {
		return nil, fmt.Errorf("VerifyJWS: sig len = %d, want 64", len(sigBytes))
	}
	r := new(big.Int).SetBytes(sigBytes[:32])
	s := new(big.Int).SetBytes(sigBytes[32:])
	signingInput := parts[0] + "." + parts[1]
	sum := sha256.Sum256([]byte(signingInput))
	if !ecdsa.Verify(pub, sum[:], r, s) {
		return nil, errors.New("VerifyJWS: signature invalid")
	}
	var claims map[string]any
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return nil, fmt.Errorf("VerifyJWS: claims parse: %w", err)
	}
	return claims, nil
}

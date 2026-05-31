// internal/auth/jwks_client.go — #486 Wave T1. Fetches + caches JWKS
// documents from peer chepherd-daemons so runners can verify JWTs
// signed by those daemons' ES256 keys.
//
// Per V0.9.2-ARCHITECTURE.md §15.1: each runner's A2A endpoint
// extracts the iss claim from an incoming JWT, fetches the issuer's
// /.well-known/jwks.json (#505 Wave T2 publishes this server-side),
// caches the public key with TTL matching the JWKS rotation overlap
// window, then verifies signature + claims.
//
// The cache key is the JWKS URL (full https://<iss>/.well-known/
// jwks.json form so different issuers don't collide). TTL default
// 1h matches the T2 overlap window — within an hour a rotated key
// is still valid for verification; after 1h we re-fetch.
//
// Refs #486 #505 V0.9.2-ARCHITECTURE.md §15.1.
package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// JWKSClient fetches + caches JWKS documents. Safe for concurrent use.
type JWKSClient struct {
	httpClient *http.Client
	ttl        time.Duration

	mu    sync.Mutex
	cache map[string]*jwksCacheEntry
}

type jwksCacheEntry struct {
	keys      map[string]*ecdsa.PublicKey // kid → key
	primary   *ecdsa.PublicKey            // first/active key (when JWT has no kid)
	expiresAt time.Time
}

// NewJWKSClient constructs a JWKSClient with the given cache TTL.
// Zero TTL → 1 hour default (matches T2's rotation-overlap window).
// nil httpClient → http.DefaultClient with 10s timeout.
func NewJWKSClient(httpClient *http.Client, ttl time.Duration) *JWKSClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	if ttl <= 0 {
		ttl = 1 * time.Hour
	}
	return &JWKSClient{
		httpClient: httpClient,
		ttl:        ttl,
		cache:      make(map[string]*jwksCacheEntry),
	}
}

// PublicKey returns the ES256 public key for the given JWKS URL +
// optional kid. Empty kid → returns the cache entry's primary key
// (first key in the JWKS document — matches T2's "active key first"
// convention). Refreshes cache when expired.
//
// JWKS-fetch failures bubble up — the middleware MUST translate
// them into 401 responses (don't fall open to unauthenticated
// access on fetch failure).
func (c *JWKSClient) PublicKey(jwksURL, kid string) (*ecdsa.PublicKey, error) {
	c.mu.Lock()
	entry, ok := c.cache[jwksURL]
	if ok && time.Now().Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.lookup(kid)
	}
	c.mu.Unlock()

	// Fetch (outside the lock so concurrent calls for different
	// URLs don't serialize).
	fresh, err := c.fetch(jwksURL)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cache[jwksURL] = fresh
	c.mu.Unlock()
	return fresh.lookup(kid)
}

func (e *jwksCacheEntry) lookup(kid string) (*ecdsa.PublicKey, error) {
	if kid == "" {
		if e.primary == nil {
			return nil, errors.New("jwks: cache entry has no primary key")
		}
		return e.primary, nil
	}
	k, ok := e.keys[kid]
	if !ok {
		return nil, fmt.Errorf("jwks: kid %q not found", kid)
	}
	return k, nil
}

func (c *JWKSClient) fetch(jwksURL string) (*jwksCacheEntry, error) {
	resp, err := c.httpClient.Get(jwksURL)
	if err != nil {
		return nil, fmt.Errorf("jwks fetch %s: %w", jwksURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks fetch %s: status %d", jwksURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("jwks read %s: %w", jwksURL, err)
	}
	return parseJWKS(body, c.ttl)
}

// parseJWKS decodes a JWKS document into a cache entry. Exported
// indirectly via NewJWKSClient.fetch; kept package-private so the
// JSON shape stays internal to this file.
func parseJWKS(body []byte, ttl time.Duration) (*jwksCacheEntry, error) {
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Alg string `json:"alg"`
			Crv string `json:"crv"`
			Kid string `json:"kid"`
			X   string `json:"x"`
			Y   string `json:"y"`
			Use string `json:"use"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("jwks parse: %w", err)
	}
	if len(doc.Keys) == 0 {
		return nil, errors.New("jwks parse: no keys in document")
	}
	entry := &jwksCacheEntry{
		keys:      make(map[string]*ecdsa.PublicKey, len(doc.Keys)),
		expiresAt: time.Now().Add(ttl),
	}
	for i, k := range doc.Keys {
		if k.Kty != "EC" {
			continue
		}
		if k.Crv != "P-256" {
			continue
		}
		if k.Alg != "" && k.Alg != "ES256" {
			continue
		}
		xBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(k.X, "="))
		if err != nil {
			return nil, fmt.Errorf("jwks parse key %d: bad x: %w", i, err)
		}
		yBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(k.Y, "="))
		if err != nil {
			return nil, fmt.Errorf("jwks parse key %d: bad y: %w", i, err)
		}
		pub := &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(xBytes),
			Y:     new(big.Int).SetBytes(yBytes),
		}
		if k.Kid != "" {
			entry.keys[k.Kid] = pub
		}
		if entry.primary == nil {
			entry.primary = pub
		}
	}
	if entry.primary == nil {
		return nil, errors.New("jwks parse: no usable ES256 P-256 keys")
	}
	return entry, nil
}

// DeriveJWKSURL constructs the standard JWKS URL for an issuer.
// iss is expected to be a scheme://host[:port] form (as minted by
// T2 #510 #505).
func DeriveJWKSURL(iss string) string {
	iss = strings.TrimRight(iss, "/")
	return iss + "/.well-known/jwks.json"
}

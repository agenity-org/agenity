// internal/auth/keystore.go implements daemon-owned ES256 key
// rotation with an overlap window for in-flight token verification
// (#505 Wave T2). The KeyStore is the single source of truth for:
//
//   - the ACTIVE private key used to sign new A2A JWTs
//   - the RETIRED but still-valid keys (within an overlap window)
//     whose signatures inbound JWTs may still carry
//   - the JWKS document published at /.well-known/jwks.json so peers
//     can fetch ALL currently-trusted public keys
//   - the kid-aware verify path so an inbound JWT carrying the
//     header `kid` of a retired-but-not-expired key still verifies
//
// Persistence: a single JSON-encoded AuthSecret row keyed by the
// new purpose "a2a-es256-keystore". Backwards-compat: when the new
// row doesn't exist but the legacy single-key row "a2a-es256-priv"
// does, LoadOrCreateKeyStore migrates the legacy key as the initial
// entry under the legacy kid constant — instances upgrading to T2
// keep validating any in-flight tokens minted before the upgrade.
//
// The JWKS endpoint MUST live on the daemon. Runners never carry
// their own signing keys and never expose their own JWKS — they
// verify inbound tokens by fetching the daemon's JWKS. This file
// is the daemon-side substrate for that invariant.
//
// Refs #505 V0.9.2-ARCHITECTURE.md §15.2 §22 §23.
package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
)

const (
	// AuthSecretPurposeKeyStore is the AuthSecret row that holds the
	// JSON-encoded KeyStore archive (all entries — active + retired).
	AuthSecretPurposeKeyStore = "a2a-es256-keystore"

	// DefaultOverlapWindow is how long retired keys stay in the JWKS
	// after rotation. Long enough that inbound tokens minted just
	// before rotation can still verify; short enough that compromise
	// of an old key has a bounded exposure. 24h matches the §15.2
	// 60-second token TTL by a wide margin.
	DefaultOverlapWindow = 24 * time.Hour
)

// KeyEntry is one signing key in the daemon's keystore.
type KeyEntry struct {
	KID       string            `json:"kid"`
	PEM       []byte            `json:"pem"`
	CreatedAt time.Time         `json:"created_at"`
	RetiredAt time.Time         `json:"retired_at,omitempty"`
	priv      *ecdsa.PrivateKey // parsed lazily; nil until needed
}

// Retired reports whether this entry has been replaced by a newer
// Active key. Retired entries continue to verify inbound JWTs until
// they fall out of the overlap window.
func (e *KeyEntry) Retired() bool { return !e.RetiredAt.IsZero() }

// Priv returns the parsed ECDSA private key, parsing the PEM bytes
// on first call and caching the result.
func (e *KeyEntry) Priv() (*ecdsa.PrivateKey, error) {
	if e.priv != nil {
		return e.priv, nil
	}
	p, err := parseES256PEM(e.PEM)
	if err != nil {
		return nil, fmt.Errorf("KeyEntry %q: %w", e.KID, err)
	}
	e.priv = p
	return p, nil
}

// KeyStore is the daemon's multi-key signing + verification store.
// It is safe for concurrent reads + serializes Rotate calls. The
// in-memory state is the source of truth between persistence
// operations; persist() writes the JSON archive after every mutation.
type KeyStore struct {
	mu      sync.RWMutex
	entries []*KeyEntry
	overlap time.Duration
	repo    persistence.AuthSecretRepository
}

// LoadOrCreateKeyStore returns the daemon's KeyStore. Lookup order:
//
//  1. If the JSON archive row exists, deserialize + return.
//  2. Else if the legacy single-key row "a2a-es256-priv" exists,
//     migrate the legacy key into a fresh archive under the legacy
//     KID constant. The legacy row is left in place so that older
//     code paths reading directly from it keep working through the
//     transition; future cleanup can remove it.
//  3. Else mint a brand-new key with a unique KID, persist, return.
//
// The returned store always has at least one entry; the newest is
// the Active key (unretired and most recently created).
func LoadOrCreateKeyStore(ctx context.Context, repo persistence.AuthSecretRepository, overlap time.Duration) (*KeyStore, error) {
	if repo == nil {
		return nil, errors.New("LoadOrCreateKeyStore: nil repository")
	}
	if overlap <= 0 {
		overlap = DefaultOverlapWindow
	}
	ks := &KeyStore{repo: repo, overlap: overlap}

	if sec, err := repo.Get(ctx, AuthSecretPurposeKeyStore); err == nil && sec != nil && len(sec.Key) > 0 {
		var doc keystoreDoc
		if err := json.Unmarshal(sec.Key, &doc); err != nil {
			return nil, fmt.Errorf("LoadOrCreateKeyStore: parse archive: %w", err)
		}
		ks.entries = doc.Entries
		ks.pruneExpiredLocked(time.Now().UTC())
		if len(ks.entries) > 0 {
			return ks, nil
		}
		// Archive exists but all entries are expired — fall through to
		// mint a fresh key.
	} else if err != nil && !strings.Contains(err.Error(), "not found") {
		return nil, fmt.Errorf("LoadOrCreateKeyStore: get archive: %w", err)
	}

	// Legacy single-key migration path.
	if sec, err := repo.Get(ctx, AuthSecretPurposeES256); err == nil && sec != nil && len(sec.Key) > 0 {
		entry := &KeyEntry{
			KID:       ES256KID,
			PEM:       sec.Key,
			CreatedAt: sec.CreatedAt,
		}
		if entry.CreatedAt.IsZero() {
			entry.CreatedAt = time.Now().UTC()
		}
		ks.entries = []*KeyEntry{entry}
		if err := ks.persistLocked(ctx); err != nil {
			return nil, fmt.Errorf("LoadOrCreateKeyStore: persist migrated archive: %w", err)
		}
		return ks, nil
	}

	// Fresh mint.
	if _, err := ks.rotateLocked(ctx); err != nil {
		return nil, fmt.Errorf("LoadOrCreateKeyStore: initial mint: %w", err)
	}
	return ks, nil
}

// keystoreDoc is the on-disk wire shape of the JSON archive.
type keystoreDoc struct {
	Entries []*KeyEntry `json:"entries"`
}

// Active returns the newest non-retired entry. Returns an error if
// the store is empty (which would indicate a bug — LoadOrCreate
// guarantees at least one entry on construction).
func (ks *KeyStore) Active() (*KeyEntry, error) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.activeLocked()
}

func (ks *KeyStore) activeLocked() (*KeyEntry, error) {
	var newest *KeyEntry
	for _, e := range ks.entries {
		if e.Retired() {
			continue
		}
		if newest == nil || e.CreatedAt.After(newest.CreatedAt) {
			newest = e
		}
	}
	if newest == nil {
		return nil, errors.New("KeyStore: no active key")
	}
	return newest, nil
}

// ByKID returns the entry whose KID matches, provided it is either
// active or retired-but-within-the-overlap-window. Returns an error
// for unknown KIDs and for KIDs that have aged out.
func (ks *KeyStore) ByKID(kid string) (*KeyEntry, error) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	now := time.Now().UTC()
	for _, e := range ks.entries {
		if e.KID != kid {
			continue
		}
		if e.Retired() && now.Sub(e.RetiredAt) > ks.overlap {
			return nil, fmt.Errorf("KeyStore: kid %q is past overlap window", kid)
		}
		return e, nil
	}
	return nil, fmt.Errorf("KeyStore: unknown kid %q", kid)
}

// Rotate mints a new active key, demotes the previous active to
// retired with RetiredAt=now, prunes any retired entries older than
// the overlap window, and persists the updated archive. Returns the
// new active key's KID.
func (ks *KeyStore) Rotate(ctx context.Context) (string, error) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	return ks.rotateLocked(ctx)
}

func (ks *KeyStore) rotateLocked(ctx context.Context) (string, error) {
	now := time.Now().UTC()
	// Demote current active(s) to retired.
	for _, e := range ks.entries {
		if !e.Retired() {
			e.RetiredAt = now
		}
	}
	// Prune any retired entries past the overlap window.
	ks.pruneExpiredLocked(now)
	// Mint the new active.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("Rotate: generate: %w", err)
	}
	pemBytes, err := encodeES256PEM(priv)
	if err != nil {
		return "", fmt.Errorf("Rotate: encode: %w", err)
	}
	kid := fmt.Sprintf("chepherd-a2a-es256-%d", now.UnixNano())
	entry := &KeyEntry{
		KID:       kid,
		PEM:       pemBytes,
		CreatedAt: now,
		priv:      priv,
	}
	ks.entries = append(ks.entries, entry)
	if err := ks.persistLocked(ctx); err != nil {
		return "", fmt.Errorf("Rotate: persist: %w", err)
	}
	return kid, nil
}

func (ks *KeyStore) pruneExpiredLocked(now time.Time) {
	kept := ks.entries[:0]
	for _, e := range ks.entries {
		if e.Retired() && now.Sub(e.RetiredAt) > ks.overlap {
			continue
		}
		kept = append(kept, e)
	}
	ks.entries = kept
}

func (ks *KeyStore) persistLocked(ctx context.Context) error {
	body, err := json.Marshal(keystoreDoc{Entries: ks.entries})
	if err != nil {
		return fmt.Errorf("persist marshal: %w", err)
	}
	return ks.repo.Save(ctx, AuthSecretPurposeKeyStore, body, "ES256-keystore")
}

// JWKS returns the marshalled JWKS document containing every entry
// that is currently published — i.e. active OR retired-but-within-
// the-overlap-window. Each key carries its own kid so peers can
// pick the right one when validating an inbound JWT.
func (ks *KeyStore) JWKS() ([]byte, error) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	keys := make([]any, 0, len(ks.entries))
	for _, e := range ks.entries {
		priv, err := e.Priv()
		if err != nil {
			return nil, err
		}
		xb := paddedBigEndian(priv.PublicKey.X, 32)
		yb := paddedBigEndian(priv.PublicKey.Y, 32)
		keys = append(keys, map[string]any{
			"kty": "EC",
			"crv": "P-256",
			"x":   base64.RawURLEncoding.EncodeToString(xb),
			"y":   base64.RawURLEncoding.EncodeToString(yb),
			"kid": e.KID,
			"use": "sig",
			"alg": "ES256",
		})
	}
	return json.Marshal(map[string]any{"keys": keys})
}

// Sign produces a JWS with the active key's kid in the JOSE header.
// Returns -32001-equivalent error when no active key exists.
func (ks *KeyStore) Sign(claims map[string]any) (string, error) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	entry, err := ks.activeLocked()
	if err != nil {
		return "", err
	}
	priv, err := entry.Priv()
	if err != nil {
		return "", err
	}
	return signJWSWithKID(priv, entry.KID, claims)
}

// Verify parses the JWS, picks the key by the header's kid, and
// verifies the signature. The kid MUST resolve to an entry that
// is either active or retired-but-within-the-overlap-window;
// expired kids are rejected as a defense against arbitrary
// historical signatures.
func (ks *KeyStore) Verify(token string) (map[string]any, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return nil, errors.New("KeyStore.Verify: token has != 3 parts")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("KeyStore.Verify: header decode: %w", err)
	}
	var header struct {
		KID string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("KeyStore.Verify: header parse: %w", err)
	}
	if header.KID == "" {
		return nil, errors.New("KeyStore.Verify: JWS missing kid")
	}
	entry, err := ks.ByKID(header.KID)
	if err != nil {
		return nil, err
	}
	priv, err := entry.Priv()
	if err != nil {
		return nil, err
	}
	return VerifyJWS(&priv.PublicKey, token)
}

// signJWSWithKID is the per-kid variant of SignJWS. Kept package-
// private because the public Sign-equivalent surface is the
// KeyStore.Sign method; callers outside this file should go
// through the store rather than passing their own kid.
func signJWSWithKID(priv *ecdsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	if priv == nil {
		return "", errors.New("signJWSWithKID: nil key")
	}
	if kid == "" {
		return "", errors.New("signJWSWithKID: empty kid")
	}
	// Defer to the existing JWS construction but with a custom kid
	// header. Re-implements the body of SignJWS so we don't have to
	// expand SignJWS's signature; keeps that function backwards-
	// compatible for any external callers.
	return signJWSCommon(priv, kid, claims)
}

func signJWSCommon(priv *ecdsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	if claims == nil {
		claims = map[string]any{}
	}
	if _, ok := claims["iat"]; !ok {
		claims["iat"] = time.Now().Unix()
	}
	header := map[string]any{"alg": "ES256", "typ": "JWT", "kid": kid}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)
	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, priv, sum[:])
	if err != nil {
		return "", fmt.Errorf("signJWSCommon: sign: %w", err)
	}
	rb := paddedBigEndian(r, 32)
	sb := paddedBigEndian(s, 32)
	sig := append(rb, sb...)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Compile-time guard against an accidental rename of the legacy KID
// constant — the migration path in LoadOrCreateKeyStore references
// it by name, and silently changing the value would leave existing
// instances unable to verify in-flight tokens after upgrade.
var _ = ES256KID

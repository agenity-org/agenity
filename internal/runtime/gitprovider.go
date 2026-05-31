package runtime

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/hkdf"
)

// GitProviderKind identifies the type of git hosting service.
type GitProviderKind string

const (
	GitProviderGitHub    GitProviderKind = "github"
	GitProviderGitLab    GitProviderKind = "gitlab"
	GitProviderGitea     GitProviderKind = "gitea"
	GitProviderBitbucket GitProviderKind = "bitbucket"
	GitProviderEmbedded  GitProviderKind = "embedded" // internal Gitea pod
)

// GitProvider is a registered git repository + token, persisted in state.
//
// Token is encrypted at rest (#140) via AES-256-GCM with a machine-derived
// key. The Token field is the cleartext, populated on load + cleared on
// save. The persisted JSON has TokenCipher (base64) instead. Callers that
// need the raw token use it directly off this struct in memory.
type GitProvider struct {
	ID           string          `json:"id"`            // stable slug derived from URL
	Kind         GitProviderKind `json:"kind"`
	RepoURL      string          `json:"repo_url"`      // e.g. https://github.com/org/repo
	Token        string          `json:"-"`             // cleartext — NEVER persisted
	TokenCipher  string          `json:"token_cipher,omitempty"` // base64(nonce|ciphertext)
	DisplayName  string          `json:"display_name"`  // user-facing label
	RegisteredAt time.Time       `json:"registered_at"`
}

func gitProvidersPath(stateDir string) string {
	return filepath.Join(stateDir, "git-providers.json")
}

// LoadGitProviders reads all registered providers from state. Decrypts
// each provider's TokenCipher into Token (in-memory cleartext only).
//
// #420 P0 — tries the stable seed-based key first; on decrypt failure
// falls back to the legacy hostname:username key + transparently
// re-encrypts under the stable key + persists. Without this, every
// chepherd container bounce changed the derived key (hostname or
// username drift) and silently zeroed every saved token → operator
// hit "NO TOKEN — re-paste" on every wizard open + lost
// "29m ago"-stamped tokens.
func LoadGitProviders(stateDir string) ([]*GitProvider, error) {
	b, err := os.ReadFile(gitProvidersPath(stateDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var providers []*GitProvider
	if err := json.Unmarshal(b, &providers); err != nil {
		return nil, err
	}
	stableKey, stableErr := deriveProviderKeyStable(stateDir)
	legacyKey, legacyErr := deriveProviderKey()
	var migrationNeeded bool
	kept := providers[:0]
	for _, p := range providers {
		if p.TokenCipher == "" {
			kept = append(kept, p)
			continue
		}
		if stableErr == nil {
			if plain, err := aesDecrypt(stableKey, p.TokenCipher); err == nil {
				p.Token = plain
				kept = append(kept, p)
				continue
			}
		}
		// Stable key didn't decrypt — try the legacy
		// hostname:username key from pre-#420 saves.
		if legacyErr == nil {
			if plain, err := aesDecrypt(legacyKey, p.TokenCipher); err == nil {
				p.Token = plain
				migrationNeeded = true
				kept = append(kept, p)
				continue
			}
		}
		// #420 follow-up — AUTO-DELETE unrecoverable legacy entries
		// instead of leaving them as "NO TOKEN" zombies. These entries
		// were encrypted under a hostname that no longer exists (every
		// chepherd container rebuild generates a new container ID
		// which is the hostname pre-#425). Operator-facing accounts
		// pane was showing "NO TOKEN — re-paste on the wizard" forever
		// even after re-paste — the new entry would have the same
		// composite ID + the stale entry's NO TOKEN state would leak
		// through if not dropped.
		//
		// Dropping the entry has zero functional cost: operator's
		// re-paste flow recreates it cleanly under the stable seed
		// key with has_token=true. Logged loudly to stderr so the
		// operator knows what happened.
		fmt.Fprintf(os.Stderr,
			"[chepherd-gitprovider] dropping unrecoverable legacy entry id=%q: encrypted under a hostname that no longer exists (pre-#425 chepherd container) — operator must re-paste the token in the wizard to recreate (#420)\n",
			p.ID)
		migrationNeeded = true // force re-save so dropped entry disappears from disk
	}
	providers = kept
	// One-shot migration: re-encrypt under the stable key so the
	// next load + every subsequent container bounce is robust. Also
	// flushes any dropped unrecoverable entries to disk.
	if migrationNeeded && stableErr == nil {
		_ = SaveGitProviders(stateDir, providers)
	}
	return providers, nil
}

// SaveGitProviders writes the full provider list to state. Encrypts each
// provider's Token before serialization; Token field itself has json:"-"
// so plaintext NEVER reaches disk (#140).
//
// #420 P0 — uses the stable seed-based key (persisted to
// <stateDir>/.provider-key-seed) so tokens survive chepherd container
// bounces. Falls back to the legacy hostname:username key only if the
// seed can't be created (e.g., read-only state-dir).
func SaveGitProviders(stateDir string, providers []*GitProvider) error {
	key, err := deriveProviderKeyStable(stateDir)
	if err != nil {
		// Fallback to legacy key so we don't regress operators whose
		// state-dir doesn't allow seed persistence. Their tokens will
		// still drop on the next bounce — but at least they save.
		var legacyErr error
		key, legacyErr = deriveProviderKey()
		if legacyErr != nil {
			return fmt.Errorf("derive provider key: %v (legacy fallback also failed: %v)", err, legacyErr)
		}
	}
	// Walk providers and refresh ciphertext from the live Token field.
	for _, p := range providers {
		if p.Token == "" {
			p.TokenCipher = ""
			continue
		}
		c, err := aesEncrypt(key, p.Token)
		if err != nil {
			return err
		}
		p.TokenCipher = c
	}
	b, err := json.MarshalIndent(providers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(gitProvidersPath(stateDir), b, 0o600)
}

// deriveProviderKey returns a 32-byte AES key for git-providers.json
// encryption derived from hostname:username.
//
// LEGACY: pre-#420 path. Retained for one-shot migration of providers
// saved under this key — see LoadGitProviders fallback path. Don't
// use for new saves; use deriveProviderKeyStable instead.
//
// The fatal flaw: hostname is container-bound. Every chepherd
// container rebuild generates a new hostname (e.g.,
// `b3f4e1ce2a91`), so the derived key changes + every saved token
// fails to decrypt + UI shows "NO TOKEN — re-paste". Same trap for
// $USER inside the container (typically `chepherd` but vulnerable
// to env edits).
func deriveProviderKey() ([]byte, error) {
	hostname, _ := os.Hostname()
	username := os.Getenv("USER")
	if username == "" {
		username = os.Getenv("LOGNAME")
	}
	salt := []byte("chepherd-providers-v1")
	h := hkdf.New(sha256.New, []byte(hostname+":"+username), salt, []byte("chepherd-provider-token-key"))
	k := make([]byte, 32)
	if _, err := io.ReadFull(h, k); err != nil {
		return nil, err
	}
	return k, nil
}

// providerKeySeedPath returns the on-disk path of the stable random
// seed used to derive the git-provider encryption key. Stored in the
// chepherd state-dir which IS bind-mounted across container
// rebuilds, so the seed (and thus the key) survives.
//
// #420 P0.
func providerKeySeedPath(stateDir string) string {
	return filepath.Join(stateDir, ".provider-key-seed")
}

// deriveProviderKeyStable returns a 32-byte AES key derived from a
// stable random seed persisted in the state-dir. The seed is
// generated on first call (32 random bytes, 0600 perms). On
// subsequent calls + after container bounces, the same seed is
// read back + the same key is derived.
//
// Migration: tokens saved under the legacy hostname:username key
// are auto-recovered on the next LoadGitProviders call (try stable
// first, fall back to legacy, re-encrypt under stable, persist).
//
// #420 P0 — fixes "NO TOKEN — re-paste on the wizard" recurring on
// every chepherd container bounce.
func deriveProviderKeyStable(stateDir string) ([]byte, error) {
	if stateDir == "" {
		return nil, fmt.Errorf("deriveProviderKeyStable: empty stateDir")
	}
	seedPath := providerKeySeedPath(stateDir)
	seed, err := os.ReadFile(seedPath)
	if os.IsNotExist(err) {
		seed = make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			return nil, fmt.Errorf("deriveProviderKeyStable: generate seed: %w", err)
		}
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			return nil, fmt.Errorf("deriveProviderKeyStable: mkdir stateDir: %w", err)
		}
		if err := os.WriteFile(seedPath, seed, 0o600); err != nil {
			return nil, fmt.Errorf("deriveProviderKeyStable: write seed: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("deriveProviderKeyStable: read seed: %w", err)
	} else if len(seed) < 32 {
		return nil, fmt.Errorf("deriveProviderKeyStable: seed too short (%d bytes; corruption?)", len(seed))
	}
	salt := []byte("chepherd-providers-stable-v420")
	h := hkdf.New(sha256.New, seed, salt, []byte("chepherd-provider-token-key"))
	k := make([]byte, 32)
	if _, err := io.ReadFull(h, k); err != nil {
		return nil, err
	}
	return k, nil
}

func aesEncrypt(key []byte, plain string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func aesDecrypt(key []byte, enc string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return "", fmt.Errorf("ciphertext too short")
	}
	plain, err := gcm.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// UpsertGitProvider adds or replaces a provider by ID.
func UpsertGitProvider(stateDir string, p *GitProvider) error {
	providers, err := LoadGitProviders(stateDir)
	if err != nil {
		return err
	}
	for i, existing := range providers {
		if existing.ID == p.ID {
			providers[i] = p
			return SaveGitProviders(stateDir, providers)
		}
	}
	providers = append(providers, p)
	return SaveGitProviders(stateDir, providers)
}

// DeleteGitProvider removes a provider by ID.
func DeleteGitProvider(stateDir string, id string) error {
	providers, err := LoadGitProviders(stateDir)
	if err != nil {
		return err
	}
	filtered := providers[:0]
	for _, p := range providers {
		if p.ID != id {
			filtered = append(filtered, p)
		}
	}
	return SaveGitProviders(stateDir, filtered)
}

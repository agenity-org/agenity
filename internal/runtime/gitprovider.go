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
	key, err := deriveProviderKey()
	if err != nil {
		return nil, err
	}
	for _, p := range providers {
		if p.TokenCipher == "" {
			continue
		}
		plain, err := aesDecrypt(key, p.TokenCipher)
		if err != nil {
			// Don't fail the whole load — surface as missing token.
			p.Token = ""
			continue
		}
		p.Token = plain
	}
	return providers, nil
}

// SaveGitProviders writes the full provider list to state. Encrypts each
// provider's Token before serialization; Token field itself has json:"-"
// so plaintext NEVER reaches disk (#140).
func SaveGitProviders(stateDir string, providers []*GitProvider) error {
	key, err := deriveProviderKey()
	if err != nil {
		return err
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
// encryption. Same approach as vault.deriveKey for consistency; the
// underlying low-entropy issue tracked separately as #141.
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

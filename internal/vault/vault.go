// Package vault provides an encrypted local credential store.
//
// Credentials are stored in ~/.local/state/chepherd/vault.json (or the
// runtime state-dir equivalent). Values are AES-256-GCM encrypted with a
// key derived from machine identity — good enough for local dev tooling
// and prevents casual reads of the file by other users.
//
// At spawn time the runtime calls Inject(sess) to merge matching credentials
// as env vars into the spawned agent's environment.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

)

// ProviderMeta describes a known credential provider.
type ProviderMeta struct {
	Label       string // human-readable (e.g. "Claude OAuth")
	DefaultEnv  string // default env var name at injection
	Description string
}

// KnownProviders lists the providers the UI exposes by default.
var KnownProviders = map[string]ProviderMeta{
	"claude-oauth": {
		Label:       "Claude OAuth (subscription)",
		DefaultEnv:  "", // delivered via /run/secrets/claude-credentials file mount
		Description: "Full claude-code OAuth credentials JSON (paste from ~/.claude/.credentials.json or auto-captured via spawn-wizard login flow).",
	},
	"anthropic-api": {
		Label:       "Anthropic API Key",
		DefaultEnv:  "ANTHROPIC_API_KEY",
		Description: "sk-ant-... — used by claude-code for direct API access",
	},
	"openai-api": {
		Label:       "OpenAI API Key",
		DefaultEnv:  "OPENAI_API_KEY",
		Description: "sk-... — OpenAI-compatible API access",
	},
	"google-api": {
		Label:       "Google Gemini API Key",
		DefaultEnv:  "GEMINI_API_KEY",
		Description: "Free key: https://aistudio.google.com/app/apikey (no credit card)",
	},
	"groq-api": {
		Label:       "Groq API Key",
		DefaultEnv:  "GROQ_API_KEY",
		Description: "Free key: https://console.groq.com/keys (no credit card)",
	},
	"cerebras-api": {
		Label:       "Cerebras API Key",
		DefaultEnv:  "CEREBRAS_API_KEY",
		Description: "Free key: https://cloud.cerebras.ai (no credit card)",
	},
	"dashscope-api": {
		Label:       "Alibaba DashScope (Qwen) API Key",
		DefaultEnv:  "DASHSCOPE_API_KEY",
		Description: "https://dashscope.console.aliyun.com",
	},
	"gemini-oauth": {
		Label:       "Gemini CLI login (Google OAuth)",
		DefaultEnv:  "", // delivered via creds-file mount (mirrors claude-oauth)
		Description: "Free, no credit card — sign in with a Google account",
	},
	"qwen-oauth": {
		Label:       "Qwen Code login (Qwen OAuth)",
		DefaultEnv:  "", // delivered via creds-file mount (mirrors claude-oauth)
		Description: "Free, no credit card — https://chat.qwen.ai",
	},
	"copilot-oauth": {
		Label:       "GitHub Copilot login",
		DefaultEnv:  "", // delivered via creds-file mount (mirrors claude-oauth)
		Description: "GitHub OAuth — enable Copilot Free at https://github.com/settings/copilot",
	},
	"openrouter": {
		Label:       "OpenRouter API Key",
		DefaultEnv:  "OPENROUTER_API_KEY",
		Description: "sk-or-... — route via openrouter.ai",
	},
	"newapi": {
		Label:       "NewAPI / qwen key",
		DefaultEnv:  "NEW_API_KEY",
		Description: "OpenOva NewAPI key for qwen-coder / kimi models",
	},
	"github-pat": {
		Label:       "GitHub PAT",
		DefaultEnv:  "GITHUB_TOKEN",
		Description: "github_pat_... — gh CLI, issue ops, PR creation",
	},
	"gitlab-pat": {
		Label:       "GitLab PAT",
		DefaultEnv:  "GITLAB_TOKEN",
		Description: "glpat-... — gl CLI / GitLab API",
	},
	"gitea": {
		Label:       "Gitea token",
		DefaultEnv:  "GITEA_TOKEN",
		Description: "Gitea personal access token",
	},
	"custom": {
		Label:       "Custom env var",
		DefaultEnv:  "",
		Description: "Arbitrary key=value injected verbatim",
	},
}

// Cred is one stored credential.
type Cred struct {
	ID        string    `json:"id"`
	Provider  string    `json:"provider"`
	Label     string    `json:"label"`      // user-given name (e.g. "work claude max")
	EnvVar    string    `json:"env_var"`    // injection env var (overrides provider default)
	Cipher    string    `json:"cipher"`     // base64(nonce+ciphertext)
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type vaultFile struct {
	Creds []Cred `json:"creds"`
}

// Vault is the in-memory credential store backed by an encrypted JSON file.
type Vault struct {
	mu      sync.RWMutex
	path    string
	key     []byte
	creds   []Cred
}

// Open loads (or creates) the vault at path. Encryption key lives at
// path's-parent/vault.key (real random 32 bytes, 0600); generated on
// first Open, persisted thereafter (#141).
func Open(path string) (*Vault, error) {
	vaultKeyPath = filepath.Join(filepath.Dir(path), "vault.key")
	key, err := deriveKey()
	if err != nil {
		return nil, fmt.Errorf("vault key: %w", err)
	}
	v := &Vault{path: path, key: key}
	if err := v.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("vault load: %w", err)
	}
	return v, nil
}

// List returns all credentials (values are NOT returned, only metadata).
func (v *Vault) List() []CredMeta {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]CredMeta, len(v.creds))
	for i, c := range v.creds {
		pm := KnownProviders[c.Provider]
		envVar := c.EnvVar
		if envVar == "" {
			envVar = pm.DefaultEnv
		}
		out[i] = CredMeta{
			ID:            c.ID,
			Provider:      c.Provider,
			ProviderLabel: pm.Label,
			Label:         c.Label,
			EnvVar:        envVar,
			CreatedAt:     c.CreatedAt,
			UpdatedAt:     c.UpdatedAt,
		}
	}
	return out
}

// CredMeta is the safe (value-less) view of a credential.
type CredMeta struct {
	ID            string    `json:"id"`
	Provider      string    `json:"provider"`
	ProviderLabel string    `json:"provider_label"`
	Label         string    `json:"label"`
	EnvVar        string    `json:"env_var"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Set stores (add or update) a credential. id="" creates a new entry.
func (v *Vault) Set(id, provider, label, envVar, plaintext string) (string, error) {
	ciphertext, err := v.encrypt(plaintext)
	if err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	now := time.Now().UTC()
	// Update existing?
	if id != "" {
		for i, c := range v.creds {
			if c.ID == id {
				v.creds[i].Provider = provider
				v.creds[i].Label = label
				v.creds[i].EnvVar = envVar
				v.creds[i].Cipher = ciphertext
				v.creds[i].UpdatedAt = now
				return id, v.save()
			}
		}
	}
	// New entry
	newID := fmt.Sprintf("%s-%d", provider, now.UnixNano())
	v.creds = append(v.creds, Cred{
		ID:        newID,
		Provider:  provider,
		Label:     label,
		EnvVar:    envVar,
		Cipher:    ciphertext,
		CreatedAt: now,
		UpdatedAt: now,
	})
	return newID, v.save()
}

// UpdateValue re-encrypts the credential value for an existing id,
// preserving provider / label / envVar / created_at. Used by the
// runtime's refresh-on-spawn path so the rotated OAuth pair lands
// back in the vault without losing the operator's label. Returns
// nil even if the id doesn't exist (no-op).
func (v *Vault) UpdateValue(id, plaintext string) error {
	ciphertext, err := v.encrypt(plaintext)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for i, c := range v.creds {
		if c.ID == id {
			v.creds[i].Cipher = ciphertext
			v.creds[i].UpdatedAt = time.Now().UTC()
			return v.save()
		}
	}
	return nil
}

// Delete removes a credential by ID.
func (v *Vault) Delete(id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i, c := range v.creds {
		if c.ID == id {
			v.creds = append(v.creds[:i], v.creds[i+1:]...)
			return v.save()
		}
	}
	return fmt.Errorf("credential %s not found", id)
}

// GetValue returns the decrypted plaintext of one credential by ID.
// Used by AgentSecretsDir to materialize Claude OAuth credentials at the
// /run/secrets bind-mount path. Returns "not found" if id is unknown.
func (v *Vault) GetValue(id string) (string, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	for _, c := range v.creds {
		if c.ID == id {
			return v.decrypt(c.Cipher)
		}
	}
	return "", fmt.Errorf("credential %s not found", id)
}

// ListByProvider returns all credential metadata for one provider, in
// stable creation order. Empty result if no creds of that provider.
func (v *Vault) ListByProvider(provider string) []CredMeta {
	v.mu.RLock()
	defer v.mu.RUnlock()
	var out []CredMeta
	for _, c := range v.creds {
		if c.Provider != provider {
			continue
		}
		pm := KnownProviders[c.Provider]
		envVar := c.EnvVar
		if envVar == "" {
			envVar = pm.DefaultEnv
		}
		out = append(out, CredMeta{
			ID:            c.ID,
			Provider:      c.Provider,
			ProviderLabel: pm.Label,
			Label:         c.Label,
			EnvVar:        envVar,
			CreatedAt:     c.CreatedAt,
			UpdatedAt:     c.UpdatedAt,
		})
	}
	return out
}

// EnvFor returns the env-var map for the given providers (nil = all).
// Returned values are decrypted plaintext — callers inject into process env.
//
// Note: providers with an empty DefaultEnv (e.g. claude-oauth, where the
// credential is delivered via a file mount rather than env) are filtered
// out automatically — they never produce an env-var entry.
func (v *Vault) EnvFor(providers []string) (map[string]string, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	set := map[string]bool{}
	for _, p := range providers {
		set[p] = true
	}
	out := map[string]string{}
	for _, c := range v.creds {
		if len(providers) > 0 && !set[c.Provider] {
			continue
		}
		plain, err := v.decrypt(c.Cipher)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", c.ID, err)
		}
		env := c.EnvVar
		if env == "" {
			if pm, ok := KnownProviders[c.Provider]; ok {
				env = pm.DefaultEnv
			}
		}
		if env != "" {
			out[env] = plain
		}
	}
	return out, nil
}

// ─── persistence ─────────────────────────────────────────────────────────────

func (v *Vault) load() error {
	data, err := os.ReadFile(v.path)
	if err != nil {
		return err
	}
	var vf vaultFile
	if err := json.Unmarshal(data, &vf); err != nil {
		return fmt.Errorf("parse vault: %w", err)
	}
	v.creds = vf.Creds
	return nil
}

func (v *Vault) save() error {
	data, err := json.MarshalIndent(vaultFile{Creds: v.creds}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(v.path), 0700); err != nil {
		return err
	}
	return os.WriteFile(v.path, data, 0600)
}

// ─── crypto ──────────────────────────────────────────────────────────────────

// deriveKey returns the 32-byte AES key for vault encryption (#141).
//
// First call: generates a real 32-byte random key, writes it to
// $stateDir/vault.key with mode 0600. Subsequent calls read it back.
// This replaces the v0.5–v0.8-pre derivation from hostname+username
// which was effectively obfuscation (anyone with read on vault.json
// AND `hostname; whoami` output could decrypt offline).
//
// The path to vault.key is derived from where vault.json lives — set
// before deriveKey is called by Open().
var vaultKeyPath string

func deriveKey() ([]byte, error) {
	if vaultKeyPath == "" {
		return nil, fmt.Errorf("vault: keyPath not set — call Open() before deriveKey()")
	}
	if b, err := os.ReadFile(vaultKeyPath); err == nil && len(b) == 32 {
		return b, nil
	}
	// Generate a fresh random key.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("vault: rand: %w", err)
	}
	// Ensure parent dir exists with restrictive perms before writing.
	if err := os.MkdirAll(filepath.Dir(vaultKeyPath), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(vaultKeyPath, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func (v *Vault) encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(v.key)
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
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (v *Vault) decrypt(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(v.key)
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
	plaintext, err := gcm.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plaintext), nil
}

// internal/keychain/openbao_backend.go — #322 (H6). OpenBao backend
// for the Keychain Backend interface. OpenBao is the Apache-2.0 fork
// of HashiCorp Vault that ships chepherd's reference HA secret
// substrate (see V0.9.2-ARCH §7).
//
// Operator wires it via:
//
//	chepherd run --keychain-backend=openbao \\
//	             --openbao-addr=https://bao.chepherd.svc:8200 \\
//	             --openbao-token-file=/var/run/secrets/openbao/token
//
// When --keychain-backend is unset, the Active() selection chain stays
// the default (macOS/secret-tool/file fallback) — H6 is opt-in for
// HA deployments + leaves dev workflows untouched.
//
// Refs #322 (#225 row H6) + #208.
package keychain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// OpenBaoBackend implements Backend against an OpenBao HTTP API.
// Uses the v2 KV secrets engine mounted at /v1/secret by default;
// operator can override the mount via Mount.
type OpenBaoBackend struct {
	// Addr is the OpenBao base URL (e.g. https://bao.chepherd.svc:8200).
	// Required.
	Addr string

	// Token is the request-scoped OpenBao auth token. Either Token or
	// TokenPath must be set; TokenPath takes precedence when both are
	// supplied (file-mounted token rotation).
	Token string

	// TokenPath is a file containing the auth token. The file is read
	// once at backend construction; future rotation needs a fresh
	// NewOpenBaoBackend call. Empty if Token is supplied inline.
	TokenPath string

	// Mount is the KV-v2 mount point. Default "secret".
	Mount string

	// HTTPClient is the underlying http.Client. Defaults to a fresh
	// client with a 10s timeout when nil.
	HTTPClient *http.Client
}

// NewOpenBaoBackend constructs an OpenBaoBackend, resolving TokenPath
// at construction time. Returns an error if both Token and TokenPath
// are empty.
func NewOpenBaoBackend(addr, token, tokenPath, mount string) (*OpenBaoBackend, error) {
	if addr == "" {
		return nil, errors.New("OpenBaoBackend: empty Addr")
	}
	if tokenPath != "" {
		b, err := os.ReadFile(tokenPath)
		if err != nil {
			return nil, fmt.Errorf("OpenBaoBackend: read TokenPath %q: %w", tokenPath, err)
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		return nil, errors.New("OpenBaoBackend: empty Token (and TokenPath read produced empty bytes)")
	}
	if mount == "" {
		mount = "secret"
	}
	return &OpenBaoBackend{
		Addr: addr, Token: token, Mount: mount,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (b *OpenBaoBackend) Name() string { return "openbao" }

// Available returns true if the backend can dial OpenBao's /sys/health
// endpoint (returns 200 / 429 / 472 / 473 — all valid OpenBao states
// per the API spec). Falls back to false on transport error.
func (b *OpenBaoBackend) Available() bool {
	if b.Addr == "" || b.Token == "" {
		return false
	}
	hc := b.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 3 * time.Second}
	}
	resp, err := hc.Get(strings.TrimRight(b.Addr, "/") + "/v1/sys/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200, 429, 472, 473, 501, 503:
		return true
	default:
		return false
	}
}

// Set stores key → value at <mount>/data/<key> via PUT.
func (b *OpenBaoBackend) Set(key, value string) error {
	if key == "" {
		return errors.New("OpenBaoBackend.Set: empty key")
	}
	body, _ := json.Marshal(map[string]any{
		"data": map[string]string{"value": value},
	})
	endpoint := b.kvPath(key)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	b.signRequest(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client().Do(req)
	if err != nil {
		return fmt.Errorf("OpenBao Set %q: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OpenBao Set %q: HTTP %d: %s",
			key, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// Get retrieves the value at <mount>/data/<key>.
func (b *OpenBaoBackend) Get(key string) (string, error) {
	if key == "" {
		return "", errors.New("OpenBaoBackend.Get: empty key")
	}
	req, err := http.NewRequest(http.MethodGet, b.kvPath(key), nil)
	if err != nil {
		return "", err
	}
	b.signRequest(req)
	resp, err := b.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("OpenBao Get %q: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("OpenBao Get %q: HTTP %d: %s",
			key, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var body struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("OpenBao Get %q: decode: %w", key, err)
	}
	val, ok := body.Data.Data["value"]
	if !ok {
		return "", ErrNotFound
	}
	return val, nil
}

// Delete removes the key (latest version + all history via metadata).
func (b *OpenBaoBackend) Delete(key string) error {
	if key == "" {
		return errors.New("OpenBaoBackend.Delete: empty key")
	}
	endpoint := strings.TrimRight(b.Addr, "/") + "/v1/" + b.Mount + "/metadata/" + key
	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	b.signRequest(req)
	resp, err := b.client().Do(req)
	if err != nil {
		return fmt.Errorf("OpenBao Delete %q: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil // idempotent
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OpenBao Delete %q: HTTP %d: %s",
			key, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func (b *OpenBaoBackend) kvPath(key string) string {
	return strings.TrimRight(b.Addr, "/") + "/v1/" + b.Mount + "/data/" + key
}

func (b *OpenBaoBackend) signRequest(req *http.Request) {
	req.Header.Set("X-Vault-Token", b.Token)
}

func (b *OpenBaoBackend) client() *http.Client {
	if b.HTTPClient != nil {
		return b.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

var _ Backend = (*OpenBaoBackend)(nil)

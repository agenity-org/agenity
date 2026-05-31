// Package keychain stores chepherd's provider credentials in the OS
// keychain (macOS Keychain, Windows Credential Manager, Linux Secret
// Service). Falls back to a 0600-mode JSON file at
// ~/.config/chepherd/credentials.json when no OS keychain is available
// (headless Linux without libsecret/DBus, etc.) — per openova caveat.
//
// The keychain entry name pattern is: "chepherd.<provider>.<instance>".
// Example: "chepherd.claude.refresh_token", "chepherd.openova.<sov>.token".
//
// On Linux this package uses the `secret-tool` CLI (libsecret) when
// available. On macOS, `security` CLI. On Windows, the future Win32
// Credential Manager binding (stubbed for v0.5; falls back to file).
// The CLI-shelling approach keeps chepherd's binary deps minimal — no
// CGo, no platform-specific Go imports.
package keychain

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const serviceName = "chepherd"

// ErrNotFound indicates the entry doesn't exist.
var ErrNotFound = errors.New("keychain: not found")

// Backend is the implementation interface. Multiple backends per OS;
// we pick the first that's available at runtime.
type Backend interface {
	Name() string
	Available() bool
	Set(key, value string) error
	Get(key string) (string, error)
	Delete(key string) error
}

// #522 — single mutex protects ALL keychain globals (installed +
// chosen). Pre-#522 install.go's installedMu protected `installed`
// while `chosen` was read here under once.Do (which doesn't lock-
// out concurrent reads), AND Install() wrote `chosen = nil` outside
// any lock. -race detector found the data race.
var (
	mu       sync.Mutex
	chosen   Backend
	chosenOK bool
)

// Active returns the backend selected for this host. Selects lazily on
// first call. Order of preference per platform:
//
//	macOS:   security CLI → file fallback
//	Linux:   secret-tool (libsecret) → file fallback
//	Windows: file fallback (Win32 binding pending)
//	other:   file fallback
func Active() Backend {
	mu.Lock()
	defer mu.Unlock()
	// #322 H6.1 — explicit install bypasses the platform chain.
	if installed != nil {
		return installed
	}
	if !chosenOK {
		candidates := platformBackends()
		for _, b := range candidates {
			if b.Available() {
				chosen = b
				chosenOK = true
				return chosen
			}
		}
		chosen = newFileBackend()
		chosenOK = true
	}
	return chosen
}

func platformBackends() []Backend {
	switch runtime.GOOS {
	case "darwin":
		return []Backend{newMacOSBackend(), newFileBackend()}
	case "linux":
		return []Backend{newSecretToolBackend(), newFileBackend()}
	default:
		return []Backend{newFileBackend()}
	}
}

// Set stores key → value via the active backend.
func Set(key, value string) error { return Active().Set(key, value) }

// Get retrieves value for key via the active backend.
func Get(key string) (string, error) { return Active().Get(key) }

// Delete removes key.
func Delete(key string) error { return Active().Delete(key) }

// ----- macOS `security` backend -----

type macosBackend struct{}

func newMacOSBackend() Backend { return &macosBackend{} }
func (b *macosBackend) Name() string { return "macos-security" }
func (b *macosBackend) Available() bool {
	_, err := exec.LookPath("security")
	return err == nil
}
func (b *macosBackend) Set(key, value string) error {
	// security add-generic-password -U -a chepherd -s <key> -w <value>
	cmd := exec.Command("security", "add-generic-password", "-U", "-a", serviceName, "-s", key, "-w", value)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("security add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
func (b *macosBackend) Get(key string) (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-a", serviceName, "-s", key, "-w").Output()
	if err != nil {
		if strings.Contains(err.Error(), "could not be found") {
			return "", ErrNotFound
		}
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}
func (b *macosBackend) Delete(key string) error {
	cmd := exec.Command("security", "delete-generic-password", "-a", serviceName, "-s", key)
	if _, err := cmd.CombinedOutput(); err != nil {
		// If not found, return ErrNotFound for parity with other backends.
		return ErrNotFound
	}
	return nil
}

// ----- Linux `secret-tool` (libsecret) backend -----

type secretToolBackend struct{}

func newSecretToolBackend() Backend { return &secretToolBackend{} }
func (b *secretToolBackend) Name() string { return "linux-secret-tool" }
func (b *secretToolBackend) Available() bool {
	_, err := exec.LookPath("secret-tool")
	if err != nil {
		return false
	}
	// Verify DBus session is reachable (secret-tool fails silently without).
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		return false
	}
	return true
}
func (b *secretToolBackend) Set(key, value string) error {
	cmd := exec.Command("secret-tool", "store", "--label="+key, "service", serviceName, "key", key)
	cmd.Stdin = strings.NewReader(value)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("secret-tool store: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
func (b *secretToolBackend) Get(key string) (string, error) {
	out, err := exec.Command("secret-tool", "lookup", "service", serviceName, "key", key).Output()
	if err != nil {
		return "", ErrNotFound
	}
	v := strings.TrimRight(string(out), "\n")
	if v == "" {
		return "", ErrNotFound
	}
	return v, nil
}
func (b *secretToolBackend) Delete(key string) error {
	if _, err := exec.Command("secret-tool", "clear", "service", serviceName, "key", key).Output(); err != nil {
		return ErrNotFound
	}
	return nil
}

// ----- File fallback (0600-mode JSON) -----

type fileBackend struct {
	mu   sync.Mutex
	path string
}

func newFileBackend() Backend {
	home, _ := os.UserHomeDir()
	return &fileBackend{path: filepath.Join(home, ".config", "chepherd", "credentials.json")}
}
func (b *fileBackend) Name() string { return "file-0600" }
func (b *fileBackend) Available() bool { return true } // always works as last resort
func (b *fileBackend) load() (map[string]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
func (b *fileBackend) save(m map[string]string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.path)
}
func (b *fileBackend) Set(key, value string) error {
	m, err := b.load()
	if err != nil {
		return err
	}
	m[key] = value
	return b.save(m)
}
func (b *fileBackend) Get(key string) (string, error) {
	m, err := b.load()
	if err != nil {
		return "", err
	}
	v, ok := m[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}
func (b *fileBackend) Delete(key string) error {
	m, err := b.load()
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return ErrNotFound
	}
	delete(m, key)
	return b.save(m)
}

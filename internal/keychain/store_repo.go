package keychain

import (
	"context"
	"strings"

	"github.com/chepherd/chepherd/internal/persistence"
)

// repoBackend implements Backend backed by a persistence.KeychainRepository.
// This is the v0.9.2 SQLite-backed Backend; the legacy
// macos/linux/file backends in this package are preserved for v0.9.1
// backward compat (Active() still selects from the platform-specific
// list when this backend isn't explicitly chosen).
//
// Refs #208.
type repoBackend struct {
	repo persistence.KeychainRepository
}

// NewBackendFromRepository wraps a persistence.KeychainRepository as a
// keychain.Backend implementing the Set/Get/Delete contract. Use via:
//
//	be := keychain.NewBackendFromRepository(store.Keychain())
//	be.Set("ANTHROPIC_API_KEY", "...")
func NewBackendFromRepository(repo persistence.KeychainRepository) Backend {
	return &repoBackend{repo: repo}
}

func (b *repoBackend) Name() string   { return "chepherd-repo" }
func (b *repoBackend) Available() bool { return b.repo != nil }

func (b *repoBackend) Set(key, value string) error {
	return b.repo.Set(context.Background(), key, value)
}

func (b *repoBackend) Get(key string) (string, error) {
	v, err := b.repo.Get(context.Background(), key)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return "", ErrNotFound
		}
		return "", err
	}
	return v, nil
}

func (b *repoBackend) Delete(key string) error {
	return b.repo.Delete(context.Background(), key)
}

package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/chepherd/chepherd/internal/persistence"
)

// NewLocalProviderFromRepository wraps a persistence.AuthSecretRepository
// as a LocalProvider. The provider's HS256 dashboard-token signing
// secret is loaded from (or initialized into) the repository under
// purpose="dashboard-hs256", aligned with the migration tool's output
// and v0.9.2 §15.2.
//
// Returns the same *LocalProvider type as NewLocalProvider so existing
// callers continue to work unchanged; this constructor is for v0.9.2
// code paths that have access to the unified persistence.Store.
//
// Refs #208.
func NewLocalProviderFromRepository(repo persistence.AuthSecretRepository) (*LocalProvider, error) {
	ctx := context.Background()
	const purpose = "dashboard-hs256"

	existing, err := repo.Get(ctx, purpose)
	if err != nil && !strings.Contains(err.Error(), "not found") {
		return nil, fmt.Errorf("auth: load %s: %w", purpose, err)
	}
	if existing != nil && len(existing.Key) > 0 {
		return &LocalProvider{secret: existing.Key}, nil
	}
	// Initialize: generate 32 random bytes + persist via Repository.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("auth: rand: %w", err)
	}
	if err := repo.Save(ctx, purpose, secret, "HS256"); err != nil {
		return nil, fmt.Errorf("auth: persist %s: %w", purpose, err)
	}
	return &LocalProvider{secret: secret}, nil
}

package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/persistence/sqlite"
)

// TestNewLocalProviderFromRepository verifies that the Repository-backed
// LocalProvider preserves the same token-mint + validate behavior as
// the file-on-disk constructor.
//
// Refs #208.
func TestNewLocalProviderFromRepository(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "auth.db")
	store, err := sqlite.NewStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	prov, err := NewLocalProviderFromRepository(store.AuthSecrets())
	if err != nil {
		t.Fatalf("NewLocalProviderFromRepository (first): %v", err)
	}
	if prov.Mode() != "local" {
		t.Errorf("Mode = %q, want local", prov.Mode())
	}

	// Mint + Validate round-trip.
	tok, err := prov.IssueBootstrapToken(context.Background(), "operator", time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	id, err := prov.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if id.Subject != "operator" {
		t.Errorf("Subject = %q, want operator", id.Subject)
	}

	// Re-open the Repository and confirm the SAME secret is loaded
	// (idempotency / persistence semantics).
	prov2, err := NewLocalProviderFromRepository(store.AuthSecrets())
	if err != nil {
		t.Fatalf("NewLocalProviderFromRepository (second): %v", err)
	}
	// A token minted by prov should validate with prov2 (same secret).
	id2, err := prov2.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("Validate via prov2: %v", err)
	}
	if id2.Subject != "operator" {
		t.Errorf("prov2 Subject = %q, want operator", id2.Subject)
	}
}

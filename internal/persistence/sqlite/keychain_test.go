package sqlite

import (
	"context"
	"strings"
	"testing"
)

func TestKeychainRepository_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewKeychainRepository(openTestDB(t))

	// Get missing → error containing "not found".
	if _, err := r.Get(ctx, "missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("Get missing err = %v, want 'not found'", err)
	}

	// Set + Get.
	if err := r.Set(ctx, "ANTHROPIC_API_KEY", "sk-...."); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, err := r.Get(ctx, "ANTHROPIC_API_KEY")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v != "sk-...." {
		t.Errorf("Get = %q, want %q", v, "sk-....")
	}

	// Set overwrites.
	if err := r.Set(ctx, "ANTHROPIC_API_KEY", "sk-new"); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}
	v, _ = r.Get(ctx, "ANTHROPIC_API_KEY")
	if v != "sk-new" {
		t.Errorf("Get after overwrite = %q, want %q", v, "sk-new")
	}

	// Delete + verify gone.
	if err := r.Delete(ctx, "ANTHROPIC_API_KEY"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := r.Get(ctx, "ANTHROPIC_API_KEY"); err == nil {
		t.Error("Get after Delete: want error, got nil")
	}
}

func TestKeychainRepository_EmptyKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewKeychainRepository(openTestDB(t))

	if _, err := r.Get(ctx, ""); err == nil {
		t.Error("Get(empty) = nil, want error")
	}
	if err := r.Set(ctx, "", "x"); err == nil {
		t.Error("Set(empty) = nil, want error")
	}
	if err := r.Delete(ctx, ""); err == nil {
		t.Error("Delete(empty) = nil, want error")
	}
}

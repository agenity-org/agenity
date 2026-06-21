package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/agenity-org/agenity/internal/persistence"
)

func TestAccountRepository_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewAccountRepository(openTestDB(t))

	// Get missing.
	if _, err := r.Get(ctx, "acc-1"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("Get missing err = %v, want 'not found'", err)
	}

	// Save.
	a := &persistence.Account{
		ID:          "acc-1",
		Class:       "anthropic",
		Label:       "personal",
		KeychainKey: "ANTHROPIC_API_KEY",
		Email:       "operator@example.com",
	}
	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() {
		t.Error("Save did not populate CreatedAt / UpdatedAt")
	}

	// Get + compare.
	got, err := r.Get(ctx, "acc-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != a.ID || got.Class != a.Class || got.Label != a.Label ||
		got.KeychainKey != a.KeychainKey || got.Email != a.Email {
		t.Errorf("Get = %+v, want %+v", got, a)
	}

	// Save again → updates UpdatedAt, keeps CreatedAt.
	a.Label = "personal-renamed"
	prevCreated := a.CreatedAt
	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("Save update: %v", err)
	}
	got, _ = r.Get(ctx, "acc-1")
	if got.Label != "personal-renamed" {
		t.Errorf("Get after update Label = %q, want personal-renamed", got.Label)
	}
	if !got.CreatedAt.Equal(prevCreated) {
		t.Errorf("CreatedAt changed: %v → %v", prevCreated, got.CreatedAt)
	}

	// List.
	if err := r.Save(ctx, &persistence.Account{ID: "acc-2", Class: "openai"}); err != nil {
		t.Fatalf("Save acc-2: %v", err)
	}
	all, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("List = %d, want 2", len(all))
	}

	// Delete.
	if err := r.Delete(ctx, "acc-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	all, _ = r.List(ctx)
	if len(all) != 1 || all[0].ID != "acc-2" {
		t.Errorf("List after delete = %v, want only acc-2", all)
	}
}

func TestAccountRepository_Validation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewAccountRepository(openTestDB(t))

	if err := r.Save(ctx, nil); err == nil {
		t.Error("Save(nil) = nil, want error")
	}
	if err := r.Save(ctx, &persistence.Account{Class: "anthropic"}); err == nil {
		t.Error("Save empty ID = nil, want error")
	}
	if err := r.Save(ctx, &persistence.Account{ID: "x"}); err == nil {
		t.Error("Save empty Class = nil, want error")
	}
	if _, err := r.Get(ctx, ""); err == nil {
		t.Error("Get(empty) = nil, want error")
	}
	if err := r.Delete(ctx, ""); err == nil {
		t.Error("Delete(empty) = nil, want error")
	}
}

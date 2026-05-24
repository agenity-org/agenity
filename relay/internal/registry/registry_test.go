package registry

import (
	"context"
	"strings"
	"testing"
)

func TestMemory_RegisterAndVerify(t *testing.T) {
	r := NewMemory()
	ctx := context.Background()

	tok, err := r.Register(ctx, Bastion{
		ID:              "bastion-alice-01",
		UserID:          "alice@example.com",
		ChepherdVersion: "0.2.0-rc1",
		Capabilities:    []string{"pause", "inject"},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !strings.HasPrefix(tok, "chep_") || len(tok) < 30 {
		t.Errorf("token looks malformed: %s", tok)
	}

	b, err := r.VerifyDaemonToken(ctx, tok)
	if err != nil {
		t.Fatalf("VerifyDaemonToken: %v", err)
	}
	if b.ID != "bastion-alice-01" || b.UserID != "alice@example.com" {
		t.Errorf("wrong bastion: %+v", b)
	}
	if b.DaemonToken != "" {
		t.Errorf("VerifyDaemonToken leaked the token field: %s", b.DaemonToken)
	}
}

func TestMemory_IdempotentReregister(t *testing.T) {
	r := NewMemory()
	ctx := context.Background()

	tok1, _ := r.Register(ctx, Bastion{ID: "b1", UserID: "u1"})
	tok2, err := r.Register(ctx, Bastion{ID: "b1", UserID: "u1"})
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if tok1 == tok2 {
		t.Errorf("expected token rotation on re-register")
	}
	// Old token must be invalid.
	if _, err := r.VerifyDaemonToken(ctx, tok1); err == nil {
		t.Errorf("old token should be revoked")
	}
	if _, err := r.VerifyDaemonToken(ctx, tok2); err != nil {
		t.Errorf("new token should work: %v", err)
	}
}

func TestMemory_IDCollisionAcrossUsers(t *testing.T) {
	r := NewMemory()
	ctx := context.Background()
	_, _ = r.Register(ctx, Bastion{ID: "shared-name", UserID: "u1"})
	_, err := r.Register(ctx, Bastion{ID: "shared-name", UserID: "u2"})
	if err != ErrTakenByOther {
		t.Errorf("want ErrTakenByOther got %v", err)
	}
}

func TestMemory_Touch(t *testing.T) {
	r := NewMemory()
	ctx := context.Background()
	_, _ = r.Register(ctx, Bastion{ID: "b1", UserID: "u1"})
	if err := r.Touch(ctx, "b1"); err != nil {
		t.Errorf("touch existing: %v", err)
	}
	if err := r.Touch(ctx, "nope"); err != ErrUnknownBastion {
		t.Errorf("touch unknown: want ErrUnknownBastion got %v", err)
	}
}

func TestMemory_ListByUser(t *testing.T) {
	r := NewMemory()
	ctx := context.Background()
	_, _ = r.Register(ctx, Bastion{ID: "b1", UserID: "alice"})
	_, _ = r.Register(ctx, Bastion{ID: "b2", UserID: "alice"})
	_, _ = r.Register(ctx, Bastion{ID: "b3", UserID: "bob"})

	aliceBastions, _ := r.ListByUser(ctx, "alice")
	if len(aliceBastions) != 2 {
		t.Errorf("alice should have 2 bastions, got %d", len(aliceBastions))
	}
	for _, b := range aliceBastions {
		if b.DaemonToken != "" {
			t.Errorf("ListByUser leaked daemon token: %s", b.DaemonToken)
		}
	}

	bobBastions, _ := r.ListByUser(ctx, "bob")
	if len(bobBastions) != 1 {
		t.Errorf("bob should have 1 bastion, got %d", len(bobBastions))
	}
}

// internal/iogrid/recipe_test.go — pins E2 recipe sign/verify roundtrip
// + tamper detection + canonical-form determinism.
//
// Refs #225 row E2.
package iogrid

import (
	"context"
	"crypto/ecdsa"
	"path/filepath"
	"testing"

	"github.com/agenity-org/agenity/internal/auth"
	"github.com/agenity-org/agenity/internal/persistence/sqlite"
)

func newTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	store, err := sqlite.NewStore(context.Background(), filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	priv, err := auth.LoadOrCreateES256(context.Background(), store.AuthSecrets())
	if err != nil {
		t.Fatalf("LoadOrCreateES256: %v", err)
	}
	return priv
}

func sampleRecipe() Recipe {
	return Recipe{
		ID:          "alibaba.qwen-code/v1.2.3",
		Version:     "1.2.3",
		Publisher:   "alibaba.qwen-code",
		AgentSlug:   "qwen-code",
		Image:       "ghcr.io/alibaba/qwen-code:1.2.3",
		DefaultArgs: []string{"--model=qwen-max"},
		RequiredEnv: []string{"DASHSCOPE_API_KEY"},
		Resources: &ResourceBudget{
			CPURequest: "100m", CPULimit: "2", MemoryRequest: "256Mi", MemoryLimit: "4Gi",
		},
		NetworkPolicy: &NetworkPolicy{
			AllowedHosts: []string{"dashscope.aliyuncs.com", "*.dashscope.cn"},
		},
		Notes: "alibaba qwen-code recipe, v1.2.3, requires DASHSCOPE_API_KEY",
	}
}

func TestRecipe_SignVerifyRoundtrip(t *testing.T) {
	t.Parallel()
	priv := newTestKey(t)
	recipe := sampleRecipe()
	signed, err := SignRecipe(recipe, priv)
	if err != nil {
		t.Fatalf("SignRecipe: %v", err)
	}
	got, err := VerifyRecipe(signed, &priv.PublicKey)
	if err != nil {
		t.Fatalf("VerifyRecipe: %v", err)
	}
	if got.ID != recipe.ID || got.Version != recipe.Version || got.AgentSlug != recipe.AgentSlug {
		t.Errorf("roundtrip mismatch: got %+v", got)
	}
	if got.Resources == nil || got.Resources.CPURequest != "100m" {
		t.Errorf("Resources lost in roundtrip: %+v", got.Resources)
	}
	if got.NetworkPolicy == nil || len(got.NetworkPolicy.AllowedHosts) != 2 {
		t.Errorf("NetworkPolicy lost: %+v", got.NetworkPolicy)
	}
}

func TestRecipe_VerifyRejectsTamperedBody(t *testing.T) {
	t.Parallel()
	priv := newTestKey(t)
	signed, _ := SignRecipe(sampleRecipe(), priv)
	// Tamper: silently change the image field by string replace in
	// the YAML wire bytes. The signature still parses but the
	// recomputed canonical form won't match.
	tampered := []byte("")
	for _, b := range signed {
		tampered = append(tampered, b)
	}
	// Replace one byte in the image field. Easier: edit a known string.
	swapped := []byte("malicious/evil-image:latest")
	out := []byte{}
	for i := 0; i < len(tampered); i++ {
		if i+len(swapped) <= len(tampered) && string(tampered[i:i+len("ghcr.io/alibaba/qwen-code:1.2.3")]) == "ghcr.io/alibaba/qwen-code:1.2.3" {
			out = append(out, swapped...)
			i += len("ghcr.io/alibaba/qwen-code:1.2.3") - 1
			continue
		}
		out = append(out, tampered[i])
	}
	_, err := VerifyRecipe(out, &priv.PublicKey)
	if err == nil {
		t.Error("VerifyRecipe accepted a tampered body (body bytes changed without re-signing)")
	}
}

func TestRecipe_VerifyRejectsWrongPublicKey(t *testing.T) {
	t.Parallel()
	priv1 := newTestKey(t)
	priv2 := newTestKey(t)
	signed, _ := SignRecipe(sampleRecipe(), priv1)
	if _, err := VerifyRecipe(signed, &priv2.PublicKey); err == nil {
		t.Error("VerifyRecipe accepted a signature signed by a different key")
	}
}

func TestRecipe_CanonicalFormIsDeterministic(t *testing.T) {
	t.Parallel()
	// Sign the same recipe twice; the JWS signature differs (ECDSA is
	// non-deterministic) but the JCS-canonical input MUST be identical
	// — we verify by re-canonicalizing twice and asserting byte equality.
	r := sampleRecipe()
	c1, err := canonicalJSON(r)
	if err != nil {
		t.Fatalf("canonicalJSON 1: %v", err)
	}
	c2, err := canonicalJSON(r)
	if err != nil {
		t.Fatalf("canonicalJSON 2: %v", err)
	}
	if string(c1) != string(c2) {
		t.Errorf("canonical form not deterministic:\n%s\n%s", c1, c2)
	}
}

func TestRecipe_CanonicalFormSortsMapKeys(t *testing.T) {
	t.Parallel()
	// Build the same recipe via two different field-order patterns
	// (Go struct layout is fixed, so we go through map[string]any).
	a := canonicalSorted(t, map[string]any{
		"id": "x", "version": "1", "publisher": "p", "agentSlug": "claude-code",
	})
	b := canonicalSorted(t, map[string]any{
		"version": "1", "agentSlug": "claude-code", "publisher": "p", "id": "x",
	})
	if string(a) != string(b) {
		t.Errorf("map insertion order leaked into canonical form:\nA = %s\nB = %s", a, b)
	}
}

func canonicalSorted(t *testing.T, m map[string]any) []byte {
	t.Helper()
	out, err := canonicalJSON(m)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	return out
}

func TestRecipe_SignRecipeValidation(t *testing.T) {
	t.Parallel()
	priv := newTestKey(t)
	cases := []struct {
		name string
		in   Recipe
	}{
		{"empty ID", Recipe{Publisher: "p"}},
		{"empty Publisher", Recipe{ID: "id"}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, err := SignRecipe(c.in, priv); err == nil {
				t.Errorf("SignRecipe(%+v) = nil, want error", c.in)
			}
		})
	}
	// nil key
	if _, err := SignRecipe(sampleRecipe(), nil); err == nil {
		t.Error("SignRecipe(nil priv) = nil, want error")
	}
}

func TestRecipe_VerifyRecipeValidation(t *testing.T) {
	t.Parallel()
	priv := newTestKey(t)
	// nil pub
	if _, err := VerifyRecipe([]byte("recipe:\n  id: x"), nil); err == nil {
		t.Error("VerifyRecipe(nil pub) = nil, want error")
	}
	// no signature
	if _, err := VerifyRecipe([]byte("recipe:\n  id: x\n  publisher: p"), &priv.PublicKey); err == nil {
		t.Error("VerifyRecipe(no signature) = nil, want error")
	}
	// malformed YAML
	if _, err := VerifyRecipe([]byte("not: yaml: at: all:"), &priv.PublicKey); err == nil {
		t.Error("VerifyRecipe(malformed yaml) = nil, want error")
	}
}

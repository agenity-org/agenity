// Package iogrid implements the chepherd v0.9.3 iogrid marketplace
// integration: agent-card extensions advertising the iogrid endpoint,
// a YAML+JWS recipe format consumed by chepherd's PodRunner /
// ProcessRunner to spawn agents from third-party "recipes", and a
// HeadlessIOgridDeliverer routing A2A messages onto the iogrid task
// queue (E3 — separate file).
//
// E2 (this file) — Recipe format:
//   - Canonical YAML body holding the recipe (agent ref, model env,
//     resource budget, network policy, etc.)
//   - ES256 JWS signature over the JCS-canonical JSON form of the
//     recipe body (RFC 8785 + RFC 7515)
//   - Compact YAML wire shape: `recipe:` + `signature:` keys at top
//     level so a single YAML document carries both halves
//
// Producers (iogrid catalogue uploader, chepherd-side recipe author):
//
//	signed, err := iogrid.SignRecipe(recipe, privateKey)
//
// Consumers (chepherd runtime when a Spawn references an iogrid recipe):
//
//	recipe, err := iogrid.VerifyRecipe(yamlBytes, publisherPublicKey)
//
// Refs #225 row E2.
package iogrid

import (
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/chepherd/chepherd/internal/auth"
)

// Recipe is the wire-shape of a single iogrid catalogue entry. v0.9.3
// scope is the minimum that lets PodRunner / ProcessRunner spawn an
// agent from a third-party recipe without trusting the publisher;
// v0.9.4 extends with pricing tiers, Stripe Connect integration, and
// reputation signals.
type Recipe struct {
	// ID is the catalogue-unique identifier. Stable across versions of
	// the same logical recipe (versioned via Version).
	ID string `yaml:"id" json:"id"`

	// Version is semver. Bumped on any change to the body's content.
	Version string `yaml:"version" json:"version"`

	// Publisher is the iogrid publisher identity (DNS-like, e.g.
	// "alibaba.qwen-code") whose public key signs this recipe.
	Publisher string `yaml:"publisher" json:"publisher"`

	// AgentSlug is the agent flavor (claude-code, qwen-code, aider,
	// gemini-cli, ...). Must be a slug chepherd's agentcatalog knows.
	AgentSlug string `yaml:"agentSlug" json:"agentSlug"`

	// Image is the OCI image reference for this recipe's container.
	// Empty falls back to chepherd's default agent image.
	Image string `yaml:"image,omitempty" json:"image,omitempty"`

	// DefaultArgs are appended to the agent binary's argv after the
	// builtin DefaultArgs from agentcatalog. Empty = none.
	DefaultArgs []string `yaml:"defaultArgs,omitempty" json:"defaultArgs,omitempty"`

	// RequiredEnv lists env-vars the spawner must populate; chepherd
	// surfaces a clean 400 if any are missing at spawn-time.
	RequiredEnv []string `yaml:"requiredEnv,omitempty" json:"requiredEnv,omitempty"`

	// Resources budgets CPU/memory the agent's container is granted.
	// Optional; empty defers to chart-level defaults.
	Resources *ResourceBudget `yaml:"resources,omitempty" json:"resources,omitempty"`

	// NetworkPolicy advertises what hosts/ports the agent expects to
	// reach. Used by the chart to provision a NetworkPolicy CRD; if
	// the operator runs without NetworkPolicy enforcement (Cilium
	// disabled), this is informational.
	NetworkPolicy *NetworkPolicy `yaml:"networkPolicy,omitempty" json:"networkPolicy,omitempty"`

	// Notes is free-form; surfaced in dashboard's recipe browser.
	Notes string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

type ResourceBudget struct {
	CPURequest    string `yaml:"cpuRequest,omitempty" json:"cpuRequest,omitempty"`
	CPULimit      string `yaml:"cpuLimit,omitempty" json:"cpuLimit,omitempty"`
	MemoryRequest string `yaml:"memoryRequest,omitempty" json:"memoryRequest,omitempty"`
	MemoryLimit   string `yaml:"memoryLimit,omitempty" json:"memoryLimit,omitempty"`
}

type NetworkPolicy struct {
	// AllowedHosts is the list of DNS names the agent is permitted to
	// reach. Empty = allow none (chepherd's default-deny). Wildcards
	// supported per cilium-cidr syntax ("*.openai.com").
	AllowedHosts []string `yaml:"allowedHosts,omitempty" json:"allowedHosts,omitempty"`
}

// SignedRecipe is the YAML wire shape of a signed recipe document.
// Recipe + Signature live as siblings under a single top-level
// document so producers can `yaml.Marshal` directly and consumers
// can split + verify in one decode pass.
type SignedRecipe struct {
	Recipe    Recipe `yaml:"recipe" json:"recipe"`
	Signature string `yaml:"signature" json:"signature"` // compact JWS over the JCS-canonical JSON of Recipe
}

// SignRecipe canonicalizes the recipe body to JCS-canonical JSON
// (RFC 8785 minimal), signs the SHA256(canonicalJSON) under ES256
// via the package-shared SignJWS helper, and returns the YAML wire
// shape carrying both halves.
//
// Refs #225 row E2.
func SignRecipe(recipe Recipe, priv *ecdsa.PrivateKey) ([]byte, error) {
	if priv == nil {
		return nil, errors.New("iogrid SignRecipe: nil private key")
	}
	if recipe.ID == "" {
		return nil, errors.New("iogrid SignRecipe: empty Recipe.ID")
	}
	if recipe.Publisher == "" {
		return nil, errors.New("iogrid SignRecipe: empty Recipe.Publisher")
	}
	canonical, err := canonicalJSON(recipe)
	if err != nil {
		return nil, fmt.Errorf("iogrid SignRecipe: canonicalize: %w", err)
	}
	// Wrap the canonical bytes as a JWS payload claim "recipe_jcs"
	// so the JWS validator (already in internal/auth) reuses its
	// payload-parsing pipeline without bespoke binary modes.
	jws, err := auth.SignJWS(priv, map[string]any{
		"recipe_jcs": string(canonical),
		"iogrid_v":   "1",
	})
	if err != nil {
		return nil, fmt.Errorf("iogrid SignRecipe: SignJWS: %w", err)
	}
	out := SignedRecipe{Recipe: recipe, Signature: jws}
	return yaml.Marshal(out)
}

// VerifyRecipe decodes the signed YAML document, recomputes the
// recipe body's JCS-canonical JSON, parses the JWS, verifies it
// under pub, and asserts the JWS payload's recipe_jcs matches the
// recomputed canonical bytes. Returns the verified Recipe on success.
//
// Refs #225 row E2.
func VerifyRecipe(yamlBytes []byte, pub *ecdsa.PublicKey) (*Recipe, error) {
	if pub == nil {
		return nil, errors.New("iogrid VerifyRecipe: nil public key")
	}
	var doc SignedRecipe
	if err := yaml.Unmarshal(yamlBytes, &doc); err != nil {
		return nil, fmt.Errorf("iogrid VerifyRecipe: yaml: %w", err)
	}
	if doc.Recipe.ID == "" {
		return nil, errors.New("iogrid VerifyRecipe: empty recipe.id in document")
	}
	if doc.Signature == "" {
		return nil, errors.New("iogrid VerifyRecipe: missing signature")
	}
	claims, err := auth.VerifyJWS(pub, doc.Signature)
	if err != nil {
		return nil, fmt.Errorf("iogrid VerifyRecipe: JWS: %w", err)
	}
	gotCanon, _ := claims["recipe_jcs"].(string)
	wantCanon, err := canonicalJSON(doc.Recipe)
	if err != nil {
		return nil, fmt.Errorf("iogrid VerifyRecipe: canonicalize: %w", err)
	}
	if gotCanon != string(wantCanon) {
		return nil, errors.New("iogrid VerifyRecipe: signed canonical form != recomputed (recipe body tampered post-signing)")
	}
	return &doc.Recipe, nil
}

// canonicalJSON returns the JCS-canonical (RFC 8785) JSON encoding
// of v. Minimal implementation: marshal to JSON with sorted keys at
// every nesting level + no whitespace. Numbers are not re-normalized
// (Go's json.Marshal already emits canonical-enough number forms for
// the integers/strings used in Recipe; floats are not used today).
// A full RFC 8785 implementation handling float normalization +
// UTF-8 unicode escapes is a v0.9.4 hardening task.
//
// The function takes any (typed struct or generic map) and routes
// through json.Marshal → map[string]any → recursive sort → re-Marshal.
func canonicalJSON(v any) ([]byte, error) {
	first, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(first, &generic); err != nil {
		return nil, err
	}
	sorted := sortKeys(generic)
	out, err := json.Marshal(sorted)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// sortKeys recursively converts every map[string]any into an ordered
// slice-of-pairs representation by sorting keys. json.Marshal of the
// resulting structure emits keys in iteration order; Go's json package
// guarantees iteration over slices is positional, so the output is
// deterministic.
//
// To get sorted-key output from json.Marshal we use a sort and then
// reassemble into a custom orderedMap-like structure that json.Marshal
// renders correctly. The simplest cross-cutting way: re-marshal map keys
// by emitting via json.RawMessage segments.
//
// Implementation: walk the value, for each map convert to a []struct{K,
// V} sorted by K, render via custom MarshalJSON below.
func sortKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(orderedMap, 0, len(keys))
		for _, k := range keys {
			out = append(out, orderedKV{K: k, V: sortKeys(x[k])})
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = sortKeys(item)
		}
		return out
	default:
		return v
	}
}

type orderedMap []orderedKV

type orderedKV struct {
	K string
	V any
}

// MarshalJSON renders orderedMap as a JSON object with keys in slice
// order — the calling code (sortKeys) has already sorted them.
func (m orderedMap) MarshalJSON() ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	var buf []byte
	buf = append(buf, '{')
	for i, kv := range m {
		if i > 0 {
			buf = append(buf, ',')
		}
		k, err := json.Marshal(kv.K)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(kv.V)
		if err != nil {
			return nil, err
		}
		buf = append(buf, k...)
		buf = append(buf, ':')
		buf = append(buf, v...)
	}
	buf = append(buf, '}')
	return buf, nil
}

// Package provider abstracts the LLM gateways chepherd can talk to:
// Claude OAuth, Anthropic API, OpenRouter, OpenAI, OpenOva NewAPI, Ollama.
//
// chepherd doesn't make LLM calls itself for inference — the agents
// (claude-code, qwen-code, ...) do that via their own configured providers.
// This abstraction exists for:
//
//  1. Health-checking each configured provider on first-run wizard
//  2. Surfacing model lists in the dashboard / provider settings UI
//  3. Telling the spawned agent which env vars / API URLs to use
//  4. (Future) Routing chepherd's OWN judge/shepherd-helper LLM calls
//     when we run a small auxiliary model inside chepherd's process
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Kind enumerates the supported provider families.
type Kind string

const (
	KindClaudeOAuth   Kind = "claude-oauth"   // Claude Pro/Max OAuth
	KindAnthropicAPI  Kind = "anthropic-api"  // direct Anthropic API
	KindOpenRouter    Kind = "openrouter"
	KindOpenAI        Kind = "openai"
	KindOpenOvaNewAPI Kind = "openova-newapi" // an OpenOva Sovereign's NewAPI gateway
	KindOllama        Kind = "ollama"
)

// AllKinds returns every supported provider in canonical UI order.
func AllKinds() []Kind {
	return []Kind{KindClaudeOAuth, KindAnthropicAPI, KindOpenRouter, KindOpenAI, KindOpenOvaNewAPI, KindOllama}
}

// Provider is the contract any LLM gateway implementation must satisfy.
type Provider interface {
	// Kind returns the provider family.
	Kind() Kind

	// Label is a short user-facing name (e.g. "Claude (subscription)",
	// "OpenOva NewAPI — sovereign.example.io"). Includes the configured
	// instance identifier so multi-instance setups are distinguishable.
	Label() string

	// Healthcheck verifies the credentials + reachability work. Returns
	// the resolved base URL + a short status string ("ok", "auth failed",
	// "unreachable"), or an error.
	Healthcheck(ctx context.Context) (baseURL, status string, err error)

	// Models lists the model IDs available through this provider, e.g.
	// ["claude-3-5-sonnet-20241022", "claude-3-opus-20240229"].
	Models(ctx context.Context) ([]string, error)

	// AgentEnv returns environment variables to inject into a spawned
	// agent's process so it routes inference through this provider.
	// Keys depend on the provider + agent (e.g. claude-code uses
	// ANTHROPIC_API_KEY; aider uses OPENAI_API_KEY + OPENAI_BASE_URL).
	AgentEnv(agentSlug string) (map[string]string, error)
}

// Config is the on-disk shape for a single configured provider instance.
// Persisted at ~/.config/chepherd/providers.json (alongside but not in
// the same file as credentials — secrets live in keychain).
type Config struct {
	Kind  Kind   `json:"kind"`
	Label string `json:"label,omitempty"` // user-supplied name (e.g. "work sovereign")
	// BaseURL is the gateway URL when applicable. Empty for providers
	// that have a canonical endpoint (Anthropic API, Claude OAuth).
	BaseURL string `json:"base_url,omitempty"`
	// SecretRef is the keychain key holding the secret. Empty for
	// providers without a secret (e.g. Ollama on localhost).
	SecretRef string `json:"secret_ref,omitempty"`
	// Model is the preferred default model ID (optional).
	Model string `json:"model,omitempty"`
}

// ErrUnconfigured is returned when an operation needs a credential the
// caller hasn't provided.
var ErrUnconfigured = errors.New("provider: unconfigured (missing secret or base URL)")

// httpClientWithTimeout returns a *http.Client that gives up quickly so
// healthchecks don't block the dashboard.
func httpClientWithTimeout(d time.Duration) *http.Client {
	return &http.Client{Timeout: d}
}

// IsHTTPURL returns true if s parses as an http or https URL.
func IsHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// agentExpectsOpenAIStyle reports whether the named agent CLI reads its
// LLM target from the OpenAI-compatible env triple (OPENAI_API_KEY +
// OPENAI_BASE_URL). Used by every provider's AgentEnv to fan out the
// right names for the right agent.
func agentExpectsOpenAIStyle(agent string) bool {
	switch agent {
	case "aider", "little-coder", "opencode", "qwen-code":
		return true
	}
	return false
}

// agentExpectsAnthropicStyle reports whether the named agent CLI reads
// ANTHROPIC_API_KEY.
func agentExpectsAnthropicStyle(agent string) bool {
	switch agent {
	case "claude-code", "cursor-agent":
		return true
	}
	return false
}

// formatBearer builds the "Authorization: Bearer <token>" header value.
func formatBearer(token string) string { return "Bearer " + token }

// envForOpenAI returns the OPENAI_* triple for an OpenAI-compatible gateway.
func envForOpenAI(baseURL, apiKey string) map[string]string {
	return map[string]string{
		"OPENAI_API_KEY":  apiKey,
		"OPENAI_BASE_URL": baseURL,
	}
}

// envForAnthropic returns the ANTHROPIC_* env set.
func envForAnthropic(baseURL, apiKey string) map[string]string {
	m := map[string]string{"ANTHROPIC_API_KEY": apiKey}
	if baseURL != "" {
		m["ANTHROPIC_BASE_URL"] = baseURL
	}
	return m
}

// emptyEnv is shorthand.
func emptyEnv() map[string]string { return map[string]string{} }

// Make returns a Provider implementation for the given config. The
// secret value should be pre-resolved from the keychain by the caller.
// Returns an error if Kind is unsupported.
func Make(cfg Config, secret string) (Provider, error) {
	switch cfg.Kind {
	case KindOpenOvaNewAPI:
		return newNewAPIProvider(cfg, secret), nil
	case KindOpenRouter:
		return newOpenRouterProvider(cfg, secret), nil
	case KindOpenAI:
		return newOpenAIProvider(cfg, secret), nil
	case KindAnthropicAPI:
		return newAnthropicProvider(cfg, secret), nil
	case KindOllama:
		return newOllamaProvider(cfg), nil
	case KindClaudeOAuth:
		return newClaudeOAuthProvider(cfg, secret), nil
	}
	return nil, fmt.Errorf("provider: unsupported kind %q", cfg.Kind)
}

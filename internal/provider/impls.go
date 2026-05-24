package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ---- NewAPI provider (OpenOva's OneAPI fork — OpenAI-compatible) ----

type newapiProvider struct {
	cfg    Config
	secret string
}

func newNewAPIProvider(cfg Config, secret string) Provider {
	return &newapiProvider{cfg: cfg, secret: secret}
}

func (p *newapiProvider) Kind() Kind { return KindOpenOvaNewAPI }
func (p *newapiProvider) Label() string {
	if p.cfg.Label != "" {
		return "OpenOva NewAPI — " + p.cfg.Label
	}
	return "OpenOva NewAPI — " + p.cfg.BaseURL
}
func (p *newapiProvider) Healthcheck(ctx context.Context) (string, string, error) {
	if p.cfg.BaseURL == "" {
		return "", "unconfigured", ErrUnconfigured
	}
	if !IsHTTPURL(p.cfg.BaseURL) {
		return "", "invalid url", fmt.Errorf("not http(s): %s", p.cfg.BaseURL)
	}
	return openaiCompatHealthcheck(ctx, p.cfg.BaseURL, p.secret)
}
func (p *newapiProvider) Models(ctx context.Context) ([]string, error) {
	return openaiCompatModels(ctx, p.cfg.BaseURL, p.secret)
}
func (p *newapiProvider) AgentEnv(agentSlug string) (map[string]string, error) {
	if p.secret == "" || p.cfg.BaseURL == "" {
		return nil, ErrUnconfigured
	}
	if agentExpectsOpenAIStyle(agentSlug) {
		return envForOpenAI(p.cfg.BaseURL, p.secret), nil
	}
	if agentExpectsAnthropicStyle(agentSlug) {
		// claude-code can speak OpenAI-compatible via ANTHROPIC_BASE_URL override + LLM_GATEWAY_TOKEN
		// Actually openova-side uses LLM_GATEWAY_URL + LLM_GATEWAY_TOKEN — preserve compatibility
		return map[string]string{
			"LLM_GATEWAY_URL":   p.cfg.BaseURL,
			"LLM_GATEWAY_TOKEN": p.secret,
			"ANTHROPIC_BASE_URL": p.cfg.BaseURL,
			"ANTHROPIC_API_KEY": p.secret,
		}, nil
	}
	return emptyEnv(), nil
}

// ---- OpenRouter ----

type openrouterProvider struct {
	cfg    Config
	secret string
}

func newOpenRouterProvider(cfg Config, secret string) Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://openrouter.ai/api"
	}
	return &openrouterProvider{cfg: cfg, secret: secret}
}

func (p *openrouterProvider) Kind() Kind     { return KindOpenRouter }
func (p *openrouterProvider) Label() string  { return "OpenRouter" }
func (p *openrouterProvider) Healthcheck(ctx context.Context) (string, string, error) {
	return openaiCompatHealthcheck(ctx, p.cfg.BaseURL, p.secret)
}
func (p *openrouterProvider) Models(ctx context.Context) ([]string, error) {
	return openaiCompatModels(ctx, p.cfg.BaseURL, p.secret)
}
func (p *openrouterProvider) AgentEnv(agentSlug string) (map[string]string, error) {
	if p.secret == "" {
		return nil, ErrUnconfigured
	}
	if agentExpectsOpenAIStyle(agentSlug) {
		return envForOpenAI(p.cfg.BaseURL+"/v1", p.secret), nil
	}
	if agentExpectsAnthropicStyle(agentSlug) {
		return envForAnthropic(p.cfg.BaseURL+"/v1", p.secret), nil
	}
	return emptyEnv(), nil
}

// ---- OpenAI ----

type openaiProvider struct {
	cfg    Config
	secret string
}

func newOpenAIProvider(cfg Config, secret string) Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com"
	}
	return &openaiProvider{cfg: cfg, secret: secret}
}

func (p *openaiProvider) Kind() Kind    { return KindOpenAI }
func (p *openaiProvider) Label() string { return "OpenAI" }
func (p *openaiProvider) Healthcheck(ctx context.Context) (string, string, error) {
	return openaiCompatHealthcheck(ctx, p.cfg.BaseURL, p.secret)
}
func (p *openaiProvider) Models(ctx context.Context) ([]string, error) {
	return openaiCompatModels(ctx, p.cfg.BaseURL, p.secret)
}
func (p *openaiProvider) AgentEnv(agentSlug string) (map[string]string, error) {
	if p.secret == "" {
		return nil, ErrUnconfigured
	}
	if agentExpectsOpenAIStyle(agentSlug) {
		return envForOpenAI(p.cfg.BaseURL+"/v1", p.secret), nil
	}
	return emptyEnv(), nil
}

// ---- Anthropic API (direct) ----

type anthropicProvider struct {
	cfg    Config
	secret string
}

func newAnthropicProvider(cfg Config, secret string) Provider {
	return &anthropicProvider{cfg: cfg, secret: secret}
}

func (p *anthropicProvider) Kind() Kind    { return KindAnthropicAPI }
func (p *anthropicProvider) Label() string { return "Anthropic API" }
func (p *anthropicProvider) Healthcheck(ctx context.Context) (string, string, error) {
	if p.secret == "" {
		return "", "unconfigured", ErrUnconfigured
	}
	// Anthropic doesn't expose /v1/models publicly; do a 200-ok HEAD on the
	// docs page or just trust the key shape (sk-ant-...).
	if !strings.HasPrefix(p.secret, "sk-ant-") {
		return "https://api.anthropic.com", "key shape suspect", nil
	}
	return "https://api.anthropic.com", "ok", nil
}
func (p *anthropicProvider) Models(_ context.Context) ([]string, error) {
	// Known model IDs as of 2026-05; in practice queried via the agent.
	return []string{
		"claude-opus-4-7",
		"claude-sonnet-4-6",
		"claude-haiku-4-5",
	}, nil
}
func (p *anthropicProvider) AgentEnv(agentSlug string) (map[string]string, error) {
	if p.secret == "" {
		return nil, ErrUnconfigured
	}
	if agentExpectsAnthropicStyle(agentSlug) {
		return envForAnthropic("", p.secret), nil
	}
	return emptyEnv(), nil
}

// ---- Claude OAuth (Pro/Max subscription) ----

type claudeOAuthProvider struct {
	cfg     Config
	refresh string // refresh token
}

func newClaudeOAuthProvider(cfg Config, refresh string) Provider {
	return &claudeOAuthProvider{cfg: cfg, refresh: refresh}
}

func (p *claudeOAuthProvider) Kind() Kind    { return KindClaudeOAuth }
func (p *claudeOAuthProvider) Label() string { return "Claude (subscription)" }
func (p *claudeOAuthProvider) Healthcheck(ctx context.Context) (string, string, error) {
	if p.refresh == "" {
		return "", "unauthenticated", ErrUnconfigured
	}
	// Real impl: exchange refresh for access via Claude's OAuth endpoint.
	// For v0.5 stub: assume valid if shaped like a refresh token.
	if !strings.HasPrefix(p.refresh, "claude-") && len(p.refresh) < 20 {
		return "https://claude.ai", "refresh token suspect", nil
	}
	return "https://claude.ai", "ok", nil
}
func (p *claudeOAuthProvider) Models(_ context.Context) ([]string, error) {
	return []string{
		"claude-opus-4-7",
		"claude-sonnet-4-6",
		"claude-haiku-4-5",
	}, nil
}
func (p *claudeOAuthProvider) AgentEnv(agentSlug string) (map[string]string, error) {
	if p.refresh == "" {
		return nil, ErrUnconfigured
	}
	// claude-code reads its OAuth state from ~/.claude/credentials.json
	// when running with a logged-in account — env vars aren't required.
	// Return empty so the spawn doesn't overwrite the user's local login.
	return emptyEnv(), nil
}

// ---- Ollama (localhost) ----

type ollamaProvider struct {
	cfg Config
}

func newOllamaProvider(cfg Config) Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:11434"
	}
	return &ollamaProvider{cfg: cfg}
}

func (p *ollamaProvider) Kind() Kind    { return KindOllama }
func (p *ollamaProvider) Label() string { return "Ollama (" + p.cfg.BaseURL + ")" }
func (p *ollamaProvider) Healthcheck(ctx context.Context) (string, string, error) {
	c := httpClientWithTimeout(2 * time.Second)
	req, _ := http.NewRequestWithContext(ctx, "GET", p.cfg.BaseURL+"/api/tags", nil)
	resp, err := c.Do(req)
	if err != nil {
		return p.cfg.BaseURL, "unreachable", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return p.cfg.BaseURL, fmt.Sprintf("status %d", resp.StatusCode), nil
	}
	return p.cfg.BaseURL, "ok", nil
}
func (p *ollamaProvider) Models(ctx context.Context) ([]string, error) {
	c := httpClientWithTimeout(3 * time.Second)
	req, _ := http.NewRequestWithContext(ctx, "GET", p.cfg.BaseURL+"/api/tags", nil)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		out = append(out, m.Name)
	}
	return out, nil
}
func (p *ollamaProvider) AgentEnv(agentSlug string) (map[string]string, error) {
	if agentExpectsOpenAIStyle(agentSlug) {
		// Ollama has an OpenAI-compatible endpoint at /v1
		return envForOpenAI(p.cfg.BaseURL+"/v1", "ollama"), nil
	}
	return emptyEnv(), nil
}

// ---- Shared OpenAI-compatible helpers (used by NewAPI / OpenRouter / OpenAI) ----

// openaiCompatHealthcheck hits GET <base>/v1/models with the bearer
// token. Returns ("<base>/v1", status, err).
func openaiCompatHealthcheck(ctx context.Context, base, secret string) (string, string, error) {
	if secret == "" {
		return base, "unauthenticated", ErrUnconfigured
	}
	c := httpClientWithTimeout(4 * time.Second)
	url := strings.TrimSuffix(base, "/") + "/v1/models"
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Authorization", formatBearer(secret))
	resp, err := c.Do(req)
	if err != nil {
		return base, "unreachable", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 200:
		return base + "/v1", "ok", nil
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return base + "/v1", "auth failed", nil
	default:
		return base + "/v1", fmt.Sprintf("status %d", resp.StatusCode), nil
	}
}

// openaiCompatModels fetches the model list from an OpenAI-compatible API.
func openaiCompatModels(ctx context.Context, base, secret string) ([]string, error) {
	if secret == "" {
		return nil, ErrUnconfigured
	}
	c := httpClientWithTimeout(5 * time.Second)
	url := strings.TrimSuffix(base, "/") + "/v1/models"
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Authorization", formatBearer(secret))
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("models: status %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		out = append(out, m.ID)
	}
	return out, nil
}

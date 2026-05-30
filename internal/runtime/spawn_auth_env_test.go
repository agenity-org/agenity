// internal/runtime/spawn_auth_env_test.go — pins the post-#227
// invariant for claude-code (file-mount canonical, env-injection
// banned) AND the #225 row H1 contract for per-flavor auth env across
// qwen-code, aider, gemini-cli. Future flavors append to the
// per-flavor guards so the contract stays explicit — adding to
// agentAuthEnvTable in runtime.go MUST be paired with a test here.
//
// Refs #208 #218 #221 #227 #225 row H1.
package runtime

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

// fakeVault implements VaultProvider for unit tests. Stores one
// credential keyed by ID; ListByProvider filters by Provider field.
type fakeVault struct {
	creds map[string]fakeCred
}

type fakeCred struct {
	meta  VaultCredMeta
	value string
	err   error
}

func (f *fakeVault) ListByProvider(provider string) []VaultCredMeta {
	out := []VaultCredMeta{}
	for _, c := range f.creds {
		if c.meta.Provider == provider {
			out = append(out, c.meta)
		}
	}
	return out
}

func (f *fakeVault) GetValue(id string) (string, error) {
	c, ok := f.creds[id]
	if !ok {
		return "", errors.New("not found")
	}
	return c.value, c.err
}

func (f *fakeVault) UpdateValue(_, _ string) error { return nil }

// TestRuntime_AgentAuthEnv_ClaudeCodeReturnsNil — #227 regression
// gate. Even when a fully-populated vault carries a claude-oauth
// entry with a valid accessToken, agentAuthEnv MUST NOT return an
// env-var injection for claude-code. The file-mount path is the
// canonical credential source; env-var pin-and-cant-refresh is the
// 401-after-expiry shape this test gates.
//
// Refs #227.
func TestRuntime_AgentAuthEnv_ClaudeCodeReturnsNil(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	vault := &fakeVault{creds: map[string]fakeCred{
		"cred-1": {
			meta:  VaultCredMeta{ID: "cred-1", Provider: "claude-oauth", Label: "operator-claude"},
			value: `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-FAKE-TOKEN-XYZ","refreshToken":"rt-FAKE","expiresAt":9999999999999}}`,
		},
	}}
	rt.SetVault(vault)

	got := rt.agentAuthEnv("claude-code")
	if got != nil {
		t.Errorf("agentAuthEnv(claude-code) = %v, want nil — #227 regression: env-var pin defeats refresh-on-expiry, file-mount is canonical", got)
	}
}

// TestRuntime_AgentAuthEnv_NoMatchingProvider — when the vault has no
// entry for the slug's configured provider, agentAuthEnv returns nil
// (CLI's own error surface tells the operator about missing creds).
// Pinned post-#225 row H1: a vault carrying only claude-oauth (no
// dashscope, no openai, no anthropic, no google) yields nil for every
// non-claude-code slug too.
//
// Refs #208 #227 #225 row H1.
func TestRuntime_AgentAuthEnv_NoMatchingProvider(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt.SetVault(&fakeVault{creds: map[string]fakeCred{
		"cred-1": {
			meta:  VaultCredMeta{ID: "cred-1", Provider: "claude-oauth"},
			value: `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-FAKE"}}`,
		},
	}})

	for _, slug := range []string{"claude-code", "claude", "qwen-code", "aider", "gemini-cli", "opencode", "unknown-slug"} {
		if got := rt.agentAuthEnv(slug); got != nil {
			t.Errorf("agentAuthEnv(%q) = %v, want nil (vault has only claude-oauth)", slug, got)
		}
	}
}

// TestRuntime_AgentAuthEnv_NoVault — backward-compat path: nil vault
// returns nil env. Preserved through #227's revert; the v0.5/v0.7
// file-on-disk fallback should never see an env injection.
//
// Refs #208 #227.
func TestRuntime_AgentAuthEnv_NoVault(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Note: do NOT call SetVault — r.vault is nil

	if got := rt.agentAuthEnv("claude-code"); got != nil {
		t.Errorf("nil vault: env = %v, want nil", got)
	}
	if got := rt.agentAuthEnv("qwen-code"); got != nil {
		t.Errorf("nil vault: qwen-code env = %v, want nil", got)
	}
}

// TestRuntime_AgentAuthEnv_QwenInjectsDashScope — #225 row H1 contract:
// qwen-code with a dashscope-api vault entry gets DASHSCOPE_API_KEY=<value>.
// Refs #225 row H1.
func TestRuntime_AgentAuthEnv_QwenInjectsDashScope(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt.SetVault(&fakeVault{creds: map[string]fakeCred{
		"cred-1": {
			meta:  VaultCredMeta{ID: "cred-1", Provider: "dashscope-api", Label: "operator-qwen"},
			value: "sk-dashscope-FAKE-12345",
		},
	}})
	got := rt.agentAuthEnv("qwen-code")
	if len(got) != 1 || got[0] != "DASHSCOPE_API_KEY=sk-dashscope-FAKE-12345" {
		t.Errorf("agentAuthEnv(qwen-code) = %v, want [DASHSCOPE_API_KEY=sk-dashscope-FAKE-12345]", got)
	}
}

// TestRuntime_AgentAuthEnv_AiderBothChannels — aider gets both
// ANTHROPIC_API_KEY and OPENAI_API_KEY when both vault providers
// carry entries. aider's --model arg decides at request time which
// it uses.
// Refs #225 row H1.
func TestRuntime_AgentAuthEnv_AiderBothChannels(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt.SetVault(&fakeVault{creds: map[string]fakeCred{
		"cred-1": {meta: VaultCredMeta{ID: "cred-1", Provider: "anthropic-api"}, value: "sk-ant-FAKE"},
		"cred-2": {meta: VaultCredMeta{ID: "cred-2", Provider: "openai-api"}, value: "sk-oai-FAKE"},
	}})
	got := rt.agentAuthEnv("aider")
	sort.Strings(got)
	want := []string{"ANTHROPIC_API_KEY=sk-ant-FAKE", "OPENAI_API_KEY=sk-oai-FAKE"}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("agentAuthEnv(aider) = %v, want %v", got, want)
	}
}

// TestRuntime_AgentAuthEnv_GeminiCLI — gemini-cli with a google-api
// vault entry gets GOOGLE_API_KEY=<value>.
// Refs #225 row H1.
func TestRuntime_AgentAuthEnv_GeminiCLI(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt.SetVault(&fakeVault{creds: map[string]fakeCred{
		"cred-1": {meta: VaultCredMeta{ID: "cred-1", Provider: "google-api"}, value: "AIza-FAKE-12345"},
	}})
	got := rt.agentAuthEnv("gemini-cli")
	if len(got) != 1 || got[0] != "GOOGLE_API_KEY=AIza-FAKE-12345" {
		t.Errorf("agentAuthEnv(gemini-cli) = %v, want [GOOGLE_API_KEY=AIza-FAKE-12345]", got)
	}
}

// TestRuntime_AgentAuthEnv_ClaudeCodeStillNilEvenWithVault — #227
// regression gate persists post-H1: even when the vault has every
// provider populated (including the ones non-claude flavors use),
// claude-code returns nil because it's deliberately ABSENT from
// agentAuthEnvTable.
// Refs #225 row H1 #227.
func TestRuntime_AgentAuthEnv_ClaudeCodeStillNilEvenWithVault(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt.SetVault(&fakeVault{creds: map[string]fakeCred{
		"cred-1": {meta: VaultCredMeta{ID: "cred-1", Provider: "claude-oauth"}, value: "tok"},
		"cred-2": {meta: VaultCredMeta{ID: "cred-2", Provider: "anthropic-api"}, value: "sk-ant"},
		"cred-3": {meta: VaultCredMeta{ID: "cred-3", Provider: "openai-api"}, value: "sk-oai"},
		"cred-4": {meta: VaultCredMeta{ID: "cred-4", Provider: "dashscope-api"}, value: "sk-ds"},
		"cred-5": {meta: VaultCredMeta{ID: "cred-5", Provider: "google-api"}, value: "AIza"},
	}})
	if got := rt.agentAuthEnv("claude-code"); got != nil {
		t.Errorf("agentAuthEnv(claude-code) = %v, want nil (#227 file-mount contract — claude-code is absent from agentAuthEnvTable)", got)
	}
}

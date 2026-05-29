// internal/runtime/spawn_auth_env_test.go — pins the post-#227
// invariant: Runtime.agentAuthEnv returns nil for every slug. The
// claude-code credential channel is the per-spawn file mount
// (/run/secrets/claude-credentials) which carries the full refreshable
// OAuth pair; injecting a static access_token via env pins a snapshot
// that can't auto-refresh and 401s on expiry. PR #221 originally
// injected `CLAUDE_CODE_OAUTH_TOKEN=<vault.accessToken>` — operator-
// reported 401s in #227 surfaced the wrong-layer fix and this PR
// reverts it. The function infrastructure stays as a scaffold for
// future flavors (qwen-code, aider, gemini-cli, opencode) whose
// credentials genuinely need env-var delivery.
//
// Refs #208 #218 #221 #227.
package runtime

import (
	"errors"
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

// TestRuntime_AgentAuthEnv_AllSlugsReturnNil — broad guard pinning
// the post-#227 contract across every slug today. Future flavors
// (qwen-code, aider, gemini-cli, opencode) may legitimately need env
// injection, in which case this test ALSO needs updating — explicit
// failure forces the deliberate decision rather than a silent slip-in.
//
// Refs #208 #227.
func TestRuntime_AgentAuthEnv_AllSlugsReturnNil(t *testing.T) {
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
			t.Errorf("agentAuthEnv(%q) = %v, want nil", slug, got)
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
}

// internal/runtime/spawn_auth_env_test.go — pins the #218 spawn-auth-env
// contract: Runtime.agentAuthEnv resolves the latest vault.claude-oauth
// entry into a CLAUDE_CODE_OAUTH_TOKEN env var for claude-code spawns,
// returns nil for unmapped slugs / empty vault / nil vault. The Spawn
// call site appends the result to envWithMCP so spawned workers have
// the OAuth token from process-start without depending on the
// /run/secrets/claude-credentials file-mount race.
//
// Refs #208.
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

// TestRuntime_AgentAuthEnv_ClaudeCodeWithVault — happy path. vault has
// a claude-oauth entry; agentAuthEnv returns CLAUDE_CODE_OAUTH_TOKEN.
//
// Refs #208.
func TestRuntime_AgentAuthEnv_ClaudeCodeWithVault(t *testing.T) {
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
	if len(got) != 1 {
		t.Fatalf("len(env) = %d, want 1\ngot: %v", len(got), got)
	}
	want := "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-FAKE-TOKEN-XYZ"
	if got[0] != want {
		t.Errorf("env[0] = %q, want %q", got[0], want)
	}
}

// TestRuntime_AgentAuthEnv_ClaudeCodeEmptyVault — vault has no
// claude-oauth entries; returns nil. Don't crash; don't inject a bare
// CLAUDE_CODE_OAUTH_TOKEN= line.
//
// Refs #208.
func TestRuntime_AgentAuthEnv_ClaudeCodeEmptyVault(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt.SetVault(&fakeVault{creds: map[string]fakeCred{}})

	if got := rt.agentAuthEnv("claude-code"); len(got) != 0 {
		t.Errorf("empty vault: env = %v, want []", got)
	}
}

// TestRuntime_AgentAuthEnv_UnknownSlug — non-claude-code slug returns
// nil; no env injected. Guards against accidentally leaking
// CLAUDE_CODE_OAUTH_TOKEN into qwen/aider/gemini spawns when those
// flavors get their own mappings later.
//
// Refs #208.
func TestRuntime_AgentAuthEnv_UnknownSlug(t *testing.T) {
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

	for _, slug := range []string{"qwen-code", "aider", "gemini-cli", "opencode", "unknown-slug"} {
		if got := rt.agentAuthEnv(slug); len(got) != 0 {
			t.Errorf("agentAuthEnv(%q) = %v, want []", slug, got)
		}
	}
}

// TestRuntime_AgentAuthEnv_NoVault — vault not configured returns nil;
// spawn proceeds with file-mount fallback path. Pins the v0.5/v0.7
// backward-compat shape.
//
// Refs #208.
func TestRuntime_AgentAuthEnv_NoVault(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Note: do NOT call SetVault — r.vault is nil

	if got := rt.agentAuthEnv("claude-code"); len(got) != 0 {
		t.Errorf("nil vault: env = %v, want []", got)
	}
}

// TestRuntime_AgentAuthEnv_MalformedPayload — vault entry exists but
// the JSON shape doesn't carry claudeAiOauth.accessToken. Don't
// hand-craft a broken env; return nil + let file-mount fallback take
// over. Pins the no-injection-on-decode-error contract.
//
// Refs #208.
func TestRuntime_AgentAuthEnv_MalformedPayload(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cases := []struct {
		name    string
		payload string
	}{
		{"not-json", `this is not json`},
		{"json-without-claudeAiOauth", `{"someOtherKey":"value"}`},
		{"claudeAiOauth-without-accessToken", `{"claudeAiOauth":{"refreshToken":"only-refresh"}}`},
		{"empty-accessToken", `{"claudeAiOauth":{"accessToken":""}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt.SetVault(&fakeVault{creds: map[string]fakeCred{
				"cred-1": {
					meta:  VaultCredMeta{ID: "cred-1", Provider: "claude-oauth"},
					value: tc.payload,
				},
			}})
			if got := rt.agentAuthEnv("claude-code"); len(got) != 0 {
				t.Errorf("payload %q: env = %v, want []", tc.payload, got)
			}
		})
	}
}

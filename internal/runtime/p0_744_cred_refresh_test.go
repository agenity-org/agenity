// internal/runtime/p0_744_cred_refresh_test.go — pins #744: the container
// copy of every claude-flavor agent's credentials.json must carry a BLANKED
// refreshToken (Anthropic rotates refresh tokens on use, so a self-refreshing
// container would invalidate the operator's own host claude-code ~1hr after
// spawn → HTTP 401 → "/login"). The daemon becomes the SOLE refresher:
//   - blankClaudeRefreshToken zeroes refreshToken while preserving every
//     other field (incl. unknown future fields like rateLimitTier).
//   - materializeClaudeSecrets writes the blanked copy to the container file.
//   - startClaudeCredRefresher keeps every running agent's bind-mounted
//     credential fresh (blanked) on the daemon's behalf.
//
// All assertions probe the WRITTEN BYTES, not a log line.
//
// Refs #744 #369 #374 #264.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBlankClaudeRefreshToken_PreservesAllOtherFields pins the byte-for-byte-
// semantic preservation contract: refreshToken→"", everything else (including
// the unknown-to-a-fixed-struct rateLimitTier field) survives unchanged.
func TestBlankClaudeRefreshToken_PreservesAllOtherFields(t *testing.T) {
	t.Parallel()

	t.Run("wrapped-shape-preserves-all-fields-incl-rateLimitTier", func(t *testing.T) {
		in := `{"claudeAiOauth":{"accessToken":"AT-keep","refreshToken":"RT-drop","expiresAt":1780148702168,"scopes":["a","b"],"subscriptionType":"pro","rateLimitTier":"default_claude_ai"}}`
		out := blankClaudeRefreshToken(in)

		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("output not valid JSON: %v\n%s", err, out)
		}
		inner, ok := got["claudeAiOauth"].(map[string]any)
		if !ok {
			t.Fatalf("claudeAiOauth missing/not-a-map in output: %s", out)
		}
		// refreshToken blanked.
		if rt, _ := inner["refreshToken"].(string); rt != "" {
			t.Errorf("refreshToken = %q, want \"\"", rt)
		}
		// Every other field byte-for-byte-semantically preserved.
		if at, _ := inner["accessToken"].(string); at != "AT-keep" {
			t.Errorf("accessToken = %q, want AT-keep", at)
		}
		// expiresAt round-trips as float64 through map[string]any.
		if exp, _ := inner["expiresAt"].(float64); int64(exp) != 1780148702168 {
			t.Errorf("expiresAt = %v, want 1780148702168", inner["expiresAt"])
		}
		if st, _ := inner["subscriptionType"].(string); st != "pro" {
			t.Errorf("subscriptionType = %q, want pro", st)
		}
		// rateLimitTier — the field a FIXED STRUCT would silently drop.
		// This is the load-bearing landmine assertion.
		if rlt, _ := inner["rateLimitTier"].(string); rlt != "default_claude_ai" {
			t.Errorf("rateLimitTier = %q, want default_claude_ai — a fixed struct would have dropped it (#744 landmine)", rlt)
		}
		scopes, _ := inner["scopes"].([]any)
		if len(scopes) != 2 || scopes[0] != "a" || scopes[1] != "b" {
			t.Errorf("scopes = %v, want [a b]", inner["scopes"])
		}
	})

	t.Run("unwrapped-shape-blanks-top-level-refreshToken", func(t *testing.T) {
		in := `{"accessToken":"AT-keep","refreshToken":"RT-drop","expiresAt":1780148702168}`
		out := blankClaudeRefreshToken(in)
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("output not valid JSON: %v\n%s", err, out)
		}
		if rt, _ := got["refreshToken"].(string); rt != "" {
			t.Errorf("top-level refreshToken = %q, want \"\"", rt)
		}
		if at, _ := got["accessToken"].(string); at != "AT-keep" {
			t.Errorf("accessToken = %q, want AT-keep", at)
		}
	})

	t.Run("malformed-input-passthrough", func(t *testing.T) {
		for _, in := range []string{"", "not json", "{", `{"different":"shape"}`} {
			if out := blankClaudeRefreshToken(in); out != in {
				t.Errorf("malformed input %q transformed to %q, want passthrough unchanged", in, out)
			}
		}
	})
}

// TestMaterializeClaudeSecrets_WritesBlankedRefreshToken drives the real
// materialize path and asserts the BYTES WRITTEN to the container file have a
// blanked refreshToken but a present accessToken. The fixture is comfortably
// fresh so no refresh POST fires (we still point the override at a server that
// would fail the test if hit). The vault must KEEP the real refreshToken.
func TestMaterializeClaudeSecrets_WritesBlankedRefreshToken(t *testing.T) {
	// HOME isolation — resolution reads $HOME via hostClaudeCredentialsPath.
	// Process-wide, so no t.Parallel.
	t.Setenv("HOME", t.TempDir())

	// A server that fails the test if any refresh POST happens — the fixture
	// is fresh so the refresh path must NOT be taken.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected refresh POST: fixture was comfortably fresh, no refresh should fire")
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer srv.Close()
	old := claudeOAuthTokenEndpointOverride
	claudeOAuthTokenEndpointOverride = srv.URL
	defer func() { claudeOAuthTokenEndpointOverride = old }()

	freshExp := time.Now().Add(2 * time.Hour).UnixMilli()
	freshPayload := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"AT-FRESH","refreshToken":"RT-REAL","expiresAt":%d,"subscriptionType":"pro","rateLimitTier":"default_claude_ai","scopes":["a"]}}`, freshExp)

	vault := &inMemoryVault{data: map[string]string{"test-token": freshPayload}}
	rt := &Runtime{
		stateDir:         t.TempDir(),
		containerRuntime: &fakeContainerRuntime{},
		vault:            vault,
	}

	spec := SpawnSpec{Name: "agent-744", ClaudeTokenID: "test-token"}
	dir, err := rt.materializeAgentSecrets(spec)
	if err != nil {
		t.Fatalf("materializeAgentSecrets: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "claude-credentials"))
	if err != nil {
		t.Fatalf("read materialised creds: %v", err)
	}

	var doc struct {
		ClaudeAiOauth struct {
			AccessToken   string `json:"accessToken"`
			RefreshToken  string `json:"refreshToken"`
			RateLimitTier string `json:"rateLimitTier"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("container creds not parseable: %v\n%s", err, got)
	}
	// CONTAINER copy: refreshToken blanked, accessToken present.
	if doc.ClaudeAiOauth.RefreshToken != "" {
		t.Errorf("container refreshToken = %q, want \"\" (#744)", doc.ClaudeAiOauth.RefreshToken)
	}
	if doc.ClaudeAiOauth.AccessToken != "AT-FRESH" {
		t.Errorf("container accessToken = %q, want AT-FRESH", doc.ClaudeAiOauth.AccessToken)
	}
	// Unknown-field survival in the written bytes.
	if doc.ClaudeAiOauth.RateLimitTier != "default_claude_ai" {
		t.Errorf("container rateLimitTier = %q, want default_claude_ai — blanking dropped an unknown field (#744)", doc.ClaudeAiOauth.RateLimitTier)
	}

	// VAULT MASTER STORE: must KEEP the real refreshToken (it is the daemon's
	// source for future refreshes). Do NOT regress this.
	vault.mu.Lock()
	stored := vault.data["test-token"]
	vault.mu.Unlock()
	if !containsSubstring(stored, "RT-REAL") {
		t.Errorf("vault lost the real refreshToken — vault is the master store and MUST keep it (#744). vault=%s", stored)
	}
}

// TestClaudeCredRefresher_RefreshesNearExpiryAgent sets up a fake agent home
// with a NEAR-EXPIRY container credential, points the master vault at a
// (still-resolvable) source, runs one refresher tick, and asserts the daemon
// PUSHES a FRESH, BLANKED credential into the agent's container. Drives a
// refresh POST through the httptest override.
//
// The actual container write goes through r.credPusher — in production a
// `podman cp` (the bind-mounted host file is owned by the container's
// remapped UID 100999, so a host os.WriteFile gets EACCES, #744). The test
// injects a fake credPusher that captures (containerName, payload) so it
// asserts on the BYTES the daemon would push, with NO podman dependency.
func TestClaudeCredRefresher_RefreshesNearExpiryAgent(t *testing.T) {
	// HOME isolation so resolveFreshestClaudeCred doesn't pick up a real
	// developer host ~/.claude/.credentials.json.
	t.Setenv("HOME", t.TempDir())

	// Mock Anthropic OAuth endpoint: returns a fresh access+refresh pair.
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"AT-DAEMON-REFRESHED","refresh_token":"RT-NEW-ROTATED","expires_in":3600}`))
	}))
	defer srv.Close()
	old := claudeOAuthTokenEndpointOverride
	claudeOAuthTokenEndpointOverride = srv.URL
	defer func() { claudeOAuthTokenEndpointOverride = old }()

	stateDir := t.TempDir()

	// Master vault credential is near-expiry (well within the refresh
	// safety margin) so refreshClaudeOAuthIfNeeded fires a POST.
	masterExp := time.Now().Add(-30 * time.Second).UnixMilli()
	masterPayload := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"AT-MASTER-STALE","refreshToken":"RT-MASTER","expiresAt":%d,"subscriptionType":"pro","scopes":["a"]}}`, masterExp)
	vault := &inMemoryVault{data: map[string]string{"test-token": masterPayload}}

	// Capture what the daemon would push into the container (the production
	// path runs `podman cp`; the test injects a fake to capture the bytes).
	var (
		pushCalls       int
		pushedContainer string
		pushedAgentName string
		pushedPayload   string
	)
	rt := &Runtime{
		stateDir:             stateDir,
		containerRuntime:     &fakeContainerRuntime{},
		vault:                vault,
		info:                 make(map[string]*SessionInfo),
		credRefreshInterval:  10 * time.Millisecond,
		credRefreshThreshold: 15 * time.Minute,
		credPusher: func(agentName, containerName, payload string) error {
			pushCalls++
			pushedAgentName = agentName
			pushedContainer = containerName
			pushedPayload = payload
			return nil
		},
	}

	// Register a running claude-flavor agent.
	agentName := "agent-refresh"
	rt.info["id-1"] = &SessionInfo{ID: "id-1", Name: agentName, AgentSlug: "claude-code"}

	// Seed the agent's bind-mounted credential file with a NEAR-EXPIRY
	// (within threshold) blanked credential, as it would be post-spawn. The
	// daemon READS this host file (it's readable); the WRITE goes through the
	// container runtime (credPusher), not back to this file.
	credDir := filepath.Join(stateDir, "agents", agentName, "home", ".claude")
	if err := os.MkdirAll(credDir, 0o755); err != nil {
		t.Fatalf("mkdir agent .claude: %v", err)
	}
	credPath := filepath.Join(credDir, ".credentials.json")
	nearExp := time.Now().Add(5 * time.Minute).UnixMilli() // < 15m threshold
	containerSeed := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"AT-OLD","refreshToken":"","expiresAt":%d}}`, nearExp)
	if err := os.WriteFile(credPath, []byte(containerSeed), 0o600); err != nil {
		t.Fatalf("write seed creds: %v", err)
	}

	// Drive one deterministic scan directly (the loop just wraps this on a
	// ticker; the loop's ctx-cancellation behaviour is covered separately
	// below). No wall-clock racing.
	rt.refreshClaudeCredsOnce()

	// (a) credPusher was called exactly once, for THIS agent, targeting the
	// instance-scoped container name (chepherd-agent-<uuid>-<name>; uuid is
	// empty in this bare-struct test → chepherd-agent-<name>).
	if pushCalls != 1 {
		t.Fatalf("expected exactly 1 credPusher call, got %d", pushCalls)
	}
	if pushedAgentName != agentName {
		t.Errorf("credPusher agentName = %q, want %q", pushedAgentName, agentName)
	}
	wantContainer := containerNamePrefix(rt.instanceUUID) + agentName
	if pushedContainer != wantContainer {
		t.Errorf("credPusher containerName = %q, want %q", pushedContainer, wantContainer)
	}

	// (b)+(c) parse the captured payload — the BYTES the daemon pushes.
	var doc struct {
		ClaudeAiOauth struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(pushedPayload), &doc); err != nil {
		t.Fatalf("pushed creds not parseable: %v\n%s", err, pushedPayload)
	}

	if posts != 1 {
		t.Errorf("expected exactly 1 refresh POST, got %d", posts)
	}
	// Fresh accessToken from the daemon's refresh.
	if doc.ClaudeAiOauth.AccessToken != "AT-DAEMON-REFRESHED" {
		t.Errorf("accessToken = %q, want AT-DAEMON-REFRESHED — daemon did not refresh the pushed payload", doc.ClaudeAiOauth.AccessToken)
	}
	// (b) refreshToken MUST be blanked in the pushed (container) payload.
	if doc.ClaudeAiOauth.RefreshToken != "" {
		t.Errorf("pushed refreshToken = %q, want \"\" — container must never hold a usable refreshToken (#744)", doc.ClaudeAiOauth.RefreshToken)
	}
	// (c) Fresh expiresAt (the daemon refresh advanced it ~1h into the future).
	if doc.ClaudeAiOauth.ExpiresAt <= time.Now().UnixMilli() {
		t.Errorf("pushed expiresAt %d is not in the future — refresh did not advance expiry", doc.ClaudeAiOauth.ExpiresAt)
	}
	// The vault master MUST hold the NEW rotated refresh token (the daemon
	// is the sole refresher and persists the rotation for next time).
	vault.mu.Lock()
	stored := vault.data["test-token"]
	vault.mu.Unlock()
	if !containsSubstring(stored, "RT-NEW-ROTATED") {
		t.Errorf("vault did not persist the rotated refreshToken — daemon must write the new pair back (#744). vault=%s", stored)
	}
}

// TestClaudeCredRefresher_LoopStopsOnContextCancel pins the loop lifecycle:
// startClaudeCredRefresher must return promptly when its context is cancelled
// (it is wired to the daemon's process-lifetime context so ctrl-C / SIGTERM
// shuts it down cleanly). Uses a tiny interval so the ticker is live.
func TestClaudeCredRefresher_LoopStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	rt := &Runtime{
		stateDir:             t.TempDir(),
		containerRuntime:     &fakeContainerRuntime{},
		info:                 make(map[string]*SessionInfo),
		credRefreshInterval:  5 * time.Millisecond,
		credRefreshThreshold: 15 * time.Minute,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		rt.startClaudeCredRefresher(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
		// returned promptly — good
	case <-time.After(2 * time.Second):
		t.Fatal("startClaudeCredRefresher did not return within 2s of ctx cancel")
	}
}

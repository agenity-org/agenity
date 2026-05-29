// internal/runtime/refresh_token_race_test.go — pins the #264 fix:
// concurrent materializeAgentSecrets racers must produce EXACTLY ONE
// outbound POST to Anthropic's OAuth /token endpoint. Pre-#264, each
// of N concurrent spawns POST'd independently with the same stale
// refresh_token; Anthropic invalidated the token on first use → calls
// 2…N got 401 → 4/5 agents in operator's scrum team booted with
// stale accessToken and hit the claude-code OAuth-login UI.
//
// The fix is `Runtime.claudeRefreshMu` serialising the refresh step
// in materializeAgentSecrets + a re-read of the vault inside the
// critical section so the second-and-later racers pick up the first
// racer's freshly-written pair.
//
// Refs #264.
package runtime

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// inMemoryVault implements VaultProvider for this test. Stores one
// claude-oauth credential keyed by ID, supports concurrent reads +
// writes via sync.Mutex.
type inMemoryVault struct {
	mu   sync.Mutex
	data map[string]string
}

func (v *inMemoryVault) ListByProvider(provider string) []VaultCredMeta {
	return []VaultCredMeta{{ID: "test-token", Provider: "claude-oauth"}}
}
func (v *inMemoryVault) GetValue(id string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s, ok := v.data[id]
	if !ok {
		return "", fmt.Errorf("not found")
	}
	return s, nil
}
func (v *inMemoryVault) UpdateValue(id, value string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.data[id] = value
	return nil
}

// stalePayload returns a credentials.json shape with expiresAt 30s in
// the past — well inside refreshClaudeOAuthIfNeeded's 60s safety
// margin, so refresh is triggered.
func stalePayload() string {
	exp := time.Now().Add(-30 * time.Second).UnixMilli()
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"AT-stale","refreshToken":"RT-original","expiresAt":%d,"subscriptionType":"pro","scopes":["a","b"]}}`, exp)
}

// TestRefreshTokenSerialisedAcrossConcurrentSpawns pins the post-#264
// invariant: when N spawns race to materialise the same vault entry,
// only ONE outbound POST to Anthropic's OAuth endpoint happens. The
// other N-1 racers observe the post-refresh vault state and skip.
//
// Pre-#264 this test FAILED — every concurrent caller POST'd.
func TestRefreshTokenSerialisedAcrossConcurrentSpawns(t *testing.T) {
	var posts int64
	// Mock Anthropic OAuth endpoint that COUNTS requests + invalidates
	// the refresh-token on first use to mirror real-world behaviour
	// (the bug operator hit was 401 on calls 2…N).
	var firstRefresh sync.Once
	var firstRTSeen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&posts, 1)
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		firstRefresh.Do(func() { firstRTSeen = body.RefreshToken })
		if body.RefreshToken != firstRTSeen {
			// Mirror Anthropic's behaviour: refresh-token-invalidated
			// 401 on subsequent uses of the same RT. (Concurrent
			// racers all see firstRTSeen here, but if the lock fails
			// they'd POST it before firstRefresh.Do finalised — so
			// the assertion that matters is `posts == 1`.)
			http.Error(w, "refresh_token invalid", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		newRT := fmt.Sprintf("RT-refreshed-%d", atomic.LoadInt64(&posts))
		fmt.Fprintf(w, `{"access_token":"AT-refreshed","refresh_token":%q,"expires_in":3600}`, newRT)
	}))
	defer srv.Close()
	prevEndpoint := claudeOAuthTokenEndpointOverride
	claudeOAuthTokenEndpointOverride = srv.URL
	defer func() { claudeOAuthTokenEndpointOverride = prevEndpoint }()

	vault := &inMemoryVault{data: map[string]string{"test-token": stalePayload()}}
	rt := &Runtime{
		stateDir:         t.TempDir(),
		containerRuntime: &fakeContainerRuntime{},
		vault:            vault,
	}

	// Fan out N concurrent materializeAgentSecrets calls — the
	// operator's failing case was 5; we run 8 for headroom.
	const N = 8
	var wg sync.WaitGroup
	errCh := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			spec := SpawnSpec{
				Name:          fmt.Sprintf("agent-%d", i),
				ClaudeTokenID: "test-token",
			}
			if _, err := rt.materializeAgentSecrets(spec); err != nil {
				errCh <- fmt.Errorf("agent-%d: %w", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	gotPosts := atomic.LoadInt64(&posts)
	if gotPosts != 1 {
		t.Fatalf("expected exactly 1 outbound refresh POST (serialised), got %d — refresh-token race not closed", gotPosts)
	}

	// Vault should now hold the refreshed payload (the first racer
	// wrote it back via UpdateValue inside the critical section).
	vault.mu.Lock()
	stored := vault.data["test-token"]
	vault.mu.Unlock()
	if stored == stalePayload() {
		t.Errorf("expected vault to be updated with refreshed credentials after the single POST; still holds stale payload")
	}
	var doc struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(stored), &doc); err != nil {
		t.Fatalf("vault stored unparseable JSON after refresh: %v", err)
	}
	if doc.ClaudeAiOauth.AccessToken != "AT-refreshed" {
		t.Errorf("expected refreshed accessToken AT-refreshed in vault, got %q", doc.ClaudeAiOauth.AccessToken)
	}
}

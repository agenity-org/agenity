// internal/runtime/p0_374_host_pick_not_clobbered_test.go — pins #374 P0:
// when materializeAgentSecrets selects the host's credentials.json
// because it's fresher than the vault snapshot (#369 P0 path), the
// BYTES WRITTEN to the per-spawn secrets file must be the host's
// bytes, not the stale vault bytes.
//
// Pre-#374 bug: the refresh-on-spawn block re-read vault inside
// `claudeRefreshMu` unconditionally, clobbering the fresh-host
// payload the #369 branch had just selected. The decision log
// said "preferring host" but the file written carried the 13H-stale
// vault payload — operator's claude printed "Please run /login ·
// API Error: 401".
//
// Architect quote 2026-05-30: "The DECISION + LOG say 'use host'.
// The WRITE outputs the stale vault payload."
//
// Refs #374 P0 #369 P0 #225.
package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestP0_374_HostPick_NotClobbered_ByVaultReread is the contract lock:
// vault is stale (13h past), host is fresh (1h future). After
// materializeAgentSecrets, the bytes at <dir>/claude-credentials MUST
// be the host's bytes verbatim, with no vault-derived clobber.
func TestP0_374_HostPick_NotClobbered_ByVaultReread(t *testing.T) {
	// HOME isolation — hostClaudeCredentialsPath reads $HOME via
	// os.UserHomeDir(). Set HOME so the test's fake credentials.json
	// is the one the materializer sees. Cannot use t.Parallel here:
	// HOME is process-wide.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Architect's reported delta: vault 13h past, host 1h future.
	staleExp := time.Now().Add(-13 * time.Hour).UnixMilli()
	freshExp := time.Now().Add(1 * time.Hour).UnixMilli()
	stalePayload := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"AT-STALE-VAULT","refreshToken":"RT-stale","expiresAt":%d,"subscriptionType":"pro","scopes":["a","b"]}}`, staleExp)
	freshPayload := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"AT-FRESH-HOST","refreshToken":"RT-fresh","expiresAt":%d,"subscriptionType":"pro","scopes":["a","b"]}}`, freshExp)

	// Seed host file at $HOME/.claude/.credentials.json
	hostCredsDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(hostCredsDir, 0o700); err != nil {
		t.Fatalf("mkdir host .claude: %v", err)
	}
	hostCredsPath := filepath.Join(hostCredsDir, ".credentials.json")
	if err := os.WriteFile(hostCredsPath, []byte(freshPayload), 0o600); err != nil {
		t.Fatalf("write host creds: %v", err)
	}

	// Vault holds stale payload for the spec's ClaudeTokenID.
	vault := &inMemoryVault{data: map[string]string{"test-token": stalePayload}}
	rt := &Runtime{
		stateDir:         t.TempDir(),
		containerRuntime: &fakeContainerRuntime{},
		vault:            vault,
	}

	spec := SpawnSpec{
		Name:          "agent-374",
		ClaudeTokenID: "test-token",
	}
	dir, err := rt.materializeAgentSecrets(spec)
	if err != nil {
		t.Fatalf("materializeAgentSecrets: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "claude-credentials"))
	if err != nil {
		t.Fatalf("read materialised creds: %v", err)
	}
	gotStr := string(got)

	// #744 — the CONTAINER copy now has its refreshToken BLANKED before
	// WriteFile (the container must never self-refresh; the daemon is the
	// sole refresher). So the written bytes are the fresh HOST payload with
	// refreshToken=="", NOT a byte-for-byte copy. We therefore assert the
	// load-bearing facts (fresh host accessToken present, no stale vault
	// marker, refreshToken blanked) by parsing rather than exact-string
	// equality. The #374 host-pin contract is unchanged: the fresh host
	// accessToken must flow through and the stale vault payload must NOT.
	var got374 struct {
		ClaudeAiOauth struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(got, &got374); err != nil {
		t.Fatalf("materialised credentials not parseable JSON: %v\n%s", err, gotStr)
	}
	if got374.ClaudeAiOauth.AccessToken != "AT-FRESH-HOST" {
		t.Fatalf("materialised accessToken = %q, want AT-FRESH-HOST — host bytes did not flow through (#374 P0)\nwrote:\n%s\nvault-stale (forbidden):\n%s",
			got374.ClaudeAiOauth.AccessToken, gotStr, stalePayload)
	}
	// #744 — container copy MUST carry a blanked refreshToken.
	if got374.ClaudeAiOauth.RefreshToken != "" {
		t.Errorf("materialised refreshToken = %q, want \"\" — container copy must blank the refreshToken (#744)", got374.ClaudeAiOauth.RefreshToken)
	}

	// Secondary: must NOT contain the stale vault's accessToken
	// marker. Cheaper failure signal if the parse above races a JSON
	// shape change in the future.
	if containsSubstring(gotStr, "AT-STALE-VAULT") {
		t.Errorf("materialised credentials contains the stale vault accessToken marker — vault clobbered the host pick (#374 P0)")
	}
}

func containsSubstring(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

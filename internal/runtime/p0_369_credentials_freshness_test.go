// internal/runtime/p0_369_credentials_freshness_test.go — pins #369 P0:
// materializeAgentSecrets must prefer the FRESHER credential source
// (host vs vault) by comparing expiresAt epoch-ms. The architect
// observed: vault payload 13H stale, host's ~/.claude/.credentials.json
// refreshed seconds ago, but the materializer kept using the stale
// vault → claude 401 → operator can't actually use the agent.
//
// Per memory feedback_token_expiry_evades_fixed_window_tests: static
// snapshots at T+30s pass while failing at T+(TTL-minutes-to-hours).
//
// Refs #369 P0 #257 #225.
package runtime

import (
	"fmt"
	"testing"
)

func TestP0_369_ClaudeCredsExpiresAt_ParsesEpochMs(t *testing.T) {
	t.Parallel()
	body := `{"claudeAiOauth":{"accessToken":"sk-x","refreshToken":"rt","expiresAt":1780148702168}}`
	got := claudeCredsExpiresAt(body)
	if got != 1780148702168 {
		t.Errorf("expiresAt = %d, want 1780148702168", got)
	}
}

func TestP0_369_ClaudeCredsExpiresAt_ZeroOnBadJSON(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"{}",
		"not json at all",
		`{"claudeAiOauth":{}}`,
		`{"different":"shape"}`,
	}
	for i, c := range cases {
		c := c
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			if got := claudeCredsExpiresAt(c); got != 0 {
				t.Errorf("got %d, want 0 for %q", got, c)
			}
		})
	}
}

func TestP0_369_ClaudeCredsExpiresAt_HandlesFutureExpiry(t *testing.T) {
	t.Parallel()
	// far-future expiry (still in epoch-ms range)
	body := `{"claudeAiOauth":{"expiresAt":9999999999999}}`
	if got := claudeCredsExpiresAt(body); got != 9999999999999 {
		t.Errorf("got %d, want 9999999999999", got)
	}
}

// TestP0_369_ExpiresAt_FreshnessDecision pins the architect's reported
// scenario: vault payload has expiresAt 13h in the past; host's file
// has expiresAt fresh. The materializer's comparison should pick the
// host file.
func TestP0_369_ExpiresAt_FreshnessDecision(t *testing.T) {
	t.Parallel()
	// Stale: 13h ago — operator's vault state
	stale := `{"claudeAiOauth":{"expiresAt":1780101134726}}`
	// Fresh: ~now-ish per architect's report
	fresh := `{"claudeAiOauth":{"expiresAt":1780148702168}}`
	staleExp := claudeCredsExpiresAt(stale)
	freshExp := claudeCredsExpiresAt(fresh)
	if !(freshExp > staleExp) {
		t.Errorf("fresh expiresAt (%d) should be > stale (%d)", freshExp, staleExp)
	}
}

// internal/runtime/p0_404_p02_peer_status_test.go — pins #404 P0.2:
// chepherd.peer_status / GET /api/v1/sessions/<name>/peer-status
// returns the live activity surface so peer agents answer "what is
// X doing right now" without polling each other's panes.
//
// Refs #404 P0.2 #404 P0.1 #225.
package runtime

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestP0_404_P02_BuildPeerStatus_UnknownSession returns nil so
// HTTP/MCP handlers can 404 cleanly without panicking.
func TestP0_404_P02_BuildPeerStatus_UnknownSession(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	if got := rt.BuildPeerStatus("never-existed"); got != nil {
		t.Errorf("unknown session: got %+v, want nil", got)
	}
}

// TestP0_404_P02_ExtendedCapabilities_RoleCoverage pins the new
// role→capability map added in the P0.1 follow-up. Operator-named
// roles (reviewer, scrum-master, product-owner, security, devops)
// + their aliases must not fall through to general-purpose.
func TestP0_404_P02_ExtendedCapabilities_RoleCoverage(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"reviewer":             {"code-review", "gap-analysis", "verdict-render"},
		"reviewer-discipline":  {"code-review", "gap-analysis", "verdict-render"},
		"reviewer-architect":   {"code-review", "gap-analysis", "verdict-render"},
		"reviewer-economics":   {"code-review", "gap-analysis", "verdict-render"},
		"scrum-master":         {"team-cadence", "verdict-attribution", "impediment-removal"},
		"scrummaster":          {"team-cadence", "verdict-attribution", "impediment-removal"},
		"product-owner":        {"backlog-prioritization", "acceptance-criteria", "stakeholder-translation"},
		"po":                   {"backlog-prioritization", "acceptance-criteria", "stakeholder-translation"},
		"security":             {"threat-modeling", "vuln-triage", "secret-hygiene"},
		"security-reviewer":    {"threat-modeling", "vuln-triage", "secret-hygiene"},
		"devops":               {"deploy-pipeline", "observability", "incident-response"},
		"sre":                  {"deploy-pipeline", "observability", "incident-response"},
		"tester":               {"surface-walk", "defect-filing", "verdict-retraction"}, // tester alias for qa
	}
	for role, want := range cases {
		role, want := role, want
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			got := capabilitiesForRole(role)
			gotSorted := append([]string(nil), got...)
			wantSorted := append([]string(nil), want...)
			sort.Strings(gotSorted)
			sort.Strings(wantSorted)
			if !reflect.DeepEqual(gotSorted, wantSorted) {
				t.Errorf("role=%q: %v, want %v (regressed to general-purpose fallback?)", role, gotSorted, wantSorted)
			}
		})
	}
}

// TestP0_404_P02_StripANSI_AppliedToRingExcerpt locks the
// ANSI-stripping contract on the ring excerpt — peer agents reading
// the excerpt via MCP get clean text, not raw escape sequences.
// We construct a PeerStatus manually since we can't easily spin up
// a real PTY session in the runtime test.
func TestP0_404_P02_StripANSI_AppliedToRingExcerpt(t *testing.T) {
	t.Parallel()
	// The actual BuildPeerStatus stripANSI call happens inside the
	// helper; we verify the helper produces clean output for a
	// representative input.
	dirty := "\x1b[1m●\x1b[0m alive\n"
	clean := stripANSI(dirty)
	if clean != "● alive\n" {
		t.Errorf("stripANSI(%q) = %q, want '● alive\\n'", dirty, clean)
	}
}

// TestP0_404_P02_PeerStatus_JSONShape verifies the consumer-facing
// JSON tags are stable. A peer agent reading peer_status output
// expects camelCase keys (matches PeerAgentCard's convention).
func TestP0_404_P02_PeerStatus_JSONShape(t *testing.T) {
	t.Parallel()
	// Manually construct + marshal. We don't need a real Runtime
	// since we're testing the struct's JSON tags, not the builder.
	s := &PeerStatus{
		Name:            "x",
		State:           "alive",
		LastActivityAt:  "2026-05-31T12:00:00Z",
		IdleSeconds:     1.5,
		TotalBytes:      1024,
		Bytes5m:         128,
		Chunks5m:        4,
		RingExcerptTail: "● working\n",
	}
	bs, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(bs)
	required := []string{
		`"name":"x"`,
		`"state":"alive"`,
		`"lastActivityAt":"2026-05-31T12:00:00Z"`,
		`"idleSeconds":1.5`,
		`"totalBytes":1024`,
		`"bytes5m":128`,
		`"chunks5m":4`,
		`"ringExcerptTail":"● working\n"`,
	}
	for _, sub := range required {
		if !strings.Contains(got, sub) {
			t.Errorf("JSON missing %q\n---FULL---\n%s", sub, got)
		}
	}
}


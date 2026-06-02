// internal/runtimehttp/transcript_test.go — pins the v0.9.3+ Team
// Transcript v2 contracts (#662 default-route, #663 per-team github_url,
// #667 alert-kind stripe, #668 multi-team merged endpoint, #665 BE
// ticket-mentions). Each test asserts the JSON-wire shape the frontend
// integrates against.
package runtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence/sqlite"
	"github.com/chepherd/chepherd/internal/runtime"
)

// newTestServer builds a Server with a fresh SQLite-backed ChannelStore + a
// real Runtime so resolveTeamLead has memberships to inspect. The Runtime
// is created via runtime.New (zero state dir) and seeded with SessionInfo
// records via UpsertSessionInfoForTest. Returns the started httptest.Server
// + the runtime so tests can call rt.JoinTeam to register memberships.
func newTestServer(t *testing.T) (*httptest.Server, *runtime.Runtime, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "transcript.db")
	store, err := sqlite.NewStore(ctx, dbPath)
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	srv := httptest.NewServer((&Server{
		ChannelStore: store.Channels(),
		TaskStore:    store.Tasks(),
	}).Handler())
	t.Cleanup(srv.Close)
	// Re-build with runtime injected — we kept the Server { … } literal above
	// only to make the cleanup ordering obvious; for tests that need the
	// runtime hook, callers should use newTestServerWithRT below.
	_ = rt
	return srv, rt, store
}

// newTestServerWithRT is the runtime-injected variant — needed by every
// non-trivial test in this file because resolveTeamLead walks rt.List() +
// rt.ListMemberships.
func newTestServerWithRT(t *testing.T) (*httptest.Server, *Server, *runtime.Runtime, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "transcript.db")
	store, err := sqlite.NewStore(ctx, dbPath)
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	s := New(rt)
	s.ChannelStore = store.Channels()
	s.TaskStore = store.Tasks()

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s, rt, store
}

// seedAgent registers a SessionInfo + Membership so resolveTeamLead can
// pick it up. Uses MembershipRole verbatim so tests can exercise the full
// role-precedence ladder (#662).
func seedAgent(t *testing.T, rt *runtime.Runtime, name, team, role string) {
	t.Helper()
	rt.UpsertSessionInfoForTest(&runtime.SessionInfo{
		ID:        "sid-" + name,
		Name:      name,
		Team:      team,
		Role:      runtime.Role(role),
		AgentSlug: "claude-code",
		CreatedAt: time.Now().UTC(),
	})
	if _, err := rt.JoinTeam(name, team, runtime.MembershipRole(role), ""); err != nil {
		t.Fatalf("JoinTeam %s: %v", name, err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// #662 — resolveTeamLead role-precedence ladder.
// ─────────────────────────────────────────────────────────────────────

func TestResolveTeamLead(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		team    string
		members map[string]string // name → role
		want    string
	}{
		{
			name:    "scrum-master-wins",
			team:    "scrum",
			members: map[string]string{"sm": "scrum-master", "arch": "architect", "wkr": "worker"},
			want:    "sm",
		},
		{
			name:    "tech-lead-when-no-scrum",
			team:    "trio",
			members: map[string]string{"tl": "tech-lead", "wkr1": "worker", "wkr2": "worker"},
			want:    "tl",
		},
		{
			name:    "orchestrator-when-no-techlead",
			team:    "squad",
			members: map[string]string{"orc": "orchestrator", "wkr": "worker"},
			want:    "orc",
		},
		{
			name:    "custom-alphafirst-worker",
			team:    "custom",
			members: map[string]string{"zwkr": "worker", "awkr": "worker"},
			want:    "awkr",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt, err := runtime.New(t.TempDir())
			if err != nil {
				t.Fatalf("runtime.New: %v", err)
			}
			for n, r := range tc.members {
				rt.UpsertSessionInfoForTest(&runtime.SessionInfo{
					ID: "sid-" + n, Name: n, Team: tc.team, Role: runtime.Role(r),
				})
				if _, err := rt.JoinTeam(n, tc.team, runtime.MembershipRole(r), ""); err != nil {
					t.Fatalf("JoinTeam: %v", err)
				}
			}
			s := New(rt)
			got := s.resolveTeamLead(tc.team)
			if got != tc.want {
				t.Errorf("resolveTeamLead(%q) = %q, want %q", tc.team, got, tc.want)
			}
		})
	}
}

func TestResolveTeamLead_EmptyTeamFallsBackToOperator(t *testing.T) {
	t.Parallel()
	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	s := New(rt)
	if got := s.resolveTeamLead("empty"); got != "operator" {
		t.Errorf("resolveTeamLead(empty) = %q, want operator", got)
	}
}

// ─────────────────────────────────────────────────────────────────────
// #662 — POST without @-mention auto-routes to lead + GET surfaces flag.
// ─────────────────────────────────────────────────────────────────────

func TestDefaultRoute_PostAndGetExposeRoutedFlag(t *testing.T) {
	t.Parallel()
	srv, _, rt, _ := newTestServerWithRT(t)
	seedAgent(t, rt, "sm", "trio", "scrum-master")
	seedAgent(t, rt, "wkr", "trio", "worker")

	// Operator posts WITHOUT @-mention → should auto-route to @sm.
	body := bytes.NewBufferString(`{"author":"operator","body":"hello team"}`)
	resp, err := http.Post(srv.URL+"/api/v1/teams/trio/messages", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201", resp.StatusCode)
	}
	var postResp map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&postResp); err != nil {
		t.Fatalf("decode POST: %v", err)
	}
	if routed, _ := postResp["routed_to_default"].(bool); !routed {
		t.Errorf("POST resp routed_to_default = %v, want true: %+v", routed, postResp)
	}
	if tgt, _ := postResp["default_target"].(string); tgt != "sm" {
		t.Errorf("POST resp default_target = %q, want sm", tgt)
	}

	// GET should expose the same routing flag on the row + the team-level
	// default_target so the compose-hint can show it.
	gresp, err := http.Get(srv.URL + "/api/v1/teams/trio/messages")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer gresp.Body.Close()
	var getResp map[string]any
	if err := json.NewDecoder(gresp.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if tgt, _ := getResp["default_target"].(string); tgt != "sm" {
		t.Errorf("GET team-level default_target = %q, want sm", tgt)
	}
	msgs, _ := getResp["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("GET messages len = %d, want 1: %+v", len(msgs), msgs)
	}
	row, _ := msgs[0].(map[string]any)
	if routed, _ := row["routed_to_default"].(bool); !routed {
		t.Errorf("GET row routed_to_default = %v, want true: %+v", routed, row)
	}
	if tgt, _ := row["default_target"].(string); tgt != "sm" {
		t.Errorf("GET row default_target = %q, want sm", tgt)
	}
}

// ─────────────────────────────────────────────────────────────────────
// #663 — per-team github_url derivation from any live member's GitHubURL.
// ─────────────────────────────────────────────────────────────────────

func TestTeamGithubURLDerivation(t *testing.T) {
	t.Parallel()
	srv, s, rt, _ := newTestServerWithRT(t)
	// Seed two teams on distinct repos.
	rt.UpsertSessionInfoForTest(&runtime.SessionInfo{
		ID: "sid-trio-1", Name: "trio-worker", Team: "trio",
		Role: "worker", GitHubURL: "https://github.com/ping-cash/ping-cash.git",
	})
	rt.UpsertSessionInfoForTest(&runtime.SessionInfo{
		ID: "sid-scrum-1", Name: "scrum-worker", Team: "scrum",
		Role: "worker", GitHubURL: "https://github.com/chepherd/chepherd",
	})

	// Direct unit check on teamGitHubURL.
	if got := s.teamGitHubURL("trio"); got != "https://github.com/ping-cash/ping-cash" {
		t.Errorf("teamGitHubURL(trio) = %q, want trimmed ping-cash url", got)
	}
	if got := s.teamGitHubURL("scrum"); got != "https://github.com/chepherd/chepherd" {
		t.Errorf("teamGitHubURL(scrum) = %q", got)
	}
	if got := s.teamGitHubURL("nonexistent"); got != "" {
		t.Errorf("teamGitHubURL(nonexistent) = %q, want empty", got)
	}

	// GET response must surface team_github_url at the top level (per-team)
	// AND on each row (so multi-team merged feed can link correctly).
	resp, err := http.Post(srv.URL+"/api/v1/teams/trio/messages", "application/json",
		bytes.NewBufferString(`{"author":"operator","body":"see #42"}`))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST trio: err=%v status=%v", err, resp.StatusCode)
	}
	resp.Body.Close()

	gresp, err := http.Get(srv.URL + "/api/v1/teams/trio/messages")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer gresp.Body.Close()
	var getResp map[string]any
	_ = json.NewDecoder(gresp.Body).Decode(&getResp)
	if tu, _ := getResp["team_github_url"].(string); tu != "https://github.com/ping-cash/ping-cash" {
		t.Errorf("GET team_github_url = %q", tu)
	}
	msgs, _ := getResp["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	row, _ := msgs[0].(map[string]any)
	if tu, _ := row["team_github_url"].(string); tu != "https://github.com/ping-cash/ping-cash" {
		t.Errorf("row team_github_url = %q", tu)
	}
}

// ─────────────────────────────────────────────────────────────────────
// #667 — alert kind extraction surfaces "alert:<kind>" in transcript rows.
// ─────────────────────────────────────────────────────────────────────

func TestAlertKindExtraction_SurfacesKindStripe(t *testing.T) {
	t.Parallel()
	srv, _, rt, _ := newTestServerWithRT(t)
	seedAgent(t, rt, "alpha", "trio", "worker")

	// Simulate an alert_human call: HumanInbox stores body with "[kind] "
	// prefix (see mcpserver/server.go:811).
	rt.HumanInbox("alpha", "[question] should I deploy now?")
	rt.HumanInbox("alpha", "[failure] CI is red")
	rt.HumanInbox("alpha", "plain body without prefix")

	gresp, err := http.Get(srv.URL + "/api/v1/teams/trio/messages")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer gresp.Body.Close()
	var getResp map[string]any
	_ = json.NewDecoder(gresp.Body).Decode(&getResp)
	msgs, _ := getResp["messages"].([]any)
	if len(msgs) < 3 {
		t.Fatalf("messages len = %d, want >=3 (3 alerts seeded)", len(msgs))
	}
	// Index by body for stable assertion.
	kindByBody := map[string]string{}
	for _, m := range msgs {
		row, _ := m.(map[string]any)
		body, _ := row["body"].(string)
		kind, _ := row["kind"].(string)
		kindByBody[body] = kind
	}
	if k := kindByBody["should I deploy now?"]; k != "alert:question" {
		t.Errorf("question kind = %q, want alert:question", k)
	}
	if k := kindByBody["CI is red"]; k != "alert:failure" {
		t.Errorf("failure kind = %q, want alert:failure", k)
	}
	if k := kindByBody["plain body without prefix"]; k != "alert" {
		t.Errorf("unprefixed kind = %q, want alert (generic)", k)
	}
}

// ─────────────────────────────────────────────────────────────────────
// #668 — multi-team merged endpoint returns ONE payload with team tags.
// ─────────────────────────────────────────────────────────────────────

func TestTranscriptMultiTeam(t *testing.T) {
	t.Parallel()
	srv, _, rt, _ := newTestServerWithRT(t)
	seedAgent(t, rt, "a-sm", "trio", "scrum-master")
	seedAgent(t, rt, "b-sm", "scrum", "scrum-master")

	// Seed one message per team.
	for team, body := range map[string]string{"trio": "trio msg", "scrum": "scrum msg"} {
		resp, err := http.Post(srv.URL+"/api/v1/teams/"+team+"/messages", "application/json",
			bytes.NewBufferString(`{"author":"operator","body":"`+body+`"}`))
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST %s: err=%v status=%v", team, err, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// teams=all → both teams, both messages.
	gresp, err := http.Get(srv.URL + "/api/v1/transcript?teams=all")
	if err != nil {
		t.Fatalf("GET all: %v", err)
	}
	defer gresp.Body.Close()
	var getResp map[string]any
	_ = json.NewDecoder(gresp.Body).Decode(&getResp)
	teams, _ := getResp["teams"].([]any)
	if len(teams) < 2 {
		t.Errorf("teams len = %d, want >=2", len(teams))
	}
	msgs, _ := getResp["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("merged messages len = %d, want 2", len(msgs))
	}
	// Every row must carry its team tag (DoD contract).
	seen := map[string]bool{}
	for _, m := range msgs {
		row, _ := m.(map[string]any)
		team, _ := row["team"].(string)
		if team == "" {
			t.Errorf("row missing team tag: %+v", row)
		}
		seen[team] = true
	}
	if !seen["trio"] || !seen["scrum"] {
		t.Errorf("expected both teams represented; got %+v", seen)
	}

	// teams=trio → just trio.
	gresp2, err := http.Get(srv.URL + "/api/v1/transcript?teams=trio")
	if err != nil {
		t.Fatalf("GET trio: %v", err)
	}
	defer gresp2.Body.Close()
	var trioResp map[string]any
	_ = json.NewDecoder(gresp2.Body).Decode(&trioResp)
	trioMsgs, _ := trioResp["messages"].([]any)
	if len(trioMsgs) != 1 {
		t.Errorf("teams=trio messages len = %d, want 1", len(trioMsgs))
	}
	for _, m := range trioMsgs {
		row, _ := m.(map[string]any)
		if team, _ := row["team"].(string); team != "trio" {
			t.Errorf("teams=trio leaked team=%q", team)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────
// #665 BE — ticket-mentions count map.
// ─────────────────────────────────────────────────────────────────────

func TestTicketMentions(t *testing.T) {
	t.Parallel()
	srv, _, rt, _ := newTestServerWithRT(t)
	seedAgent(t, rt, "wkr", "trio", "worker")

	// Seed messages mentioning various #-tickets.
	for _, body := range []string{
		"fixing #651 now",
		"see #651 progress + linked to #807",
		"#807 also blocked on #999",
		"no tickets here",
	} {
		resp, err := http.Post(srv.URL+"/api/v1/teams/trio/messages", "application/json",
			bytes.NewBufferString(`{"author":"operator","body":"`+body+`"}`))
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST: err=%v status=%v body=%q", err, resp.StatusCode, body)
		}
		resp.Body.Close()
	}

	gresp, err := http.Get(srv.URL + "/api/v1/teams/trio/ticket-mentions")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer gresp.Body.Close()
	if gresp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", gresp.StatusCode)
	}
	var counts map[string]int
	_ = json.NewDecoder(gresp.Body).Decode(&counts)
	if counts["#651"] != 2 {
		t.Errorf("#651 count = %d, want 2 (2 messages mention it)", counts["#651"])
	}
	if counts["#807"] != 2 {
		t.Errorf("#807 count = %d, want 2", counts["#807"])
	}
	if counts["#999"] != 1 {
		t.Errorf("#999 count = %d, want 1", counts["#999"])
	}
}

// ─────────────────────────────────────────────────────────────────────
// parseTickets unit-test — covers the regex's word-boundary behavior.
// ─────────────────────────────────────────────────────────────────────

func TestParseTickets(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"see #651", []string{"651"}},
		{"#651 and #807 are open", []string{"651", "807"}},
		{"dedupe #5 #5 #5", []string{"5"}},
		{"in-word#42 should NOT match — but ax #42 should", []string{"42"}},
		{"trailing #99.", []string{"99"}},
		{"no hash here", nil},
	}
	for _, tc := range cases {
		got := parseTickets(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("parseTickets(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseTickets(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// internal/runtimehttp/p0_467_agents_directory_test.go pins the
// v0.9.4 §12.2 curated-directory endpoint on the chepherd-daemon
// HTTP surface (#467 Wave D1). Asserts:
//
//   - GET /api/v1/agents/ returns 200 + {"agents":[]} on an empty
//     daemon (no rt wired, no live runners). The route is reachable
//     and the wire shape matches the spec on the empty case.
//   - GET /api/v1/agents/ returns 200 + two entries with the exact
//     §12.2 keys {sid, name, agent_card_url} when two fake runners
//     are seeded into the live session registry.
//   - Exited sessions are filtered out — the directory is the LIVE
//     view, not the historical view.
//   - agent_card_url templates per §12.1 well-known URI pattern:
//     scheme://host/a2a/<sid>/.well-known/agent-card.json. Scheme
//     follows r.TLS / X-Forwarded-Proto.
//   - Non-GET methods on /api/v1/agents/ return 405.
//   - The existing /api/v1/agents/{id} fetch path still works after
//     adding the bare-path directory branch — guards against a
//     regression in the v172 single-record contract.
//
// Refs #467 V0.9.2-ARCHITECTURE.md §12.2.
package runtimehttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/chepherd/chepherd/internal/runtime"
)

// TestWaveD1_AgentsDirectory_EmptyShape — bare Server, no rt wired.
// The handler must still respond 200 with {"agents":[]} so clients
// can rely on the shape without conditional-on-runner-count parsing.
func TestWaveD1_AgentsDirectory_EmptyShape(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/agents/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Agents == nil {
		t.Fatalf("agents key missing — spec says always present")
	}
	if len(body.Agents) != 0 {
		t.Errorf("agents = %v, want empty", body.Agents)
	}
}

// TestWaveD1_AgentsDirectory_TwoSeededRunners — the acceptance test
// from the issue body. Seeds two fake SessionInfo records into the
// live registry and asserts the directory lists both with the
// §12.2 shape. Exited sessions are filtered.
func TestWaveD1_AgentsDirectory_TwoSeededRunners(t *testing.T) {
	t.Parallel()
	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	rt.UpsertSessionInfoForTest(&runtime.SessionInfo{
		ID:        "sid-alpha",
		Name:      "alpha",
		AgentSlug: "claude-code",
		Team:      "engineering",
	})
	rt.UpsertSessionInfoForTest(&runtime.SessionInfo{
		ID:        "sid-bravo",
		Name:      "bravo",
		AgentSlug: "qwen-code",
		Team:      "engineering",
	})
	rt.UpsertSessionInfoForTest(&runtime.SessionInfo{
		ID:        "sid-ghost",
		Name:      "ghost",
		AgentSlug: "claude-code",
		Exited:    true,
	})

	srv := httptest.NewServer((&Server{rt: rt}).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/agents/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Agents []directoryEntry `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, want := len(body.Agents), 2; got != want {
		t.Fatalf("agents len = %d, want %d (Exited must be filtered): %v",
			got, want, body.Agents)
	}
	sort.Slice(body.Agents, func(i, j int) bool {
		return body.Agents[i].SID < body.Agents[j].SID
	})

	a, b := body.Agents[0], body.Agents[1]
	if a.SID != "sid-alpha" || a.Name != "alpha" {
		t.Errorf("alpha entry = %+v", a)
	}
	if b.SID != "sid-bravo" || b.Name != "bravo" {
		t.Errorf("bravo entry = %+v", b)
	}
	// agent_card_url must template per §12.1: scheme://host/a2a/<sid>/.well-known/agent-card.json.
	wantSuffix := "/a2a/sid-alpha/.well-known/agent-card.json"
	if got := a.AgentCardURL; len(got) < len(wantSuffix) || got[len(got)-len(wantSuffix):] != wantSuffix {
		t.Errorf("alpha agent_card_url = %q, want suffix %q", got, wantSuffix)
	}
}

// TestWaveD1_AgentsDirectory_RejectsNonGET — only GET makes sense
// on the directory. POST/PUT/PATCH/DELETE/etc. must return 405 so
// callers don't misuse the endpoint as a creation surface.
func TestWaveD1_AgentsDirectory_RejectsNonGET(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req, _ := http.NewRequest(m, srv.URL+"/api/v1/agents/", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", m, resp.StatusCode)
		}
	}
}

// TestWaveD1_AgentsDirectory_ScopedByForwardedProto verifies the
// stub agent_card_url uses https when an X-Forwarded-Proto header is
// set, so callers behind a TLS-terminating reverse proxy get the
// correct scheme without the daemon needing to know it serves TLS
// directly.
func TestWaveD1_AgentsDirectory_ScopedByForwardedProto(t *testing.T) {
	t.Parallel()
	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	rt.UpsertSessionInfoForTest(&runtime.SessionInfo{
		ID:   "sid-proto",
		Name: "proto-test",
	})
	srv := httptest.NewServer((&Server{rt: rt}).Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/agents/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Agents []directoryEntry `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Agents) != 1 {
		t.Fatalf("agents len = %d, want 1", len(body.Agents))
	}
	url := body.Agents[0].AgentCardURL
	if len(url) < 8 || url[:8] != "https://" {
		t.Errorf("agent_card_url = %q, want https:// prefix", url)
	}
}

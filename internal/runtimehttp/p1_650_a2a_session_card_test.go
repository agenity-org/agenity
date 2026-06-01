// internal/runtimehttp/p1_650_a2a_session_card_test.go — regression
// pins for #650: GET /a2a/<sid>/.well-known/agent-card.json must
// return JSON, not the SPA HTML fallback.
//
// Before the fix, any /a2a/ path fell through to the SPA catchall
// ("/") which served index.html with HTTP 200 + text/html — breaking
// A2A federation discovery.
package runtimehttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/runtime"
)

func TestP1_650_A2ASessionCard_UnknownSIDReturns404JSON(t *testing.T) {
	t.Parallel()
	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	srv := httptest.NewServer((&Server{rt: rt}).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/a2a/no-such-sid" + a2a.AgentCardPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (not SPA HTML)", ct)
	}
}

func TestP1_650_A2ASessionCard_RejectsNonGET(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/a2a/some-sid"+a2a.AgentCardPath, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestP1_650_A2ASessionCard_LiveSessionReturnsCard(t *testing.T) {
	t.Parallel()
	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	// Inject a stub SessionInfo directly into the runtime's info index
	// so GetByContextID can resolve it without spinning up a container.
	testSID := "test-agent-99999"
	rt.UpsertSessionInfoForTest(&runtime.SessionInfo{
		ID:        testSID,
		Name:      "test-agent",
		Role:      "worker",
		AgentSlug: "claude-code",
	})

	srv := httptest.NewServer((&Server{rt: rt}).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/a2a/" + testSID + a2a.AgentCardPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var card map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if card["name"] != "test-agent" {
		t.Errorf("card[name] = %v, want test-agent", card["name"])
	}
}

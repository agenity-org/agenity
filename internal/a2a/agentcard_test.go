package a2a

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAgentCardPath_Hyphenated pins the spec-mandated hyphenated path
// (NOT underscore-or-camelCase) so any future refactor that drifts
// from spec gets caught here.
//
// Refs #208.
func TestAgentCardPath_Hyphenated(t *testing.T) {
	t.Parallel()
	if AgentCardPath != "/.well-known/agent-card.json" {
		t.Fatalf("AgentCardPath = %q, want %q (A2A spec)",
			AgentCardPath, "/.well-known/agent-card.json")
	}
}

func TestServeAgentCard_RoundTrip(t *testing.T) {
	t.Parallel()
	card := &AgentCard{
		ProtocolVersion: "1.0",
		Name:            "chepherd-runner-test",
		URL:             "https://chepherd.test/runner/abc",
		Version:         "0.9.2",
		Capabilities: AgentCapabilities{
			Streaming: true, PushNotifications: true, ExtendedCard: true,
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills:             []AgentSkill{{ID: "s1", Name: "echo"}},
		XChepherdP2P:       DefaultExtension(),
	}
	srv := httptest.NewServer(ServeAgentCard(card))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != card.Name || got.Version != card.Version {
		t.Errorf("round-trip lost data: %+v", got)
	}
	if got.XChepherdP2P == nil || got.XChepherdP2P.Version != "0.9.4" {
		t.Errorf("x-chepherd-p2p extension lost: %+v", got.XChepherdP2P)
	}
}

func TestServeAgentCard_RejectsNonGET(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(ServeAgentCard(&AgentCard{Name: "x"}))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

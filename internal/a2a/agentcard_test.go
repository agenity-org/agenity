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

// TestAgentCard_Spec4OptionalFields_RoundTrip — #577 pins the §4.4
// optional fields (provider, documentationUrl, supportedInterfaces,
// signatures, iconUrl) round-trip through JSON unchanged. Pre-#577
// the AgentCard struct omitted these entirely, so a spec-conformant
// SDK decoding the card would never see them and would mark the
// agent as incomplete.
func TestAgentCard_Spec4OptionalFields_RoundTrip(t *testing.T) {
	t.Parallel()
	card := &AgentCard{
		ProtocolVersion: "1.0",
		Name:            "chepherd-test",
		URL:             "https://chepherd.test/jsonrpc",
		Version:         "0.9.4",
		Capabilities:    AgentCapabilities{Streaming: true},
		Provider: &AgentProvider{
			URL:          "https://chepherd.org",
			Organization: "chepherd",
		},
		DocumentationURL: "https://chepherd.org/docs",
		SupportedInterfaces: []AgentInterface{
			{
				URL:             "https://chepherd.test/jsonrpc",
				ProtocolBinding: "JSONRPC",
				ProtocolVersion: "1.0",
			},
		},
		IconURL: "https://chepherd.org/icon.png",
	}
	body, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Decode into a plain map to assert exact wire keys (camelCase).
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantKeys := []string{"provider", "documentationUrl", "supportedInterfaces", "iconUrl"}
	for _, k := range wantKeys {
		if _, ok := raw[k]; !ok {
			t.Errorf("wire missing %q field — A2A v1.0 §4.4 optional Agent Card field: %s", k, body)
		}
	}
	// Round-trip back to AgentCard + assert fields preserved.
	var got AgentCard
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if got.Provider == nil || got.Provider.Organization != "chepherd" {
		t.Errorf("provider lost on round-trip: %+v", got.Provider)
	}
	if got.DocumentationURL != "https://chepherd.org/docs" {
		t.Errorf("documentationUrl lost: %q", got.DocumentationURL)
	}
	if len(got.SupportedInterfaces) != 1 || got.SupportedInterfaces[0].ProtocolBinding != "JSONRPC" {
		t.Errorf("supportedInterfaces lost: %+v", got.SupportedInterfaces)
	}
	if got.IconURL != "https://chepherd.org/icon.png" {
		t.Errorf("iconUrl lost: %q", got.IconURL)
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

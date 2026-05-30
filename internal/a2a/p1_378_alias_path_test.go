// internal/a2a/p1_378_alias_path_test.go — pins #378 P1: the
// /.well-known/agent-card alias (suffix-less, the architect's
// repro path) must serve the AgentCard JSON, not fall through to
// the SPA wildcard's landing-page HTML.
//
// Pre-fix: only the canonical /.well-known/agent-card.json was
// registered. A peer A2A client that tries the shorter form first
// landed on chepherd's static-file 404-fallback to index.html.
//
// Architect's 2026-05-30 repro:
//
//	curl -s http://localhost:8083/.well-known/agent-card | head -2
//	→ <!DOCTYPE html><html lang="en"> <head>...
//
// Refs #378 P1 #208 #225.
package a2a

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestP1_378_AliasPath_ServesAgentCardJSON locks the alias-path
// behavior: both /.well-known/agent-card and /.well-known/agent-card.json
// MUST return identical JSON bodies. Without the alias, the SPA's
// catch-all swallowed the request and returned landing-page HTML —
// opaque to A2A peer discovery.
func TestP1_378_AliasPath_ServesAgentCardJSON(t *testing.T) {
	t.Parallel()
	card := &AgentCard{
		ProtocolVersion: "1.0",
		Name:            "chepherd-378-test",
		URL:             "https://chepherd.test/378",
		Version:         "0.9.3",
		Capabilities: AgentCapabilities{
			Streaming: true, PushNotifications: true, ExtendedCard: true,
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills:             []AgentSkill{{ID: "s1", Name: "echo"}},
	}
	mux := http.NewServeMux()
	router := NewRouter()
	RegisterRoutes(mux, card, router, nil, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	paths := []string{
		"/.well-known/agent-card",      // alias (#378 surface)
		"/.well-known/agent-card.json", // canonical
	}
	for _, p := range paths {
		p := p
		t.Run(p, func(t *testing.T) {
			resp, err := http.Get(srv.URL + p)
			if err != nil {
				t.Fatalf("GET %s: %v", p, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200", p, resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("GET %s Content-Type = %q, want application/json", p, ct)
			}
			var got AgentCard
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatalf("GET %s decode: %v", p, err)
			}
			if got.Name != "chepherd-378-test" {
				t.Errorf("GET %s decoded card Name = %q, want chepherd-378-test", p, got.Name)
			}
			if got.URL != "https://chepherd.test/378" {
				t.Errorf("GET %s decoded card URL = %q, want propagated", p, got.URL)
			}
		})
	}
}

// TestP1_378_AliasPath_RejectsNonGET locks consistent method enforcement
// for the alias: only GET works, matching the canonical handler.
func TestP1_378_AliasPath_RejectsNonGET(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	router := NewRouter()
	RegisterRoutes(mux, &AgentCard{Name: "x", ProtocolVersion: "1.0"}, router, nil, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+AgentCardAliasPath, strings.NewReader(""))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST alias: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST alias status = %d, want 405", resp.StatusCode)
	}
}

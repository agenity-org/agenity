// internal/mcpserver/list_peers.go — V0.9.2-ARCHITECTURE §10 Pattern
// 1 step 1: chepherd.list_peers MCP tool — Agent Card directory in
// the caller's team scope.
//
// Wire shape matches worker2's Wave D1 (#467) /api/v1/agents/
// directory endpoint verbatim: {sid, name, agent_card_url} per
// entry. Data source is rt.List() filtered by team — same path
// agentsDirectory uses in internal/runtimehttp/agents_v172.go. The
// k3 MCP tool is the agent-facing wrapper; D1 is the operator/
// dashboard-facing HTTP surface. ONE shape, two callers.
//
// agent_card_url construction:
//   - If Server.a2aBaseURL is set (via SetA2ABaseURL, wired from
//     cmd/run.go), uses scheme://host portion to template the §12.1
//     well-known URI: <a2aBaseURL>/a2a/<sid>/.well-known/agent-card.json
//   - Otherwise emits the RELATIVE path /a2a/<sid>/.well-known/
//     agent-card.json. Callers (agents inside containers) resolve
//     relative to the daemon they're already talking to.
//
// Wave R3 (per-runner Agent Card hosting) replaces this with each
// runner's own A2ABaseURL from the registration record. Shape stays.
//
// Refs #474 #467 #455 V0.9.2-ARCHITECTURE §10 Pattern 1 §12.2.
package mcpserver

import (
	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/runtime"
)

// listPeerEntry is the per-peer wire shape. JSON tags MUST match
// internal/runtimehttp/agents_v172.go directoryEntry — the spec is
// frozen at the D1 contract.
type listPeerEntry struct {
	SID          string `json:"sid"`
	Name         string `json:"name"`
	AgentCardURL string `json:"agent_card_url"`
}

// buildListPeersEntries filters the runtime's session registry to
// the given team, excludes the caller's own session, and projects
// each remaining info into the {sid, name, agent_card_url} shape.
//
// Empty teamFilter returns an empty slice (callers in no team have
// no team peers; chepherd.list is the right tool for global view).
func buildListPeersEntries(rt *runtime.Runtime, caller, teamFilter, baseURL string) []listPeerEntry {
	entries := []listPeerEntry{}
	if rt == nil {
		return entries
	}
	for _, info := range rt.List() {
		if info == nil || info.Exited {
			continue
		}
		if info.Name == caller {
			continue
		}
		if teamFilter == "" || info.Team != teamFilter {
			continue
		}
		entries = append(entries, listPeerEntry{
			SID:          info.ID,
			Name:         info.Name,
			AgentCardURL: agentCardURL(baseURL, info.ID),
		})
	}
	return entries
}

// agentCardURL templates the §12.1 well-known URI. If baseURL is
// empty the result is a relative path the caller resolves against
// its daemon's known origin.
func agentCardURL(baseURL, sid string) string {
	if baseURL == "" {
		return "/a2a/" + sid + a2a.AgentCardPath
	}
	// baseURL is "scheme://host[:port]"; trim trailing slash so we
	// don't emit a double-slash.
	for len(baseURL) > 0 && baseURL[len(baseURL)-1] == '/' {
		baseURL = baseURL[:len(baseURL)-1]
	}
	return baseURL + "/a2a/" + sid + a2a.AgentCardPath
}

// SetA2ABaseURL configures the daemon's externally-reachable base
// URL (scheme://host:port) so chepherd.list_peers emits absolute
// agent_card_urls. Empty leaves URLs relative — agents resolve
// against the daemon they're already connected to. Wired by
// cmd/run.go after the runtime HTTP listen address is known.
//
// Refs #474.
func (s *Server) SetA2ABaseURL(url string) {
	s.a2aBaseURL = url
}

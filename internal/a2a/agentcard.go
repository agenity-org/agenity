// Package a2a implements the chepherd v0.9.2 Agent-to-Agent (A2A)
// protocol scaffold per docs/V0.9.2-ARCHITECTURE.md §S3 and the A2A
// specification: https://google-a2a.github.io/A2A/
//
// This sub-branch lands the SCAFFOLD only: Agent Card serving at
// /.well-known/agent-card.json + all 11 JSON-RPC method routes
// registered with stub bodies. Method semantics + delivery logic
// land in subsequent sub-branches (S5-S7).
//
// Refs #208.
package a2a

import (
	"encoding/json"
	"net/http"
)

// AgentCard is the canonical A2A Agent Card document served at
// /.well-known/agent-card.json. Fields mirror the A2A spec
// (hyphenated path; required protocolVersion + name + url +
// capabilities + defaultInputModes + defaultOutputModes + skills).
//
// JSON tags use the A2A-spec PascalCase-to-camelCase convention.
type AgentCard struct {
	ProtocolVersion    string                    `json:"protocolVersion"`
	Name               string                    `json:"name"`
	Description        string                    `json:"description,omitempty"`
	URL                string                    `json:"url"`
	Version            string                    `json:"version"`
	Capabilities       AgentCapabilities         `json:"capabilities"`
	DefaultInputModes  []string                  `json:"defaultInputModes"`
	DefaultOutputModes []string                  `json:"defaultOutputModes"`
	Skills             []AgentSkill              `json:"skills"`
	Security           []map[string][]string     `json:"security,omitempty"`
	SecuritySchemes    map[string]SecurityScheme `json:"securitySchemes,omitempty"`

	// XChepherdP2P is the open-source chepherd extension advertising
	// P2P plumbing (signaling endpoint, ICE servers, etc.). Vanilla
	// A2A clients ignore unknown extensions; chepherd-aware clients
	// negotiate WebRTC P2P via this block. Plumbing arrives in S5.
	XChepherdP2P *ChepherdP2PExtension `json:"x-chepherd-p2p,omitempty"`
}

// AgentCapabilities advertises which A2A features this agent supports.
// v0.9.2 ships all three set to true; bodies arrive in later sub-branches.
type AgentCapabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
	ExtendedCard      bool `json:"extendedCard"`
}

// AgentSkill describes one capability the agent exposes.
type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty"`
}

// SecurityScheme matches A2A's adoption of the OpenAPI 3.x security
// scheme object. v0.9.2 advertises all 5 schemes (mTLS,
// HTTPAuthSecurityScheme, APIKeySecurityScheme, OAuth2SecurityScheme,
// OpenIdConnectSecurityScheme) per §6 of the architecture doc.
type SecurityScheme struct {
	Type             string            `json:"type"`
	Scheme           string            `json:"scheme,omitempty"`
	BearerFormat     string            `json:"bearerFormat,omitempty"`
	In               string            `json:"in,omitempty"`
	Name             string            `json:"name,omitempty"`
	Flows            map[string]any    `json:"flows,omitempty"`
	OpenIDConnectURL string            `json:"openIdConnectUrl,omitempty"`
	Description      string            `json:"description,omitempty"`
	Extensions       map[string]string `json:"-"`
}

// ServeAgentCard renders the AgentCard JSON at /.well-known/agent-card.json.
// Hyphenated path per the A2A spec; spec-violation-protector test in
// agentcard_test.go pins the URL exactly.
func ServeAgentCard(card *AgentCard) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(card)
	})
}

// AgentCardPath is the well-known path the A2A spec mandates.
const AgentCardPath = "/.well-known/agent-card.json"

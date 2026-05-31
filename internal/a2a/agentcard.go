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

	// #577 — A2A v1.0 §4.4.1 optional Agent Card fields. Pre-#577
	// chepherd omitted these even when the spec lists them; missing
	// fields make a spec-conformance audit (or a strict canonical-SDK
	// decoder) flag the card as incomplete. Refs #561 #577.

	// Provider identifies the organization serving this agent. §4.4.2
	// AgentProvider proto schema (url + organization).
	Provider *AgentProvider `json:"provider,omitempty"`

	// DocumentationURL points operators + tooling to human-readable
	// docs for the agent. §4.4.1 documentation_url.
	DocumentationURL string `json:"documentationUrl,omitempty"`

	// SupportedInterfaces enumerates each protocol binding this agent
	// serves at this URL. Per §4.4.1 line 370 this is REQUIRED, but
	// the chepherd's top-level URL field (a chepherd-historical field
	// outside spec) lets callers reach the JSON-RPC binding without
	// this list. We populate it explicitly for spec round-trippers.
	SupportedInterfaces []AgentInterface `json:"supportedInterfaces,omitempty"`

	// Signatures hold JWS signatures of the canonical Agent Card per
	// §4.4.7. v0.9.4 ships the field shape; signature plumbing arrives
	// in a future Wave (#225 B2 covers JWKS publication; signing TBD).
	Signatures []AgentCardSignature `json:"signatures,omitempty"`

	// IconURL is an optional URL to a square icon for the agent. §4.4.1
	// icon_url.
	IconURL string `json:"iconUrl,omitempty"`

	// XChepherdP2P is the open-source chepherd extension advertising
	// P2P plumbing (signaling endpoint, ICE servers, etc.). Vanilla
	// A2A clients ignore unknown extensions; chepherd-aware clients
	// negotiate WebRTC P2P via this block. Plumbing arrives in S5.
	XChepherdP2P *ChepherdP2PExtension `json:"x-chepherd-p2p,omitempty"`

	// XIOgrid is the chepherd-defined extension advertising the iogrid
	// recipe-dispatch endpoint. Populated by cmd/run.go when the
	// --iogrid-endpoint flag is set. Vanilla A2A clients ignore;
	// chepherd-aware iogrid catalogues can discover a peer that
	// accepts recipes via this block. Refs #318 (#225 row E1).
	XIOgrid *IOgridExtension `json:"x-iogrid,omitempty"`

	// XChepherdMethodAliases publishes the slash-camelCase →
	// PascalCase JSON-RPC method-name translation table this server
	// accepts. Spec-conformant a2a-python clients use PascalCase
	// (the table values + map keys to the right of the colon); stale
	// a2a-js clients use slash-camelCase (the keys). Inbound calls
	// in either form route to the same handler. Refs #561 #568.
	XChepherdMethodAliases map[string]string `json:"x-chepherd-method-aliases,omitempty"`
}

// AgentProvider per A2A v1.0 spec §4.4.2.
type AgentProvider struct {
	URL          string `json:"url"`
	Organization string `json:"organization"`
}

// AgentInterface per A2A v1.0 spec §4.4.6. Lets the card enumerate
// each transport binding this agent exposes (JSON-RPC, gRPC,
// HTTP+JSON, custom URI-identified bindings per §5.8) so clients can
// pick the binding they speak.
type AgentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	Tenant          string `json:"tenant,omitempty"`
	ProtocolVersion string `json:"protocolVersion"`
}

// AgentCardSignature per A2A v1.0 spec §4.4.7. RFC 7515 JWS shape.
// Signing pipeline is plumbed in a future Wave; this type ships so
// the wire schema doesn't omit a spec field.
type AgentCardSignature struct {
	Protected string         `json:"protected"`
	Signature string         `json:"signature"`
	Header    map[string]any `json:"header,omitempty"`
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

// AgentCardPath is the well-known path the A2A spec mandates (the
// JSON-suffixed canonical form).
const AgentCardPath = "/.well-known/agent-card.json"

// AgentCardAliasPath is the suffix-less form that peer agents commonly
// try first per the A2A discovery convention (mirrors how /.well-known/
// host-meta is canonical but /.well-known/host-meta.json is often
// added as an alias). Without this alias, a peer that follows the
// shorter form lands on chepherd's SPA wildcard and gets the
// marketing landing page HTML — opaque to A2A discovery.
//
// Refs #378 P1.
const AgentCardAliasPath = "/.well-known/agent-card"

package a2a

// ChepherdP2PExtension is the chepherd-specific Agent Card extension
// advertising the WebRTC P2P plumbing for chepherd-aware A2A clients.
// Published as an open spec at
// https://github.com/agenity-org/agenity-p2p-extension (Apache-2.0);
// AGNTCY working group submission tracked in #208.
//
// Vanilla A2A clients ignore this block (per A2A spec on unknown
// extensions); chepherd-aware clients negotiate P2P via the
// signalingEndpoint and iceServers. Full negotiation logic + WebRTC
// data plane arrives in S5.
//
// Refs #208.
type ChepherdP2PExtension struct {
	// Version of the chepherd-p2p extension schema (semver).
	Version string `json:"version"`

	// Supported is the chepherd-p2p capability flag (#488 Wave F1).
	// true → this runner has WebRTC plumbing wired and accepts SDP
	// offers at SignalingEndpoint. false → P2P negotiation is
	// disabled; peers should fall back to HTTP A2A.
	Supported bool `json:"supported"`

	// SignalingEndpoint is where the peer reaches the chepherd-relay
	// to exchange WebRTC SDP offers + ICE candidates. Populated by
	// the runner with its own /webrtc/offer URL when the
	// runtimehttp.Server has the signaling routes mounted.
	SignalingEndpoint string `json:"signalingEndpoint,omitempty"`

	// IceServers lists STUN + TURN servers the chepherd-relay exposes
	// for ICE candidate gathering. Empty in the PUBLIC extension —
	// authenticated callers receive the full list via A4's extended
	// Agent Card (auth-gated to avoid leaking STUN/TURN URLs to
	// unauthenticated discovery).
	IceServers []IceServer `json:"iceServers,omitempty"`

	// SupportedDataChannels lists the WebRTC DataChannel labels this
	// peer accepts for A2A traffic. v0.9.2 ships with the canonical
	// "a2a" label; future flavors (audio/video bridges) may extend.
	SupportedDataChannels []string `json:"supportedDataChannels,omitempty"`
}

// IceServer is a STUN or TURN endpoint per the W3C WebRTC spec.
type IceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// DefaultExtension returns the PUBLIC v0.9.4 chepherd-p2p extension
// every spawned runner advertises by default — Supported=true plus
// the "a2a" DataChannel label. Suitable for the
// /.well-known/agent-card.json endpoint (unauthenticated discovery).
//
// The SignalingEndpoint stays empty here because at construction
// time the runner doesn't know its own externally-reachable URL;
// the runner's a2a_endpoint.go AgentCard builder fills it in via
// PopulateSignalingEndpoint once a2aBaseURL is known. Same pattern
// applies to runner IceServers (the operator's --webrtc-ice-server
// flag drives those at runtime).
//
// Refs #488 Wave F1.
func DefaultExtension() *ChepherdP2PExtension {
	return &ChepherdP2PExtension{
		Version:               "0.9.4",
		Supported:             true,
		SupportedDataChannels: []string{"a2a"},
	}
}

// PopulateSignalingEndpoint sets the runner's externally-reachable
// /webrtc/offer URL on the extension. Pass the runner's A2A base URL
// (scheme://host:port). When base is empty (e.g. R1 scaffold mode),
// SignalingEndpoint stays empty so the extension stays internally
// consistent.
//
// Refs #488 Wave F1.
func (e *ChepherdP2PExtension) PopulateSignalingEndpoint(a2aBaseURL string) {
	if e == nil || a2aBaseURL == "" {
		return
	}
	trim := a2aBaseURL
	for len(trim) > 0 && trim[len(trim)-1] == '/' {
		trim = trim[:len(trim)-1]
	}
	e.SignalingEndpoint = trim + "/webrtc/offer"
}

// WithICEServers returns a copy of the extension with the given ICE
// server list attached. Used by A4's getAuthenticatedExtendedCard
// path to expose the full STUN/TURN config to authenticated callers
// while DefaultExtension's public copy stays minimal.
//
// Refs #488 Wave F1 — substrate for the A4/F1 auth-vs-public split.
func (e *ChepherdP2PExtension) WithICEServers(servers []IceServer) *ChepherdP2PExtension {
	if e == nil {
		return nil
	}
	cp := *e
	cp.IceServers = append([]IceServer(nil), servers...)
	return &cp
}

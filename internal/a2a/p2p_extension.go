package a2a

// ChepherdP2PExtension is the chepherd-specific Agent Card extension
// advertising the WebRTC P2P plumbing for chepherd-aware A2A clients.
// Published as an open spec at
// https://github.com/chepherd/chepherd-p2p-extension (Apache-2.0);
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

	// SignalingEndpoint is where the peer reaches the chepherd-relay
	// to exchange WebRTC SDP offers + ICE candidates. Empty in v0.9.2
	// scaffold; populated by chepherd-relay in S4.
	SignalingEndpoint string `json:"signalingEndpoint,omitempty"`

	// IceServers lists STUN + TURN servers the chepherd-relay exposes
	// for ICE candidate gathering. Empty in v0.9.2 scaffold.
	IceServers []IceServer `json:"iceServers,omitempty"`

	// SupportedDataChannels lists the WebRTC DataChannel labels this
	// peer accepts for A2A traffic. v0.9.2 ships with the canonical
	// "a2a" label only.
	SupportedDataChannels []string `json:"supportedDataChannels,omitempty"`
}

// IceServer is a STUN or TURN endpoint per the W3C WebRTC spec.
type IceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// DefaultExtension returns the v0.9.2-scaffold p2p extension that
// every spawned runner advertises by default. The relay endpoint
// stays empty until S4 lights the chepherd-relay infrastructure.
func DefaultExtension() *ChepherdP2PExtension {
	return &ChepherdP2PExtension{
		Version:               "0.9.2",
		SupportedDataChannels: []string{"a2a"},
	}
}

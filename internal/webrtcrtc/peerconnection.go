// Package webrtcrtc wraps pion v4's PeerConnection + DataChannel for
// chepherd's P2P substrate (C4 #310 NAT traversal + C5 #311 signaling/
// ICE/DTLS). The chepherd-p2p extension on AgentCard advertises the
// 'a2a' DataChannel label as the canonical A2A traffic carrier.
//
// Architecture per docs/V0.9.2-ARCHITECTURE.md §S5:
//
//	chepherd-A                                         chepherd-B
//	  │                                                    │
//	  │ ──── SDP offer (POST /webrtc/offer) ───────────►   │
//	  │ ◄─── SDP answer (200 body) ─────────────────────── │
//	  │ ──── ICE candidates (POST /webrtc/ice) ─────────►  │
//	  │ ◄─── ICE candidates (200 body) ─────────────────── │
//	  │                                                    │
//	  │ ═══ DTLS handshake ═══════════════════════════════ │
//	  │ ═══ DataChannel 'a2a' open ══════════════════════  │
//	  │ ──── A2A JSON-RPC over DataChannel ─────────────►  │
//	  │ ◄─── A2A JSON-RPC over DataChannel ─────────────── │
//
// The /webrtc/* HTTP endpoints are the SIGNALING relay (out-of-band);
// once the DataChannel opens, A2A traffic flows P2P over DTLS without
// touching the relay anymore.
//
// Refs #310 (C4) #311 (C5) + #208.
//
// Package name is webrtcrtc to avoid name collision with the imported
// github.com/pion/webrtc/v4 module — Go's package-name resolution
// rejects two packages with the same short name.
package webrtcrtc

import (
	"errors"
	"fmt"
	"sync"

	"github.com/pion/webrtc/v4"
)

// DefaultICEServers returns the chepherd-default ICE server list.
// Public Google STUN is the bare minimum; production deployments can
// override via Config.ICEServers when constructing PeerConnection
// (chepherd's --webrtc-ice-server flag or the chepherd-p2p extension's
// iceServers block).
func DefaultICEServers() []webrtc.ICEServer {
	return []webrtc.ICEServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
		{URLs: []string{"stun:stun1.l.google.com:19302"}},
	}
}

// Config carries the chepherd-side knobs for constructing a
// PeerConnection. All fields optional — zero-value Config uses
// DefaultICEServers() + 'a2a' DataChannel label + no fingerprint
// pinning (pion's default RFC 8829 verification still applies).
type Config struct {
	ICEServers   []webrtc.ICEServer
	ChannelLabel string // default 'a2a' per chepherd-p2p extension

	// PinnedFingerprints is the optional out-of-band fingerprint
	// pin set (#494 Wave F4). When non-empty, SetRemoteOffer +
	// SetRemoteAnswer reject SDPs whose a=fingerprint doesn't
	// match at least one entry — fast-fails before the DTLS
	// handshake. Defends against SDP-signaling-compromise MITM
	// where both cert + advertised fingerprint are attacker-swapped
	// (pion's default verification only checks cert↔SDP match,
	// not that the SDP fingerprint is the operator-trusted one).
	// Empty disables pinning (pion's RFC 8829 default still
	// rejects cert/fingerprint mismatches).
	PinnedFingerprints []webrtc.DTLSFingerprint
}

// PeerConnection is the chepherd-wrapped pion v4 PeerConnection plus
// the application DataChannel. Use NewPeerConnection to construct;
// CreateOffer / SetRemoteDescription / CreateAnswer / AddICECandidate
// are forwarded to the underlying pion methods.
type PeerConnection struct {
	mu sync.Mutex

	pc *webrtc.PeerConnection
	ch *webrtc.DataChannel

	// pinned is the operator-supplied out-of-band fingerprint pin
	// set (#494 Wave F4). nil/empty disables pinning.
	pinned []webrtc.DTLSFingerprint

	// onMessage fires for every inbound DataChannel message.
	// Caller sets via OnMessage.
	onMessage func(payload []byte)

	// onOpen fires when the DataChannel transitions to Open state.
	// Caller sets via OnOpen.
	onOpen func()
}

// NewPeerConnection constructs a chepherd PeerConnection in the
// CALLER (offering) role: it owns the DataChannel + will produce
// the SDP offer.
//
// Use NewPeerConnectionForAnswerer when this peer is the answering
// side (waits for the inbound DataChannel announcement).
//
// Refs #310 #311.
func NewPeerConnection(cfg Config) (*PeerConnection, error) {
	pc, err := webrtc.NewPeerConnection(buildConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("webrtc: NewPeerConnection: %w", err)
	}
	label := cfg.ChannelLabel
	if label == "" {
		label = "a2a"
	}
	ch, err := pc.CreateDataChannel(label, nil)
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("webrtc: CreateDataChannel: %w", err)
	}
	p := &PeerConnection{pc: pc, ch: ch, pinned: cfg.PinnedFingerprints}
	p.wireChannel()
	return p, nil
}

// NewPeerConnectionForAnswerer constructs a PeerConnection in the
// ANSWERER role: it does NOT pre-create a DataChannel; instead it
// waits for the caller's announcement via OnDataChannel.
//
// Refs #310 #311.
func NewPeerConnectionForAnswerer(cfg Config) (*PeerConnection, error) {
	pc, err := webrtc.NewPeerConnection(buildConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("webrtc: NewPeerConnection (answerer): %w", err)
	}
	p := &PeerConnection{pc: pc, pinned: cfg.PinnedFingerprints}
	pc.OnDataChannel(func(ch *webrtc.DataChannel) {
		p.mu.Lock()
		p.ch = ch
		p.mu.Unlock()
		p.wireChannel()
	})
	return p, nil
}

func buildConfig(cfg Config) webrtc.Configuration {
	servers := cfg.ICEServers
	if len(servers) == 0 {
		servers = DefaultICEServers()
	}
	return webrtc.Configuration{ICEServers: servers}
}

// wireChannel attaches pion DataChannel callbacks to our caller-set
// closures. Idempotent — safe to call from both NewPeerConnection
// (channel pre-created) and OnDataChannel (answerer path).
func (p *PeerConnection) wireChannel() {
	p.mu.Lock()
	ch := p.ch
	p.mu.Unlock()
	if ch == nil {
		return
	}
	ch.OnMessage(func(msg webrtc.DataChannelMessage) {
		p.mu.Lock()
		cb := p.onMessage
		p.mu.Unlock()
		if cb != nil {
			cb(msg.Data)
		}
	})
	ch.OnOpen(func() {
		p.mu.Lock()
		cb := p.onOpen
		p.mu.Unlock()
		if cb != nil {
			cb()
		}
	})
}

// OnMessage registers a callback invoked for every inbound DataChannel
// message. Safe to call before OR after the channel opens.
func (p *PeerConnection) OnMessage(cb func(payload []byte)) {
	p.mu.Lock()
	p.onMessage = cb
	p.mu.Unlock()
}

// OnOpen registers a callback invoked when the DataChannel transitions
// to Open.
func (p *PeerConnection) OnOpen(cb func()) {
	p.mu.Lock()
	p.onOpen = cb
	p.mu.Unlock()
}

// Send writes payload to the DataChannel. Errors if the channel
// isn't yet open OR has been closed.
func (p *PeerConnection) Send(payload []byte) error {
	p.mu.Lock()
	ch := p.ch
	p.mu.Unlock()
	if ch == nil {
		return errors.New("webrtc: DataChannel not yet announced (waiting for OnDataChannel)")
	}
	if ch.ReadyState() != webrtc.DataChannelStateOpen {
		return fmt.Errorf("webrtc: DataChannel not open (state=%s)", ch.ReadyState().String())
	}
	return ch.Send(payload)
}

// CreateOffer generates an SDP offer + sets it as the local description.
// Returns the SDP body the caller POSTs to the peer's /webrtc/offer.
func (p *PeerConnection) CreateOffer() (webrtc.SessionDescription, error) {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("CreateOffer: %w", err)
	}
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("SetLocalDescription: %w", err)
	}
	return offer, nil
}

// SetRemoteOffer accepts an inbound SDP offer + generates an answer.
// Answerer-side flow.
//
// #494 Wave F4 — when PinnedFingerprints is configured, the inbound
// offer's a=fingerprint MUST match at least one pinned entry. On
// mismatch, the function returns ErrFingerprintMismatch + the
// underlying PeerConnection is closed so no DTLS handshake attempt
// can leak state. Pinning fast-fails BEFORE pion's own RFC 8829
// verification (which would catch a same-tampered cert+SDP from
// matching, but not catch a different-cert-and-SDP swap by a MITM).
func (p *PeerConnection) SetRemoteOffer(offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	if err := p.verifyRemoteFingerprint(offer); err != nil {
		_ = p.pc.Close()
		return webrtc.SessionDescription{}, err
	}
	if err := p.pc.SetRemoteDescription(offer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("SetRemoteDescription: %w", err)
	}
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("CreateAnswer: %w", err)
	}
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("SetLocalDescription: %w", err)
	}
	return answer, nil
}

// SetRemoteAnswer accepts the answer to a previously-created offer.
// Caller-side flow.
//
// #494 Wave F4 — same pinning gate as SetRemoteOffer.
func (p *PeerConnection) SetRemoteAnswer(answer webrtc.SessionDescription) error {
	if err := p.verifyRemoteFingerprint(answer); err != nil {
		_ = p.pc.Close()
		return err
	}
	if err := p.pc.SetRemoteDescription(answer); err != nil {
		return fmt.Errorf("SetRemoteDescription: %w", err)
	}
	return nil
}

// verifyRemoteFingerprint is the F4 pinning gate. No-op when
// PinnedFingerprints is empty (pion's RFC 8829 cert↔SDP verification
// still applies separately).
func (p *PeerConnection) verifyRemoteFingerprint(sdp webrtc.SessionDescription) error {
	p.mu.Lock()
	pinned := p.pinned
	p.mu.Unlock()
	if len(pinned) == 0 {
		return nil
	}
	return VerifyPinnedSDP(sdp, pinned)
}

// AddICECandidate trickles a remote ICE candidate into the PeerConnection.
func (p *PeerConnection) AddICECandidate(c webrtc.ICECandidateInit) error {
	return p.pc.AddICECandidate(c)
}

// OnICECandidate registers a callback invoked for every LOCAL ICE
// candidate the gathering layer produces. Caller relays each candidate
// to the peer via the signaler.
func (p *PeerConnection) OnICECandidate(cb func(*webrtc.ICECandidate)) {
	p.pc.OnICECandidate(cb)
}

// Close releases the PeerConnection + DataChannel.
func (p *PeerConnection) Close() error {
	return p.pc.Close()
}

// LocalDescription returns the local SDP description (offer or answer
// depending on role). Useful for re-emitting the SDP after gathering.
func (p *PeerConnection) LocalDescription() *webrtc.SessionDescription {
	return p.pc.LocalDescription()
}

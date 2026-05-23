// Package transport is the abstract layer between chepherd's protocol v1
// (docs/PROTOCOL.md) and the underlying wire — either WebRTC DataChannel
// (default, P2P, privacy-preserving) or WebSocket via relay (opt-in
// fallback). The same Transport interface satisfies both, so business
// logic in internal/daemon/rc/ is transport-agnostic.
//
// Layered against docs/PROTOCOL.md:
//
//	Layer 3 — Message envelope               ← internal/daemon/rc/envelope/
//	Layer 2 — Transport (this package)
//	             ┌── WebRTC DataChannel
//	             └── WebSocket relay
//	Layer 1 — Auth                            ← internal/daemon/rc/auth/
package transport

import (
	"context"
	"errors"
)

// Mode names the transport implementation choice.
type Mode string

const (
	ModeWebRTC Mode = "webrtc" // P2P, DTLS-encrypted, server can't see data
	ModeWS     Mode = "ws"     // Through relay, TLS to relay, server CAN see data
)

// Transport is the abstract wire between the daemon and one connected peer
// (client or server). All bytes flowing through it are framed JSON envelopes
// from internal/daemon/rc/envelope; this layer doesn't know about message
// shape, only delivery.
//
// Implementations MUST be safe for concurrent calls to Send + Recv.
// Calling Close while Send/Recv is blocked MUST cause those calls to return.
type Transport interface {
	// Mode reports which transport implementation is in use.
	Mode() Mode

	// Send delivers one frame. Returns when the frame has been handed off
	// to the underlying transport (does not wait for peer ack).
	// Returns ErrClosed if the transport has been closed.
	// Returns ErrBackpressure if the local send-buffer is overflowing
	// (caller should apply drop-oldest policy per protocol §7).
	Send(ctx context.Context, frame []byte) error

	// Recv returns the next frame from the peer. Blocks until a frame
	// arrives or ctx is cancelled. Returns ErrClosed when the peer
	// disconnects cleanly. Frame is a single JSON envelope (no newline
	// framing on this layer).
	Recv(ctx context.Context) ([]byte, error)

	// Close terminates the transport and releases resources.
	// Calling Close more than once is a no-op.
	Close() error

	// Stats returns a snapshot of transport-level counters for observability.
	// Used by the chepherd TUI to display "rc: connected · 3 clients listening".
	Stats() Stats
}

// Stats is the observability snapshot every Transport implementation provides.
type Stats struct {
	Mode           Mode
	Connected      bool
	PeerID         string // remote peer identifier — bastion_id when daemon-side, user_id when client-side
	FramesSent     uint64
	FramesReceived uint64
	BytesSent      uint64
	BytesReceived  uint64
	// LastActivity unix-nano; updated on any send/recv.
	LastActivity int64
	// SendBufferDepth: outstanding frames waiting in the send queue.
	SendBufferDepth int
	// Reconnects: count of successful reconnects on this Transport instance.
	Reconnects int
}

// Common errors returned across implementations.
var (
	// ErrClosed — Send/Recv called on a closed Transport.
	ErrClosed = errors.New("transport: closed")

	// ErrBackpressure — local send queue is full; caller should drop oldest
	// info-level frames per protocol §7 backpressure policy.
	ErrBackpressure = errors.New("transport: backpressure")

	// ErrNoPeer — Send called before a peer was established.
	ErrNoPeer = errors.New("transport: no peer connected")

	// ErrUnauthorized — peer's auth token was rejected at handshake.
	ErrUnauthorized = errors.New("transport: unauthorized")

	// ErrPeerSelected — daemon advertised but multiple peers connected; only
	// the first is accepted in v1. Future versions may multiplex.
	ErrPeerSelected = errors.New("transport: peer already selected")
)

// Factory builds a Transport from the daemon's rc configuration.
//
// The daemon picks the factory based on the user's `chepherd rc enable`
// flag (--p2p default, --relay-mode opt-in). The factory abstracts the
// connection establishment (signaling for WebRTC, WSS handshake for WS).
type Factory interface {
	// Mode reports which Mode this factory produces.
	Mode() Mode

	// Dial establishes a Transport to the named peer. For WebRTC, this
	// initiates SDP offer/answer/ICE via the signaling endpoint. For WS,
	// this opens the WSS connection with the bearer token.
	//
	// peerID is the bastion_id (when called from a client) or the
	// expected client_id (when called from a daemon ready-to-accept).
	Dial(ctx context.Context, peerID string) (Transport, error)

	// Listen opens the transport to accept incoming peer connections.
	// For WebRTC, registers with the signaling endpoint for offer arrivals.
	// For WS, dials to the relay + subscribes to commands directed at this
	// bastion. Returns when ctx is cancelled.
	Listen(ctx context.Context, onPeer func(Transport)) error

	// Close releases factory-level resources (signaling client, WSS pool).
	Close() error
}

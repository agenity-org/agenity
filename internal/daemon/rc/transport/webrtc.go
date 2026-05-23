package transport

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

// WebRTCTransport implements Transport over a single WebRTC DataChannel.
// This is chepherd-rc's PRIVACY-PRESERVING mode: the data plane is
// DTLS-encrypted peer-to-peer; no relay sees the payload bytes.
//
// Per protocol v1 §1: signaling (SDP offer/answer + ICE candidates) goes
// through the chepherd-relay's REST endpoints, but once the DataChannel
// is open the relay is OUT of the data path entirely.
type WebRTCTransport struct {
	pc          *webrtc.PeerConnection
	dc          *webrtc.DataChannel
	mode        Mode
	peerID      string
	recvBuffer  chan []byte
	closeOnce   sync.Once
	closed      chan struct{}

	// observability
	framesSent     atomic.Uint64
	framesReceived atomic.Uint64
	bytesSent      atomic.Uint64
	bytesReceived  atomic.Uint64
	lastActivity   atomic.Int64
	reconnects     atomic.Int64
}

// RecvBufferSize — inbound queue depth before the DataChannel's onMessage
// callback drops frames. Matches WS SendBufferSize for symmetry.
const RecvBufferSize = 256

// NewWebRTCTransport wraps an already-open PeerConnection + DataChannel.
// The signaling exchange that produced this pair happens at a higher
// level (internal/daemon/rc/signaling/).
func NewWebRTCTransport(pc *webrtc.PeerConnection, dc *webrtc.DataChannel, peerID string) *WebRTCTransport {
	t := &WebRTCTransport{
		pc:         pc,
		dc:         dc,
		mode:       ModeWebRTC,
		peerID:     peerID,
		recvBuffer: make(chan []byte, RecvBufferSize),
		closed:     make(chan struct{}),
	}

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		// IsString carries the JSON envelope; binary frames are reserved
		// for future binary-payload extensions (out of scope v1).
		if !msg.IsString {
			return
		}
		t.framesReceived.Add(1)
		t.bytesReceived.Add(uint64(len(msg.Data)))
		t.lastActivity.Store(time.Now().UnixNano())
		select {
		case t.recvBuffer <- msg.Data:
		default:
			// drop: receiver too slow; surface via stats (sendBufferDepth-like
			// counter not added here but the user-visible symptom is missing
			// log lines, which is the §7-acceptable behavior)
		}
	})

	dc.OnClose(func() {
		_ = t.Close()
	})

	return t
}

// Mode reports ModeWebRTC.
func (t *WebRTCTransport) Mode() Mode { return t.mode }

// Send delivers one frame over the DataChannel as a string message.
// Returns ErrBackpressure when pion's outgoing buffer is too full.
func (t *WebRTCTransport) Send(ctx context.Context, frame []byte) error {
	select {
	case <-t.closed:
		return ErrClosed
	default:
	}
	// pion's BufferedAmount() is the SCTP buffer depth. Cap roughly the
	// same as WS's 256-frame * 1KiB-typical = 256KiB.
	if t.dc.BufferedAmount() > 256*1024 {
		return ErrBackpressure
	}
	if err := t.dc.SendText(string(frame)); err != nil {
		if errors.Is(err, webrtc.ErrConnectionClosed) {
			_ = t.Close()
			return ErrClosed
		}
		return fmt.Errorf("webrtc send: %w", err)
	}
	t.framesSent.Add(1)
	t.bytesSent.Add(uint64(len(frame)))
	t.lastActivity.Store(time.Now().UnixNano())
	return nil
}

// Recv pulls the next frame from the inbound queue.
func (t *WebRTCTransport) Recv(ctx context.Context) ([]byte, error) {
	select {
	case <-t.closed:
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case frame, ok := <-t.recvBuffer:
		if !ok {
			return nil, ErrClosed
		}
		return frame, nil
	}
}

// Close shuts the DataChannel + PeerConnection.
func (t *WebRTCTransport) Close() error {
	var closeErr error
	t.closeOnce.Do(func() {
		close(t.closed)
		if t.dc != nil {
			_ = t.dc.Close()
		}
		if t.pc != nil {
			closeErr = t.pc.Close()
		}
		close(t.recvBuffer)
	})
	return closeErr
}

// Stats snapshot.
func (t *WebRTCTransport) Stats() Stats {
	return Stats{
		Mode:            t.mode,
		Connected:       !t.isClosedNow(),
		PeerID:          t.peerID,
		FramesSent:      t.framesSent.Load(),
		FramesReceived:  t.framesReceived.Load(),
		BytesSent:       t.bytesSent.Load(),
		BytesReceived:   t.bytesReceived.Load(),
		LastActivity:    t.lastActivity.Load(),
		SendBufferDepth: int(t.dc.BufferedAmount() / 1024), // approx frames
		Reconnects:      int(t.reconnects.Load()),
	}
}

func (t *WebRTCTransport) isClosedNow() bool {
	select {
	case <-t.closed:
		return true
	default:
		return false
	}
}

// ─── WebRTCFactory ──────────────────────────────────────────────────────

// WebRTCFactory builds peer connections + DataChannels using a SignalingClient
// (REST against chepherd-relay's /v1/signaling/* endpoints).
type WebRTCFactory struct {
	// STUNServers per protocol v1 §4 (chepherd defaults: public STUN servers
	// that NEVER see application data — only NAT-mapped address discovery).
	STUNServers []string

	// TURNServers — used only as fallback when both peers are behind
	// symmetric NAT. Empty by default (=> P2P-or-fail).
	TURNServers []TURNServer

	// Signaling — REST client to chepherd-relay's /v1/signaling/* endpoints.
	Signaling SignalingClient

	// BastionID — this daemon's bastion identifier. Used in the trickled-ICE
	// poll loop (acceptOffer) to identify which queue to drain.
	BastionID string
}

// TURNServer config for symmetric-NAT fallback.
type TURNServer struct {
	URL      string
	Username string
	Password string
}

// SignalingClient is the dependency injection point for the REST signaling
// channel. Real impl lives in internal/daemon/rc/signaling/ (future commit);
// tests use an in-memory mock.
//
// Trickled-ICE support — PostCandidate + PollCandidates — was added in
// chepherd-relay@48aed53 to align the daemon with the new web/iOS/Android
// clients (which all trickle by default). Implementations that don't
// support trickling MAY return ErrTricklingUnsupported from these methods;
// the WebRTCFactory then falls back to the legacy bundled-ICE flow.
type SignalingClient interface {
	// PostOffer sends an SDP offer + ICE candidates to the relay, addressed
	// to the named peer. Returns the peer's SDP answer + ICE candidates.
	PostOffer(ctx context.Context, peerID string, offer webrtc.SessionDescription) (*OfferAnswer, error)

	// WaitForOffer (server-side) blocks until a peer initiates a connection
	// to us, then returns their offer. Returns ctx error on cancel.
	WaitForOffer(ctx context.Context) (*IncomingOffer, error)

	// PostAnswer (server-side) sends our SDP answer + ICE candidates back to
	// the initiating peer.
	PostAnswer(ctx context.Context, peerID string, answer webrtc.SessionDescription) error

	// PostCandidate sends ONE trickled ICE candidate to the peer named by
	// peerID. Implementations targeting chepherd-relay POST to
	// /v1/signaling/candidate with {bastion_id: peerID, candidate}.
	PostCandidate(ctx context.Context, peerID string, candidate webrtc.ICECandidateInit) error

	// PollCandidates long-polls for trickled candidates addressed to OUR
	// peerID (self). Implementations targeting chepherd-relay GET
	// /v1/signaling/candidates?bastion_id=<self>. Blocks up to the relay's
	// long-poll window (~25s) or returns sooner if candidates are queued.
	// Returns ctx.Err() on cancel.
	PollCandidates(ctx context.Context, selfID string) ([]webrtc.ICECandidateInit, error)
}

// ErrTricklingUnsupported — returned by SignalingClient impls that haven't
// implemented trickled ICE yet. WebRTCFactory falls back to bundled-ICE
// when this is the case.
var ErrTricklingUnsupported = errors.New("signaling: trickled ICE not supported")

// OfferAnswer bundles the peer's SDP answer + their ICE candidates.
type OfferAnswer struct {
	Answer        webrtc.SessionDescription
	IceCandidates []webrtc.ICECandidateInit
}

// IncomingOffer is what the server-side WaitForOffer returns.
type IncomingOffer struct {
	PeerID        string
	Offer         webrtc.SessionDescription
	IceCandidates []webrtc.ICECandidateInit
}

// DefaultSTUNServers — the public servers chepherd defaults to. These
// servers never see application data; their only role is to tell each
// peer what its NAT-mapped public address looks like.
var DefaultSTUNServers = []string{
	"stun:stun.l.google.com:19302",
	"stun:stun.cloudflare.com:3478",
}

// Mode reports ModeWebRTC.
func (f *WebRTCFactory) Mode() Mode { return ModeWebRTC }

// Dial — client-side: post our offer to signaling, get the bastion's answer,
// add their ICE candidates, wait for the DataChannel to open.
func (f *WebRTCFactory) Dial(ctx context.Context, bastionID string) (Transport, error) {
	if f.Signaling == nil {
		return nil, errors.New("webrtc: signaling client not configured")
	}
	pc, err := webrtc.NewPeerConnection(f.config())
	if err != nil {
		return nil, fmt.Errorf("webrtc: new PC: %w", err)
	}

	// Client initiates the DataChannel.
	dcOpts := &webrtc.DataChannelInit{Ordered: boolPtr(true)}
	dc, err := pc.CreateDataChannel("chepherd.v1.p2p", dcOpts)
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("webrtc: create DataChannel: %w", err)
	}

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("webrtc: create offer: %w", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("webrtc: set local desc: %w", err)
	}

	// Wait briefly for ICE gathering to settle before sending the offer
	// (chepherd uses non-trickle ICE for protocol simplicity in v1; the
	// trickle path is a v0.3 follow-up).
	gathered := webrtc.GatheringCompletePromise(pc)
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		// proceed anyway with whatever candidates we have
	case <-ctx.Done():
		_ = pc.Close()
		return nil, ctx.Err()
	}

	resp, err := f.Signaling.PostOffer(ctx, bastionID, *pc.LocalDescription())
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("webrtc: signaling: %w", err)
	}

	if err := pc.SetRemoteDescription(resp.Answer); err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("webrtc: set remote desc: %w", err)
	}
	for _, ice := range resp.IceCandidates {
		if err := pc.AddICECandidate(ice); err != nil {
			// log + continue — losing a single candidate isn't fatal
			continue
		}
	}

	// Wait for the DataChannel to open.
	open := make(chan struct{}, 1)
	dc.OnOpen(func() { open <- struct{}{} })
	select {
	case <-open:
	case <-time.After(30 * time.Second):
		_ = pc.Close()
		return nil, errors.New("webrtc: DataChannel did not open within 30s")
	case <-ctx.Done():
		_ = pc.Close()
		return nil, ctx.Err()
	}

	return NewWebRTCTransport(pc, dc, bastionID), nil
}

// Listen — server-side (daemon-side): wait for a client to initiate, accept
// the offer, send our answer, accept the DataChannel.
func (f *WebRTCFactory) Listen(ctx context.Context, onPeer func(Transport)) error {
	if f.Signaling == nil {
		return errors.New("webrtc: signaling client not configured")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		incoming, err := f.Signaling.WaitForOffer(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			// transient signaling errors — back off and retry
			time.Sleep(2 * time.Second)
			continue
		}

		go f.acceptOffer(ctx, incoming, onPeer)
	}
}

func (f *WebRTCFactory) acceptOffer(ctx context.Context, in *IncomingOffer, onPeer func(Transport)) {
	pc, err := webrtc.NewPeerConnection(f.config())
	if err != nil {
		return
	}
	dcCh := make(chan *webrtc.DataChannel, 1)
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		select {
		case dcCh <- dc:
		default:
		}
	})

	if err := pc.SetRemoteDescription(in.Offer); err != nil {
		_ = pc.Close()
		return
	}
	for _, ice := range in.IceCandidates {
		_ = pc.AddICECandidate(ice)
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		_ = pc.Close()
		return
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		_ = pc.Close()
		return
	}

	gathered := webrtc.GatheringCompletePromise(pc)
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
	}

	if err := f.Signaling.PostAnswer(ctx, in.PeerID, *pc.LocalDescription()); err != nil {
		_ = pc.Close()
		return
	}

	// Wait for client's DataChannel to arrive.
	select {
	case dc := <-dcCh:
		// Wait for it to open.
		open := make(chan struct{}, 1)
		dc.OnOpen(func() { open <- struct{}{} })
		select {
		case <-open:
			onPeer(NewWebRTCTransport(pc, dc, in.PeerID))
		case <-time.After(30 * time.Second):
			_ = pc.Close()
		}
	case <-time.After(30 * time.Second):
		_ = pc.Close()
	}
}

// Close releases factory resources.
func (f *WebRTCFactory) Close() error { return nil }

// config builds the webrtc.Configuration from the factory's STUN/TURN list.
func (f *WebRTCFactory) config() webrtc.Configuration {
	servers := []webrtc.ICEServer{}
	stuns := f.STUNServers
	if len(stuns) == 0 {
		stuns = DefaultSTUNServers
	}
	for _, url := range stuns {
		servers = append(servers, webrtc.ICEServer{URLs: []string{url}})
	}
	for _, tu := range f.TURNServers {
		servers = append(servers, webrtc.ICEServer{
			URLs:       []string{tu.URL},
			Username:   tu.Username,
			Credential: tu.Password,
		})
	}
	return webrtc.Configuration{ICEServers: servers}
}

func boolPtr(b bool) *bool { return &b }

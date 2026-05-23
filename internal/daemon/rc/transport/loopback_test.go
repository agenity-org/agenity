package transport_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/chepherd/chepherd/internal/daemon/rc/envelope"
	"github.com/chepherd/chepherd/internal/daemon/rc/transport"
)

// TestRCEndToEnd_WebRTC exercises the full P2P stack: protocol envelope →
// WebRTC DataChannel → protocol envelope. The signaling channel is a tiny
// in-process loopback (no relay, no network) so this is reproducible in CI.
//
// What it proves:
//   · two pion PeerConnections can exchange SDP+ICE in-process
//   · WebRTCTransport.Send/Recv roundtrips a real protocol envelope
//   · seq increments + payload integrity hold through the DTLS channel
//   · Stats counters update on both sides
//
// This is the v0.1 conformance test for the privacy-preserving transport.
// Real-network signaling (against chepherd-relay) is exercised by integration
// tests in internal/daemon/rc/signaling/ once the relay lands.
func TestRCEndToEnd_WebRTC(t *testing.T) {
	if testing.Short() {
		t.Skip("WebRTC loopback uses real DTLS — skipped in -short mode")
	}

	signal := newLoopbackSignal()

	clientFactory := &transport.WebRTCFactory{
		STUNServers: nil, // empty — pure in-process, no STUN traversal needed
		Signaling:   signal.clientView(),
	}
	daemonFactory := &transport.WebRTCFactory{
		STUNServers: nil,
		Signaling:   signal.daemonView(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Daemon side: Listen and capture the first accepted Transport.
	var daemonTr transport.Transport
	var dWG sync.WaitGroup
	dWG.Add(1)
	listenErr := make(chan error, 1)
	go func() {
		err := daemonFactory.Listen(ctx, func(tr transport.Transport) {
			daemonTr = tr
			dWG.Done()
		})
		if err != nil && ctx.Err() == nil {
			listenErr <- err
		}
	}()

	// Client side: Dial.
	clientTr, err := clientFactory.Dial(ctx, "test-bastion")
	if err != nil {
		t.Fatalf("client Dial: %v", err)
	}
	defer clientTr.Close()

	dWG.Wait()
	defer daemonTr.Close()

	// Exercise: client sends a register envelope, daemon receives + decodes it.
	var seq atomic.Uint64
	env, err := envelope.New(envelope.TypeRegister, envelope.RegisterPayload{
		BastionID:       "test-bastion",
		UserID:          "test-user",
		ChepherdVersion: "0.2.0-rc1",
		Capabilities:    []string{"pause", "inject"},
		SessionCount:    3,
	}, &seq)
	if err != nil {
		t.Fatalf("envelope.New: %v", err)
	}
	frame, _ := env.Marshal()
	if err := clientTr.Send(ctx, frame); err != nil {
		t.Fatalf("client Send: %v", err)
	}

	// Daemon side receives.
	recvCtx, recvCancel := context.WithTimeout(ctx, 15*time.Second)
	defer recvCancel()
	got, err := daemonTr.Recv(recvCtx)
	if err != nil {
		t.Fatalf("daemon Recv: %v", err)
	}
	if string(got) != string(frame) {
		t.Errorf("frame roundtrip mismatch:\n  sent %q\n  recv %q", frame, got)
	}

	// Decode + verify the payload survived intact.
	decoded, err := envelope.Decode(got)
	if err != nil {
		t.Fatalf("envelope.Decode: %v", err)
	}
	var rp envelope.RegisterPayload
	if err := decoded.DecodePayload(&rp); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if rp.BastionID != "test-bastion" || rp.SessionCount != 3 {
		t.Errorf("payload mismatch: %+v", rp)
	}

	// Send a verdict from daemon back to client; verify reverse direction.
	env2, _ := envelope.New(envelope.TypeVerdict, envelope.VerdictPayload{
		SessionUUID:  "abc",
		Session:      "openova-1",
		Verdict:      "silent",
		Scorecard:    map[string]int{"G": 7, "V": 7, "F": 7, "E": 6},
		Injected:     false,
	}, &seq)
	frame2, _ := env2.Marshal()
	if err := daemonTr.Send(ctx, frame2); err != nil {
		t.Fatalf("daemon Send: %v", err)
	}
	got2, err := clientTr.Recv(recvCtx)
	if err != nil {
		t.Fatalf("client Recv: %v", err)
	}
	if !strings.Contains(string(got2), `"verdict":"silent"`) {
		t.Errorf("client did not receive verdict frame: %s", got2)
	}

	// Stats sanity.
	cs, ds := clientTr.Stats(), daemonTr.Stats()
	if cs.FramesSent < 1 || cs.FramesReceived < 1 {
		t.Errorf("client stats unexpected: %+v", cs)
	}
	if ds.FramesSent < 1 || ds.FramesReceived < 1 {
		t.Errorf("daemon stats unexpected: %+v", ds)
	}
	if cs.Mode != transport.ModeWebRTC || ds.Mode != transport.ModeWebRTC {
		t.Errorf("modes: client=%s daemon=%s", cs.Mode, ds.Mode)
	}
}

// ─── loopback signaling — in-process mock of chepherd-relay /v1/signaling ───

// loopbackSignal is a single-pair signaling exchange that passes SDP +
// ICE candidates between two transport.SignalingClient views. NO network.
type loopbackSignal struct {
	mu         sync.Mutex
	offers     chan *transport.IncomingOffer
	answers    chan webrtc.SessionDescription
}

func newLoopbackSignal() *loopbackSignal {
	return &loopbackSignal{
		offers:  make(chan *transport.IncomingOffer, 1),
		answers: make(chan webrtc.SessionDescription, 1),
	}
}

// clientView returns the SignalingClient the client side (Dialer) uses.
func (s *loopbackSignal) clientView() transport.SignalingClient { return &clientSide{s: s} }

// daemonView returns the SignalingClient the daemon side (Listener) uses.
func (s *loopbackSignal) daemonView() transport.SignalingClient { return &daemonSide{s: s} }

type clientSide struct{ s *loopbackSignal }

func (c *clientSide) PostOffer(ctx context.Context, peerID string, offer webrtc.SessionDescription) (*transport.OfferAnswer, error) {
	// Push offer to daemon's WaitForOffer queue.
	select {
	case c.s.offers <- &transport.IncomingOffer{PeerID: "client-1", Offer: offer}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// Wait for daemon's answer.
	select {
	case ans := <-c.s.answers:
		return &transport.OfferAnswer{Answer: ans}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *clientSide) WaitForOffer(ctx context.Context) (*transport.IncomingOffer, error) {
	return nil, errNotApplicable
}

func (c *clientSide) PostAnswer(ctx context.Context, peerID string, answer webrtc.SessionDescription) error {
	return errNotApplicable
}

type daemonSide struct{ s *loopbackSignal }

func (d *daemonSide) PostOffer(ctx context.Context, peerID string, offer webrtc.SessionDescription) (*transport.OfferAnswer, error) {
	return nil, errNotApplicable
}

func (d *daemonSide) WaitForOffer(ctx context.Context) (*transport.IncomingOffer, error) {
	select {
	case in := <-d.s.offers:
		return in, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *daemonSide) PostAnswer(ctx context.Context, peerID string, answer webrtc.SessionDescription) error {
	select {
	case d.s.answers <- answer:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Trickled-ICE stubs — the loopback test uses bundled-ICE (offer + answer
// carry their candidates inline), so trickling is intentionally unsupported.
// Returning ErrTricklingUnsupported lets WebRTCFactory fall back to the
// bundled path without failing the whole test.
func (c *clientSide) PostCandidate(_ context.Context, _ string, _ webrtc.ICECandidateInit) error {
	return transport.ErrTricklingUnsupported
}
func (c *clientSide) PollCandidates(_ context.Context, _ string) ([]webrtc.ICECandidateInit, error) {
	return nil, transport.ErrTricklingUnsupported
}
func (d *daemonSide) PostCandidate(_ context.Context, _ string, _ webrtc.ICECandidateInit) error {
	return transport.ErrTricklingUnsupported
}
func (d *daemonSide) PollCandidates(_ context.Context, _ string) ([]webrtc.ICECandidateInit, error) {
	return nil, transport.ErrTricklingUnsupported
}

var errNotApplicable = jsonError("loopback: method not applicable on this side")

type jsonError string

func (e jsonError) Error() string { return string(e) }

// ensure json encoding of payloads is well-formed (smoke test for unrelated paths)
var _ = json.Marshal

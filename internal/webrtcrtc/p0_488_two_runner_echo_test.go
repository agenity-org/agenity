// internal/webrtcrtc/p0_488_two_runner_echo_test.go is the live-walk
// acceptance gate for #488 Wave F1 — verifies the substrate F1 adds
// against the existing P2P plumbing:
//
//   - F1's PUBLIC ChepherdP2PExtension shape (Supported=true,
//     SignalingEndpoint populated from baseURL) — pinned in
//     internal/a2a tests.
//
//   - SDP exchange via HTTP /webrtc/offer round-trip — caller posts
//     an SDP offer through the production DefaultHTTPSignaler at
//     the same handler chepherd-runner mounts under runtimehttp.
//     Asserts that the answerer's PeerConnection produces a valid
//     SDP answer + the wire shape is the canonical
//     webrtc.SessionDescription envelope.
//
// The full DataChannel round-trip (offer → answer → ICE trickle →
// DTLS → channel open → bytes flow) lives in
// peerconnection_test.go TestPeerConnection_DataChannelRoundTrip
// which uses in-memory ICE candidate exchange — that test already
// proves the pion v4 substrate works. F1's HTTP /webrtc/ice
// candidate exchange against a SHARED PC store lands with Wave F3
// (ICE-server discovery + per-session PC store on the runner).
//
// Refs #488 V0.9.2-ARCHITECTURE.md §10 §20.
package webrtcrtc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestWaveF1_HTTPSDPExchangeViaSignaler(t *testing.T) {
	t.Parallel()

	// Answerer-side HTTP handler is the SAME factory function
	// chepherd-runner's runtimehttp.Server mounts on /webrtc/offer.
	answererSrv := httptest.NewServer(HandleOffer(func() (*PeerConnection, error) {
		return NewPeerConnectionForAnswerer(Config{})
	}))
	defer answererSrv.Close()

	caller, err := NewPeerConnection(Config{})
	if err != nil {
		t.Fatalf("caller NewPeerConnection: %v", err)
	}
	defer caller.Close()

	offer, err := caller.CreateOffer()
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	if offer.Type != webrtc.SDPTypeOffer {
		t.Fatalf("offer.Type = %v, want SDPTypeOffer", offer.Type)
	}
	if offer.SDP == "" {
		t.Fatal("offer SDP empty")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	signaler := NewDefaultHTTPSignaler()
	answer, err := signaler.ExchangeOffer(ctx, answererSrv.URL, offer)
	if err != nil {
		t.Fatalf("ExchangeOffer: %v", err)
	}
	if answer.Type != webrtc.SDPTypeAnswer {
		t.Errorf("answer.Type = %v, want SDPTypeAnswer", answer.Type)
	}
	if answer.SDP == "" {
		t.Error("answer SDP empty")
	}
	// The caller can apply the answer without error — proves the
	// answerer produced a syntactically valid SDP.
	if err := caller.SetRemoteAnswer(answer); err != nil {
		t.Errorf("SetRemoteAnswer rejected answerer's SDP: %v", err)
	}
}

func TestWaveF1_HandleOffer_RejectsNonPOST(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(HandleOffer(func() (*PeerConnection, error) {
		return NewPeerConnectionForAnswerer(Config{})
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Errorf("GET unexpectedly accepted; want method-not-allowed-ish")
	}
}

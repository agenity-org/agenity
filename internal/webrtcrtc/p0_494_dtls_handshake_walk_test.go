// internal/webrtcrtc/p0_494_dtls_handshake_walk_test.go is the v0.9.4
// LIVE-WALK gate for #494 Wave F4 — drives a REAL pion DTLS handshake
// between two in-process PeerConnections + asserts both pinning
// outcomes:
//
//   - Pinned to the peer's actual fingerprint → handshake completes,
//     DataChannel opens, bytes round-trip.
//   - Pinned to a BOGUS fingerprint → SetRemoteOffer rejects before
//     the DTLS handshake even starts; DataChannel NEVER opens; the
//     PC is closed; subsequent Send calls error.
//
// "Tampered" here means the operator's pre-shared expected fingerprint
// doesn't match the peer's actual SDP fingerprint — the exact MITM
// scenario F4 defends against (per V0.9.2-ARCH §10 Pattern 2 Phase 7).
//
// Refs #494 V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 7.
package webrtcrtc

import (
	"errors"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestV094Walk_F4_MatchingPin_HandshakeSucceedsBytesFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live walk in -short")
	}
	// Set up caller without pinning so we can pre-create its
	// offer + extract the fingerprint to pin on the answerer.
	caller, err := NewPeerConnection(Config{ICEServers: nil})
	if err != nil {
		t.Fatalf("caller NewPeerConnection: %v", err)
	}
	defer caller.Close()
	offer, err := caller.CreateOffer()
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	callerFps := ExtractFingerprints(offer)
	if len(callerFps) == 0 {
		t.Fatal("pion offer carried no fingerprint")
	}

	// Answerer pinned to the CALLER's real fingerprint.
	answerer, err := NewPeerConnectionForAnswerer(Config{
		ICEServers:         nil,
		PinnedFingerprints: callerFps,
	})
	if err != nil {
		t.Fatalf("answerer NewPeerConnectionForAnswerer: %v", err)
	}
	defer answerer.Close()

	// Trickle ICE bidirectionally.
	caller.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			_ = answerer.AddICECandidate(c.ToJSON())
		}
	})
	answerer.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			_ = caller.AddICECandidate(c.ToJSON())
		}
	})

	answer, err := answerer.SetRemoteOffer(offer)
	if err != nil {
		t.Fatalf("SetRemoteOffer with matching pin: %v", err)
	}

	// Now caller pins to ANSWERER's fingerprint + accepts the answer.
	answerFps := ExtractFingerprints(answer)
	if len(answerFps) == 0 {
		t.Fatal("pion answer carried no fingerprint")
	}
	caller.mu.Lock()
	caller.pinned = answerFps // late-bind: tests bypass the constructor
	caller.mu.Unlock()
	if err := caller.SetRemoteAnswer(answer); err != nil {
		t.Fatalf("SetRemoteAnswer with matching pin: %v", err)
	}

	openA := make(chan struct{}, 1)
	openB := make(chan struct{}, 1)
	caller.OnOpen(func() { openA <- struct{}{} })
	answerer.OnOpen(func() { openB <- struct{}{} })
	timeout := time.After(walkTimeout(15 * time.Second))
	for ok := 0; ok < 2; {
		select {
		case <-openA:
			ok++
		case <-openB:
			ok++
		case <-timeout:
			t.Fatal("DataChannel didn't open — F4 matching-pin path broken")
		}
	}

	gotOnB := make(chan []byte, 1)
	answerer.OnMessage(func(payload []byte) { gotOnB <- payload })
	if err := caller.Send([]byte("f4-handshake-bytes")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case payload := <-gotOnB:
		if string(payload) != "f4-handshake-bytes" {
			t.Errorf("payload = %q", payload)
		}
		t.Logf("F4 matching-pin handshake: DataChannel open + bytes flowed")
	case <-time.After(walkTimeout(5 * time.Second)):
		t.Fatal("F4 matching-pin: payload didn't arrive")
	}
}

func TestV094Walk_F4_TamperedPin_DataChannelNeverOpens(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live walk in -short")
	}
	// Tampered = operator's pre-shared expected fingerprint doesn't
	// match the peer's actual one. The MITM analog: attacker sat
	// between the peers, swapped both the cert AND the fingerprint
	// in the SDP — pion's own RFC 8829 check passes (the new cert
	// matches the new fingerprint), but the operator's pinned
	// expected fingerprint catches the swap because they pre-shared
	// the LEGIT peer's fingerprint via a trusted side-channel.
	tampered := webrtc.DTLSFingerprint{
		Algorithm: "sha-256",
		Value:     "DE:AD:BE:EF:DE:AD:BE:EF:DE:AD:BE:EF:DE:AD:BE:EF:DE:AD:BE:EF:DE:AD:BE:EF:DE:AD:BE:EF:DE:AD:BE:EF",
	}
	answerer, err := NewPeerConnectionForAnswerer(Config{
		ICEServers:         nil,
		PinnedFingerprints: []webrtc.DTLSFingerprint{tampered},
	})
	if err != nil {
		t.Fatalf("answerer NewPeerConnectionForAnswerer: %v", err)
	}
	caller, err := NewPeerConnection(Config{ICEServers: nil})
	if err != nil {
		t.Fatalf("caller NewPeerConnection: %v", err)
	}
	defer caller.Close()
	offer, err := caller.CreateOffer()
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}

	// Bidirectional trickle so if pin-check ever leaks through, the
	// DTLS handshake has a chance to start (proving the rejection
	// fast-failed BEFORE handshake).
	caller.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			_ = answerer.AddICECandidate(c.ToJSON())
		}
	})
	answerer.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			_ = caller.AddICECandidate(c.ToJSON())
		}
	})

	openA := make(chan struct{}, 1)
	openB := make(chan struct{}, 1)
	caller.OnOpen(func() { openA <- struct{}{} })
	answerer.OnOpen(func() { openB <- struct{}{} })

	_, err = answerer.SetRemoteOffer(offer)
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("F4 tampered-pin: got %v, want ErrFingerprintMismatch", err)
	}

	// Confirm the DataChannel NEVER opens (give DTLS a chance to
	// race — if our gate didn't fast-fail, this would fire).
	select {
	case <-openA:
		t.Fatal("F4 tampered-pin: caller saw DataChannel open — the gate leaked")
	case <-openB:
		t.Fatal("F4 tampered-pin: answerer saw DataChannel open — the gate leaked")
	case <-time.After(3 * time.Second):
		t.Logf("F4 tampered-pin: DataChannel correctly never opened (3s window)")
	}

	// Send on the closed answerer must error.
	if err := answerer.Send([]byte("never-flows")); err == nil {
		t.Error("Send on a mismatch-closed answerer should fail")
	}
}

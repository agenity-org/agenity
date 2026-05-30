// internal/webrtcrtc/peerconnection_test.go — pins #310 #311.
// Exercises the full offer→answer→DataChannel→Send→OnMessage round-trip
// in-process using 2 PeerConnections wired to each other.
package webrtcrtc

import (
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// connectPair wires two PeerConnections (caller A + answerer B) by
// trickling SDP + ICE between them in-memory. Returns when both
// DataChannels have transitioned to Open.
func connectPair(t *testing.T) (*PeerConnection, *PeerConnection) {
	t.Helper()
	a, err := NewPeerConnection(Config{ICEServers: nil})
	if err != nil {
		t.Fatalf("NewPeerConnection A: %v", err)
	}
	b, err := NewPeerConnectionForAnswerer(Config{ICEServers: nil})
	if err != nil {
		t.Fatalf("NewPeerConnection B: %v", err)
	}

	// Trickle ICE: A's local candidates → B; B's local candidates → A.
	a.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		_ = b.AddICECandidate(c.ToJSON())
	})
	b.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		_ = a.AddICECandidate(c.ToJSON())
	})

	offer, err := a.CreateOffer()
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	answer, err := b.SetRemoteOffer(offer)
	if err != nil {
		t.Fatalf("SetRemoteOffer: %v", err)
	}
	if err := a.SetRemoteAnswer(answer); err != nil {
		t.Fatalf("SetRemoteAnswer: %v", err)
	}
	return a, b
}

func TestPeerConnection_DataChannelRoundTrip(t *testing.T) {
	t.Parallel()
	a, b := connectPair(t)
	defer a.Close()
	defer b.Close()

	openA := make(chan struct{}, 1)
	openB := make(chan struct{}, 1)
	a.OnOpen(func() { openA <- struct{}{} })
	b.OnOpen(func() { openB <- struct{}{} })

	gotOnB := make(chan []byte, 1)
	b.OnMessage(func(payload []byte) { gotOnB <- payload })

	// Wait for both sides to report open. ICE + DTLS handshake takes
	// ~hundreds of ms in-process.
	timeout := time.After(15 * time.Second)
	for ok := 0; ok < 2; {
		select {
		case <-openA:
			ok++
		case <-openB:
			ok++
		case <-timeout:
			t.Fatal("DataChannel didn't open within 15s")
		}
	}

	if err := a.Send([]byte("hello-from-A")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case payload := <-gotOnB:
		if string(payload) != "hello-from-A" {
			t.Errorf("payload = %q, want hello-from-A", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B never received A's message")
	}
}

func TestPeerConnection_SendBeforeOpenFails(t *testing.T) {
	t.Parallel()
	a, err := NewPeerConnection(Config{})
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	defer a.Close()
	if err := a.Send([]byte("too-early")); err == nil {
		t.Error("Send before open accepted; want error")
	}
}

func TestPeerConnection_DefaultICEServers(t *testing.T) {
	t.Parallel()
	servers := DefaultICEServers()
	if len(servers) < 1 {
		t.Error("DefaultICEServers returned empty")
	}
	if len(servers[0].URLs) == 0 || servers[0].URLs[0] == "" {
		t.Error("First ICE server has no URL")
	}
}

func TestPeerConnection_CustomChannelLabel(t *testing.T) {
	t.Parallel()
	a, err := NewPeerConnection(Config{ChannelLabel: "custom-label"})
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	defer a.Close()
	if a.ch.Label() != "custom-label" {
		t.Errorf("ChannelLabel = %q, want custom-label", a.ch.Label())
	}
}

func TestPeerConnection_AnswererOnlyOpensOnDataChannelAnnounce(t *testing.T) {
	t.Parallel()
	b, err := NewPeerConnectionForAnswerer(Config{})
	if err != nil {
		t.Fatalf("NewPeerConnectionForAnswerer: %v", err)
	}
	defer b.Close()
	// No DataChannel pre-created on answerer side.
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch != nil {
		t.Error("Answerer has pre-created DataChannel; want nil until OnDataChannel fires")
	}
}

func TestPeerConnection_MultipleMessagesInOrder(t *testing.T) {
	t.Parallel()
	a, b := connectPair(t)
	defer a.Close()
	defer b.Close()

	var (
		mu       sync.Mutex
		received [][]byte
	)
	b.OnMessage(func(payload []byte) {
		mu.Lock()
		received = append(received, append([]byte(nil), payload...))
		mu.Unlock()
	})

	openA := make(chan struct{}, 1)
	a.OnOpen(func() { openA <- struct{}{} })
	select {
	case <-openA:
	case <-time.After(15 * time.Second):
		t.Fatal("A side never opened")
	}

	for i := 0; i < 5; i++ {
		if err := a.Send([]byte("msg-" + string(rune('0'+i)))); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	// Wait briefly for all 5 to deliver.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= 5 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only received %d of 5 messages", n)
		case <-time.After(50 * time.Millisecond):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for i, msg := range received {
		want := "msg-" + string(rune('0'+i))
		if string(msg) != want {
			t.Errorf("received[%d] = %q, want %q", i, msg, want)
		}
	}
}

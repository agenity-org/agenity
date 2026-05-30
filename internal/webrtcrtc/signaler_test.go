// internal/webrtcrtc/signaler_test.go — pins #311 (C5).
package webrtcrtc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pion/webrtc/v4"
)

func TestDefaultHTTPSignaler_ExchangeOffer_Roundtrip(t *testing.T) {
	t.Parallel()
	// Peer-side handler: spawn answerer PeerConnection, return its answer.
	srv := httptest.NewServer(HandleOffer(func() (*PeerConnection, error) {
		return NewPeerConnectionForAnswerer(Config{})
	}))
	defer srv.Close()

	// Caller side: create offer, ExchangeOffer to peer.
	caller, err := NewPeerConnection(Config{})
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	defer caller.Close()
	offer, err := caller.CreateOffer()
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}

	s := NewDefaultHTTPSignaler()
	answer, err := s.ExchangeOffer(context.Background(), srv.URL, offer)
	if err != nil {
		t.Fatalf("ExchangeOffer: %v", err)
	}
	if !strings.HasPrefix(answer.SDP, "v=") {
		t.Errorf("answer.SDP malformed: %q", answer.SDP)
	}
}

func TestDefaultHTTPSignaler_ExchangeOffer_SurfacesPeerErrors(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no answer", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	s := NewDefaultHTTPSignaler()
	_, err := s.ExchangeOffer(context.Background(), srv.URL, webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: "v=0"})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("err = %v, want 503 surfaced", err)
	}
}

func TestDefaultHTTPSignaler_SendICECandidate_OK(t *testing.T) {
	t.Parallel()
	got := make(chan webrtc.ICECandidateInit, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c webrtc.ICECandidateInit
		_ = json.NewDecoder(r.Body).Decode(&c)
		got <- c
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	s := NewDefaultHTTPSignaler()
	candidate := webrtc.ICECandidateInit{Candidate: "candidate:test-candidate"}
	if err := s.SendICECandidate(context.Background(), srv.URL, candidate); err != nil {
		t.Fatalf("SendICECandidate: %v", err)
	}
	select {
	case rec := <-got:
		if rec.Candidate != candidate.Candidate {
			t.Errorf("received candidate = %q, want %q", rec.Candidate, candidate.Candidate)
		}
	default:
		t.Error("server never received candidate")
	}
}

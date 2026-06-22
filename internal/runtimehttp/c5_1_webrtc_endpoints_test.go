// internal/runtimehttp/c5_1_webrtc_endpoints_test.go — pins #311 C5.1.
// Verifies /webrtc/offer + /webrtc/ice routes are registered on the
// Server's mux and reach the expected handlers.
//
// Refs #311 C5.1.
package runtimehttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agenity-org/agenity/internal/webrtcrtc"
	"github.com/pion/webrtc/v4"
)

func TestWebRTC_OfferEndpoint_RegisteredAndReturnsAnswer(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()

	// Build a real SDP offer from a caller-side PeerConnection.
	caller, err := webrtcrtc.NewPeerConnection(webrtcrtc.Config{})
	if err != nil {
		t.Fatalf("caller NewPeerConnection: %v", err)
	}
	defer caller.Close()
	offer, err := caller.CreateOffer()
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	body, _ := json.Marshal(offer)
	resp, err := http.Post(srv.URL+"/webrtc/offer", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /webrtc/offer: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var answer webrtc.SessionDescription
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if !strings.HasPrefix(answer.SDP, "v=") {
		t.Errorf("answer.SDP doesn't look like SDP: %q", answer.SDP[:80])
	}
}

func TestWebRTC_ICEEndpoint_RegisteredAndAccepts(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()
	candidate, _ := json.Marshal(webrtc.ICECandidateInit{Candidate: "candidate:test"})
	resp, err := http.Post(srv.URL+"/webrtc/ice", "application/json", bytes.NewReader(candidate))
	if err != nil {
		t.Fatalf("POST /webrtc/ice: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestWebRTC_ICEEndpoint_RejectsNonPOST(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/webrtc/ice")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestWebRTC_ICEEndpoint_RejectsEmptyBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/webrtc/ice", "application/json", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// internal/webrtcrtc/signaler.go — #311 (C5) HTTP signaling relay.
// Out-of-band SDP + ICE exchange while WebRTC plumbing is bringing
// up the DTLS handshake. Once the DataChannel opens, A2A traffic
// flows P2P over DTLS bypassing this relay entirely.
package webrtcrtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

// Signaler is the seam between PeerConnection and chepherd's HTTP
// relay endpoints. Implementations:
//
//   - DefaultHTTPSignaler — POSTs to peer's /webrtc/{offer,answer,ice}
//     endpoints (production substrate)
//   - in-memory test signalers — pass SDP/ICE directly between two
//     PeerConnection instances (see peerconnection_test.go's
//     connectPair)
//
// Refs #311 (C5).
type Signaler interface {
	ExchangeOffer(ctx context.Context, peerURL string, offer webrtc.SessionDescription) (webrtc.SessionDescription, error)
	SendICECandidate(ctx context.Context, peerURL string, candidate webrtc.ICECandidateInit) error
}

// DefaultHTTPSignaler relays SDP + ICE over HTTP. Each call POSTs to
// peerURL + the appropriate suffix (/webrtc/offer, /webrtc/ice).
type DefaultHTTPSignaler struct {
	HTTPClient *http.Client
}

// NewDefaultHTTPSignaler constructs a Signaler with a 10s-timeout
// http.Client.
func NewDefaultHTTPSignaler() *DefaultHTTPSignaler {
	return &DefaultHTTPSignaler{HTTPClient: &http.Client{Timeout: 10 * time.Second}}
}

// ExchangeOffer POSTs the SDP offer to peerURL + "/webrtc/offer".
// Expects the peer's SDP answer as the response body.
func (s *DefaultHTTPSignaler) ExchangeOffer(ctx context.Context, peerURL string, offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	body, err := json.Marshal(offer)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("marshal offer: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(peerURL, "/")+"/webrtc/offer", bytes.NewReader(body))
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	hc := s.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("POST offer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return webrtc.SessionDescription{}, fmt.Errorf("peer HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var answer webrtc.SessionDescription
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("decode answer: %w", err)
	}
	return answer, nil
}

// SendICECandidate trickles a single ICE candidate to the peer via
// POST peerURL + "/webrtc/ice". Best-effort — caller doesn't await
// the candidate's full negotiation outcome.
func (s *DefaultHTTPSignaler) SendICECandidate(ctx context.Context, peerURL string, candidate webrtc.ICECandidateInit) error {
	body, err := json.Marshal(candidate)
	if err != nil {
		return fmt.Errorf("marshal candidate: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(peerURL, "/")+"/webrtc/ice", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	hc := s.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("POST ice: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("peer HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// HandleOffer is the chepherd-side HTTP handler factory for the
// /webrtc/offer endpoint. Constructs an answerer PeerConnection per
// request, sets the remote offer, and returns the generated answer.
// The PeerConnection persists in pc storage keyed by an opaque ID
// returned in the answer body's `sessionId` so subsequent
// /webrtc/ice POSTs find it.
//
// Production deployments wire this onto their HTTP mux; chepherd's
// runtimehttp/server.go wires it under /webrtc/* by default.
//
// Refs #311 (C5).
func HandleOffer(pcMaker func() (*PeerConnection, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var offer webrtc.SessionDescription
		if err := json.NewDecoder(r.Body).Decode(&offer); err != nil {
			http.Error(w, "decode offer: "+err.Error(), http.StatusBadRequest)
			return
		}
		pc, err := pcMaker()
		if err != nil {
			http.Error(w, "make peer connection: "+err.Error(), http.StatusInternalServerError)
			return
		}
		answer, err := pc.SetRemoteOffer(offer)
		if err != nil {
			http.Error(w, "set remote offer: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	})
}

// ErrSignalerNotConnected is returned by Send/Receive helpers when
// the underlying PeerConnection isn't yet attached.
var ErrSignalerNotConnected = errors.New("webrtc: signaler not connected to peer")

// Package signaling implements the REST signaling client that
// internal/daemon/rc/transport.WebRTCFactory uses to exchange SDP
// offers/answers + ICE candidates with peers.
//
// All endpoints are POST/GET against the chepherd-relay (default:
// https://relay.chepherd.org/v1/signaling/*) — see docs/PROTOCOL.md §1
// WebRTC mode diagram.
//
// Crucially the relay sees ONLY:
//   · the bearer auth token (identity)
//   · the SDP blobs (NOT the resulting DTLS keys — those are derived
//     fresh per-channel inside each peer's DTLS stack)
//   · ICE candidates (NAT-mapped addresses)
//
// The relay NEVER sees application-layer envelope contents. That is the
// privacy contract enforced by this package's interface boundary.
package signaling

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/agenity-org/agenity/internal/daemon/rc/transport"
)

// HTTPClient is the REST signaling implementation. Implements
// transport.SignalingClient.
type HTTPClient struct {
	BaseURL    string // e.g. "https://relay.chepherd.org/v1/signaling"
	Token      string // OAuth2 bearer
	BastionID  string // own bastion identity — used in Listen-side polling
	HTTP       *http.Client

	// PollInterval — how often WaitForOffer polls when long-poll isn't supported.
	PollInterval time.Duration
}

// New constructs an HTTPClient with sensible defaults.
func New(baseURL, token, bastionID string) *HTTPClient {
	return &HTTPClient{
		BaseURL:      baseURL,
		Token:        token,
		BastionID:    bastionID,
		HTTP:         &http.Client{Timeout: 30 * time.Second},
		PollInterval: 5 * time.Second,
	}
}

// Compile-time interface check.
var _ transport.SignalingClient = (*HTTPClient)(nil)

// ─── client-initiated (Dial) ────────────────────────────────────────────

// PostOffer sends the client's SDP offer + collected ICE candidates to the
// relay, blocks for the peer's SDP answer + candidates.
//
// Endpoint: POST /v1/signaling/initiate
// Body:     { peer: bastionID, sdp: {...}, ice: [...] }
// Response: { sdp: {...answer...}, ice: [...] }
func (c *HTTPClient) PostOffer(ctx context.Context, peerID string, offer webrtc.SessionDescription) (*transport.OfferAnswer, error) {
	body := initiateBody{Peer: peerID, SDP: offer}
	respBody, err := c.postJSON(ctx, "/initiate", body)
	if err != nil {
		return nil, err
	}
	var resp answerBody
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("signaling: decode answer: %w", err)
	}
	return &transport.OfferAnswer{
		Answer:        resp.SDP,
		IceCandidates: resp.ICE,
	}, nil
}

// ─── server-initiated (Listen) ──────────────────────────────────────────

// WaitForOffer polls the relay for a client trying to reach this bastion.
//
// Endpoint: GET /v1/signaling/poll?bastion=<id>
// Response (200): { peer: clientID, sdp: {...offer...}, ice: [...] }
// Response (204): no offer pending — poll again
func (c *HTTPClient) WaitForOffer(ctx context.Context) (*transport.IncomingOffer, error) {
	if c.BastionID == "" {
		return nil, fmt.Errorf("signaling: BastionID required for WaitForOffer")
	}
	poll := time.NewTicker(c.PollInterval)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-poll.C:
		}

		body, status, err := c.getRaw(ctx, "/poll?bastion="+url.QueryEscape(c.BastionID))
		if err != nil {
			continue // transient — keep polling
		}
		if status == http.StatusNoContent {
			continue
		}
		if status != http.StatusOK {
			continue
		}
		var inc incomingOfferBody
		if err := json.Unmarshal(body, &inc); err != nil {
			continue
		}
		return &transport.IncomingOffer{
			PeerID:        inc.Peer,
			Offer:         inc.SDP,
			IceCandidates: inc.ICE,
		}, nil
	}
}

// PostAnswer sends our SDP answer back to the relay for the named peer.
//
// Endpoint: POST /v1/signaling/answer
// Body:     { peer: clientID, sdp: {...answer...}, ice: [...] }
func (c *HTTPClient) PostAnswer(ctx context.Context, peerID string, answer webrtc.SessionDescription) error {
	body := answerBody{Peer: peerID, SDP: answer}
	_, err := c.postJSON(ctx, "/answer", body)
	return err
}

// ─── trickled ICE (new relay endpoints from 48aed53) ────────────────────

// PostCandidate sends ONE trickled ICE candidate to peerID via the
// relay's /v1/signaling/candidate endpoint.
//
// Endpoint: POST /v1/signaling/candidate
// Body:     { bastion_id: peerID, candidate: {...} }
func (c *HTTPClient) PostCandidate(ctx context.Context, peerID string, cand webrtc.ICECandidateInit) error {
	body := candidatePostBody{BastionID: peerID, Candidate: cand}
	_, err := c.postJSON(ctx, "/candidate", body)
	return err
}

// PollCandidates long-polls for trickled candidates addressed to selfID.
//
// Endpoint: GET /v1/signaling/candidates?bastion_id=<selfID>
// Response: { candidates: [...], cursor: "..." }
func (c *HTTPClient) PollCandidates(ctx context.Context, selfID string) ([]webrtc.ICECandidateInit, error) {
	body, status, err := c.getRaw(ctx, "/candidates?bastion_id="+url.QueryEscape(selfID))
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("signaling: /candidates: HTTP %d", status)
	}
	var resp candidatesPollBody
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("signaling: decode candidates: %w", err)
	}
	return resp.Candidates, nil
}

// ─── HTTP plumbing ──────────────────────────────────────────────────────

func (c *HTTPClient) postJSON(ctx context.Context, path string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("signaling: %s: %w", path, err)
	}
	defer resp.Body.Close()
	respBody := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			respBody = append(respBody, buf[:n]...)
		}
		if rerr != nil {
			break
		}
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("signaling: %s: HTTP %d: %s", path, resp.StatusCode, respBody)
	}
	return respBody, nil
}

func (c *HTTPClient) getRaw(ctx context.Context, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if rerr != nil {
			break
		}
	}
	return body, resp.StatusCode, nil
}

// ─── wire shapes ────────────────────────────────────────────────────────

type initiateBody struct {
	Peer string                    `json:"peer"`
	SDP  webrtc.SessionDescription `json:"sdp"`
	ICE  []webrtc.ICECandidateInit `json:"ice,omitempty"`
}

type answerBody struct {
	Peer string                    `json:"peer"`
	SDP  webrtc.SessionDescription `json:"sdp"`
	ICE  []webrtc.ICECandidateInit `json:"ice,omitempty"`
}

type incomingOfferBody struct {
	Peer string                    `json:"peer"`
	SDP  webrtc.SessionDescription `json:"sdp"`
	ICE  []webrtc.ICECandidateInit `json:"ice,omitempty"`
}

type candidatePostBody struct {
	BastionID string                  `json:"bastion_id"`
	Candidate webrtc.ICECandidateInit `json:"candidate"`
}

type candidatesPollBody struct {
	Candidates []webrtc.ICECandidateInit `json:"candidates"`
	Cursor     string                    `json:"cursor"`
}

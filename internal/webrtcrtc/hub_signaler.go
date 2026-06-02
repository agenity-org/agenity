// internal/webrtcrtc/hub_signaler.go — #672 hub-relayed WebRTC A2A
// (epic). HubSignaler implements the Signaler seam against a central
// chepherd-hub's body-blind signaling queue instead of POSTing SDP
// directly to the peer's /webrtc/* endpoints.
//
// The whole point of #672: two chepherd daemons that expose NOTHING to
// the internet can still exchange A2A messages. Neither daemon can POST
// to the other — so the DefaultHTTPSignaler's "POST offer to peerURL/
// webrtc/offer" model breaks. HubSignaler inverts the data flow: every
// frame (offer/answer/ice) is an OUTBOUND request to the hub, and the
// answer is retrieved by an OUTBOUND poll of the hub's pending queue.
//
// Hub contract (cmd/chepherd-hub/signaling.go):
//
//	POST <hub>/v1/signaling/offer   {fromOrgId,toOrgId,sessionId,payload}
//	POST <hub>/v1/signaling/answer  (same shape)
//	POST <hub>/v1/signaling/ice     (payload = webrtc.ICECandidateInit)
//	GET  <hub>/v1/signaling/pending?orgId=<me>  → {org,frames:[...],count}
//
// payload is a JSON-marshalled webrtc.SessionDescription (offer/answer)
// or webrtc.ICECandidateInit (ice). Identity in dev/no-mTLS mode is the
// X-Chepherd-Org header on every request.
//
// peerURL encoding: callers (PCStore) pass the target as `hub://<org>`.
// HubSignaler parses out the target orgID; any non-hub:// URL is
// rejected (this signaler only handles hub peers — the direct-HTTP path
// uses DefaultHTTPSignaler).
//
// Non-trickle: pair HubSignaler with PCStore.GatherBeforeOffer=true so
// the offer SDP carries all ICE candidates inline. The hub's
// /v1/signaling/ice receiver is scaffold, so SendICECandidate is
// best-effort and the connection MUST be able to complete from the
// bundled offer+answer alone.
//
// Refs #672.
package webrtcrtc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
)

// HubPeerScheme is the URL scheme that marks a peerURL as hub-relayed.
// PCStore keys hub PeerConnections by `hub://<orgId>`; HubSignaler
// parses the orgId back out of that form.
const HubPeerScheme = "hub://"

// hubPollInterval is how often ExchangeOffer polls the hub's pending
// queue for the matching answer frame. Short enough that the offer→
// answer round-trip stays sub-second on a local hub, long enough not to
// hammer the hub. Pinned, not slept-on, in tests (they drive a fast
// in-process hub so the first or second poll already has the answer).
const hubPollInterval = 300 * time.Millisecond

// HubSignaler relays SDP + ICE through a central chepherd-hub. Implements
// Signaler. Construct via NewHubSignaler.
type HubSignaler struct {
	hubURL  string // base hub URL, e.g. https://hub.example.com (no trailing /v1/...)
	myOrgID string // this daemon's org identity (X-Chepherd-Org + fromOrgId)
	http    *http.Client

	// PollInterval overrides hubPollInterval. Zero uses the default.
	// Tests set a small value to keep the round-trip tight.
	PollInterval time.Duration
}

// NewHubSignaler constructs a HubSignaler. httpClient may be nil (a
// 30s-timeout client is used — the per-call ctx deadline is the real
// bound on ExchangeOffer's poll loop).
func NewHubSignaler(hubURL, myOrgID string, httpClient *http.Client) *HubSignaler {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &HubSignaler{
		hubURL:  strings.TrimRight(hubURL, "/"),
		myOrgID: myOrgID,
		http:    httpClient,
	}
}

// compile-time assertion that HubSignaler satisfies Signaler.
var _ Signaler = (*HubSignaler)(nil)

// SessionDescription is a convenience alias so the federation package's
// HubAnswerer can name the SDP type via webrtcrtc without a second pion
// import. Same underlying type as webrtc.SessionDescription.
type SessionDescription = webrtc.SessionDescription

// HubFrame mirrors the hub's signalingRequest (POST) + SignalingFrame
// (pending response) wire shapes. One struct serves both directions.
// Exported so the federation HubAnswerer shares the exact wire shape.
type HubFrame struct {
	Kind      string          `json:"kind,omitempty"`
	FromOrgID string          `json:"fromOrgId"`
	ToOrgID   string          `json:"toOrgId"`
	SessionID string          `json:"sessionId"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt,omitempty"`
}

// pendingResponse is the GET /v1/signaling/pending envelope.
type pendingResponse struct {
	Org    string     `json:"org"`
	Frames []HubFrame `json:"frames"`
	Count  int        `json:"count"`
}

// parseHubPeer extracts the target orgID from a `hub://<org>` peerURL.
// Returns a clear error for any other form so a mis-routed direct-HTTP
// peerURL fails loudly instead of silently dialing the wrong transport.
func parseHubPeer(peerURL string) (string, error) {
	if !strings.HasPrefix(peerURL, HubPeerScheme) {
		return "", fmt.Errorf("webrtcrtc.HubSignaler: peerURL %q is not a hub peer (want %s<orgId>)", peerURL, HubPeerScheme)
	}
	org := strings.TrimPrefix(peerURL, HubPeerScheme)
	org = strings.Trim(org, "/")
	if org == "" {
		return "", fmt.Errorf("webrtcrtc.HubSignaler: peerURL %q has empty orgId", peerURL)
	}
	return org, nil
}

// ExchangeOffer POSTs the offer to the hub addressed to the target org,
// then polls the hub's pending queue until the matching answer arrives
// (correlated by sessionId) or ctx expires. Returns the peer's answer.
func (s *HubSignaler) ExchangeOffer(ctx context.Context, peerURL string, offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	target, err := parseHubPeer(peerURL)
	if err != nil {
		return webrtc.SessionDescription{}, err
	}
	sessionID := uuid.NewString()
	payload, err := json.Marshal(offer)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("HubSignaler: marshal offer: %w", err)
	}
	if err := s.postFrame(ctx, "offer", HubFrame{
		FromOrgID: s.myOrgID,
		ToOrgID:   target,
		SessionID: sessionID,
		Payload:   payload,
	}); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("HubSignaler: post offer: %w", err)
	}

	interval := s.PollInterval
	if interval <= 0 {
		interval = hubPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// First poll immediately — on a fast in-process hub the answerer may
	// already have responded by the time we'd wait the first interval.
	for {
		frames, perr := s.pending(ctx)
		if perr == nil {
			for _, f := range frames {
				// Only answers come back to the offerer; tolerate stray
				// ice frames by ignoring any non-answer / mismatched id.
				if f.Kind == "answer" && f.SessionID == sessionID {
					var ans webrtc.SessionDescription
					if uerr := json.Unmarshal(f.Payload, &ans); uerr != nil {
						return webrtc.SessionDescription{}, fmt.Errorf("HubSignaler: unmarshal answer: %w", uerr)
					}
					return ans, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return webrtc.SessionDescription{}, fmt.Errorf("HubSignaler.ExchangeOffer: no answer for session %s: %w", sessionID, ctx.Err())
		case <-ticker.C:
		}
	}
}

// SendICECandidate POSTs a single ICE candidate to the hub addressed to
// the target org. Best-effort — the #672 hub's /v1/signaling/ice
// receiver is scaffold, and the non-trickle bundled-offer path doesn't
// depend on these frames for correctness. Errors are returned so callers
// can log, but the connection still completes without them.
func (s *HubSignaler) SendICECandidate(ctx context.Context, peerURL string, candidate webrtc.ICECandidateInit) error {
	target, err := parseHubPeer(peerURL)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(candidate)
	if err != nil {
		return fmt.Errorf("HubSignaler: marshal candidate: %w", err)
	}
	return s.postFrame(ctx, "ice", HubFrame{
		FromOrgID: s.myOrgID,
		ToOrgID:   target,
		SessionID: "ice-" + uuid.NewString(),
		Payload:   payload,
	})
}

// postFrame POSTs a frame to <hub>/v1/signaling/<kind>.
func (s *HubSignaler) postFrame(ctx context.Context, kind string, f HubFrame) error {
	return HubPostFrame(ctx, s.http, s.hubURL, s.myOrgID, kind, f)
}

// pending GETs + drains this org's pending frames from the hub.
func (s *HubSignaler) pending(ctx context.Context) ([]HubFrame, error) {
	return HubPending(ctx, s.http, s.hubURL, s.myOrgID)
}

// HubPostFrame POSTs a frame to <hub>/v1/signaling/<kind> with the
// X-Chepherd-Org identity header. The hub returns 202 Accepted on
// success. Exported so the federation HubAnswerer posts answer frames
// through the exact same code path the offerer's HubSignaler uses.
func HubPostFrame(ctx context.Context, hc *http.Client, hubURL, orgID, kind string, f HubFrame) error {
	body, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}
	url := strings.TrimRight(hubURL, "/") + "/v1/signaling/" + kind
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Chepherd-Org", orgID)
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("hub HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// HubPending is the shared GET /v1/signaling/pending implementation
// reused by both HubSignaler (offerer) and the federation HubAnswerer
// (answerer) so the two sides drain the same mailbox the same way.
func HubPending(ctx context.Context, hc *http.Client, hubURL, orgID string) ([]HubFrame, error) {
	url := strings.TrimRight(hubURL, "/") + "/v1/signaling/pending?orgId=" + orgID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Chepherd-Org", orgID)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("hub HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var pr pendingResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("decode pending: %w", err)
	}
	return pr.Frames, nil
}

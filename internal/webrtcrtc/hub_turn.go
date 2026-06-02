// internal/webrtcrtc/hub_turn.go — #672 hub-relayed WebRTC A2A (epic).
// FetchHubTURN retrieves short-lived TURN credentials from the central
// chepherd-hub + builds the webrtc.ICEServer entry so both the offerer
// (HubSignaler-backed PCStore) and the answerer (HubAnswerer) can relay
// media through the hub's TURN server when both peers are behind
// symmetric NAT.
//
// Hub contract (cmd/chepherd-hub/turn.go handleTURNCredentials):
//
//	GET <hub>/v1/turn/credentials  (X-Chepherd-Org header)
//	  → {username, password, ttl, uris:[...], realm}
//
// The returned TURN ICEServer is appended to the STUN defaults so ICE
// gathering produces host + srflx + relay candidates, all bundled into
// the offer/answer SDP (non-trickle).
//
// Refs #672 #496.
package webrtcrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

// HubTURNCredentials is the wire shape the hub's /v1/turn/credentials
// returns (mirrors cmd/chepherd-hub/turn.go turnCredentialsResponse).
type HubTURNCredentials struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	TTL      int      `json:"ttl"` // seconds
	URIs     []string `json:"uris"`
	Realm    string   `json:"realm"`
}

// ICEServer converts the fetched creds into a webrtc.ICEServer. Returns
// the zero value (no URIs) when the hub has no TURN configured.
func (c HubTURNCredentials) ICEServer() webrtc.ICEServer {
	return webrtc.ICEServer{
		URLs:       c.URIs,
		Username:   c.Username,
		Credential: c.Password,
	}
}

// Expiry returns when these creds expire, given the fetch time. Callers
// refresh before this to avoid a window where allocate fails.
func (c HubTURNCredentials) Expiry(fetchedAt time.Time) time.Time {
	if c.TTL <= 0 {
		return fetchedAt.Add(10 * time.Minute) // hub default turnCredTTL
	}
	return fetchedAt.Add(time.Duration(c.TTL) * time.Second)
}

// FetchHubTURN GETs short-lived TURN creds from the hub. Returns the
// creds + nil error on success. A 503 (TURN not configured on the hub)
// returns (zero, nil) so callers fall through to STUN-only gracefully.
func FetchHubTURN(ctx context.Context, hc *http.Client, hubURL, orgID string) (HubTURNCredentials, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	url := strings.TrimRight(hubURL, "/") + "/v1/turn/credentials"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return HubTURNCredentials{}, err
	}
	req.Header.Set("X-Chepherd-Org", orgID)
	resp, err := hc.Do(req)
	if err != nil {
		return HubTURNCredentials{}, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusServiceUnavailable {
		// Hub has no TURN — STUN-only is still a valid config.
		return HubTURNCredentials{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return HubTURNCredentials{}, fmt.Errorf("hub TURN HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var creds HubTURNCredentials
	if err := json.NewDecoder(resp.Body).Decode(&creds); err != nil {
		return HubTURNCredentials{}, fmt.Errorf("decode TURN creds: %w", err)
	}
	return creds, nil
}

// MergeTURN returns a copy of cfg with the TURN ICEServer appended to
// its ICEServers. STUN defaults are preserved (when cfg.ICEServers was
// empty, DefaultICEServers() is materialized first so the TURN entry
// doesn't suppress the STUN fallback inside NewPeerConnection). A creds
// value with no URIs (TURN disabled) returns cfg unchanged.
func MergeTURN(cfg Config, creds HubTURNCredentials) Config {
	if len(creds.URIs) == 0 {
		return cfg
	}
	servers := cfg.ICEServers
	if len(servers) == 0 {
		servers = DefaultICEServers()
	}
	merged := make([]webrtc.ICEServer, 0, len(servers)+1)
	merged = append(merged, servers...)
	merged = append(merged, creds.ICEServer())
	out := cfg
	out.ICEServers = merged
	return out
}

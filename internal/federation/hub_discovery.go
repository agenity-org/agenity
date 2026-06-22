// internal/federation/hub_discovery.go — #672 hub-relayed WebRTC A2A
// (epic). HubDiscovery is the directory client: it announces this
// daemon's agent card to the central chepherd-hub (heartbeat every 60s)
// and polls the hub's peer directory, upserting each remote org into the
// AgentCardRepository (so HubDeliverer can resolve them) AND into the
// runtime PeerRegistry surface (so chepherd.list / list_peers shows them
// with external=true).
//
// Hub directory contract (sibling agent adds these to chepherd-hub):
//
//	POST <hub>/v1/registry/announce  {orgId, card}   (card = agent-card JSON)
//	GET  <hub>/v1/registry/peers     → {peers:[{orgId, card, lastSeen}]}
//
// Both carry the X-Chepherd-Org identity header. The peers response
// excludes self by orgId; HubDiscovery defensively skips self too.
//
// Card persistence: each discovered peer's card is stored under SID =
// its orgId, with the card's `url` REWRITTEN to `hub://<orgId>` so the
// HubDeliverer's selection logic recognizes it as a hub-only peer
// regardless of what (possibly unreachable) url the peer self-advertised.
// The peer's real card body is preserved in the AgentCard.Body for the
// dashboard; only the routing url surfaced to the deliverer is the
// hub:// form.
//
// Refs #672.
package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/agenity-org/agenity/internal/persistence"
	"github.com/agenity-org/agenity/internal/runtime"
	"github.com/agenity-org/agenity/internal/webrtcrtc"
)

// hubAnnounceInterval is the heartbeat cadence for re-announcing self to
// the hub directory. 60s per the #672 contract — a peer missing ~2-3
// heartbeats falls out of other daemons' directories.
const hubAnnounceInterval = 60 * time.Second

// hubPeersPollInterval is how often we refresh the peer directory. More
// frequent than the announce heartbeat so a newly-joined peer becomes
// dialable within ~15s.
const hubPeersPollInterval = 15 * time.Second

// HubDiscovery announces self + discovers peers via the hub directory.
// Construct via NewHubDiscovery; drive with Start(ctx).
type HubDiscovery struct {
	hubURL  string
	myOrgID string
	// selfCard is this daemon's agent-card JSON (the body served at
	// /.well-known/agent-card.json). Announced verbatim to the hub.
	selfCard json.RawMessage
	// team is the team external peers are registered under in the
	// PeerRegistry (so they appear in the operator's team views).
	team  string
	cards persistence.AgentCardRepository
	peers *runtime.PeerRegistry
	http  *http.Client

	// AnnounceInterval / PeersPollInterval override the defaults. Zero
	// uses the constants. Tests set small values.
	AnnounceInterval  time.Duration
	PeersPollInterval time.Duration
}

// NewHubDiscovery constructs the discovery loop. selfCard is the daemon's
// agent-card JSON; cards + peers are the upsert sinks. Either sink may be
// nil (the loop skips that upsert). httpClient may be nil (30s client).
func NewHubDiscovery(hubURL, myOrgID, team string, selfCard json.RawMessage, cards persistence.AgentCardRepository, peers *runtime.PeerRegistry, httpClient *http.Client) *HubDiscovery {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &HubDiscovery{
		hubURL:   strings.TrimRight(hubURL, "/"),
		myOrgID:  myOrgID,
		selfCard: selfCard,
		team:     team,
		cards:    cards,
		peers:    peers,
		http:     httpClient,
	}
}

// Start runs the announce heartbeat + peer poll until ctx is canceled.
// Blocking — callers typically `go discovery.Start(ctx)`. Announces +
// polls once immediately so the daemon is discoverable + sees peers
// without waiting a full interval.
func (h *HubDiscovery) Start(ctx context.Context) {
	annInterval := h.AnnounceInterval
	if annInterval <= 0 {
		annInterval = hubAnnounceInterval
	}
	pollInterval := h.PeersPollInterval
	if pollInterval <= 0 {
		pollInterval = hubPeersPollInterval
	}
	annTicker := time.NewTicker(annInterval)
	defer annTicker.Stop()
	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()

	h.announce(ctx)
	h.pollPeers(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-annTicker.C:
			h.announce(ctx)
		case <-pollTicker.C:
			h.pollPeers(ctx)
		}
	}
}

// announce POSTs this daemon's card to the hub directory (heartbeat).
func (h *HubDiscovery) announce(ctx context.Context) {
	body, err := json.Marshal(map[string]any{
		"orgId": h.myOrgID,
		"card":  h.selfCard,
	})
	if err != nil {
		fmt.Fprintf(stderrPrintf, "[hub-discovery] announce marshal: %v\n", err)
		return
	}
	url := h.hubURL + "/v1/registry/announce"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderrPrintf, "[hub-discovery] announce build: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Chepherd-Org", h.myOrgID)
	resp, err := h.http.Do(req)
	if err != nil {
		fmt.Fprintf(stderrPrintf, "[hub-discovery] announce: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(stderrPrintf, "[hub-discovery] announce HTTP %d (hub=%s)\n", resp.StatusCode, h.hubURL)
	}
}

// hubPeer is one entry in the GET /v1/registry/peers response.
type hubPeer struct {
	OrgID    string          `json:"orgId"`
	Card     json.RawMessage `json:"card"`
	LastSeen time.Time       `json:"lastSeen"`
}

type hubPeersResponse struct {
	Peers []hubPeer `json:"peers"`
}

// pollPeers fetches the hub directory + upserts each remote org into the
// AgentCard cache + the PeerRegistry.
func (h *HubDiscovery) pollPeers(ctx context.Context) {
	url := h.hubURL + "/v1/registry/peers"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(stderrPrintf, "[hub-discovery] peers build: %v\n", err)
		return
	}
	req.Header.Set("X-Chepherd-Org", h.myOrgID)
	resp, err := h.http.Do(req)
	if err != nil {
		fmt.Fprintf(stderrPrintf, "[hub-discovery] peers: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(stderrPrintf, "[hub-discovery] peers HTTP %d (hub=%s)\n", resp.StatusCode, h.hubURL)
		return
	}
	var pr hubPeersResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		fmt.Fprintf(stderrPrintf, "[hub-discovery] peers decode: %v\n", err)
		return
	}
	for _, p := range pr.Peers {
		if p.OrgID == "" || p.OrgID == h.myOrgID {
			continue // skip self / malformed
		}
		h.upsertPeer(ctx, p)
	}
}

// upsertPeer persists one discovered peer into the AgentCard cache (with
// a hub:// routing url) + registers it in the PeerRegistry as external.
func (h *HubDiscovery) upsertPeer(ctx context.Context, p hubPeer) {
	hubURL := webrtcrtc.HubPeerScheme + p.OrgID

	if h.cards != nil {
		// Rewrite the card's `url` to the hub:// form so HubDeliverer
		// recognizes it as a hub-only peer; preserve every other field
		// of the peer's self-advertised card for the dashboard.
		body := rewriteCardURL(p.Card, hubURL)
		name := cardName(body, p.OrgID)
		card := &persistence.AgentCard{
			SID:      p.OrgID,
			Name:     name,
			Body:     body,
			SyncedAt: time.Now().UTC(),
		}
		if err := h.cards.Save(ctx, card); err != nil {
			fmt.Fprintf(stderrPrintf, "[hub-discovery] persist card %s: %v\n", p.OrgID, err)
		}
	}

	if h.peers != nil {
		// External peers surface in chepherd.list / list_peers with
		// external=true. AgentCardURL carries the hub:// origin marker so
		// the operator sees the peer is reached via the hub, not HTTP.
		h.peers.Register(p.OrgID, h.team, hubURL, hubURL)
	}
}

// rewriteCardURL returns card with its top-level `url` field set to
// newURL. On any parse error it returns a minimal card carrying just the
// url so the deliverer can still route (the dashboard loses richness but
// routing is preserved).
func rewriteCardURL(card json.RawMessage, newURL string) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(card, &m); err != nil || m == nil {
		b, _ := json.Marshal(map[string]any{"url": newURL})
		return b
	}
	m["url"] = newURL
	b, err := json.Marshal(m)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"url": newURL})
	}
	return b
}

// cardName extracts the `name` field from a card body, falling back to
// the orgId when absent.
func cardName(card json.RawMessage, fallback string) string {
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(card, &m); err == nil && m.Name != "" {
		return m.Name
	}
	return fallback
}

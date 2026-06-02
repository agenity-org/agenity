// Package runtime — PeerRegistry tracks external A2A peers that have
// registered themselves as team members via POST /api/v1/peers/register
// (#669). External peers don't have a chepherd-managed session.Session +
// container; they live in another process / host / federation peer +
// expose their own /jsonrpc endpoint to receive A2A messages.
//
// Without this registry, the Team Transcript fan-out (teamMembersOf in
// internal/runtimehttp/team_transcript.go) only enumerates Runtime.List()
// chepherd-managed sessions; @everyone broadcasts silently skip the
// external peer, breaking federation parity.
//
// Lifecycle:
//
//   - POST /api/v1/peers/register     → Register(name, team, agentCardURL)
//   - POST /api/v1/peers/{name}/heartbeat → Heartbeat(name) extends TTL
//   - DELETE /api/v1/peers/{name}      → Deregister(name)
//
// Entries with no heartbeat in the last 90 seconds are evicted by Sweep
// (called opportunistically on every read). This matches the operator-
// intuitive "missed 3 heartbeats at 30s cadence → dead peer" rule from
// the #669 DoD.
//
// Thread-safe: all methods take the internal mutex; no external locking
// required by callers.
//
// Refs #669.
package runtime

import (
	"sync"
	"time"
)

// PeerTTL is the inactivity window after which a registered peer is
// considered dead and evicted from the registry. Operator-visible knob;
// matches the DoD "missing 3 polls at 30s cadence → deregister".
const PeerTTL = 90 * time.Second

// PeerInfo is the in-memory record for one registered external A2A peer.
// JSON-tagged for direct serialization from the GET /peers/registered
// endpoint.
type PeerInfo struct {
	Name            string    `json:"name"`              // canonical @-handle (must be unique across registry + chepherd sessions)
	Team            string    `json:"team"`              // team membership (matches Runtime.SessionInfo.Team semantics)
	AgentCardURL    string    `json:"agent_card_url"`    // peer's /.well-known/agent-card.json URL (advertised by the peer)
	JSONRPCURL      string    `json:"jsonrpc_url"`       // peer's /jsonrpc endpoint (derived from agent_card_url at register-time)
	JoinedAt        time.Time `json:"joined_at"`         // wall-clock at Register
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"` // wall-clock at last Register or Heartbeat
}

// PeerRegistry holds the live set of externally-registered A2A peers.
// Use NewPeerRegistry to construct; the zero value is NOT usable
// (sync.Mutex zero-value is fine but the map is nil).
type PeerRegistry struct {
	mu    sync.Mutex
	peers map[string]*PeerInfo // keyed by Name
	// now is the clock source. Production = time.Now; tests override to
	// drive TTL expiry deterministically without sleeps.
	now func() time.Time
}

// NewPeerRegistry constructs an empty registry with the wall-clock as
// the time source.
func NewPeerRegistry() *PeerRegistry {
	return &PeerRegistry{
		peers: make(map[string]*PeerInfo),
		now:   time.Now,
	}
}

// SetClockForTest swaps the time source. Tests use this to drive TTL
// expiry without real sleeps. Production code MUST NOT call this.
func (r *PeerRegistry) SetClockForTest(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
}

// Register inserts or refreshes a peer record. Idempotent: re-registering
// the same name updates AgentCardURL/JSONRPCURL/Team and extends the TTL.
// jsonrpcURL is derived by the caller (typically by replacing the
// agent-card URL's trailing path with /jsonrpc).
func (r *PeerRegistry) Register(name, team, agentCardURL, jsonrpcURL string) *PeerInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	p := &PeerInfo{
		Name:            name,
		Team:            team,
		AgentCardURL:    agentCardURL,
		JSONRPCURL:      jsonrpcURL,
		JoinedAt:        now,
		LastHeartbeatAt: now,
	}
	// Preserve JoinedAt on re-registration so the registry shows the
	// original join time + the latest heartbeat (matches operator
	// expectation: "this peer has been here since X, last seen Y").
	if existing, ok := r.peers[name]; ok {
		p.JoinedAt = existing.JoinedAt
	}
	r.peers[name] = p
	return p
}

// Heartbeat extends the TTL on an existing peer. Returns false when the
// peer isn't registered (caller should respond 404).
func (r *PeerRegistry) Heartbeat(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.peers[name]
	if !ok {
		return false
	}
	p.LastHeartbeatAt = r.now()
	return true
}

// Deregister removes a peer. Returns false when the peer wasn't
// registered (caller should respond 404).
func (r *PeerRegistry) Deregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.peers[name]; !ok {
		return false
	}
	delete(r.peers, name)
	return true
}

// Get returns a copy of the peer's record, or nil + false when not
// registered. Returns a COPY so callers can't mutate the registry's
// internal state by accident.
func (r *PeerRegistry) Get(name string) (PeerInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	p, ok := r.peers[name]
	if !ok {
		return PeerInfo{}, false
	}
	return *p, true
}

// List returns a snapshot of all currently-registered peers (after
// sweeping expired entries). The slice is freshly allocated; callers
// can sort / filter freely.
func (r *PeerRegistry) List() []PeerInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	out := make([]PeerInfo, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, *p)
	}
	return out
}

// ListByTeam returns a snapshot of peers filtered to the given team.
// Convenience wrapper around List used by teamMembersOf to merge
// peer @-handles into the team's member list.
func (r *PeerRegistry) ListByTeam(team string) []PeerInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	out := make([]PeerInfo, 0, len(r.peers))
	for _, p := range r.peers {
		if p.Team == team {
			out = append(out, *p)
		}
	}
	return out
}

// sweepLocked evicts entries whose LastHeartbeatAt is older than PeerTTL.
// Caller MUST hold r.mu. Quiet — no logging; callers shouldn't care which
// reads triggered the sweep.
func (r *PeerRegistry) sweepLocked() {
	cutoff := r.now().Add(-PeerTTL)
	for name, p := range r.peers {
		if p.LastHeartbeatAt.Before(cutoff) {
			delete(r.peers, name)
		}
	}
}

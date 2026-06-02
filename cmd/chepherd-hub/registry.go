// cmd/chepherd-hub/registry.go — #672 peer DIRECTORY (discovery).
// Implements the hub binary's /v1/registry/{announce,peers} endpoints
// with an in-memory presence store keyed by org and a TTL GC sweep,
// mirroring signaling.go's queue structure. The directory lets
// independent chepherd parties ("orgs") find each other from the
// central rendezvous: a daemon POSTs /v1/registry/announce on a 60s
// heartbeat; peers GET /v1/registry/peers to enumerate live orgs.
//
// DESIGN INVARIANTS (consistent with signaling.go):
//
//  1. Authoritative identity = authenticatedOrg(r) (mTLS cert subject
//     OR X-Chepherd-Org header in dev). The body's orgId is a
//     CONVENIENCE only; a non-empty body orgId that doesn't match the
//     authenticated org is rejected 403 (spoofing defense, mirroring
//     signaling.go's fromOrgId check).
//
//  2. Hub is BODY-BLIND about the card. The card payload is stored as
//     an opaque json.RawMessage and round-tripped byte-exact; the hub
//     never decodes its contents.
//
//  3. Records expire after registryTTL (120s) of no announce. The
//     daemon-side heartbeat is 60s, so two missed beats = dead. A GC
//     goroutine sweeps on a ticker; gcOnce is exposed so unit tests
//     drive eviction deterministically (same pattern as the signaling
//     queue's gcOnce).
//
// Refs #672 epic — hub peer-discovery directory.
package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// registryTTL caps how long a peer record survives without a fresh
// announce. 120s = 2× the daemon-side 60s heartbeat, so a peer is
// considered dead only after two consecutive missed beats.
const registryTTL = 120 * time.Second

// registryPeer is one announced presence record. Card is treated as
// opaque bytes per the body-blind invariant — round-tripped exactly.
type registryPeer struct {
	OrgID    string          `json:"orgId"`
	Card     json.RawMessage `json:"card"`
	LastSeen time.Time       `json:"lastSeen"`
}

// registryStore is the in-memory presence directory. Keyed by orgId
// so an announce overwrites the prior record for that org (heartbeat
// semantics). Safe for concurrent use. CloseAll halts the GC
// goroutine. Mirrors signalingQueue's structure.
type registryStore struct {
	mu       sync.Mutex
	peers    map[string]*registryPeer // key: orgId
	stopGC   chan struct{}
	gcTicker *time.Ticker
}

func newRegistryStore() *registryStore {
	rs := &registryStore{
		peers:    map[string]*registryPeer{},
		stopGC:   make(chan struct{}),
		gcTicker: time.NewTicker(registryTTL / 4),
	}
	go rs.runGC()
	return rs
}

// Announce stores/overwrites the record for org and bumps LastSeen to
// now. The card is stored as-is (opaque). Heartbeat = re-announce.
func (rs *registryStore) Announce(org string, card json.RawMessage) {
	rs.mu.Lock()
	rs.peers[org] = &registryPeer{
		OrgID:    org,
		Card:     card,
		LastSeen: time.Now().UTC(),
	}
	rs.mu.Unlock()
}

// ListLive returns every peer whose record is within registryTTL.
// Expired-but-not-yet-GC'd records are filtered here too so a slow GC
// tick never leaks a stale peer to a caller.
func (rs *registryStore) ListLive() []registryPeer {
	cutoff := time.Now().UTC().Add(-registryTTL)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]registryPeer, 0, len(rs.peers))
	for _, p := range rs.peers {
		if p.LastSeen.After(cutoff) {
			out = append(out, *p)
		}
	}
	return out
}

// CloseAll stops the GC goroutine. Called on hub shutdown.
func (rs *registryStore) CloseAll() {
	rs.gcTicker.Stop()
	close(rs.stopGC)
}

// runGC periodically drops records older than registryTTL. Run as a
// goroutine; exits when stopGC closes.
func (rs *registryStore) runGC() {
	for {
		select {
		case <-rs.stopGC:
			return
		case <-rs.gcTicker.C:
			rs.gcOnce()
		}
	}
}

// gcOnce sweeps the store + drops expired records. Exposed for unit
// tests so they can drive the TTL behavior deterministically.
func (rs *registryStore) gcOnce() {
	cutoff := time.Now().UTC().Add(-registryTTL)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for org, p := range rs.peers {
		if !p.LastSeen.After(cutoff) {
			delete(rs.peers, org)
		}
	}
}

// ─── HTTP handlers ────────────────────────────────────────────────

// registryAnnounceRequest is the wire shape for POST
// /v1/registry/announce. orgId is supplied by the caller as a
// CONVENIENCE only — authoritative identity comes from
// authenticatedOrg(r). A non-empty mismatching orgId is rejected 403.
type registryAnnounceRequest struct {
	OrgID string          `json:"orgId"`
	Card  json.RawMessage `json:"card"`
}

// handleRegistryAnnounce implements POST /v1/registry/announce. A
// daemon announces/heartbeats its presence; the hub stores the record
// keyed by the authenticated org and bumps lastSeen.
func (s *server) handleRegistryAnnounce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed,
			map[string]string{"error": "POST only"})
		return
	}
	authOrg := authenticatedOrg(r)
	if authOrg == "" {
		writeJSON(w, http.StatusUnauthorized,
			map[string]string{"error": "no authenticated org identity (mTLS cert subject or X-Chepherd-Org header required)"})
		return
	}
	if !s.orgAllowed(authOrg) {
		writeJSON(w, http.StatusForbidden,
			map[string]string{"error": "org not in --allowed-orgs allowlist", "org": authOrg})
		return
	}
	var req registryAnnounceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "decode body: " + err.Error()})
		return
	}
	// Spoofing defense: authoritative identity comes from the auth
	// layer, not the caller-supplied orgId. Reject mismatch (mirrors
	// signaling.go's fromOrgId check).
	if req.OrgID != "" && req.OrgID != authOrg {
		writeJSON(w, http.StatusForbidden,
			map[string]string{
				"error":       "orgId doesn't match authenticated org identity",
				"auth_org":    authOrg,
				"claimed_org": req.OrgID,
			})
		return
	}
	s.registry.Announce(authOrg, req.Card)
	writeJSON(w, http.StatusOK, map[string]any{
		"announced": true,
		"org":       authOrg,
	})
}

// handleRegistryPeers implements GET /v1/registry/peers. Returns every
// live (TTL-filtered) peer record. The caller's own org is INCLUDED;
// the caller filters itself out client-side. Auth mirrors announce.
func (s *server) handleRegistryPeers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed,
			map[string]string{"error": "GET only"})
		return
	}
	authOrg := authenticatedOrg(r)
	if authOrg == "" {
		writeJSON(w, http.StatusUnauthorized,
			map[string]string{"error": "no authenticated org identity (mTLS cert subject or X-Chepherd-Org header required)"})
		return
	}
	if !s.orgAllowed(authOrg) {
		writeJSON(w, http.StatusForbidden,
			map[string]string{"error": "org not in --allowed-orgs allowlist", "org": authOrg})
		return
	}
	peers := s.registry.ListLive()
	writeJSON(w, http.StatusOK, map[string]any{
		"peers": peers,
		"count": len(peers),
	})
}

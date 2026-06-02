// cmd/chepherd-hub/registry_test.go pins the #672 peer-discovery
// directory contract.
//
// Coverage:
//
//   - Store: announce → ListLive returns it; re-announce overwrites +
//     bumps lastSeen; card bytes round-trip exact (body-blind)
//   - TTL: a backdated record is dropped by gcOnce (deterministic
//     eviction, same pattern as the signaling queue test) and is also
//     filtered by ListLive before GC runs
//   - HTTP: announce → peers returns it; two orgs → both listed
//   - Auth: no X-Chepherd-Org → 401; spoofed body orgId != auth org
//     → 403; non-allowlisted org → 403
//
// Refs #672 epic — hub peer-discovery directory.
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// ─── Store ────────────────────────────────────────────────────────

func TestRegistry_Store_AnnounceListRoundTrip(t *testing.T) {
	t.Parallel()
	rs := newRegistryStore()
	defer rs.CloseAll()
	card := json.RawMessage(`{"name":"alice-daemon","url":"https://alice.example/a2a"}`)
	rs.Announce("alice.example", card)
	live := rs.ListLive()
	if len(live) != 1 {
		t.Fatalf("got %d live peers, want 1", len(live))
	}
	if live[0].OrgID != "alice.example" {
		t.Errorf("orgId = %q, want alice.example", live[0].OrgID)
	}
	// Body-blind: card bytes are EXACT round-trip.
	if !bytes.Equal(live[0].Card, card) {
		t.Errorf("card mutated:\n got: %s\nwant: %s", live[0].Card, card)
	}
	if live[0].LastSeen.IsZero() {
		t.Error("lastSeen not set")
	}
}

func TestRegistry_Store_ReAnnounceOverwritesAndBumps(t *testing.T) {
	t.Parallel()
	rs := newRegistryStore()
	defer rs.CloseAll()
	rs.Announce("a.example", json.RawMessage(`{"v":1}`))
	first := rs.ListLive()[0].LastSeen
	// Backdate so the bump is observable.
	rs.mu.Lock()
	rs.peers["a.example"].LastSeen = time.Now().UTC().Add(-30 * time.Second)
	rs.mu.Unlock()
	rs.Announce("a.example", json.RawMessage(`{"v":2}`))
	live := rs.ListLive()
	if len(live) != 1 {
		t.Fatalf("got %d live peers, want 1 (overwrite, not append)", len(live))
	}
	if !bytes.Equal(live[0].Card, json.RawMessage(`{"v":2}`)) {
		t.Errorf("card = %s, want {\"v\":2}", live[0].Card)
	}
	if !live[0].LastSeen.After(first.Add(-time.Second)) {
		t.Errorf("lastSeen not bumped: %v", live[0].LastSeen)
	}
}

func TestRegistry_Store_GCDropsExpired(t *testing.T) {
	t.Parallel()
	rs := newRegistryStore()
	defer rs.CloseAll()
	rs.Announce("stale.example", json.RawMessage(`{}`))
	rs.Announce("fresh.example", json.RawMessage(`{}`))
	// Backdate one record past TTL.
	rs.mu.Lock()
	rs.peers["stale.example"].LastSeen = time.Now().UTC().Add(-registryTTL - time.Minute)
	rs.mu.Unlock()
	// ListLive filters the stale one even before GC runs.
	if live := rs.ListLive(); len(live) != 1 || live[0].OrgID != "fresh.example" {
		t.Errorf("ListLive = %+v, want only fresh.example", live)
	}
	// gcOnce physically evicts it.
	rs.gcOnce()
	rs.mu.Lock()
	_, stillThere := rs.peers["stale.example"]
	_, freshThere := rs.peers["fresh.example"]
	rs.mu.Unlock()
	if stillThere {
		t.Error("GC failed to evict stale peer")
	}
	if !freshThere {
		t.Error("GC wrongly evicted fresh peer")
	}
}

// ─── HTTP handlers ────────────────────────────────────────────────

// postRegistry posts a JSON body to a registry path with the given
// X-Chepherd-Org auth header (empty = unauthenticated).
func postRegistry(t *testing.T, baseURL, path, org, body string) *http.Response {
	t.Helper()
	return postSignaling(t, baseURL, path, org, body)
}

func getRegistryPeers(t *testing.T, baseURL, org string) (int, []registryPeer) {
	t.Helper()
	resp := getSignaling(t, baseURL, "/v1/registry/peers", org)
	defer resp.Body.Close()
	var p struct {
		Peers []registryPeer `json:"peers"`
		Count int            `json:"count"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&p)
	return resp.StatusCode, p.Peers
}

func TestRegistry_Announce_ThenPeersReturnsIt(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	resp := postRegistry(t, hub.URL, "/v1/registry/announce", "alice.example",
		`{"orgId":"alice.example","card":{"name":"alice"}}`)
	if resp.StatusCode != http.StatusOK {
		b, _ := readAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("announce = %d, want 200\n%s", resp.StatusCode, b)
	}
	resp.Body.Close()

	code, peers := getRegistryPeers(t, hub.URL, "alice.example")
	if code != http.StatusOK {
		t.Fatalf("peers = %d, want 200", code)
	}
	if len(peers) != 1 || peers[0].OrgID != "alice.example" {
		t.Fatalf("peers = %+v, want [alice.example]", peers)
	}
	if !bytes.Contains(peers[0].Card, []byte(`"name":"alice"`)) {
		t.Errorf("card missing name field: %s", peers[0].Card)
	}
}

func TestRegistry_TwoOrgs_BothListed(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	postRegistry(t, hub.URL, "/v1/registry/announce", "alice.example",
		`{"card":{"name":"alice"}}`).Body.Close()
	postRegistry(t, hub.URL, "/v1/registry/announce", "bob.example",
		`{"card":{"name":"bob"}}`).Body.Close()

	code, peers := getRegistryPeers(t, hub.URL, "alice.example")
	if code != http.StatusOK {
		t.Fatalf("peers = %d, want 200", code)
	}
	if len(peers) != 2 {
		t.Fatalf("got %d peers, want 2: %+v", len(peers), peers)
	}
	seen := map[string]bool{}
	for _, p := range peers {
		seen[p.OrgID] = true
	}
	if !seen["alice.example"] || !seen["bob.example"] {
		t.Errorf("peers = %v, want {alice.example, bob.example}", seen)
	}
}

func TestRegistry_Announce_MissingOrgHeader_401(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	resp := postRegistry(t, hub.URL, "/v1/registry/announce", "",
		`{"card":{}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-org announce = %d, want 401", resp.StatusCode)
	}
}

func TestRegistry_Peers_MissingOrgHeader_401(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	code, _ := getRegistryPeers(t, hub.URL, "")
	if code != http.StatusUnauthorized {
		t.Errorf("no-org peers = %d, want 401", code)
	}
}

func TestRegistry_Announce_SpoofedOrgId_403(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	// Auth'd as alice.example but body claims carol.example.
	resp := postRegistry(t, hub.URL, "/v1/registry/announce", "alice.example",
		`{"orgId":"carol.example","card":{}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("spoofed orgId = %d, want 403", resp.StatusCode)
	}
}

func TestRegistry_Announce_NonAllowlisted_403(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{allowedOrgs: "alice.example,bob.example"})
	resp := postRegistry(t, hub.URL, "/v1/registry/announce", "carol.example",
		`{"card":{}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-allowlisted announce = %d, want 403", resp.StatusCode)
	}
}

func TestRegistry_Peers_TTLEvictsStale(t *testing.T) {
	t.Parallel()
	hub, srv := newHubServer(t, &config{})
	postRegistry(t, hub.URL, "/v1/registry/announce", "stale.example",
		`{"card":{}}`).Body.Close()
	postRegistry(t, hub.URL, "/v1/registry/announce", "fresh.example",
		`{"card":{}}`).Body.Close()
	// Backdate stale.example past TTL, then drive GC deterministically.
	srv.registry.mu.Lock()
	srv.registry.peers["stale.example"].LastSeen = time.Now().UTC().Add(-registryTTL - time.Minute)
	srv.registry.mu.Unlock()
	srv.registry.gcOnce()

	code, peers := getRegistryPeers(t, hub.URL, "fresh.example")
	if code != http.StatusOK {
		t.Fatalf("peers = %d, want 200", code)
	}
	if len(peers) != 1 || peers[0].OrgID != "fresh.example" {
		t.Fatalf("peers = %+v, want only fresh.example after TTL eviction", peers)
	}
}

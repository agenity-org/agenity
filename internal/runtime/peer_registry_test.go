// internal/runtime/peer_registry_test.go — pins #669 PeerRegistry
// behavior: register/heartbeat/deregister + TTL expiry + team filtering.
//
// Refs #669.
package runtime

import (
	"sort"
	"sync"
	"testing"
	"time"
)

// TestPeerRegistry_TTLExpiry verifies that a registered peer is evicted
// after PeerTTL elapses with no heartbeat. The clock is injected so the
// test runs deterministically (no real sleeps).
func TestPeerRegistry_TTLExpiry(t *testing.T) {
	t.Parallel()
	reg := NewPeerRegistry()
	now := time.Unix(1_700_000_000, 0).UTC()
	reg.SetClockForTest(func() time.Time { return now })

	reg.Register("external-peer", "default",
		"http://peer:8080/.well-known/agent-card.json",
		"http://peer:8080/jsonrpc")

	// Fresh peer is visible.
	if got := reg.List(); len(got) != 1 || got[0].Name != "external-peer" {
		t.Fatalf("expected 1 peer after Register, got %+v", got)
	}
	if _, ok := reg.Get("external-peer"); !ok {
		t.Fatalf("Get returned !ok for freshly-registered peer")
	}

	// Advance just under the TTL — peer still visible.
	now = now.Add(PeerTTL - time.Second)
	reg.SetClockForTest(func() time.Time { return now })
	if got := reg.List(); len(got) != 1 {
		t.Fatalf("just-under-TTL: expected 1 peer, got %d", len(got))
	}

	// Advance past TTL — peer should be evicted by the sweep.
	now = now.Add(2 * time.Second)
	reg.SetClockForTest(func() time.Time { return now })
	if got := reg.List(); len(got) != 0 {
		t.Fatalf("past-TTL: expected 0 peers, got %+v", got)
	}
	if _, ok := reg.Get("external-peer"); ok {
		t.Fatalf("Get returned ok for expired peer")
	}
}

// TestPeerRegistry_HeartbeatExtends verifies that Heartbeat extends the
// TTL so a peer staying alive past PeerTTL is NOT evicted as long as it
// keeps heartbeating.
func TestPeerRegistry_HeartbeatExtends(t *testing.T) {
	t.Parallel()
	reg := NewPeerRegistry()
	now := time.Unix(1_700_000_000, 0).UTC()
	reg.SetClockForTest(func() time.Time { return now })

	reg.Register("alive-peer", "default",
		"http://peer/.well-known/agent-card.json",
		"http://peer/jsonrpc")

	// Advance to T+60s and heartbeat (well within TTL).
	now = now.Add(60 * time.Second)
	reg.SetClockForTest(func() time.Time { return now })
	if !reg.Heartbeat("alive-peer") {
		t.Fatalf("Heartbeat returned false for registered peer")
	}

	// Advance to T+150s (>PeerTTL from registration, but only 90s since
	// heartbeat) — peer should STILL be present because the heartbeat
	// reset the clock.
	now = now.Add(90 * time.Second)
	reg.SetClockForTest(func() time.Time { return now })
	if got := reg.List(); len(got) != 1 {
		t.Fatalf("after heartbeat + 90s: expected 1 peer, got %d", len(got))
	}
	// Just over TTL since the heartbeat — should now evict.
	now = now.Add(2 * time.Second)
	reg.SetClockForTest(func() time.Time { return now })
	if got := reg.List(); len(got) != 0 {
		t.Fatalf("post-heartbeat past TTL: expected 0 peers, got %+v", got)
	}

	// Heartbeating an unknown peer returns false (404 signal).
	if reg.Heartbeat("never-existed") {
		t.Fatalf("Heartbeat returned true for unknown peer")
	}
}

// TestPeerRegistry_TeamFilter verifies that ListByTeam returns only the
// peers in the requested team. Doubles as a multi-peer sanity check.
func TestPeerRegistry_TeamFilter(t *testing.T) {
	t.Parallel()
	reg := NewPeerRegistry()
	now := time.Unix(1_700_000_000, 0).UTC()
	reg.SetClockForTest(func() time.Time { return now })

	reg.Register("peer-a", "trio", "http://a/card", "http://a/jsonrpc")
	reg.Register("peer-b", "trio", "http://b/card", "http://b/jsonrpc")
	reg.Register("peer-c", "scrum", "http://c/card", "http://c/jsonrpc")

	trio := names(reg.ListByTeam("trio"))
	sort.Strings(trio)
	if want := []string{"peer-a", "peer-b"}; !equal(trio, want) {
		t.Fatalf("trio members: got %v, want %v", trio, want)
	}
	scrum := names(reg.ListByTeam("scrum"))
	if want := []string{"peer-c"}; !equal(scrum, want) {
		t.Fatalf("scrum members: got %v, want %v", scrum, want)
	}
	empty := reg.ListByTeam("nonexistent")
	if len(empty) != 0 {
		t.Fatalf("nonexistent team: expected 0 peers, got %d", len(empty))
	}

	// Deregister one peer and verify the team-filtered view updates.
	if !reg.Deregister("peer-a") {
		t.Fatalf("Deregister returned false for registered peer")
	}
	trio = names(reg.ListByTeam("trio"))
	if want := []string{"peer-b"}; !equal(trio, want) {
		t.Fatalf("after Deregister: trio members got %v, want %v", trio, want)
	}
	if reg.Deregister("peer-a") {
		t.Fatalf("Deregister returned true for already-removed peer")
	}
}

// TestPeerRegistry_ReregisterPreservesJoinedAt verifies that
// re-registering an existing peer keeps the original JoinedAt timestamp
// so the registry shows the original join time + latest heartbeat.
func TestPeerRegistry_ReregisterPreservesJoinedAt(t *testing.T) {
	t.Parallel()
	reg := NewPeerRegistry()
	t0 := time.Unix(1_700_000_000, 0).UTC()
	now := t0
	reg.SetClockForTest(func() time.Time { return now })

	reg.Register("dup-peer", "default", "http://x/card", "http://x/jsonrpc")
	now = t0.Add(30 * time.Second)
	reg.SetClockForTest(func() time.Time { return now })
	reg.Register("dup-peer", "default", "http://x/card", "http://x/jsonrpc")

	got, ok := reg.Get("dup-peer")
	if !ok {
		t.Fatalf("peer missing after re-register")
	}
	if !got.JoinedAt.Equal(t0) {
		t.Fatalf("JoinedAt: got %s, want %s (original join)", got.JoinedAt, t0)
	}
	if !got.LastHeartbeatAt.Equal(now) {
		t.Fatalf("LastHeartbeatAt: got %s, want %s (re-register time)", got.LastHeartbeatAt, now)
	}
}

// TestPeerRegistry_ConcurrentSafe is a smoke test that drives
// register/heartbeat/list calls from many goroutines simultaneously to
// catch any locking bugs (the registry MUST be safe for concurrent use
// per the package contract).
func TestPeerRegistry_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	reg := NewPeerRegistry()
	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			name := "peer-" + itoa(i)
			reg.Register(name, "default", "http://"+name+"/card", "http://"+name+"/jsonrpc")
			reg.Heartbeat(name)
			_ = reg.List()
		}(i)
	}
	wg.Wait()
	if got := len(reg.List()); got != N {
		t.Fatalf("after concurrent register: expected %d peers, got %d", N, got)
	}
}

// names extracts the Name field from a slice of PeerInfo.
func names(ps []PeerInfo) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

// equal compares two string slices position-wise.
func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// itoa is a minimal int → string for test ids (avoids importing strconv
// at the top of the file).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [12]byte
	n := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}

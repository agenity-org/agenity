// internal/federation/federation_test.go — v0.9.3 #225 row C1.
// Pins HostedRegistryDiscoverer announce/poll behaviour + Federation
// orchestrator's fetch-and-persist on each peer announcement.
//
// Refs #225 row C1.
package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

func newTestStore(t *testing.T) persistence.AgentCardRepository {
	t.Helper()
	store, err := sqlite.NewStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store.AgentCards()
}

// TestHostedRegistryDiscoverer_AnnounceAndPoll verifies the announce
// → poll → emit cycle against a httptest registry server.
func TestHostedRegistryDiscoverer_AnnounceAndPoll(t *testing.T) {
	SetStderr(io.Discard)
	t.Cleanup(func() { SetStderr(nil) })

	var (
		mu          sync.Mutex
		announces   []registryPeer
		peerToServe = registryPeer{SID: "peer-xyz", URL: "http://peer-xyz.example.com"}
	)
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/announce":
			var p registryPeer
			_ = json.NewDecoder(r.Body).Decode(&p)
			mu.Lock()
			announces = append(announces, p)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case "/peers":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(registryPeersResponse{
				Peers: []registryPeer{peerToServe},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer reg.Close()

	d := &HostedRegistryDiscoverer{
		RegistryURL:    reg.URL,
		SelfSID:        "self-abc",
		SelfURL:        "http://self.example.com",
		AnnouncePeriod: 50 * time.Millisecond,
		PollPeriod:     50 * time.Millisecond,
	}
	out := make(chan PeerAnnouncement, 4)
	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx, out)

	// Wait for at least one announce + one peer emit.
	select {
	case ann := <-out:
		if ann.SID != "peer-xyz" {
			t.Errorf("expected peer-xyz announcement, got %+v", ann)
		}
		if ann.Source != "hosted-registry" {
			t.Errorf("expected source=hosted-registry, got %q", ann.Source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for peer announcement")
	}
	// Give the announcer a chance to POST at least once.
	time.Sleep(150 * time.Millisecond)
	cancel()

	mu.Lock()
	got := len(announces)
	mu.Unlock()
	if got == 0 {
		t.Errorf("expected at least one /announce POST, got %d", got)
	}
}

// TestHostedRegistryDiscoverer_SkipsSelf — registry returning the
// SelfSID is filtered (peer doesn't fan its own announce back).
func TestHostedRegistryDiscoverer_SkipsSelf(t *testing.T) {
	SetStderr(io.Discard)
	t.Cleanup(func() { SetStderr(nil) })

	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/peers" {
			_ = json.NewEncoder(w).Encode(registryPeersResponse{
				Peers: []registryPeer{{SID: "self-abc", URL: "http://x"}},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer reg.Close()

	d := &HostedRegistryDiscoverer{
		RegistryURL: reg.URL,
		SelfSID:     "self-abc",
		SelfURL:     "http://self",
		PollPeriod:  20 * time.Millisecond,
	}
	out := make(chan PeerAnnouncement, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx, out)
	select {
	case ann := <-out:
		t.Fatalf("unexpected self-announcement: %+v", ann)
	case <-time.After(150 * time.Millisecond):
		// expected — self filtered
	}
}

// TestFederation_FetchesAndPersistsAgentCard pins the orchestrator's
// happy path: announcement → GET .well-known/agent-card.json → persist.
func TestFederation_FetchesAndPersistsAgentCard(t *testing.T) {
	SetStderr(io.Discard)
	t.Cleanup(func() { SetStderr(nil) })

	cardJSON := `{"name":"peer-runner","protocolVersion":"1.0.0","url":"http://peer.example.com"}`
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/agent-card.json" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cardJSON))
			return
		}
		http.NotFound(w, r)
	}))
	defer peer.Close()

	store := newTestStore(t)
	fed := New(store)
	fed.FetchTimeout = time.Second

	// Hand-deliver a peer announcement to exercise fetchAndPersist directly.
	fed.fetchAndPersist(context.Background(), PeerAnnouncement{
		SID:    "fed-peer-1",
		URL:    peer.URL,
		Source: "hosted-registry",
	})

	card, err := store.Get(context.Background(), "fed-peer-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if card.Name != "peer-runner" {
		t.Errorf("Name = %q, want peer-runner", card.Name)
	}
	if !card.PublicVisibility {
		t.Errorf("hosted-registry source should produce PublicVisibility=true")
	}
	if !strings.Contains(string(card.Body), "protocolVersion") {
		t.Errorf("Body should preserve original JSON: %s", string(card.Body))
	}
}

// TestFederation_RejectsNonJSON — peers serving HTML / 5xx error pages
// must NOT pollute the cache.
func TestFederation_RejectsNonJSON(t *testing.T) {
	SetStderr(io.Discard)
	t.Cleanup(func() { SetStderr(nil) })

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Status 200 but HTML body — a common misconfigured-reverse-proxy
		// failure shape.
		w.Write([]byte("<html><body>error</body></html>"))
	}))
	defer peer.Close()

	store := newTestStore(t)
	fed := New(store)
	fed.fetchAndPersist(context.Background(), PeerAnnouncement{
		SID: "bad-peer", URL: peer.URL, Source: "hosted-registry",
	})
	if _, err := store.Get(context.Background(), "bad-peer"); err == nil {
		t.Errorf("expected not-found after rejecting non-JSON, got hit")
	}
}

// TestFederation_RunSpawnsAndStops verifies the orchestrator wires
// discoverers + halts on ctx cancel.
func TestFederation_RunSpawnsAndStops(t *testing.T) {
	SetStderr(io.Discard)
	t.Cleanup(func() { SetStderr(nil) })

	store := newTestStore(t)
	fed := New(store)
	d := &countingDiscoverer{}
	fed.Register(d)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { fed.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Federation.Run didn't return after ctx cancel")
	}
	if d.runs < 1 {
		t.Errorf("expected countingDiscoverer to have run at least once, got %d", d.runs)
	}
}

type countingDiscoverer struct {
	mu   sync.Mutex
	runs int
}

func (c *countingDiscoverer) Name() string { return "counting" }
func (c *countingDiscoverer) Run(ctx context.Context, _ chan<- PeerAnnouncement) {
	c.mu.Lock()
	c.runs++
	c.mu.Unlock()
	<-ctx.Done()
}

// Compile-time silence for unused imports under certain skip paths.
var _ = bytes.NewReader

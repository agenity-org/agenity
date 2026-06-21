// internal/federation/multi_host_walk_test.go — v0.9.3 #225 row C3.
// THE DoD walk for the federation epic: two chepherd-instance HTTP
// services + a shared registry + a federated SendMessage roundtrip.
//
// Exercises C1 (peer registry + AgentCard cache) + C2 (FederatedDeliverer
// routing) + B1 (Bearer-token auth) together — the same chain a real
// multi-host deployment runs on bastion + remote.
//
// The two instances are bare http.Server muxes (not real chepherd run
// processes — that flakes on podman cleanup per #218). Each runs:
//   - GET /.well-known/agent-card.json   → its own AgentCard
//   - POST /jsonrpc                       → auth-gated A2A Router with
//                                            the 10 method bodies + a
//                                            captured Deliverer
//
// Instance A also runs the FederatedDeliverer on outbound SendMessage
// so a contextID `@<B-uuid>/<sess>` routes through C2.
//
// Refs #225 row C3 row C2 row C1 row B1 #277.
package federation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/a2a"
	"github.com/agenity-org/agenity/internal/persistence"
	"github.com/agenity-org/agenity/internal/persistence/sqlite"
)

// fakeRegistry serves /announce + /peers backed by an in-memory map,
// modeling the chepherd.org canonical registry contract.
type fakeRegistry struct {
	mu    sync.Mutex
	peers map[string]registryPeer
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{peers: map[string]registryPeer{}}
}

func (r *fakeRegistry) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/announce":
			var p registryPeer
			_ = json.NewDecoder(req.Body).Decode(&p)
			r.mu.Lock()
			r.peers[p.SID] = p
			r.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case "/peers":
			r.mu.Lock()
			out := registryPeersResponse{Peers: make([]registryPeer, 0, len(r.peers))}
			for _, p := range r.peers {
				out.Peers = append(out.Peers, p)
			}
			r.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, req)
		}
	})
}

// stubValidator accepts a single shared bearer. Used by B's
// /jsonrpc auth gate so A's outbound forward carries the same token.
type stubValidator struct{ want string }

func (s *stubValidator) Validate(_ context.Context, token string) (string, error) {
	if token == s.want {
		return "peer", nil
	}
	return "", io.ErrUnexpectedEOF
}

// captureDeliverer records the last delivered Message + returns a
// caller-supplied Task. Plays the role of B's local A2ADeliverer
// without needing a real Runtime.
type captureDeliverer struct {
	mu       sync.Mutex
	captured a2a.Message
	returns  func(a2a.Message) (*a2a.Task, error)
}

func (c *captureDeliverer) Deliver(_ context.Context, msg a2a.Message) (*a2a.Task, error) {
	c.mu.Lock()
	c.captured = msg
	ret := c.returns
	c.mu.Unlock()
	if ret != nil {
		return ret(msg)
	}
	return &a2a.Task{
		ID:        "B-task-77",
		ContextID: msg.ContextID,
		Status:    a2a.TaskStatus{State: a2a.TaskStateWorking},
		Kind:      "task",
	}, nil
}

func (c *captureDeliverer) lastContextID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.captured.ContextID
}
func (c *captureDeliverer) lastMessageID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.captured.MessageID
}

// instance encapsulates one chepherd-instance HTTP service for the walk.
type instance struct {
	sid       string
	store     *sqlite.Store
	cards     persistence.AgentCardRepository
	deliverer *captureDeliverer
	srv       *httptest.Server
}

func newInstance(t *testing.T, sid string, bearer string) *instance {
	t.Helper()
	store, err := sqlite.NewStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	deliv := &captureDeliverer{}
	router := a2a.NewRouter()
	_ = router.WireDeliverer(deliv)
	mb := &a2a.MethodBodies{
		Store:       store,
		AgentCardFn: func() a2a.AgentCard { return a2a.AgentCard{Name: "instance-" + sid, ProtocolVersion: "1.0.0"} },
		RunnerSID:   sid,
	}
	if err := mb.Register(router); err != nil {
		t.Fatalf("MethodBodies.Register: %v", err)
	}
	mux := http.NewServeMux()
	// AgentCard advertises the instance URL so the federation
	// orchestrator can extract `url` from the cached body.
	card := &a2a.AgentCard{
		Name:            "instance-" + sid,
		ProtocolVersion: "1.0.0",
	}
	// Fill in URL after the test server boots — done below via SetURL.
	a2a.RegisterRoutes(mux, card, router, &stubValidator{want: bearer}, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	card.URL = srv.URL
	return &instance{
		sid:       sid,
		store:     store,
		cards:     store.AgentCards(),
		deliverer: deliv,
		srv:       srv,
	}
}

// TestMultiHostWalk_FederatedSendMessageRoundtrip is the v0.9.3 DoD
// walk for the federation epic. Asserts the end-to-end chain:
//
//	Instance A (sender)
//	  └── FederatedDeliverer
//	        └── HTTPS POST /jsonrpc → Instance B (Bearer-gated)
//	              └── a2a.AuthMiddleware verifies token
//	                    └── Router dispatches SendMessage
//	                          └── captureDeliverer (B's local fake)
//	                                ← Task returned
//	          ← Task re-prefixed with @<B-uuid>/
//	   ← assertion: Task.ID matches B-task-77 + prefix restored
func TestMultiHostWalk_FederatedSendMessageRoundtrip(t *testing.T) {
	SetStderr(io.Discard)
	t.Cleanup(func() { SetStderr(nil) })

	const sharedBearer = "walk-shared-bearer"

	// ─── stand up registry + 2 instances ────────────────────────────
	reg := httptest.NewServer(newFakeRegistry().handler())
	defer reg.Close()

	a := newInstance(t, "uuid-A", sharedBearer)
	b := newInstance(t, "uuid-B", sharedBearer)

	// ─── both instances run a HostedRegistryDiscoverer ──────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fedA := New(a.cards)
	fedA.Register(&HostedRegistryDiscoverer{
		RegistryURL: reg.URL, SelfSID: a.sid, SelfURL: a.srv.URL,
		AnnouncePeriod: 50 * time.Millisecond,
		PollPeriod:     50 * time.Millisecond,
	})
	go fedA.Run(ctx)

	fedB := New(b.cards)
	fedB.Register(&HostedRegistryDiscoverer{
		RegistryURL: reg.URL, SelfSID: b.sid, SelfURL: b.srv.URL,
		AnnouncePeriod: 50 * time.Millisecond,
		PollPeriod:     50 * time.Millisecond,
	})
	go fedB.Run(ctx)

	// ─── wait for A's cache to learn about B ────────────────────────
	deadline := time.Now().Add(3 * time.Second)
	var card *persistence.AgentCard
	for time.Now().Before(deadline) {
		c, err := a.cards.Get(context.Background(), b.sid)
		if err == nil && c != nil {
			card = c
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if card == nil {
		t.Fatal("instance A never learned about instance B via federation")
	}
	url, _ := extractPeerURL(card)
	if url != b.srv.URL {
		t.Errorf("cached B URL = %q, want %q", url, b.srv.URL)
	}

	// ─── A.FederatedDeliverer.Deliver against @<B>/sess1 ────────────
	fedDeliv := &FederatedDeliverer{
		Local:          &captureDeliverer{}, // never fires — peer-prefixed contextID
		Cards:          a.cards,
		SelfSID:        a.sid,
		OutboundBearer: sharedBearer,
	}
	msg := a2a.Message{
		Role:      "user",
		ContextID: "@" + b.sid + "/sess1",
		MessageID: "walk-msg-1",
		Parts:     []a2a.Part{{Kind: "text", Text: "hello, federated peer"}},
	}
	task, err := fedDeliv.Deliver(context.Background(), msg)
	if err != nil {
		t.Fatalf("FederatedDeliver: %v", err)
	}
	if task == nil || task.ID != "B-task-77" {
		t.Errorf("Task = %v, want ID=B-task-77 (B's local captureDeliverer return)", task)
	}
	if !strings.HasPrefix(task.ContextID, "@"+b.sid+"/") {
		t.Errorf("Task.ContextID = %q, want @<B>/ prefix restored", task.ContextID)
	}
	// B should have seen the message with the prefix STRIPPED.
	if got := b.deliverer.lastContextID(); got != "sess1" {
		t.Errorf("B saw ContextID = %q, want sess1 (prefix stripped before forward)", got)
	}
	if got := b.deliverer.lastMessageID(); got != "walk-msg-1" {
		t.Errorf("B saw MessageID = %q, want walk-msg-1", got)
	}
}

// TestMultiHostWalk_AuthGateRejectsUnauthenticatedForward — proves the
// B1 auth gate refuses an A→B forward when the shared bearer doesn't
// match. Operator-visible: misconfigured peer trust must NOT silently
// succeed.
func TestMultiHostWalk_AuthGateRejectsUnauthenticatedForward(t *testing.T) {
	SetStderr(io.Discard)
	t.Cleanup(func() { SetStderr(nil) })

	b := newInstance(t, "uuid-B", "the-correct-bearer")

	// Pre-seed A's cache directly (skip federation; testing auth, not C1).
	storeA, err := sqlite.NewStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	if err := storeA.AgentCards().Save(context.Background(), &persistence.AgentCard{
		SID:  "uuid-B",
		Name: "instance-uuid-B",
		Body: []byte(`{"url":"` + b.srv.URL + `"}`),
	}); err != nil {
		t.Fatalf("seed B card on A: %v", err)
	}

	fedDeliv := &FederatedDeliverer{
		Local:          &captureDeliverer{},
		Cards:          storeA.AgentCards(),
		SelfSID:        "uuid-A",
		OutboundBearer: "wrong-bearer", // mismatch — B's gate should reject
	}
	task, err := fedDeliv.Deliver(context.Background(), a2a.Message{
		ContextID: "@uuid-B/sess1",
		MessageID: "auth-walk-msg",
	})
	if err == nil {
		t.Fatal("expected auth error from peer, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected HTTP 401 in error, got %v", err)
	}
	if task == nil || task.Status.State != a2a.TaskStateFailed {
		t.Errorf("expected failed-state Task, got %+v", task)
	}
}

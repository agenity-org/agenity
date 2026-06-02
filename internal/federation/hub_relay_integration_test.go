// internal/federation/hub_relay_integration_test.go — #672 hub-relayed
// WebRTC A2A (epic). End-to-end pin: an OFFERER daemon and an ANSWERER
// daemon exchange an A2A message/send through an in-process hub with NO
// direct reachability between them — every frame is an outbound request
// to the hub. Proves offer→answer→DataChannel-open→A2A JSON-RPC
// round-trip + the HubDeliverer outbound-selection logic.
package federation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/webrtcrtc"
)

// ─── in-process hub implementing the #672 signaling-queue contract ───

// fakeHub is the minimal body-blind signaling queue + directory the
// real chepherd-hub implements (cmd/chepherd-hub/signaling.go). Keyed by
// toOrgId; DrainPending on read. No mTLS — identity is the
// X-Chepherd-Org header (dev mode).
type fakeHub struct {
	mu     sync.Mutex
	frames map[string][]webrtcrtc.HubFrame // key: toOrgId
}

func newFakeHub() *fakeHub {
	return &fakeHub{frames: map[string][]webrtcrtc.HubFrame{}}
}

func (h *fakeHub) handler() http.Handler {
	mux := http.NewServeMux()
	post := func(kind string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var f webrtcrtc.HubFrame
			if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.Kind = kind
			f.CreatedAt = time.Now().UTC()
			h.mu.Lock()
			h.frames[f.ToOrgID] = append(h.frames[f.ToOrgID], f)
			h.mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true})
		}
	}
	mux.HandleFunc("/v1/signaling/offer", post("offer"))
	mux.HandleFunc("/v1/signaling/answer", post("answer"))
	mux.HandleFunc("/v1/signaling/ice", post("ice"))
	mux.HandleFunc("/v1/signaling/pending", func(w http.ResponseWriter, r *http.Request) {
		org := r.URL.Query().Get("orgId")
		h.mu.Lock()
		frames := h.frames[org]
		delete(h.frames, org)
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"org":    org,
			"frames": frames,
			"count":  len(frames),
		})
	})
	// TURN disabled in this test → 503 so the daemon falls through to
	// STUN-only (which for loopback host candidates is fine).
	mux.HandleFunc("/v1/turn/credentials", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "TURN not configured", http.StatusServiceUnavailable)
	})
	return mux
}

// ─── stub local Deliverer on the answerer side ──────────────────────

type stubDeliverer struct {
	mu       sync.Mutex
	received []a2a.Message
}

func (d *stubDeliverer) Deliver(_ context.Context, msg a2a.Message) (*a2a.Task, error) {
	d.mu.Lock()
	d.received = append(d.received, msg)
	d.mu.Unlock()
	return &a2a.Task{
		ID:        "task-" + msg.MessageID,
		ContextID: msg.ContextID,
		Kind:      "task",
		Status:    a2a.TaskStatus{State: a2a.TaskStateCompleted},
	}, nil
}

// ─── the round-trip test ────────────────────────────────────────────

func TestHubRelay_OfferAnswerDataChannelA2ARoundTrip(t *testing.T) {
	t.Parallel()
	hub := newFakeHub()
	srv := httptest.NewServer(hub.handler())
	defer srv.Close()

	const offererOrg = "org-alice"
	const answererOrg = "org-bob"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ── ANSWERER (org-bob): no inbound HTTP; drains the hub for offers.
	stub := &stubDeliverer{}
	answerer := NewHubAnswerer(srv.URL, answererOrg, webrtcrtc.Config{}, stub, srv.Client())
	answerer.PollInterval = 50 * time.Millisecond
	go answerer.Start(ctx)
	defer answerer.Wait()

	// ── OFFERER (org-alice): dials org-bob through the hub. The peer's
	// agent card carries url = hub://org-bob, so HubDeliverer routes over
	// the hub instead of HTTP.
	cards := newMemCards()
	bobCard, _ := json.Marshal(map[string]any{
		"protocolVersion": "1.0",
		"name":            "bob",
		"url":             webrtcrtc.HubPeerScheme + answererOrg,
	})
	_ = cards.Save(ctx, &persistence.AgentCard{SID: answererOrg, Name: "bob", Body: bobCard})

	signaler := webrtcrtc.NewHubSignaler(srv.URL, offererOrg, srv.Client())
	signaler.PollInterval = 50 * time.Millisecond
	pcStore := webrtcrtc.NewPCStore(webrtcrtc.Config{}, signaler)
	pcStore.GatherBeforeOffer = true
	defer pcStore.CloseAll()

	deliverer := &HubDeliverer{
		Fallback:    &stubDeliverer{}, // must NOT be hit for the hub peer
		Cards:       cards,
		PCStore:     pcStore,
		SelfSID:     offererOrg,
		DialTimeout: 20 * time.Second,
		SendTimeout: 20 * time.Second,
	}

	// ContextID @<peerOrg>/<rest> selects peer routing; HubDeliverer sees
	// the hub:// card url + routes over the DataChannel.
	msg := a2a.Message{
		Role:      "user",
		Kind:      "message",
		MessageID: "m1",
		ContextID: "@" + answererOrg + "/session-1",
		Parts:     []a2a.Part{{Kind: "text", Text: "ping over hub"}},
	}
	task, err := deliverer.Deliver(ctx, msg)
	if err != nil {
		t.Fatalf("Deliver over hub: %v", err)
	}
	if task == nil {
		t.Fatal("nil task")
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("task state = %q, want completed", task.Status.State)
	}
	// ContextID prefix restored so downstream pollers keep a stable handle.
	if want := "@" + answererOrg + "/session-1"; task.ContextID != want {
		t.Errorf("task.ContextID = %q, want %q", task.ContextID, want)
	}

	// The answerer's local deliverer must have received the message with
	// the @<peer>/ prefix STRIPPED (peer sees its own bare session id).
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.received) != 1 {
		t.Fatalf("answerer received %d messages, want 1", len(stub.received))
	}
	got := stub.received[0]
	if got.ContextID != "session-1" {
		t.Errorf("answerer-side ContextID = %q, want session-1 (prefix stripped)", got.ContextID)
	}
	if text, _ := a2a.ExtractText(got); text != "ping over hub" {
		t.Errorf("answerer-side text = %q, want %q", text, "ping over hub")
	}
}

// TestHubDeliverer_FallbackForHTTPPeer pins the selection logic: a peer
// whose card carries a normal http url is delegated to the Fallback
// deliverer, NOT routed over the hub.
func TestHubDeliverer_FallbackForHTTPPeer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cards := newMemCards()
	httpCard, _ := json.Marshal(map[string]any{
		"protocolVersion": "1.0",
		"name":            "carol",
		"url":             "https://carol.example.com",
	})
	_ = cards.Save(ctx, &persistence.AgentCard{SID: "org-carol", Body: httpCard})

	fallback := &stubDeliverer{}
	d := &HubDeliverer{
		Fallback: fallback,
		Cards:    cards,
		PCStore:  nil, // must never be dialed for an HTTP peer
		SelfSID:  "org-me",
	}
	msg := a2a.Message{
		MessageID: "m2",
		ContextID: "@org-carol/sess",
		Parts:     []a2a.Part{{Kind: "text", Text: "via http"}},
	}
	if _, err := d.Deliver(ctx, msg); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	fallback.mu.Lock()
	defer fallback.mu.Unlock()
	if len(fallback.received) != 1 {
		t.Fatalf("fallback received %d, want 1 (HTTP peer must delegate)", len(fallback.received))
	}
}

// TestHubDeliverer_LocalContextDelegates pins that a non-peer contextID
// (no @<sid>/ prefix) goes straight to the fallback (local delivery).
func TestHubDeliverer_LocalContextDelegates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fallback := &stubDeliverer{}
	d := &HubDeliverer{Fallback: fallback, Cards: newMemCards(), SelfSID: "org-me"}
	msg := a2a.Message{MessageID: "m3", ContextID: "plain-session"}
	if _, err := d.Deliver(ctx, msg); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	fallback.mu.Lock()
	defer fallback.mu.Unlock()
	if len(fallback.received) != 1 {
		t.Fatalf("fallback received %d, want 1 (local context must delegate)", len(fallback.received))
	}
}

// ─── tiny in-memory AgentCardRepository ─────────────────────────────

type memCards struct {
	mu sync.Mutex
	m  map[string]*persistence.AgentCard
}

func newMemCards() *memCards { return &memCards{m: map[string]*persistence.AgentCard{}} }

func (c *memCards) Get(_ context.Context, sid string) (*persistence.AgentCard, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[sid], nil
}
func (c *memCards) Save(_ context.Context, card *persistence.AgentCard) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[card.SID] = card
	return nil
}
func (c *memCards) List(_ context.Context, _ persistence.AgentCardListOpts) ([]*persistence.AgentCard, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*persistence.AgentCard, 0, len(c.m))
	for _, v := range c.m {
		out = append(out, v)
	}
	return out, nil
}
func (c *memCards) Delete(_ context.Context, sid string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, sid)
	return nil
}

var _ persistence.AgentCardRepository = (*memCards)(nil)

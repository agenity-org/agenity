// internal/federation/federated_deliverer_test.go — v0.9.3 #225 row C2.
// Pins the cross-instance routing semantics of FederatedDeliverer:
// @<peer-sid>/<rest> → peer's /jsonrpc; bare contextID → local
// Deliverer; @<self-sid>/<rest> → local Deliverer with prefix stripped.
//
// Refs #225 row C2.
package federation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agenity-org/agenity/internal/a2a"
	"github.com/agenity-org/agenity/internal/persistence"
)

type recordingLocalDeliverer struct {
	mu       sync.Mutex
	captured []a2a.Message
	respond  func(a2a.Message) (*a2a.Task, error)
}

func (r *recordingLocalDeliverer) Deliver(_ context.Context, msg a2a.Message) (*a2a.Task, error) {
	r.mu.Lock()
	r.captured = append(r.captured, msg)
	respond := r.respond
	r.mu.Unlock()
	if respond != nil {
		return respond(msg)
	}
	return &a2a.Task{ID: "local-task", ContextID: msg.ContextID, Status: a2a.TaskStatus{State: a2a.TaskStateWorking}}, nil
}

func (r *recordingLocalDeliverer) captureCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.captured)
}

func (r *recordingLocalDeliverer) last() a2a.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.captured) == 0 {
		return a2a.Message{}
	}
	return r.captured[len(r.captured)-1]
}

// Test 1 — bare contextID falls through to local deliverer.
func TestFederatedDeliverer_LocalFallthrough(t *testing.T) {
	SetStderr(io.Discard)
	t.Cleanup(func() { SetStderr(nil) })

	local := &recordingLocalDeliverer{}
	fed := &FederatedDeliverer{
		Local:   local,
		Cards:   newTestStore(t),
		SelfSID: "self-aaa",
	}
	msg := a2a.Message{ContextID: "shepherd", MessageID: "m1"}
	task, err := fed.Deliver(context.Background(), msg)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if local.captureCount() != 1 {
		t.Errorf("expected local Deliver to fire once, got %d", local.captureCount())
	}
	if local.last().ContextID != "shepherd" {
		t.Errorf("local saw ContextID = %q, want shepherd", local.last().ContextID)
	}
	if task.ID != "local-task" {
		t.Errorf("Task.ID = %q, want local-task", task.ID)
	}
}

// Test 2 — @<self-sid>/<rest> routes to local with prefix stripped.
func TestFederatedDeliverer_AtSelfPrefixStrippedLocally(t *testing.T) {
	SetStderr(io.Discard)
	t.Cleanup(func() { SetStderr(nil) })

	local := &recordingLocalDeliverer{}
	fed := &FederatedDeliverer{
		Local:   local,
		Cards:   newTestStore(t),
		SelfSID: "self-aaa",
	}
	msg := a2a.Message{ContextID: "@self-aaa/shepherd", MessageID: "m2"}
	if _, err := fed.Deliver(context.Background(), msg); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if local.last().ContextID != "shepherd" {
		t.Errorf("local saw ContextID = %q, want shepherd (prefix stripped)", local.last().ContextID)
	}
}

// Test 3 — @<peer-sid>/<rest> forwards to peer when peer card cached.
func TestFederatedDeliverer_ForwardsToPeer(t *testing.T) {
	SetStderr(io.Discard)
	t.Cleanup(func() { SetStderr(nil) })

	store := newTestStore(t)

	// Track the inbound peer request.
	var (
		mu             sync.Mutex
		gotAuth        string
		gotContextID   string
		gotMessageID   string
	)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		var env struct {
			Method string `json:"method"`
			Params struct {
				Message a2a.Message `json:"message"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&env)
		mu.Lock()
		gotContextID = env.Params.Message.ContextID
		gotMessageID = env.Params.Message.MessageID
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      "x",
			"result": map[string]any{
				"task": map[string]any{
					"id":        "peer-task-77",
					"contextId": env.Params.Message.ContextID,
					"status":    map[string]any{"state": "working"},
					"kind":      "task",
				},
			},
		})
	}))
	defer peer.Close()

	cardBody := `{"name":"peer-runner","protocolVersion":"1.0.0","url":"` + peer.URL + `"}`
	if err := store.Save(context.Background(), &persistence.AgentCard{
		SID: "peer-bbb", Name: "peer-runner", Body: []byte(cardBody),
	}); err != nil {
		t.Fatalf("Save card: %v", err)
	}

	local := &recordingLocalDeliverer{}
	fed := &FederatedDeliverer{
		Local:          local,
		Cards:          store,
		SelfSID:        "self-aaa",
		OutboundBearer: "peer-token-xyz",
	}
	msg := a2a.Message{ContextID: "@peer-bbb/architect", MessageID: "m3", Role: "user"}
	task, err := fed.Deliver(context.Background(), msg)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if local.captureCount() != 0 {
		t.Errorf("expected local Deliver to NOT fire, got %d captures", local.captureCount())
	}
	if task == nil || task.ID != "peer-task-77" {
		t.Errorf("Task.ID = %v, want peer-task-77", task)
	}
	if task.ContextID != "@peer-bbb/architect" {
		t.Errorf("Task.ContextID = %q, want restored prefix @peer-bbb/architect", task.ContextID)
	}
	mu.Lock()
	if gotAuth != "Bearer peer-token-xyz" {
		t.Errorf("peer saw Auth = %q, want Bearer peer-token-xyz", gotAuth)
	}
	if gotContextID != "architect" {
		t.Errorf("peer saw ContextID = %q, want architect (prefix stripped before forward)", gotContextID)
	}
	if gotMessageID != "m3" {
		t.Errorf("peer saw MessageID = %q, want m3", gotMessageID)
	}
	mu.Unlock()
}

// Test 4 — peer not in cache returns failed Task + error.
func TestFederatedDeliverer_UnknownPeerFails(t *testing.T) {
	SetStderr(io.Discard)
	t.Cleanup(func() { SetStderr(nil) })

	local := &recordingLocalDeliverer{}
	fed := &FederatedDeliverer{
		Local:   local,
		Cards:   newTestStore(t),
		SelfSID: "self-aaa",
	}
	task, err := fed.Deliver(context.Background(), a2a.Message{
		ContextID: "@unknown-sid/architect", MessageID: "m4",
	})
	if err == nil {
		t.Fatal("expected error for unknown peer, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected error to mention 'unknown', got %v", err)
	}
	if task == nil || task.Status.State != a2a.TaskStateFailed {
		t.Errorf("expected failed-state Task, got %+v", task)
	}
	if local.captureCount() != 0 {
		t.Errorf("local Deliver should NOT fire on peer-unknown path")
	}
}

// Test 5 — peer 5xx + non-JSON body bubbles up as a routing error.
func TestFederatedDeliverer_PeerErrorBubblesUp(t *testing.T) {
	SetStderr(io.Discard)
	t.Cleanup(func() { SetStderr(nil) })

	store := newTestStore(t)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	}))
	defer peer.Close()
	cardBody := `{"name":"peer","url":"` + peer.URL + `"}`
	if err := store.Save(context.Background(), &persistence.AgentCard{
		SID: "peer-zzz", Name: "peer", Body: []byte(cardBody),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fed := &FederatedDeliverer{Local: &recordingLocalDeliverer{}, Cards: store, SelfSID: "self"}
	task, err := fed.Deliver(context.Background(), a2a.Message{
		ContextID: "@peer-zzz/anything", MessageID: "m5",
	})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("expected HTTP 502 error, got %v", err)
	}
	if task == nil || task.Status.State != a2a.TaskStateFailed {
		t.Errorf("expected failed-state Task, got %+v", task)
	}
}

// Test 6 — parsePeerContextID rejects malformed prefixes.
func TestParsePeerContextID(t *testing.T) {
	cases := []struct {
		input string
		sid   string
		rest  string
		ok    bool
	}{
		{"bare-id", "", "bare-id", false},
		{"@/missing-sid", "", "@/missing-sid", false},
		{"@sid/", "", "@sid/", false},
		{"@sid/session", "sid", "session", true},
		{"@long-uuid-form/session-id-uuid", "long-uuid-form", "session-id-uuid", true},
		{"@a/b/c", "a", "b/c", true}, // first slash splits
	}
	for _, c := range cases {
		sid, rest, ok := parsePeerContextID(c.input)
		if sid != c.sid || rest != c.rest || ok != c.ok {
			t.Errorf("parsePeerContextID(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.input, sid, rest, ok, c.sid, c.rest, c.ok)
		}
	}
}

// Test 7 — extractPeerURL handles missing/invalid bodies.
func TestExtractPeerURL(t *testing.T) {
	if _, err := extractPeerURL(nil); err == nil {
		t.Error("expected error on nil card")
	}
	if _, err := extractPeerURL(&persistence.AgentCard{}); err == nil {
		t.Error("expected error on empty body")
	}
	if _, err := extractPeerURL(&persistence.AgentCard{Body: []byte(`not json`)}); err == nil {
		t.Error("expected error on non-JSON body")
	}
	if _, err := extractPeerURL(&persistence.AgentCard{Body: []byte(`{"name":"x"}`)}); err == nil {
		t.Error("expected error when url field missing")
	}
	url, err := extractPeerURL(&persistence.AgentCard{
		Body: []byte(`{"url":"https://example.com/"}`),
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if url != "https://example.com" {
		t.Errorf("expected trailing slash trimmed, got %q", url)
	}
}

// noopDeliverer compile-check helpers (referenced in failed cases).
var _ a2a.Deliverer = (*recordingLocalDeliverer)(nil)
var _ error = errors.New("compile-time")

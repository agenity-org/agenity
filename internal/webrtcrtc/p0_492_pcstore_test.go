// internal/webrtcrtc/p0_492_pcstore_test.go pins the v0.9.4 §S5+§20
// PCStore + DataChannel-JSONRPC contract (#492 Wave F2).
//
// Cache-only PCStore tests (GetOrDial dialing is exercised by the
// p0_492_two_runner_live_walk test which boots real chepherd runners
// with both /webrtc/offer + /webrtc/ice routes wired into the
// production HTTP signaling chain).
//
// Refs #492 V0.9.2-ARCHITECTURE.md §S5 §20.
package webrtcrtc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// stashConnected inserts a pre-connected PC into the store via the
// internal map. Used by the cache-logic tests to skip the
// network-negotiated dial path.
func stashConnected(s *PCStore, peerURL string, pc *PeerConnection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[peerURL] = pc
}

func waitOpenBoth(t *testing.T, a, b *PeerConnection) {
	t.Helper()
	openA := make(chan struct{}, 1)
	openB := make(chan struct{}, 1)
	a.OnOpen(func() { openA <- struct{}{} })
	b.OnOpen(func() { openB <- struct{}{} })
	timeout := time.After(15 * time.Second)
	for ok := 0; ok < 2; {
		select {
		case <-openA:
			ok++
		case <-openB:
			ok++
		case <-timeout:
			t.Fatal("DataChannel didn't open within 15s")
		}
	}
}

// ─── PCStore cache logic ──────────────────────────────────────────

func TestWaveF2_PCStore_GetReturnsStashedPC(t *testing.T) {
	t.Parallel()
	a, b := connectPair(t)
	defer a.Close()
	defer b.Close()
	waitOpenBoth(t, a, b)

	store := NewPCStore(Config{}, nil)
	defer store.CloseAll()
	stashConnected(store, "https://peer-A.example", a)

	got := store.Get("https://peer-A.example")
	if got != a {
		t.Errorf("Get returned %v, want stashed PC", got)
	}
	if store.Len() != 1 {
		t.Errorf("Len = %d, want 1", store.Len())
	}
}

func TestWaveF2_PCStore_GetOrDial_ReturnsStashedHealthyEntry(t *testing.T) {
	t.Parallel()
	a, b := connectPair(t)
	defer a.Close()
	defer b.Close()
	waitOpenBoth(t, a, b)

	store := NewPCStore(Config{}, &failingSignaler{})
	defer store.CloseAll()
	stashConnected(store, "https://peer.example", a)

	// Should return the cached PC without invoking the signaler
	// (which would fail).
	pc, err := store.GetOrDial(context.Background(), "https://peer.example", time.Second)
	if err != nil {
		t.Fatalf("unexpected dial: %v", err)
	}
	if pc != a {
		t.Error("did not return the cached PC")
	}
}

func TestWaveF2_PCStore_StalePCIsDropped(t *testing.T) {
	t.Parallel()
	a, b := connectPair(t)
	waitOpenBoth(t, a, b)
	_ = a.Close()
	_ = b.Close()

	store := NewPCStore(Config{}, &failingSignaler{})
	defer store.CloseAll()
	stashConnected(store, "https://peer.example", a)

	// Stale entry — failingSignaler proves the store TRIED to re-dial.
	if _, err := store.GetOrDial(context.Background(), "https://peer.example", time.Second); err == nil {
		t.Error("expected re-dial attempt, got cached entry returned")
	}
	if store.Get("https://peer.example") != nil {
		t.Error("stale entry should have been dropped after re-dial failure")
	}
}

func TestWaveF2_PCStore_CloseAllClosesEveryPC(t *testing.T) {
	t.Parallel()
	a, b := connectPair(t)
	defer b.Close()
	waitOpenBoth(t, a, b)

	c, d := connectPair(t)
	defer d.Close()
	waitOpenBoth(t, c, d)

	store := NewPCStore(Config{}, nil)
	stashConnected(store, "https://A", a)
	stashConnected(store, "https://C", c)
	if store.Len() != 2 {
		t.Fatalf("Len = %d, want 2", store.Len())
	}
	if err := store.CloseAll(); err != nil {
		t.Errorf("CloseAll: %v", err)
	}
	if store.Len() != 0 {
		t.Errorf("after CloseAll, Len = %d, want 0", store.Len())
	}
	if a.isHealthy() {
		t.Error("PC a still healthy after CloseAll")
	}
}

func TestWaveF2_PCStore_CloseDropsSingleEntry(t *testing.T) {
	t.Parallel()
	a, b := connectPair(t)
	defer b.Close()
	waitOpenBoth(t, a, b)

	store := NewPCStore(Config{}, nil)
	defer store.CloseAll()
	stashConnected(store, "https://target", a)

	if err := store.Close("https://target"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if store.Len() != 0 {
		t.Errorf("Len = %d, want 0", store.Len())
	}
	// Closing again is a no-op (idempotent).
	if err := store.Close("https://target"); err != nil {
		t.Errorf("idempotent Close: %v", err)
	}
}

func TestWaveF2_PCStore_GetOrDial_RejectsEmptyURL(t *testing.T) {
	t.Parallel()
	store := NewPCStore(Config{}, nil)
	defer store.CloseAll()
	if _, err := store.GetOrDial(context.Background(), "", time.Second); err == nil {
		t.Error("empty URL should error")
	}
}

// ─── DataChannel JSON-RPC ─────────────────────────────────────────

func TestWaveF2_JSONRPCClient_DataChannelRoundTrip(t *testing.T) {
	t.Parallel()
	a, b := connectPair(t)
	defer a.Close()
	defer b.Close()
	waitOpenBoth(t, a, b)

	var handlerCount int32
	ServeJSONRPC(b, func(req []byte) ([]byte, error) {
		atomic.AddInt32(&handlerCount, 1)
		var parsed struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(req, &parsed); err != nil {
			return nil, err
		}
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      parsed.ID,
			"result":  map[string]any{"echoed_method": parsed.Method},
		}
		body, _ := json.Marshal(resp)
		return body, nil
	})

	client := NewJSONRPCClient(a)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := []byte(`{"jsonrpc":"2.0","id":"rpc-1","method":"message/send","params":{"x":1}}`)
	resp, elapsed, err := client.MeasuredSendRPC(ctx, req)
	if err != nil {
		t.Fatalf("SendRPC: %v", err)
	}
	t.Logf("DataChannel JSON-RPC round-trip: %v", elapsed)

	var parsed struct {
		ID     string         `json:"id"`
		Result map[string]any `json:"result"`
		Error  map[string]any `json:"error"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("decode response: %v\n%s", err, resp)
	}
	if parsed.Error != nil {
		t.Errorf("unexpected error: %+v", parsed.Error)
	}
	if parsed.Result["echoed_method"] != "message/send" {
		t.Errorf("Result.echoed_method = %v, want message/send", parsed.Result)
	}
	if got := atomic.LoadInt32(&handlerCount); got != 1 {
		t.Errorf("handler fired %d times, want 1", got)
	}
}

func TestWaveF2_JSONRPCClient_RejectsNotifications(t *testing.T) {
	t.Parallel()
	a, b := connectPair(t)
	defer a.Close()
	defer b.Close()
	waitOpenBoth(t, a, b)
	client := NewJSONRPCClient(a)
	defer client.Close()
	_, err := client.SendRPC(context.Background(),
		[]byte(`{"jsonrpc":"2.0","method":"foo"}`))
	if err == nil {
		t.Error("notification (no id) should error")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("error didn't mention id: %v", err)
	}
}

func TestWaveF2_ServeJSONRPC_HandlerErrorReturnsInternalError(t *testing.T) {
	t.Parallel()
	a, b := connectPair(t)
	defer a.Close()
	defer b.Close()
	waitOpenBoth(t, a, b)
	ServeJSONRPC(b, func(req []byte) ([]byte, error) {
		return nil, errPlanned
	})
	client := NewJSONRPCClient(a)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := client.SendRPC(ctx, []byte(`{"jsonrpc":"2.0","id":"x","method":"y"}`))
	if err != nil {
		t.Fatalf("SendRPC: %v", err)
	}
	var parsed struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp, &parsed)
	if parsed.Error == nil {
		t.Fatalf("expected error envelope, got: %s", resp)
	}
	if parsed.Error.Code != -32603 {
		t.Errorf("error.code = %d, want -32603", parsed.Error.Code)
	}
}

// ─── helpers ──────────────────────────────────────────────────────

var errPlanned = errors.New("planned handler failure")

// failingSignaler always errors on ExchangeOffer — used to prove
// the cache DOES try to re-dial when an entry is stale.
type failingSignaler struct{}

func (f *failingSignaler) ExchangeOffer(_ context.Context, _ string, _ webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	return webrtc.SessionDescription{}, errors.New("failingSignaler: refusing")
}

func (f *failingSignaler) SendICECandidate(_ context.Context, _ string, _ webrtc.ICECandidateInit) error {
	return errors.New("failingSignaler: refusing")
}

// Compile-time interface assertion.
var _ Signaler = (*failingSignaler)(nil)

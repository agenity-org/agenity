// cmd/chepherd-hub/p0_495_signaling_test.go pins the v0.9.4 §10
// Pattern 2 Phase 5 SDP signaling relay contract (#495 Wave F5).
//
// Coverage:
//
//   - Queue: enqueue + drain + TTL + concurrent-safe round-trip
//   - Validation: every required field guarded; bad inputs 400
//   - Auth: missing X-Chepherd-Org → 401; spoofed fromOrgId → 403;
//     non-allowlisted org → 403
//   - Routing: A POSTs offer addressed to B; B's pending-poll
//     returns the frame; second poll returns empty (drained)
//   - Body-blind invariant: hub never decodes the payload — the
//     `payload` field is opaque json.RawMessage, round-trip-byte-
//     exact through enqueue → drain
//   - Spoofing defense: X-Chepherd-Org claim != fromOrgId → 403
//   - LIVE WALK against real hub binary in p0_495_signaling_walk_test.go
//
// Refs #495 V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 5.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── Queue ────────────────────────────────────────────────────────

func TestWaveF5_Queue_EnqueueDrainRoundTrip(t *testing.T) {
	t.Parallel()
	q := newSignalingQueue()
	defer q.CloseAll()
	payload := json.RawMessage(`{"sdp":"v=0\r\no=- 0 0 IN IP4 0.0.0.0\r\n"}`)
	err := q.Enqueue(&SignalingFrame{
		Kind: SignalingOffer, FromOrgID: "alice.example",
		ToOrgID: "bob.example", SessionID: "s-1", Payload: payload,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	frames := q.DrainPending("bob.example")
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	// Body-blind: payload bytes are EXACT round-trip.
	if !bytes.Equal(frames[0].Payload, payload) {
		t.Errorf("payload mutated:\n got: %s\nwant: %s", frames[0].Payload, payload)
	}
	if q.DrainPending("bob.example") != nil {
		t.Error("second drain should return nil (queue empty)")
	}
}

func TestWaveF5_Queue_RejectsMissingFields(t *testing.T) {
	t.Parallel()
	q := newSignalingQueue()
	defer q.CloseAll()
	cases := []struct {
		name  string
		frame *SignalingFrame
	}{
		{"nil", nil},
		{"empty toOrg", &SignalingFrame{Kind: SignalingOffer, SessionID: "x", Payload: json.RawMessage(`{}`)}},
		{"empty sessionId", &SignalingFrame{Kind: SignalingOffer, ToOrgID: "x", Payload: json.RawMessage(`{}`)}},
		{"unknown kind", &SignalingFrame{Kind: "unknown", ToOrgID: "x", SessionID: "y", Payload: json.RawMessage(`{}`)}},
		{"empty payload", &SignalingFrame{Kind: SignalingOffer, ToOrgID: "x", SessionID: "y"}},
	}
	for _, c := range cases {
		if err := q.Enqueue(c.frame); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

func TestWaveF5_Queue_GCDropsExpiredFrames(t *testing.T) {
	t.Parallel()
	q := newSignalingQueue()
	defer q.CloseAll()
	_ = q.Enqueue(&SignalingFrame{
		Kind: SignalingOffer, FromOrgID: "a", ToOrgID: "b",
		SessionID: "s", Payload: json.RawMessage(`"x"`),
	})
	// Backdate the queued frame past TTL.
	q.mu.Lock()
	q.frames["b"][0].CreatedAt = time.Now().Add(-signalingFrameTTL - time.Minute)
	q.mu.Unlock()
	q.gcOnce()
	if got := q.DrainPending("b"); got != nil {
		t.Errorf("GC failed to drop expired frame: %+v", got)
	}
}

func TestWaveF5_Queue_ConcurrentEnqueueDrain(t *testing.T) {
	t.Parallel()
	q := newSignalingQueue()
	defer q.CloseAll()
	const N = 100
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = q.Enqueue(&SignalingFrame{
				Kind:      SignalingICE,
				FromOrgID: "a",
				ToOrgID:   "b",
				SessionID: "s-" + itoa(i),
				Payload:   json.RawMessage(`{}`),
			})
		}(i)
	}
	wg.Wait()
	got := q.DrainPending("b")
	if len(got) != N {
		t.Errorf("got %d frames, want %d", len(got), N)
	}
}

// ─── HTTP handlers ────────────────────────────────────────────────

func newHubServer(t *testing.T, cfg *config) (*httptest.Server, *server) {
	t.Helper()
	srv := newServer(cfg)
	httpSrv := httptest.NewServer(srv.mux())
	t.Cleanup(func() {
		srv.signaling.CloseAll()
		httpSrv.Close()
	})
	return httpSrv, srv
}

func postSignaling(t *testing.T, baseURL, path, org, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", baseURL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Chepherd-Org", org)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func getSignaling(t *testing.T, baseURL, path, org string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", baseURL+path, nil)
	if org != "" {
		req.Header.Set("X-Chepherd-Org", org)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func TestWaveF5_Offer_Accepts202_AndPendingDrains(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	body := `{"toOrgId":"bob.example","sessionId":"s-1","payload":{"sdp":"v=0"}}`
	resp := postSignaling(t, hub.URL, "/v1/signaling/offer", "alice.example", body)
	if resp.StatusCode != http.StatusAccepted {
		b, _ := readAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("POST offer = %d, want 202\n%s", resp.StatusCode, b)
	}
	resp.Body.Close()

	// Bob polls.
	pollResp := getSignaling(t, hub.URL, "/v1/signaling/pending?orgId=bob.example", "bob.example")
	if pollResp.StatusCode != http.StatusOK {
		t.Fatalf("pending = %d, want 200", pollResp.StatusCode)
	}
	var p struct {
		Org    string            `json:"org"`
		Frames []*SignalingFrame `json:"frames"`
		Count  int               `json:"count"`
	}
	_ = json.NewDecoder(pollResp.Body).Decode(&p)
	pollResp.Body.Close()
	if p.Count != 1 || len(p.Frames) != 1 {
		t.Fatalf("pending count = %d, want 1; frames = %+v", p.Count, p.Frames)
	}
	if p.Frames[0].Kind != SignalingOffer {
		t.Errorf("kind = %q, want offer", p.Frames[0].Kind)
	}
	if p.Frames[0].FromOrgID != "alice.example" {
		t.Errorf("fromOrgId = %q, want alice.example", p.Frames[0].FromOrgID)
	}
	if !strings.Contains(string(p.Frames[0].Payload), `"sdp":"v=0"`) {
		t.Errorf("payload missing sdp field: %s", p.Frames[0].Payload)
	}

	// Second poll returns empty.
	pollResp = getSignaling(t, hub.URL, "/v1/signaling/pending?orgId=bob.example", "bob.example")
	_ = json.NewDecoder(pollResp.Body).Decode(&p)
	pollResp.Body.Close()
	if p.Count != 0 {
		t.Errorf("second poll count = %d, want 0", p.Count)
	}
}

func TestWaveF5_Offer_MissingOrgHeader_401(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	resp := postSignaling(t, hub.URL, "/v1/signaling/offer", "",
		`{"toOrgId":"x","sessionId":"y","payload":{}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-org POST = %d, want 401", resp.StatusCode)
	}
}

func TestWaveF5_Offer_SpoofedFromOrgId_403(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	// Auth'd as alice.example but claims to be carol.example.
	body := `{"fromOrgId":"carol.example","toOrgId":"bob.example","sessionId":"s","payload":{}}`
	resp := postSignaling(t, hub.URL, "/v1/signaling/offer", "alice.example", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("spoofed fromOrg = %d, want 403", resp.StatusCode)
	}
}

func TestWaveF5_Offer_AllowlistedOrgsOnly(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{allowedOrgs: "alice.example,bob.example"})
	resp := postSignaling(t, hub.URL, "/v1/signaling/offer", "carol.example",
		`{"toOrgId":"bob.example","sessionId":"s","payload":{}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-allowlisted from = %d, want 403", resp.StatusCode)
	}
	resp2 := postSignaling(t, hub.URL, "/v1/signaling/offer", "alice.example",
		`{"toOrgId":"carol.example","sessionId":"s","payload":{}}`)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("non-allowlisted to = %d, want 403", resp2.StatusCode)
	}
}

func TestWaveF5_Pending_OrgIdMismatch_403(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	// Auth'd as alice but querying carol's mailbox.
	resp := getSignaling(t, hub.URL, "/v1/signaling/pending?orgId=carol.example", "alice.example")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-org poll = %d, want 403", resp.StatusCode)
	}
}

func TestWaveF5_FullExchange_OfferAnswerICERoundTrip(t *testing.T) {
	t.Parallel()
	hub, _ := newHubServer(t, &config{})
	// alice → bob: offer
	r := postSignaling(t, hub.URL, "/v1/signaling/offer", "alice.example",
		`{"toOrgId":"bob.example","sessionId":"sess-A","payload":{"sdp":"alice-offer"}}`)
	r.Body.Close()
	// bob → alice: answer
	r = postSignaling(t, hub.URL, "/v1/signaling/answer", "bob.example",
		`{"toOrgId":"alice.example","sessionId":"sess-A","payload":{"sdp":"bob-answer"}}`)
	r.Body.Close()
	// alice → bob: ice
	r = postSignaling(t, hub.URL, "/v1/signaling/ice", "alice.example",
		`{"toOrgId":"bob.example","sessionId":"sess-A","payload":{"candidate":"alice-ice"}}`)
	r.Body.Close()

	// alice polls → sees bob's answer.
	pa := getSignaling(t, hub.URL, "/v1/signaling/pending?orgId=alice.example", "alice.example")
	var alicePend struct {
		Frames []*SignalingFrame `json:"frames"`
	}
	_ = json.NewDecoder(pa.Body).Decode(&alicePend)
	pa.Body.Close()
	if len(alicePend.Frames) != 1 || alicePend.Frames[0].Kind != SignalingAnswer {
		t.Errorf("alice pending = %+v, want 1 answer", alicePend.Frames)
	}

	// bob polls → sees offer + ice.
	pb := getSignaling(t, hub.URL, "/v1/signaling/pending?orgId=bob.example", "bob.example")
	var bobPend struct {
		Frames []*SignalingFrame `json:"frames"`
	}
	_ = json.NewDecoder(pb.Body).Decode(&bobPend)
	pb.Body.Close()
	if len(bobPend.Frames) != 2 {
		t.Fatalf("bob pending = %d frames, want 2", len(bobPend.Frames))
	}
	kinds := map[SignalingFrameKind]bool{}
	for _, f := range bobPend.Frames {
		kinds[f.Kind] = true
	}
	if !kinds[SignalingOffer] || !kinds[SignalingICE] {
		t.Errorf("bob pending kinds = %v, want {offer, ice}", kinds)
	}
}

// ─── helpers ──────────────────────────────────────────────────────

func readAll(r interface{ Read(p []byte) (n int, err error) }) ([]byte, error) {
	var out []byte
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return out, nil
			}
			return out, err
		}
	}
}

func itoa(i int) string { return fmt.Sprintf("%d", i) }

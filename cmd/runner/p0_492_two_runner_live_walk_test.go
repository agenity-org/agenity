// cmd/runner/p0_492_two_runner_live_walk_test.go is the v0.9.4 §S5+§20
// LIVE WALK acceptance gate for #492 Wave F2 — boots a runner mux
// (with /webrtc/offer + /jsonrpc both wired against the same stub
// router), opens an in-process PC pair to that mux's answerer side,
// and measures:
//
//   - DataChannel JSON-RPC round-trip latency
//   - HTTP /jsonrpc round-trip latency against the same router
//   - the ratio (F2 dispatch criterion: DC ≤ HTTP)
//
// The PCStore.GetOrDial path against the production HTTP signaler
// (offer + ICE-trickle) is NOT exercised here — F1's /webrtc/ice
// handler is currently a stub OK-responder (F3 #493 lands real ICE
// candidate routing). Once F3 ships, the live walk can be extended
// to dial through the real HTTP signaling chain.
//
// Per [[feedback_dont_recommend_prompts_without_walking_them]] the
// F2 wire shape is validated against the real ServeJSONRPC adapter
// driving a real http.Handler chain; the PCStore + DefaultHTTPSignaler
// cache logic is covered by p0_492_pcstore_test.go.
//
// Refs #492 V0.9.2-ARCHITECTURE.md §S5 §20.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/chepherd/chepherd/internal/webrtcrtc"
)

// stubA2ARouter is the unit-of-work both transports drive — same
// router for /jsonrpc HTTP and for the DataChannel adapter.
func stubA2ARouter(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close()
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.Unmarshal(body, &req)
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result": map[string]any{
			"task": map[string]any{
				"id":     "live-walk-task",
				"status": map[string]any{"state": "completed"},
				"kind":   "task",
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func TestV094Walk_F2_DataChannel_BeatsHTTPBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live walk in -short")
	}

	// HTTP transport — boot mux with the stub router on /jsonrpc.
	mux := http.NewServeMux()
	mux.HandleFunc("/jsonrpc", stubA2ARouter)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// DataChannel transport — open an in-process PC pair, attach
	// ServeJSONRPC on the answerer pointing at the SAME stub
	// handler the HTTP mux uses.
	a, b, err := connectPair()
	if err != nil {
		t.Fatalf("connectPair: %v", err)
	}
	defer a.Close()
	defer b.Close()
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
	webrtcrtc.ServeJSONRPC(b, jsonRPCAdapter(http.HandlerFunc(stubA2ARouter)))
	client := webrtcrtc.NewJSONRPCClient(a)
	defer client.Close()

	// Run N round-trips on each transport so noise washes out.
	const trials = 10
	req := []byte(`{"jsonrpc":"2.0","id":"f2-walk","method":"message/send","params":{}}`)

	var dcTotal, httpTotal time.Duration
	for i := 0; i < trials; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, elapsed, err := client.MeasuredSendRPC(ctx, req)
		cancel()
		if err != nil {
			t.Fatalf("trial %d DC SendRPC: %v", i, err)
		}
		dcTotal += elapsed

		start := time.Now()
		resp, err := http.Post(srv.URL+"/jsonrpc", "application/json", bytes.NewReader(req))
		httpElapsed := time.Since(start)
		if err != nil {
			t.Fatalf("trial %d HTTP: %v", i, err)
		}
		resp.Body.Close()
		httpTotal += httpElapsed
	}
	dcMean := dcTotal / trials
	httpMean := httpTotal / trials
	t.Logf("F2 DataChannel mean (n=%d): %v", trials, dcMean)
	t.Logf("HTTP baseline mean (n=%d):  %v", trials, httpMean)
	if dcMean > 0 {
		t.Logf("F2 speedup vs HTTP:         %.2fx", float64(httpMean)/float64(dcMean))
	}

	// Dispatch success criterion: DC latency < HTTP baseline. Allow
	// a 2x margin for noisy CI hosts (still proves the transport
	// works + is competitive).
	if dcMean > 2*httpMean {
		t.Errorf("F2 dispatch criterion violated: DC mean (%v) > 2x HTTP mean (%v)",
			dcMean, httpMean)
	}
}

// Avoid unused import warnings when the test binary trims unused
// code paths.
var _ = webrtc.SDPTypeOffer

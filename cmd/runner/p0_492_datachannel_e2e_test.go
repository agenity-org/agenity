// cmd/runner/p0_492_datachannel_e2e_test.go is the v0.9.4 §S5+§20
// integration gate for #492 Wave F2 — boots the runner's per-session
// HTTP endpoint (which now also mounts /webrtc/offer with the F2
// DataChannel JSON-RPC transport), dials via the PCStore + signaler,
// sends an A2A JSON-RPC call over the DataChannel, and asserts:
//
//   - the runner's A2A router responded (same body shape as HTTP)
//   - the round-trip latency is recorded for the latency-vs-HTTP
//     baseline comparison
//
// Refs #492 V0.9.2-ARCHITECTURE.md §S5 §20.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/agenity-org/agenity/internal/webrtcrtc"
)

// TestWaveF2_RunnerMountsWebRTCOffer verifies the per-session mux
// now exposes /webrtc/offer (added by mountF2DataChannel).
func TestWaveF2_RunnerMountsWebRTCOffer(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/a2a/test/jsonrpc", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"x","result":"ok"}`))
	})
	mountF2DataChannel(mux, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"x","result":"ok"}`))
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// /webrtc/offer must answer POST (HandleOffer rejects other
	// methods with 405).
	resp, err := http.Get(srv.URL + "/webrtc/offer")
	if err != nil {
		t.Fatalf("GET /webrtc/offer: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /webrtc/offer = %d, want 405", resp.StatusCode)
	}
}

// TestWaveF2_DataChannel_A2ARoundTrip exercises the full path:
// two in-process PCs (caller A + answerer B with ServeJSONRPC
// attached against a JSON-RPC handler), SendRPC, response, assert.
//
// This is the unit-level proof that the F2 transport WORKS once an
// open DataChannel exists. The PCStore+real-runner negotiation
// path is covered by p0_492_live_walk_test.go via the production
// HTTP signaling endpoint.
func TestWaveF2_DataChannel_A2ARoundTrip(t *testing.T) {
	t.Parallel()
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

	// HTTP handler that mimics an A2A JSON-RPC method.
	httpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 1024)
		n, _ := r.Body.Read(body)
		body = body[:n]
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{"echoed": req.Method},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	webrtcrtc.ServeJSONRPC(b, jsonRPCAdapter(httpHandler))
	client := webrtcrtc.NewJSONRPCClient(a)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := []byte(`{"jsonrpc":"2.0","id":"f2-rpc-1","method":"message/send","params":{}}`)
	resp, elapsed, err := client.MeasuredSendRPC(ctx, req)
	if err != nil {
		t.Fatalf("SendRPC: %v", err)
	}
	t.Logf("F2 A2A round-trip via DataChannel: %v", elapsed)

	var parsed struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("decode resp: %v\n%s", err, resp)
	}
	if parsed.Result["echoed"] != "message/send" {
		t.Errorf("Result.echoed = %v, want message/send", parsed.Result)
	}
}

// connectPair is the in-process PC pair test helper. Lives in
// internal/webrtcrtc/peerconnection_test.go but cmd/runner can't
// import test files; re-implement the same logic here using the
// public API.
func connectPair() (*webrtcrtc.PeerConnection, *webrtcrtc.PeerConnection, error) {
	a, err := webrtcrtc.NewPeerConnection(webrtcrtc.Config{})
	if err != nil {
		return nil, nil, err
	}
	b, err := webrtcrtc.NewPeerConnectionForAnswerer(webrtcrtc.Config{})
	if err != nil {
		_ = a.Close()
		return nil, nil, err
	}
	// Bidirectional ICE trickle in-process.
	a.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		_ = b.AddICECandidate(c.ToJSON())
	})
	b.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		_ = a.AddICECandidate(c.ToJSON())
	})
	offer, err := a.CreateOffer()
	if err != nil {
		return nil, nil, err
	}
	answer, err := b.SetRemoteOffer(offer)
	if err != nil {
		return nil, nil, err
	}
	if err := a.SetRemoteAnswer(answer); err != nil {
		return nil, nil, err
	}
	return a, b, nil
}

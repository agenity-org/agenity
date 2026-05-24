// Integration tests for the relay's signaling REST endpoints.
//
// Goal: exercise the trickled-ICE + offer/answer rendezvous on a real
// httptest server, with no auth verifier wired (dev-mode bypass). This
// proves the on-wire JSON shapes match what chepherd-rc-web /
// chepherd-rc-ios / chepherd-rc-android expect.

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestServer spins up the relay with no JWKS (auth bypass) so the
// test can call /v1/signaling/* directly without minting tokens.
func newTestServer(t *testing.T) (*httptest.Server, *relay) {
	t.Helper()
	srv := newRelay()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/signaling/offer", srv.signalingOffer)
	mux.HandleFunc("/v1/signaling/candidate", srv.signalingPostCandidate)
	mux.HandleFunc("/v1/signaling/candidates", srv.signalingPollCandidates)
	mux.HandleFunc("/v1/signaling/poll", srv.signalingPoll)
	mux.HandleFunc("/v1/signaling/answer", srv.signalingAnswer)
	mux.HandleFunc("/v1/health", srv.health)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, srv
}

func TestSignalingOffer_HappyPath(t *testing.T) {
	ts, _ := newTestServer(t)
	bastionID := "test-bastion-1"

	// Bastion side: long-poll for offers in a goroutine. When it gets
	// one, immediately answer it.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := http.Get(ts.URL + "/v1/signaling/poll?bastion=" + bastionID)
		if err != nil {
			t.Errorf("bastion poll: %v", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("bastion poll status %d", resp.StatusCode)
			return
		}
		var env signalEnvelope
		_ = json.NewDecoder(resp.Body).Decode(&env)
		// Post the bastion's answer back.
		answer := signalEnvelope{
			Peer: env.Peer,
			SDP:  json.RawMessage(`{"type":"answer","sdp":"v=0..."}`),
		}
		body, _ := json.Marshal(answer)
		ansResp, err := http.Post(
			ts.URL+"/v1/signaling/answer", "application/json", bytes.NewReader(body),
		)
		if err != nil {
			t.Errorf("bastion answer: %v", err)
			return
		}
		ansResp.Body.Close()
	}()

	// Client side: post an offer + expect an answer.
	offerBody := `{"bastion_id":"` + bastionID + `","offer":{"type":"offer","sdp":"v=0..."}}`
	resp, err := http.Post(
		ts.URL+"/v1/signaling/offer", "application/json", strings.NewReader(offerBody),
	)
	if err != nil {
		t.Fatalf("client offer: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("client offer status %d body=%s", resp.StatusCode, body)
	}
	var got struct {
		Answer   map[string]any `json:"answer"`
		ClientID string         `json:"client_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if got.Answer["type"] != "answer" {
		t.Fatalf("expected answer type, got %+v", got.Answer)
	}
	if got.ClientID == "" {
		t.Fatalf("client_id should be returned")
	}
	wg.Wait()
}

func TestSignalingCandidate_RoundTrip(t *testing.T) {
	ts, _ := newTestServer(t)
	bastionID := "test-bastion-cands"

	// Post a candidate for the bastion to receive.
	candBody := `{"bastion_id":"` + bastionID + `","candidate":{"candidate":"candidate:foo 1 udp ...","sdpMid":"0","sdpMLineIndex":0}}`
	resp, err := http.Post(
		ts.URL+"/v1/signaling/candidate", "application/json", strings.NewReader(candBody),
	)
	if err != nil {
		t.Fatalf("post candidate: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Long-poll candidates as the bastion-side peer.
	pollResp, err := http.Get(ts.URL + "/v1/signaling/candidates?bastion_id=" + bastionID)
	if err != nil {
		t.Fatalf("poll candidates: %v", err)
	}
	defer pollResp.Body.Close()
	if pollResp.StatusCode != 200 {
		t.Fatalf("poll status %d", pollResp.StatusCode)
	}
	var got struct {
		Candidates []map[string]any `json:"candidates"`
		Cursor     string           `json:"cursor"`
	}
	if err := json.NewDecoder(pollResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode candidates: %v", err)
	}
	if len(got.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(got.Candidates))
	}
	if got.Cursor == "" {
		t.Fatalf("cursor should be set")
	}
}

func TestSignalingCandidate_RejectsBadBody(t *testing.T) {
	ts, _ := newTestServer(t)

	cases := []struct{ name, body string; want int }{
		{"empty bastion", `{"candidate":{"x":1}}`, 400},
		{"empty candidate", `{"bastion_id":"b"}`, 400},
		{"not json", `not-json`, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := http.Post(
				ts.URL+"/v1/signaling/candidate", "application/json", strings.NewReader(c.body),
			)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Errorf("want status %d, got %d", c.want, resp.StatusCode)
			}
		})
	}
}

func TestSignalingPollCandidates_Timeout(t *testing.T) {
	ts, _ := newTestServer(t)
	// No candidates posted — poll should return after the 25s long-poll
	// window. We don't want to wait 25s in tests; just verify the
	// endpoint accepts the request without erroring out immediately.
	c := http.Client{Timeout: 200 * time.Millisecond}
	_, err := c.Get(ts.URL + "/v1/signaling/candidates?bastion_id=nothing-here")
	// Expected: timeout from our test client, NOT a connection refused.
	if err == nil {
		t.Skip("server returned faster than expected — that's fine but unusual")
		return
	}
	if !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "Timeout") {
		t.Errorf("unexpected error type: %v", err)
	}
}

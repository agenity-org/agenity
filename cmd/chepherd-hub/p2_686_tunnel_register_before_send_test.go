// cmd/chepherd-hub/p2_686_tunnel_register_before_send_test.go pins #686:
// the relay round-trip must register its response waiter BEFORE sending
// the frame to the runner. The old shape (send → awaitResponse) had a
// race: a fast runner reply reached dispatch() before the waiter
// existed, was dropped as unsolicited, and the relay handler blocked
// until relayRequestTimeout → spurious 504. The F7 live walk flaked on
// exactly this under CI load.
//
// The test injects sendFn so the "runner" responds SYNCHRONOUSLY inside
// send — the most extreme version of the race. With register-before-send
// the response matches; with the old ordering this test deadlocks until
// ctx timeout and fails.
//
// Refs #686.
package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestP2_686_RoundTrip_MatchesResponseArrivingDuringSend(t *testing.T) {
	tn := &tunnel{
		orgID:   "bob.example",
		pending: map[string]chan *relayFrame{},
	}
	// Runner replies synchronously inside send — before send even returns.
	tn.sendFn = func(f *relayFrame) error {
		tn.dispatch(&relayFrame{
			RequestID: f.RequestID,
			Direction: "to-hub",
			Status:    200,
			Body:      f.Body,
		})
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := tn.roundTrip(ctx, &relayFrame{
		RequestID: "req-1",
		Direction: "to-runner",
		Method:    "POST",
		Path:      "/a2a/123/jsonrpc",
		Body:      []byte("opaque"),
	})
	if err != nil {
		t.Fatalf("roundTrip: %v (response dispatched during send was dropped — register-before-send broken)", err)
	}
	if resp.Status != 200 || string(resp.Body) != "opaque" {
		t.Fatalf("resp = %+v, want status 200 + echoed body", resp)
	}
}

func TestP2_686_RoundTrip_TimesOutWhenRunnerSilent(t *testing.T) {
	tn := &tunnel{
		orgID:   "bob.example",
		pending: map[string]chan *relayFrame{},
	}
	tn.sendFn = func(f *relayFrame) error { return nil } // runner never replies

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := tn.roundTrip(ctx, &relayFrame{RequestID: "req-2", Direction: "to-runner"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	// The slot must be cleaned up after timeout.
	tn.mu.Lock()
	_, leaked := tn.pending["req-2"]
	tn.mu.Unlock()
	if leaked {
		t.Fatal("pending slot leaked after timeout")
	}
}

func TestP2_686_RoundTrip_ClosedTunnelErrors(t *testing.T) {
	tn := &tunnel{
		orgID:   "bob.example",
		pending: map[string]chan *relayFrame{},
		closed:  true,
	}
	_, err := tn.roundTrip(context.Background(), &relayFrame{RequestID: "req-3"})
	if err == nil {
		t.Fatal("expected error on closed tunnel, got nil")
	}
}

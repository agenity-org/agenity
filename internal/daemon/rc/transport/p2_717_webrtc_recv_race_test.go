// internal/daemon/rc/transport/p2_717_webrtc_recv_race_test.go pins #717:
// the inbound recvBuffer enqueue (pion OnMessage) must not panic
// "send on closed channel" when it races Close(). Pre-fix Close()
// closed recvBuffer while OnMessage's goroutine could be mid-send;
// the fix stops closing recvBuffer (Recv terminates via t.closed).
// 6th member of the close-during-send family (#686/#703/#688/#711/#715).
package transport

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestP2_717_WebRTC_EnqueueRecvRacesClose_NoPanic(t *testing.T) {
	// Bare transport — Close()'s dc/pc are nil-guarded, so no real pion
	// objects are needed to exercise the recvBuffer enqueue↔close race.
	tr := &WebRTCTransport{
		recvBuffer: make(chan []byte, RecvBufferSize),
		closed:     make(chan struct{}),
	}

	var wg sync.WaitGroup
	// Flood inbound enqueues (simulating pion OnMessage on its goroutine).
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5000; j++ {
				tr.enqueueRecv([]byte("frame"))
			}
		}()
	}
	// Close mid-flight.
	time.Sleep(2 * time.Millisecond)
	_ = tr.Close()
	wg.Wait()
	// No panic under -race = the primary pass. Post-close, Recv may first
	// drain buffered frames (select picks randomly among ready cases),
	// then must reach ErrClosed within a bounded number of calls.
	got := false
	for i := 0; i < RecvBufferSize+2; i++ {
		if _, err := tr.Recv(context.Background()); err == ErrClosed {
			got = true
			break
		}
	}
	if !got {
		t.Fatalf("Recv never returned ErrClosed after Close within %d calls", RecvBufferSize+2)
	}
}

func TestP2_717_WebRTC_RecvTerminatesWithoutRecvBufferClose(t *testing.T) {
	tr := &WebRTCTransport{
		recvBuffer: make(chan []byte, RecvBufferSize),
		closed:     make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() { _, err := tr.Recv(context.Background()); done <- err }()
	time.Sleep(10 * time.Millisecond)
	_ = tr.Close() // closes t.closed only; Recv must unblock via that
	select {
	case err := <-done:
		if err != ErrClosed {
			t.Fatalf("Recv unblocked with %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not terminate on Close (relied on recvBuffer close?)")
	}
}

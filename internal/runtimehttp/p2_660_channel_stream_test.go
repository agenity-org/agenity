// internal/runtimehttp/p2_660_channel_stream_test.go pins #660: the
// transcript broadcaster wakes subscribers on notify (and only for the
// right team), drops onto a full buffer without blocking, and the SSE
// handler emits a tick after a notify.
package runtimehttp

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestP2_660_Broadcaster_NotifyWakesOnlyThatTeam(t *testing.T) {
	b := newTranscriptBroadcaster()
	trio, unsubTrio := b.subscribe("trio")
	defer unsubTrio()
	scrum, unsubScrum := b.subscribe("scrum")
	defer unsubScrum()

	b.notify("trio")
	select {
	case <-trio:
	case <-time.After(time.Second):
		t.Fatal("trio subscriber not woken by notify(trio)")
	}
	select {
	case <-scrum:
		t.Fatal("scrum subscriber wrongly woken by notify(trio)")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestP2_660_Broadcaster_NotifyNeverBlocks(t *testing.T) {
	b := newTranscriptBroadcaster()
	_, unsub := b.subscribe("trio")
	defer unsub()
	// Buffer is 1; hammer many notifies with no reader draining — must
	// not block (excess ticks dropped, next fetch is full-state anyway).
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			b.notify("trio")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notify blocked on a full subscriber buffer")
	}
}

func TestP2_660_Broadcaster_UnsubStopsDelivery(t *testing.T) {
	b := newTranscriptBroadcaster()
	ch, unsub := b.subscribe("trio")
	unsub()
	b.notify("trio") // must not panic (closed/removed) and not deliver
	select {
	case <-ch:
		// ch is not closed by unsub (we just remove it); a stray tick is
		// acceptable only if it raced before unsub — here it must be empty.
		t.Fatal("delivery after unsub")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestP2_660_StreamHandler_EmitsTickAfterNotify(t *testing.T) {
	s := &Server{transcripts: newTranscriptBroadcaster()} // rt nil → activity arm inert
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.teamTranscriptStream(w, r, "trio")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	// First event is the connect tick.
	if !scanForTick(t, sc) {
		t.Fatal("no connect tick")
	}
	// Notify → a message tick must arrive.
	go func() { time.Sleep(50 * time.Millisecond); s.transcripts.notify("trio") }()
	if !scanForTick(t, sc) {
		t.Fatal("no tick after notify")
	}
}

func scanForTick(t *testing.T, sc *bufio.Scanner) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sc.Scan() {
		if strings.HasPrefix(sc.Text(), "event: tick") {
			return true
		}
	}
	return false
}

// internal/federation/p1_703_discoverer_close_race_test.go pins #703:
// HostedRegistryDiscoverer.Run must not return while any of its one-shot
// poll/announce goroutines is still able to send on `out` — callers
// (Federation.Run) close that channel right after Run returns, and a
// late send panics ("send on closed channel"). The test drives many
// rapid cancel→close cycles against a slow registry so in-flight polls
// overlap the close window; pre-fix this panics within a few cycles.
package federation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestP1_703_RunWaitsForInflightPolls_NoSendAfterClose(t *testing.T) {
	// Slow registry: every /peers response takes a moment, widening the
	// window where a pollOnce is mid-flight when ctx cancels.
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"peers": []map[string]string{{"sid": "peer-x", "url": "http://example.invalid"}},
		})
	}))
	defer reg.Close()

	for i := 0; i < 30; i++ {
		d := &HostedRegistryDiscoverer{
			RegistryURL:    reg.URL,
			SelfSID:        "self",
			SelfURL:        "http://self.invalid",
			AnnouncePeriod: time.Millisecond,
			PollPeriod:     time.Millisecond,
		}
		ctx, cancel := context.WithCancel(context.Background())
		out := make(chan PeerAnnouncement, 1) // tiny buffer → senders hit the select often
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Run(ctx, out)
		}()
		// Drain a little so polls are demonstrably active, then cancel
		// mid-flight and close as soon as Run returns — exactly what
		// Federation.Run does (wg.Wait → close).
		deadline := time.After(20 * time.Millisecond)
	drain:
		for {
			select {
			case <-out:
			case <-deadline:
				break drain
			}
		}
		cancel()
		wg.Wait()  // Run returned — per #703 ownership, no sender may remain
		close(out) // pre-fix: in-flight pollOnce panics here
	}
}

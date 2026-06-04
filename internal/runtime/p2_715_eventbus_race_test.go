// internal/runtime/p2_715_eventbus_race_test.go pins #715: eventBuffer
// must not panic "send on closed channel" when push() races unsub().
// Pre-fix, push snapshotted subs then sent OUTSIDE the lock while unsub
// closed channels UNDER the lock — the classic close-during-send race
// (5th member of the #686/#703/#688/#711 family). The fix fans out
// under the lock so send and close are serialized.
//
// This test hammers concurrent publish + subscribe/unsub churn; under
// -race it deadlock-frees and panic-frees only when the ordering holds.
package runtime

import (
	"sync"
	"testing"
	"time"
)

func TestP2_715_EventBuffer_PushUnsubNoSendOnClosed(t *testing.T) {
	b := &eventBuffer{max: 64}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Publisher: push as fast as possible.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				b.push(Event{Kind: "test", Body: "x"})
			}
		}
	}()

	// Churn: many short-lived subscribers that subscribe then immediately
	// unsub (close) — maximizing the close-during-send window.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				ch, unsub := b.subscribe()
				// drain a little so a send is likely in-flight at unsub
				select {
				case <-ch:
				default:
				}
				unsub()
			}
		}
	}()

	time.Sleep(750 * time.Millisecond)
	close(stop)
	wg.Wait()
	// Reaching here without a panic (esp. under -race) is the pass.
}

// A subscriber that never drains must not block the publisher (buffered
// drop semantics preserved by the under-lock send).
func TestP2_715_EventBuffer_SlowSubscriberDoesNotBlockPush(t *testing.T) {
	b := &eventBuffer{max: 64}
	_, unsub := b.subscribe() // never drained
	defer unsub()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			b.push(Event{Kind: "flood"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("push blocked on a full/undrained subscriber buffer (drop semantics broken)")
	}
}

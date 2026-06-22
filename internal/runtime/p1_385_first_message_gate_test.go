// internal/runtime/p1_385_first_message_gate_test.go — pins #385 P1:
// the silence-window completer in pumpPTYToBroker must NOT fire on
// pure-startup-chrome content (banner + permission warning) before
// claude-code emits its prompt cursor (❯). Only after the cursor
// appears does silence == "response complete".
//
// Pre-fix (#381 silence-only): the FIRST message-send to a freshly-
// spawned agent's completer fired during banner-paint silence and
// captured chrome — task.history's "agent" message contained
// claude-code's banner instead of the actual reply.
//
// Subsequent messages worked correctly because the cursor was
// already in the ring by then.
//
// Refs #385 P1 #381 #379 #225.
package runtime

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/a2a"
)

// TestP1_385_StartupChromeWithoutCursor_DoesNotFireCompleter — push
// claude-code's banner WITHOUT the prompt cursor and assert the
// completer does NOT fire during the silence window. The pump
// should re-arm the silence timer and keep waiting.
func TestP1_385_StartupChromeWithoutCursor_DoesNotFireCompleter(t *testing.T) {
	t.Setenv("CHEPHERD_A2A_SILENCE_WINDOW_MS", "60")
	src := newFakeSubscriberSource(16)
	task := &a2a.Task{ID: "t-385-banner", ContextID: "ctx-385", Kind: "task"}
	pub := newFakePublisher()

	var mu sync.Mutex
	var calls int
	completer := func(_, _ string) {
		mu.Lock()
		defer mu.Unlock()
		calls++
	}

	go pumpPTYToBroker(pub, src, task, completer, nil)
	time.Sleep(15 * time.Millisecond) // let pump subscribe

	// Banner + permission warning chunks — no prompt cursor (❯) yet.
	src.PushChunk([]byte("Welcome to Claude Code\n"))
	src.PushChunk([]byte("To grant tool access, type 'yes' at the prompt\n"))
	// Wait well past silence window. With the #385 gate, the completer
	// must NOT fire — the cursor hasn't been observed.
	time.Sleep(250 * time.Millisecond)

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 0 {
		t.Errorf("completer fired %d times during banner-only silence; want 0 (gate broken — chrome would be captured as agent response)", got)
	}

	// Confirm the pump's still alive — close the channel to make it exit.
	close(src.sub.Ch)
	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump didn't exit on channel close after banner-only — finalize path broken")
	}
}

// TestP1_385_CursorThenSilence_FiresCompleter — push banner WITHOUT
// cursor, observe gate hold, then push reply WITH cursor, observe
// completer fire. Locks the two-phase contract.
func TestP1_385_CursorThenSilence_FiresCompleter(t *testing.T) {
	t.Setenv("CHEPHERD_A2A_SILENCE_WINDOW_MS", "60")
	src := newFakeSubscriberSource(16)
	task := &a2a.Task{ID: "t-385-cursor", ContextID: "ctx-385", Kind: "task"}
	pub := newFakePublisher()

	var mu sync.Mutex
	var calls int
	var capturedResponse string
	completer := func(_, response string) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		capturedResponse = response
	}

	go pumpPTYToBroker(pub, src, task, completer, nil)
	time.Sleep(15 * time.Millisecond)

	// Phase 1: banner chunks WITHOUT cursor — gate must hold.
	src.PushChunk([]byte("Welcome to Claude Code\n"))
	src.PushChunk([]byte("To grant tool access, type 'yes'\n"))
	time.Sleep(150 * time.Millisecond) // > silence window

	mu.Lock()
	if calls != 0 {
		mu.Unlock()
		t.Fatalf("completer fired during banner-only phase; got %d, want 0", calls)
	}
	mu.Unlock()

	// Phase 2: cursor arrives, then the actual reply. Now silence
	// finalize SHOULD fire — and only the reply (not banner chunks)
	// matters per the spec, though our cheap heuristic still includes
	// the full buffer; future enhancement could slice at cursor.
	src.PushChunk([]byte("❯ pong\n"))

	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump didn't fire silence-finalize after cursor + silence (#385 gate stuck)")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("completer fired %d times after cursor + silence, want 1", calls)
	}
	if !strings.Contains(capturedResponse, "pong") {
		t.Errorf("captured response = %q, want to contain 'pong'", capturedResponse)
	}
}

// TestP1_385_BannerOnlyChannelClose_StillFinalizes — even when only
// banner content arrives + the channel closes (e.g. agent died on
// boot before printing a cursor), the finalize path must still fire
// so the Task transitions out of "working". Channel-close + sub.Done
// finalizes are intentionally NOT gated on cursor presence.
func TestP1_385_BannerOnlyChannelClose_StillFinalizes(t *testing.T) {
	t.Setenv("CHEPHERD_A2A_SILENCE_WINDOW_MS", "5000")
	src := newFakeSubscriberSource(8)
	task := &a2a.Task{ID: "t-385-died", ContextID: "ctx-385", Kind: "task"}
	pub := newFakePublisher()

	var mu sync.Mutex
	var calls int
	completer := func(_, _ string) {
		mu.Lock()
		defer mu.Unlock()
		calls++
	}

	go pumpPTYToBroker(pub, src, task, completer, nil)
	time.Sleep(15 * time.Millisecond)
	src.PushChunk([]byte("Welcome to Claude Code\n"))
	time.Sleep(15 * time.Millisecond)
	close(src.sub.Ch) // simulate agent dying mid-boot

	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("channel-close didn't fire finalize on banner-only — fast-exit agents would stay 'working' forever")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("completer fired %d times on channel-close, want 1 (sub.Done path must bypass cursor gate)", calls)
	}
}

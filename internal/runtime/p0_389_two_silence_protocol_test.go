// internal/runtime/p0_389_two_silence_protocol_test.go — pins #389 P0:
// the two-silence protocol replaces #385's cursor-gate + #387's
// mark-coordination. Both prior approaches failed live because:
//
//   - #385 cursor gate ran against full responseBuf, but real
//     claude-code banners contain ❯ inside TUI chrome (5+
//     occurrences per architect grep)
//   - #387 mark-coordination raced: Deliver.MarkSendNow fired ~100ms
//     after subscribe, but banner chunks arrived ~500ms later, so
//     sendOffset=0 → slice = full buf including banner
//
// Two-silence is structurally race-free: pump observes silence from
// inside its own goroutine. Silence by definition only follows
// content, so sendOffset advances only after observable activity.
// No coordination with Deliver is required.
//
// FIXTURE DISCIPLINE per memory feedback_real_fixtures_not_minimal_repro:
// these tests use real-banner byte sequences (multiple ❯ chars
// inside TUI chrome) AND model concurrent timing explicitly via
// time.Sleep separating banner from reply.
//
// Refs #389 P0 #387 P0 #385 P1 #381 #225.
package runtime

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/a2a"
)

// realBanner is the byte sequence claude-code paints between PTY
// open and the first idle prompt. Multiple ❯ chars are present in
// TUI chrome — defeats any naive "cursor anywhere → response done"
// check.
var realBanner = [][]byte{
	[]byte("\x1b[2J\x1b[H"),
	[]byte("●  Welcome to Claude Code!\n"),
	[]byte("   /help · /status · /clear\n"),
	[]byte(" ╭──────────────────────────────────────────────╮\n"),
	[]byte(" │ ❯ /help    Show help                         │\n"),
	[]byte(" │ ❯ /clear   Clear conversation                │\n"),
	[]byte(" ╰──────────────────────────────────────────────╯\n"),
	[]byte(" Bypass Permissions ❯ ON\n"),
	[]byte("\x1b[3;4HReady — ❯ \n"),
}

// realReply is the byte sequence after agent receives a message:
// brief input echo, "thinking" status, response, prompt return.
var realReply = [][]byte{
	[]byte("\n● Thinking…\n"),
	[]byte("\x1b[1m●\x1b[0m alive\n"),
	[]byte("\n❯ "),
}

func pushAll(src *fakeSubscriberSource, chunks [][]byte) {
	for _, c := range chunks {
		src.PushChunk(c)
	}
}

// TestP0_389_TwoSilence_BannerThenReply — the canonical production
// timing: banner paints → silence → reply arrives → silence. First
// silence marks sendOffset; second silence finalizes buf[sendOffset:].
// Captured response MUST contain only reply content, not banner.
func TestP0_389_TwoSilence_BannerThenReply(t *testing.T) {
	t.Setenv("CHEPHERD_A2A_SILENCE_WINDOW_MS", "60")
	src := newFakeSubscriberSource(64)
	task := &a2a.Task{ID: "t-389-canon", ContextID: "ctx-389", Kind: "task"}
	pub := newFakePublisher()

	var mu sync.Mutex
	var calls int
	var captured string
	completer := func(_, response string) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		captured = response
	}

	go pumpPTYToBroker(pub, src, task, completer)
	time.Sleep(15 * time.Millisecond) // pump subscribes

	// Phase 1: real banner with ❯ chars in chrome.
	pushAll(src, realBanner)
	time.Sleep(90 * time.Millisecond) // > silence (60ms) → first silence fires → sendOffset = banner end

	// Phase 2: real reply.
	pushAll(src, realReply)
	// Wait > silence for second silence → finalize.
	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump never finalized via two-silence")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("completer called %d times, want 1", calls)
	}
	if !strings.Contains(captured, "alive") {
		t.Errorf("captured response missing 'alive': %q", captured)
	}
	// Banner sentinels MUST be excluded.
	bannerSentinels := []string{
		"Welcome to Claude Code",
		"Bypass Permissions",
		"/clear   Clear conversation",
		"Ready — ❯",
	}
	for _, s := range bannerSentinels {
		if strings.Contains(captured, s) {
			t.Errorf("captured response contains banner sentinel %q — two-silence boundary broken: full captured=\n%s", s, captured)
		}
	}
}

// TestP0_389_TwoSilence_RaceArchitectScenario reproduces the
// architect's #389 race: pump subscribes BEFORE banner arrives
// (which is the realistic case). Pre-#387/#388 the mark-based fix
// raced because MarkSendNow fired at T~100ms but banner arrived at
// T~500ms. Under two-silence this is irrelevant — pump just
// observes silences regardless of when chunks arrive.
//
// We model the race by sleeping AFTER subscribe but BEFORE pushing
// the banner — simulating the ~400ms PTY-latency gap.
func TestP0_389_TwoSilence_RaceArchitectScenario(t *testing.T) {
	t.Setenv("CHEPHERD_A2A_SILENCE_WINDOW_MS", "60")
	src := newFakeSubscriberSource(64)
	task := &a2a.Task{ID: "t-389-race", ContextID: "ctx-389", Kind: "task"}
	pub := newFakePublisher()

	var captured string
	completer := func(_, response string) { captured = response }

	go pumpPTYToBroker(pub, src, task, completer)
	time.Sleep(15 * time.Millisecond) // pump subscribes

	// Simulate the architect's racing condition: 100ms gap before
	// banner chunks arrive (vs pre-#388 mark-fires-immediately race).
	// Pre-#387 mark approach: MarkSendNow fires here at T~100ms,
	// sendOffset=0, banner arriving next contaminates response.
	// Two-silence: pump simply waits for actual chunks.
	time.Sleep(100 * time.Millisecond)

	pushAll(src, realBanner)
	time.Sleep(90 * time.Millisecond) // first silence → sendOffset
	pushAll(src, realReply)

	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("two-silence didn't finalize despite race-modeling timing")
	}

	if !strings.Contains(captured, "alive") {
		t.Errorf("captured missing 'alive' under race scenario: %q", captured)
	}
	if strings.Contains(captured, "Welcome to Claude Code") {
		t.Errorf("captured contains banner under race scenario — race-immunity broken: %q", captured)
	}
}

// TestP0_389_TwoSilence_SubsequentMessage_NoBanner — steady-state
// session, no banner. Input echo + response is the only content.
// First silence marks (after echo); second silence finalizes
// (after response). Echo content is excluded from the response slice.
func TestP0_389_TwoSilence_SubsequentMessage_NoBanner(t *testing.T) {
	t.Setenv("CHEPHERD_A2A_SILENCE_WINDOW_MS", "60")
	src := newFakeSubscriberSource(32)
	task := &a2a.Task{ID: "t-389-sub", ContextID: "ctx-389", Kind: "task"}
	pub := newFakePublisher()

	var captured string
	completer := func(_, response string) { captured = response }

	go pumpPTYToBroker(pub, src, task, completer)
	time.Sleep(15 * time.Millisecond)

	// Phase 1: input-echo of operator's typed message (PTY echo).
	src.PushChunk([]byte("Reply with one word: pong\n"))
	time.Sleep(90 * time.Millisecond) // first silence → mark
	// Phase 2: claude's reply.
	src.PushChunk([]byte("pong\n"))

	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("subsequent-message two-silence didn't finalize")
	}

	if !strings.Contains(captured, "pong") {
		t.Errorf("captured missing 'pong': %q", captured)
	}
	if strings.Contains(captured, "Reply with one word") {
		t.Errorf("captured contains echo (pre-mark content leaked): %q", captured)
	}
}

// TestP0_389_TwoSilence_OneBurstSubDoneFinalizes — degenerate case
// where the entire response arrives in one burst with no silence
// between phases. sub.Done finalizes whatever's in the slice
// (sendOffset=-1 ⇒ full buf, otherwise post-mark slice). This
// guarantees fast-exiting agents don't leave Task in 'working'.
func TestP0_389_TwoSilence_OneBurstSubDoneFinalizes(t *testing.T) {
	t.Setenv("CHEPHERD_A2A_SILENCE_WINDOW_MS", "5000") // long
	src := newFakeSubscriberSource(8)
	task := &a2a.Task{ID: "t-389-burst", ContextID: "ctx-389", Kind: "task"}
	pub := newFakePublisher()

	var captured string
	completer := func(_, response string) { captured = response }

	go pumpPTYToBroker(pub, src, task, completer)
	time.Sleep(15 * time.Millisecond)
	src.PushChunk([]byte("burst-content"))
	time.Sleep(15 * time.Millisecond)
	close(src.sub.Ch) // close before any silence fires

	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("burst+close didn't finalize")
	}
	if !strings.Contains(captured, "burst-content") {
		t.Errorf("captured = %q, want 'burst-content' (sub.Done fallback didn't capture full buf)", captured)
	}
}

// TestP0_389_RealBanner_HasMultipleCursors_FixtureGuard locks the
// fixture-drift contract per memory feedback_real_fixtures_not_minimal_repro:
// if real claude-code TUI evolves and drops the ❯ chars from
// banner, this CI assertion fires loudly with instructions to re-
// grep podman logs.
func TestP0_389_RealBanner_HasMultipleCursors_FixtureGuard(t *testing.T) {
	t.Parallel()
	cursor := []byte{0xe2, 0x9d, 0xaf}
	var count int
	for _, c := range realBanner {
		// count occurrences in each chunk
		for i := 0; i+len(cursor) <= len(c); i++ {
			if string(c[i:i+len(cursor)]) == string(cursor) {
				count++
			}
		}
	}
	if count < 3 {
		t.Fatalf("realBanner has %d ❯ chars; architect grep'd 5+ in real podman logs — fixture DRIFTED. Re-grep podman logs for current chrome content + update realBanner.", count)
	}
}

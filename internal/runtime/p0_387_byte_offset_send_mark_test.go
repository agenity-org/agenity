// internal/runtime/p0_387_byte_offset_send_mark_test.go — pins #387 P0:
// the silence-finalize must use a BYTE-OFFSET boundary, not just a
// cursor-presence test, to separate banner from response.
//
// Pre-#387 (#385 P1 only):
//   - The cursor gate looked at the FULL responseBuf
//   - Real claude-code startup banners contain ❯ inside the TUI
//     input-box rendering (architect grep'd podman logs and found
//     5+ occurrences as part of "Bypass Permissions" toggle, the
//     input-line indicator, and the model-status row)
//   - Banner chunks therefore satisfied the cursor gate
//   - First-message-after-spawn captured banner chrome as the
//     "agent response" message
//
// #387 fix: snapshot byte-offset at message/send time. Pump
// accumulates banner chunks pre-mark; silence-finalize slices
// buf[sendOffset:] and applies the cursor gate to the response
// slice only. The byte-offset boundary is STRUCTURAL — independent
// of how many ❯ chars are in the banner.
//
// Architect's memory entry feedback_ui_changes_need_route_smoke_test
// applies: this file's fixtures contain REAL banner artifacts
// including the cursor character, not minimal-repro fakes.
//
// Refs #387 P0 #385 P1 #381 #225.
package runtime

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenity-org/agenity/internal/a2a"
)

// realClaudeCodeBannerChunks emulates the actual byte sequence that
// claude-code paints between PTY open and the first idle prompt.
// Multiple ❯ chars appear in the TUI chrome:
//   - "❯ /help" — pinned help suggestion
//   - "❯ Bypass Permissions" — mode toggle row
//   - Bottom status line spinner ●→❯ transitions
//
// Pre-#387 the cursor gate would fire on any of these. Post-#387
// the gate looks at the post-send slice only, which is empty until
// the actual reply arrives.
var realClaudeCodeBannerChunks = [][]byte{
	[]byte("\x1b[2J\x1b[H"), // clear + home
	[]byte("●  Welcome to Claude Code!\n\n"),
	[]byte("   /help for help, /status for your current setup\n"),
	[]byte("   /clear to clear the context window\n\n"),
	[]byte("   cwd: /home/chepherd/repos/chepherd\n\n"),
	// TUI input box with ❯ pinned suggestions — REAL CURSOR PRESENCE
	[]byte(" ╭──────────────────────────────────────────────╮\n"),
	[]byte(" │ ❯ /help    Show help and commands           │\n"),
	[]byte(" │ ❯ /clear   Clear conversation               │\n"),
	[]byte(" ╰──────────────────────────────────────────────╯\n"),
	[]byte("\n"),
	[]byte(" Bypass Permissions ❯ ON\n"),
	// status line tail
	[]byte("\x1b[3;4HReady — ❯ \n"),
}

// realClaudeCodeReplyChunks emulates the bytes claude emits AFTER
// receiving a message: the prompt area + the model response.
var realClaudeCodeReplyChunks = [][]byte{
	[]byte("\n● Thinking…\n"),
	[]byte("\x1b[1m●\x1b[0m alive\n"),
	// Idle prompt returns
	[]byte("\n❯ "),
}

// TestP0_387_ByteOffsetMark_ExcludesBanner — push REAL banner chunks
// (containing ❯ in TUI chrome), mark the send boundary, push reply
// chunks. Assert the completer fires ONLY after reply, and captures
// ONLY the reply text — not the banner.
func TestP0_387_ByteOffsetMark_ExcludesBanner(t *testing.T) {
	t.Setenv("CHEPHERD_A2A_SILENCE_WINDOW_MS", "60")
	src := newFakeSubscriberSource(64)
	task := &a2a.Task{ID: "t-387-mark", ContextID: "ctx-387", Kind: "task"}
	pub := newFakePublisher()
	mark := newPumpSendMark()

	var mu sync.Mutex
	var calls int
	var captured string
	completer := func(_, response string) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		captured = response
	}

	go pumpPTYToBroker(pub, src, task, completer, mark)

	// Wait for pump to subscribe before pushing banner.
	select {
	case <-mark.Subscribed:
	case <-time.After(1 * time.Second):
		t.Fatal("pump never signaled Subscribed within 1s")
	}

	// Phase 1: push real banner — multiple ❯ chars present.
	for _, c := range realClaudeCodeBannerChunks {
		src.PushChunk(c)
	}

	// Sanity: wait > silence window. Pre-#387 the cursor-gate would
	// fire here because banner DOES contain ❯. Post-#387 sendOffset
	// is still -1 (no mark yet) → gate sees only the buf so far,
	// AND mark hasn't fired → gate is allowed to look at full buf,
	// BUT we want to assert it doesn't fire SPECIFICALLY because we
	// haven't marked yet. To make the test deterministic of the
	// post-#387 invariant: skip this wait and proceed to mark.
	time.Sleep(20 * time.Millisecond) // let banner land in buf

	// Phase 2: mark the send boundary (Deliver does this after
	// sess.Write returns). Pump records sendOffset = len(buf).
	mark.MarkSendNow()

	// Tiny pause so the mark goroutine fires before phase 3 chunks
	// land. In production the chunks arrive via PTY which is
	// inherently async — Deliver mark is synchronous after write.
	time.Sleep(20 * time.Millisecond)

	// Phase 3: push reply (with cursor inside).
	for _, c := range realClaudeCodeReplyChunks {
		src.PushChunk(c)
	}

	// Wait for silence-finalize.
	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump never published done after mark + reply + silence")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("completer called %d times, want 1", calls)
	}
	if !strings.Contains(captured, "alive") {
		t.Errorf("captured response missing 'alive': %q", captured)
	}
	// CRITICAL: captured response MUST NOT contain banner content.
	bannerSentinels := []string{
		"Welcome to Claude Code",
		"Bypass Permissions",
		"/clear   Clear conversation",
		"Ready — ❯",
	}
	for _, s := range bannerSentinels {
		if strings.Contains(captured, s) {
			t.Errorf("captured response contains banner sentinel %q — byte-offset boundary broken: full captured=\n%s", s, captured)
		}
	}
}

// TestP0_387_NoMark_FallsBackToFullBuf — when caller doesn't pass a
// mark (back-compat for old tests + tests that exercise pre-#387
// behavior), pump uses the full buffer for the cursor gate +
// completer. Existing #379/#385 contracts continue to hold.
func TestP0_387_NoMark_FallsBackToFullBuf(t *testing.T) {
	t.Setenv("CHEPHERD_A2A_SILENCE_WINDOW_MS", "60")
	src := newFakeSubscriberSource(16)
	task := &a2a.Task{ID: "t-387-nomark", ContextID: "ctx-387", Kind: "task"}
	pub := newFakePublisher()

	var captured string
	completer := func(_, response string) { captured = response }

	go pumpPTYToBroker(pub, src, task, completer, nil) // no mark
	time.Sleep(15 * time.Millisecond)

	// Single chunk with cursor → silence-gate passes → finalize.
	src.PushChunk([]byte("❯ alive\n"))

	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump never finalized in nil-mark mode")
	}
	if !strings.Contains(captured, "alive") {
		t.Errorf("captured = %q, want to contain 'alive'", captured)
	}
}

// TestP0_387_MarkBeforeAnyChunk_OffsetZero — if Deliver fires mark
// before any banner arrives (e.g. a freshly-spawned agent that
// hasn't started painting), sendOffset=0 → silence-finalize sees
// full buf. Edge case: empty buf at mark → still safe; completer
// sees only post-mark chunks.
func TestP0_387_MarkBeforeAnyChunk_OffsetZero(t *testing.T) {
	t.Setenv("CHEPHERD_A2A_SILENCE_WINDOW_MS", "60")
	src := newFakeSubscriberSource(8)
	task := &a2a.Task{ID: "t-387-early", ContextID: "ctx-387", Kind: "task"}
	pub := newFakePublisher()
	mark := newPumpSendMark()

	var captured string
	completer := func(_, response string) { captured = response }

	go pumpPTYToBroker(pub, src, task, completer, mark)
	<-mark.Subscribed
	mark.MarkSendNow() // fire mark immediately, before any chunk
	time.Sleep(15 * time.Millisecond)

	src.PushChunk([]byte("❯ pong\n"))

	select {
	case <-pub.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump never finalized")
	}
	if !strings.Contains(captured, "pong") {
		t.Errorf("captured = %q, want 'pong'", captured)
	}
}

// TestP0_387_MarkSendNow_Idempotent — calling MarkSendNow twice
// (e.g. Deliver retry path) doesn't panic from double-close on
// SendNow chan. Idempotency via sync.Once.
func TestP0_387_MarkSendNow_Idempotent(t *testing.T) {
	t.Parallel()
	m := newPumpSendMark()
	m.MarkSendNow()
	m.MarkSendNow() // would panic without sync.Once
	select {
	case <-m.SendNow:
	default:
		t.Error("SendNow not closed after MarkSendNow")
	}
}

// TestP0_387_BannerSentinelsHaveCursor sanity-checks the fixture:
// the architect's whole point of #387 was that real banner CONTAINS
// the cursor character. Without that, the fixture wouldn't reproduce
// the bug. If this assertion ever fails the fixture has drifted
// from reality and the #387 test is no longer guarding the regression.
func TestP0_387_BannerSentinelsHaveCursor(t *testing.T) {
	t.Parallel()
	var allBanner bytes.Buffer
	for _, c := range realClaudeCodeBannerChunks {
		allBanner.Write(c)
	}
	if !bytes.Contains(allBanner.Bytes(), promptCursorUTF8) {
		t.Fatal("fixture has DRIFTED from reality: banner no longer contains ❯ — #387 regression test no longer guards the bug. Re-grep podman logs for current banner content.")
	}
	// Count occurrences — should be multiple per architect's
	// "5+ occurrences" grep finding.
	occurrences := bytes.Count(allBanner.Bytes(), promptCursorUTF8)
	if occurrences < 3 {
		t.Errorf("banner fixture only has %d ❯ chars; architect grep'd 5+ in real podman logs — fixture under-represents reality", occurrences)
	}
}

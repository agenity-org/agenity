// internal/completion/detector_test.go — deterministic unit tests for
// the H2 CompletionDetector. Frozen-clock pattern: every test passes
// explicit time.Time values into OnChunk/Tick so there is no real-time
// sleeping in the detector path, no race-condition window, and no
// flake from CI hosts with different scheduling.
//
// Refs #225 row H2.
package completion

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)

func TestDetector_StartsWorking(t *testing.T) {
	t.Parallel()
	d := New("claude-code", t0)
	if got := d.State(); got != StateWorking {
		t.Errorf("initial state = %q, want %q", got, StateWorking)
	}
}

func TestDetector_ExitZeroCompleted(t *testing.T) {
	t.Parallel()
	d := New("claude-code", t0)
	d.OnExit(0)
	if got := d.State(); got != StateCompleted {
		t.Errorf("OnExit(0) state = %q, want %q", got, StateCompleted)
	}
}

func TestDetector_ExitNonZeroFailed(t *testing.T) {
	t.Parallel()
	for _, code := range []int{1, 2, 127, 130, 137} {
		d := New("claude-code", t0)
		d.OnExit(code)
		if got := d.State(); got != StateFailed {
			t.Errorf("OnExit(%d) state = %q, want %q", code, got, StateFailed)
		}
	}
}

func TestDetector_TickAfterExitIsNoOp(t *testing.T) {
	t.Parallel()
	d := New("claude-code", t0)
	d.OnExit(0)
	// Even a huge time leap won't flip away from completed.
	d.Tick(t0.Add(24 * time.Hour))
	if got := d.State(); got != StateCompleted {
		t.Errorf("Tick after OnExit(0): state = %q, want %q", got, StateCompleted)
	}
}

func TestDetector_IdleTransitionsToInputRequired(t *testing.T) {
	t.Parallel()
	d := New("claude-code", t0)
	// 5s < claude-code idleThresh (10s) → still working
	d.Tick(t0.Add(5 * time.Second))
	if got := d.State(); got != StateWorking {
		t.Errorf("5s idle: state = %q, want %q", got, StateWorking)
	}
	// 10s == threshold → flip to input-required
	d.Tick(t0.Add(10 * time.Second))
	if got := d.State(); got != StateInputRequired {
		t.Errorf("10s idle: state = %q, want %q", got, StateInputRequired)
	}
}

func TestDetector_NewChunkResetsToWorking(t *testing.T) {
	t.Parallel()
	d := New("claude-code", t0)
	d.Tick(t0.Add(15 * time.Second))
	if d.State() != StateInputRequired {
		t.Fatal("setup: expected InputRequired after 15s idle")
	}
	// Output arrives — should flip back to working (and not match prompt).
	d.OnChunk(t0.Add(15*time.Second), []byte("processing the request...\n"))
	if got := d.State(); got != StateWorking {
		t.Errorf("post-chunk state = %q, want %q", got, StateWorking)
	}
}

func TestDetector_PromptMarkerShortensThreshold(t *testing.T) {
	t.Parallel()
	d := New("claude-code", t0)
	// Output ending in claude prompt marker.
	d.OnChunk(t0, []byte("All done, awaiting input.\n│ > "))
	// 250ms after the chunk: short threshold met → input-required.
	d.Tick(t0.Add(250 * time.Millisecond))
	if got := d.State(); got != StateInputRequired {
		t.Errorf("prompt-match + 250ms idle: state = %q, want %q", got, StateInputRequired)
	}
}

func TestDetector_NoPromptMarker_LongThreshold(t *testing.T) {
	t.Parallel()
	d := New("claude-code", t0)
	d.OnChunk(t0, []byte("Some neutral output without a prompt suffix."))
	// 250ms with no prompt marker → still working (need full 10s).
	d.Tick(t0.Add(250 * time.Millisecond))
	if got := d.State(); got != StateWorking {
		t.Errorf("no prompt + 250ms idle: state = %q, want %q", got, StateWorking)
	}
}

func TestDetector_AiderPromptMarkers(t *testing.T) {
	t.Parallel()
	d := New("aider", t0)
	d.OnChunk(t0, []byte("changes applied\naider> "))
	d.Tick(t0.Add(300 * time.Millisecond))
	if got := d.State(); got != StateInputRequired {
		t.Errorf("aider prompt + 300ms: state = %q, want %q", got, StateInputRequired)
	}
}

func TestDetector_UnknownSlugUsesGenericProfile(t *testing.T) {
	t.Parallel()
	d := New("unknown-slug", t0)
	// Generic profile has 30s idleThresh.
	d.Tick(t0.Add(20 * time.Second))
	if got := d.State(); got != StateWorking {
		t.Errorf("20s idle (generic 30s thresh): state = %q, want %q", got, StateWorking)
	}
	d.Tick(t0.Add(31 * time.Second))
	if got := d.State(); got != StateInputRequired {
		t.Errorf("31s idle (generic 30s thresh): state = %q, want %q", got, StateInputRequired)
	}
}

func TestDetector_TailBoundedToMaxTail(t *testing.T) {
	t.Parallel()
	d := New("claude-code", t0)
	// Push 10× maxTail of unrelated bytes, then a chunk that ends with
	// the prompt marker. The prompt match still has to win — proves
	// the tail is being rolled and the suffix comparison still works.
	big := make([]byte, 10*maxTail)
	for i := range big {
		big[i] = 'x'
	}
	d.OnChunk(t0, big)
	d.OnChunk(t0, []byte("│ > "))
	d.Tick(t0.Add(250 * time.Millisecond))
	if got := d.State(); got != StateInputRequired {
		t.Errorf("post-rolled-tail prompt: state = %q, want %q", got, StateInputRequired)
	}
}

func TestDetector_ChunkLargerThanTail(t *testing.T) {
	t.Parallel()
	d := New("claude-code", t0)
	// Single chunk larger than maxTail, ending in prompt marker.
	prefix := make([]byte, 2*maxTail)
	for i := range prefix {
		prefix[i] = 'y'
	}
	chunk := append(prefix, []byte("│ > ")...)
	d.OnChunk(t0, chunk)
	d.Tick(t0.Add(250 * time.Millisecond))
	if got := d.State(); got != StateInputRequired {
		t.Errorf("big single chunk w/ prompt suffix: state = %q, want %q", got, StateInputRequired)
	}
}

func TestDetector_ExitOverridesIdleInputRequired(t *testing.T) {
	t.Parallel()
	d := New("claude-code", t0)
	d.Tick(t0.Add(30 * time.Second))
	if d.State() != StateInputRequired {
		t.Fatal("setup: expected InputRequired after 30s idle")
	}
	// Exit observed — overrides to completed.
	d.OnExit(0)
	if got := d.State(); got != StateCompleted {
		t.Errorf("OnExit(0) after InputRequired: state = %q, want %q", got, StateCompleted)
	}
}

func TestDetector_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	d := New("claude-code", t0)
	// 50 goroutines hammering OnChunk + Tick + State concurrently.
	// The mutex inside Detector must serialize cleanly; -race catches
	// any unprotected field.
	done := make(chan struct{})
	for i := 0; i < 50; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			d.OnChunk(t0.Add(time.Duration(i)*time.Millisecond), []byte("output\n"))
			d.Tick(t0.Add(time.Duration(i+1) * time.Second))
			_ = d.State()
		}(i)
	}
	for i := 0; i < 50; i++ {
		<-done
	}
}

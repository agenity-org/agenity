package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/agenity-org/agenity/internal/scrummaster"
)

// fakeShepherd captures events broadcast via Runtime.RecordEvent so the
// test can assert WithShepherd actually wires the broadcast path.
type fakeShepherd struct {
	mu   sync.Mutex
	seen []any
}

func (f *fakeShepherd) Observe(_ context.Context, evt any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, evt)
}

func (f *fakeShepherd) Judge(_ context.Context, _ string, _ []byte) (*scrummaster.Verdict, error) {
	return nil, nil
}

func (f *fakeShepherd) Alert(_ context.Context, _ *scrummaster.Verdict) error { return nil }

func (f *fakeShepherd) Run(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

func TestRuntime_WithShepherd_BroadcastsRecordEvent(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f := &fakeShepherd{}
	rt.WithShepherd(f)

	rt.RecordEvent(Event{Kind: "test-event", Actor: "operator"})
	rt.RecordEvent(Event{Kind: "test-event-2", Actor: "agent-1"})

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) != 2 {
		t.Errorf("scrummaster.Observe called %d times, want 2", len(f.seen))
	}
}

// TestRuntime_RecordEvent_NoShepherdIsSafe — RecordEvent works fine on
// a Runtime that never had WithShepherd called.
func TestRuntime_RecordEvent_NoShepherdIsSafe(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Should not panic.
	rt.RecordEvent(Event{Kind: "test-event", Actor: "operator"})
}

func TestRuntime_WithShepherd_Idempotent(t *testing.T) {
	t.Parallel()
	rt, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f1 := &fakeShepherd{}
	f2 := &fakeShepherd{}
	rt.WithShepherd(f1)
	rt.WithShepherd(f2) // replaces f1

	rt.RecordEvent(Event{Kind: "x"})

	f1.mu.Lock()
	if len(f1.seen) != 0 {
		t.Errorf("f1.seen = %d after replacement, want 0", len(f1.seen))
	}
	f1.mu.Unlock()

	f2.mu.Lock()
	if len(f2.seen) != 1 {
		t.Errorf("f2.seen = %d, want 1 (received the post-replacement event)", len(f2.seen))
	}
	f2.mu.Unlock()
}

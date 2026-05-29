package scrummaster

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

func TestNew_ReturnsShepherd(t *testing.T) {
	t.Parallel()
	s := New(JudgeConfig{})
	if s == nil {
		t.Fatal("New returned nil")
	}
}

func TestNewWithStore_RequiresStore(t *testing.T) {
	t.Parallel()
	// NewWithStore accepts a nil store but the tick loop's discovery
	// will fall back to file-on-disk. The constructor itself does not
	// error — confirm it returns a working ScrumMaster.
	s := NewWithStore(nil, Config{StateDir: t.TempDir()})
	if s == nil {
		t.Fatal("NewWithStore returned nil")
	}
}

func TestShepherdImpl_ObserveNoOps(t *testing.T) {
	t.Parallel()
	s := New(JudgeConfig{})
	// Scaffold Observe drops events without panicking.
	s.Observe(context.Background(), "any event payload")
	s.Observe(context.Background(), nil)
}

func TestShepherdImpl_JudgeReturnsNilNilWhenNoPrompt(t *testing.T) {
	t.Parallel()
	s := New(JudgeConfig{})
	v, err := s.Judge(context.Background(), "sess-1", []byte("recent output"))
	if err != nil {
		t.Errorf("Judge err = %v, want nil", err)
	}
	if v != nil {
		t.Errorf("Judge verdict = %+v, want nil", v)
	}
}

func TestShepherdImpl_AlertReturnsNil(t *testing.T) {
	t.Parallel()
	s := New(JudgeConfig{})
	if err := s.Alert(context.Background(), &Verdict{}); err != nil {
		t.Errorf("Alert err = %v, want nil", err)
	}
}

// TestShepherdImpl_Run_RespectsContextCancel — Run exits cleanly when
// ctx is cancelled before the first tick fires.
func TestShepherdImpl_Run_RespectsContextCancel(t *testing.T) {
	t.Parallel()
	s := NewWithStore(nil, Config{
		TickInterval: 10 * time.Millisecond,
		StateDir:     t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Run err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after ctx cancel")
	}
}

// TestShepherdImpl_Run_TickDiscoversSessions — when Run starts with
// SessionRepository sessions already present, tickOnce discovers
// them + writes back next_tick_at state.
func TestShepherdImpl_Run_TickDiscoversSessions(t *testing.T) {
	t.Parallel()
	store, err := sqlite.NewStore(context.Background(), filepath.Join(t.TempDir(), "shep.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Seed two sessions in the repository.
	ctx := context.Background()
	if err := store.Sessions().Save(ctx, "sess-1", map[string]any{"trust_band": "trusted"}); err != nil {
		t.Fatalf("seed sess-1: %v", err)
	}
	if err := store.Sessions().Save(ctx, "sess-2", map[string]any{"trust_band": "concerned"}); err != nil {
		t.Fatalf("seed sess-2: %v", err)
	}

	impl := &shepherdImpl{
		cfg:   Config{TickInterval: time.Hour, StateDir: t.TempDir()},
		store: store,
	}
	if err := impl.tickOnce(ctx); err != nil {
		t.Fatalf("tickOnce: %v", err)
	}

	// Verify both sessions had next_tick_at stamped.
	for _, id := range []string{"sess-1", "sess-2"} {
		s, _ := store.Sessions().Get(ctx, id)
		if _, ok := s["next_tick_at"]; !ok {
			t.Errorf("session %s missing next_tick_at after tick", id)
		}
	}
}

// TestShepherdImpl_TickOnce_HonorsAdaptiveCadence — when next_tick_at
// is in the future, the session is skipped this tick.
func TestShepherdImpl_TickOnce_HonorsAdaptiveCadence(t *testing.T) {
	t.Parallel()
	store, _ := sqlite.NewStore(context.Background(), filepath.Join(t.TempDir(), "shep.db"))
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	futureTick := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	if err := store.Sessions().Save(ctx, "sess-future", map[string]any{
		"trust_band":   "trusted",
		"next_tick_at": futureTick,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	impl := &shepherdImpl{
		cfg:   Config{TickInterval: time.Hour, StateDir: t.TempDir()},
		store: store,
	}
	if err := impl.tickOnce(ctx); err != nil {
		t.Fatalf("tickOnce: %v", err)
	}

	// next_tick_at should be UNCHANGED (the cadence check skipped the
	// session before the saveState write).
	s, _ := store.Sessions().Get(ctx, "sess-future")
	if s["next_tick_at"] != futureTick {
		t.Errorf("next_tick_at = %q, want unchanged %q", s["next_tick_at"], futureTick)
	}
}

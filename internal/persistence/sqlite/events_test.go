package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

func TestEventRepository_AppendList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewEventRepository(openTestDB(t))

	// Empty list.
	events, err := r.List(ctx, persistence.EventListOpts{})
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("List empty = %v, want []", events)
	}

	// Append 3 events with explicit timestamps.
	base := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	seed := []persistence.Event{
		{ID: "e1", Kind: "spawn", Actor: "operator", Timestamp: base},
		{ID: "e2", Kind: "a2a_call", Actor: "agent-X", Timestamp: base.Add(time.Minute),
			A2AMethod: "message/send", CallerOrg: "org-Y", CallerSID: "sid-c"},
		{ID: "e3", Kind: "spawn", Actor: "operator", Timestamp: base.Add(2 * time.Minute)},
	}
	for _, e := range seed {
		if err := r.Append(ctx, e); err != nil {
			t.Fatalf("Append %q: %v", e.ID, err)
		}
	}

	// Append duplicate ID → unique-constraint error.
	if err := r.Append(ctx, persistence.Event{ID: "e1", Kind: "spawn", Timestamp: base}); err == nil {
		t.Error("Append duplicate ID = nil, want error")
	}

	// List all → 3 events ordered by timestamp ascending.
	events, err = r.List(ctx, persistence.EventListOpts{})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("List all = %d events, want 3", len(events))
	}
	for i, want := range []string{"e1", "e2", "e3"} {
		if events[i].ID != want {
			t.Errorf("events[%d].ID = %q, want %q", i, events[i].ID, want)
		}
	}

	// A2A fields round-trip correctly on the second event.
	if events[1].A2AMethod != "message/send" || events[1].CallerOrg != "org-Y" || events[1].CallerSID != "sid-c" {
		t.Errorf("a2a fields lost: %+v", events[1])
	}

	// Filter by Kind.
	events, _ = r.List(ctx, persistence.EventListOpts{Kinds: []string{"spawn"}})
	if len(events) != 2 {
		t.Errorf("List kind=spawn = %d, want 2", len(events))
	}

	// Filter by Since.
	events, _ = r.List(ctx, persistence.EventListOpts{Since: base.Add(time.Minute)})
	if len(events) != 2 {
		t.Errorf("List since=t+1m = %d, want 2", len(events))
	}

	// Limit.
	events, _ = r.List(ctx, persistence.EventListOpts{Limit: 2})
	if len(events) != 2 {
		t.Errorf("List limit=2 = %d, want 2", len(events))
	}
}

func TestEventRepository_AppendValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewEventRepository(openTestDB(t))

	if err := r.Append(ctx, persistence.Event{Kind: "x"}); err == nil {
		t.Error("Append empty ID = nil, want error")
	}
	if err := r.Append(ctx, persistence.Event{ID: "e1"}); err == nil {
		t.Error("Append empty Kind = nil, want error")
	}
	// Append with zero Timestamp → auto-stamped to Now (not an error).
	if err := r.Append(ctx, persistence.Event{ID: "e1", Kind: "spawn"}); err != nil {
		t.Errorf("Append zero ts: %v", err)
	}
	_ = errors.New
}

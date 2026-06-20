// internal/persistence/sqlite/tasks_newest_tiebreak_test.go — P2 adversarial
// review finding: TaskRepository.List(Newest:true) ordered by `created_at DESC`
// with NO secondary key. When two rows share the same created_at at the LIMIT
// boundary it is non-deterministic which one falls inside the window, so the
// Talk transcript could flicker / drop a message arbitrarily across refreshes.
//
// Fix: Newest path is `ORDER BY created_at DESC, id DESC`. This test pins that
// tie-break: rows with an IDENTICAL created_at must come back in id-DESC order
// deterministically. (Authored for this ticket — does not edit the test agent's
// existing tasks_newest_test.go.)
package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

func TestTaskList_Newest_TieBreakIsDeterministic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := NewStore(ctx, filepath.Join(t.TempDir(), "tasks_newest_tiebreak.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	r := store.Tasks()

	// All tasks share the EXACT same created_at — created_at DESC alone cannot
	// disambiguate them. Insert in ascending-id order; if we insert with rowid
	// ascending == id ascending, a naive `created_at DESC` may return rows in
	// rowid (== id ascending) order, which is the WRONG order for "newest" and
	// — more importantly — undefined per the SQL spec. The id-DESC secondary
	// key makes it well-defined: highest id first.
	tie := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	const n = 6
	for i := 0; i < n; i++ {
		task := &persistence.Task{
			ID:        fmt.Sprintf("task-%04d", i),
			RunnerSID: "runner",
			State:     "working",
			Method:    "message/send",
			CreatedAt: tie, // identical for every row
		}
		if err := r.Save(ctx, task); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	// Run the query several times; the order must be stable AND equal to
	// id-DESC (task-0005, task-0004, … task-0000).
	want := make([]string, n)
	for i := 0; i < n; i++ {
		want[i] = fmt.Sprintf("task-%04d", n-1-i)
	}
	for attempt := 0; attempt < 5; attempt++ {
		got, err := r.List(ctx, persistence.TaskListOpts{Limit: n, Newest: true})
		if err != nil {
			t.Fatalf("List newest (attempt %d): %v", attempt, err)
		}
		if len(got) != n {
			t.Fatalf("attempt %d: got %d tasks, want %d", attempt, len(got), n)
		}
		for i := range want {
			if got[i].ID != want[i] {
				t.Fatalf("attempt %d: tie-break order wrong at pos %d: got %q want %q (full=%v)",
					attempt, i, got[i].ID, want[i], idsOf(got))
			}
		}
	}

	// Boundary determinism: with two tied rows and Limit:1, the row that falls
	// inside the LIMIT must be the deterministic one (highest id).
	got, err := r.List(ctx, persistence.TaskListOpts{Limit: 1, Newest: true})
	if err != nil {
		t.Fatalf("List newest limit=1: %v", err)
	}
	if len(got) != 1 || got[0].ID != fmt.Sprintf("task-%04d", n-1) {
		t.Fatalf("limit=1 boundary: got %v, want [task-%04d]", idsOf(got), n-1)
	}
}

func idsOf(ts []*persistence.Task) []string {
	out := make([]string, len(ts))
	for i, x := range ts {
		out[i] = x.ID
	}
	return out
}

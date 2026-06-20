// internal/persistence/sqlite/tasks_newest_test.go — regression for the
// Talk transcript drop (operator-reported 2026-06-20: "message delivered but
// I can't see it").
//
// Bug: TaskRepository.List ordered `ORDER BY id LIMIT N`. Task ids are UUIDv7
// (time-ordered ascending), so a bounded Limit returned the OLDEST N tasks.
// The team transcript reads List(Limit:200); once a daemon accumulates >200
// tasks, the most-recent operator/agent messages fell outside the window and
// vanished from the Talk feed. Fix: TaskListOpts.Newest → ORDER BY created_at
// DESC so a bounded Limit returns the most-recent N. Default order is unchanged
// (ascending id) so SinceID cursor pagination keeps working.
package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
)

func TestTaskList_Newest_ReturnsMostRecentWithinLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := NewStore(ctx, filepath.Join(t.TempDir(), "tasks_newest.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	r := store.Tasks()

	// 250 tasks with strictly increasing created_at. id sort order ==
	// chronological (mirrors UUIDv7) so the bug repro is faithful.
	base := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	const n = 250
	for i := 0; i < n; i++ {
		task := &persistence.Task{
			ID:        fmt.Sprintf("task-%04d", i), // ascending == chronological
			RunnerSID: "runner",
			State:     "working",
			Method:    "message/send",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := r.Save(ctx, task); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	// Newest:true with Limit 200 must return the 200 MOST-RECENT tasks
	// (task-0049 .. task-0249), NOT the oldest 200 (task-0000..task-0199).
	got, err := r.List(ctx, persistence.TaskListOpts{Limit: 200, Newest: true})
	if err != nil {
		t.Fatalf("List newest: %v", err)
	}
	if len(got) != 200 {
		t.Fatalf("newest: got %d tasks, want 200", len(got))
	}
	// The single most-recent task MUST be present (this is the one the
	// operator's "invisible" message corresponds to).
	var sawNewest bool
	for _, x := range got {
		if x.ID == fmt.Sprintf("task-%04d", n-1) {
			sawNewest = true
		}
		if x.ID == "task-0000" {
			t.Errorf("newest set must NOT contain the oldest task-0000")
		}
	}
	if !sawNewest {
		t.Fatalf("newest:true did NOT return the most-recent task-%04d — this is the operator-visible bug", n-1)
	}
}

func TestTaskList_DefaultOrder_StaysAscendingForPagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := NewStore(ctx, filepath.Join(t.TempDir(), "tasks_asc.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	r := store.Tasks()

	base := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := r.Save(ctx, &persistence.Task{
			ID: fmt.Sprintf("task-%04d", i), RunnerSID: "runner", State: "working",
			Method: "message/send", CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	// Default (Newest=false) keeps ascending id order — SinceID pagination
	// relies on `id > cursor`, so order must not regress to DESC.
	got, err := r.List(ctx, persistence.TaskListOpts{Limit: 100})
	if err != nil {
		t.Fatalf("List default: %v", err)
	}
	if len(got) < 2 || got[0].ID != "task-0000" {
		t.Fatalf("default order must be ascending id (got[0]=%v)", got[0].ID)
	}
}

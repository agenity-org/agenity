// internal/persistence/postgres/tasks_newest_test.go — POSTGRES parity for the
// Talk transcript Newest-ordering fix (sqlite sibling:
// internal/persistence/sqlite/tasks_newest_test.go).
//
// Bug recap: TaskRepository.List ordered `ORDER BY id LIMIT N`. Task ids are
// UUIDv7 (time-ordered ascending), so a bounded Limit returned the OLDEST N.
// The transcript reads List(Limit:200); past 200 tasks the newest messages
// fell out of the window and vanished. Fix: TaskListOpts.Newest → ORDER BY
// created_at DESC. Default order stays ascending id so SinceID cursor
// pagination keeps working.
//
// The postgres backend has its OWN List query (postgres/tasks.go) with a
// parallel `if opts.Newest { order = "ORDER BY created_at DESC" }` branch, so
// it needs its own regression. This needs a real PostgreSQL: it boots one via
// testcontainers (exactly like equivalence_test.go) and SKIPS cleanly when
// Docker is unavailable or in -short mode. No live DB ⇒ skip, not fake.
package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/chepherd/chepherd/internal/persistence"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// newTaskRepoPG boots a throwaway postgres and returns its TaskRepository, or
// skips the test when no Docker is reachable. Mirrors equivalence_test.go's
// gating so CI (Docker present) runs it for real and local dev skips.
func newTaskRepoPG(t *testing.T) (persistence.TaskRepository, context.Context) {
	t.Helper()
	if testing.Short() {
		t.Skip("postgres tasks-newest parity requires Docker; skipping in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("chepherd_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("postgres container unavailable (Docker not running?): %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("terminate container: %v", err)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("ConnectionString: %v", err)
	}
	store, err := NewStore(ctx, dsn)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store.Tasks(), ctx
}

func TestTaskList_Newest_ReturnsMostRecentWithinLimit_Postgres(t *testing.T) {
	r, ctx := newTaskRepoPG(t)

	base := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	const n = 250
	for i := 0; i < n; i++ {
		if err := r.Save(ctx, &persistence.Task{
			ID: fmt.Sprintf("task-%04d", i), RunnerSID: "runner", State: "working",
			Method: "message/send", CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	got, err := r.List(ctx, persistence.TaskListOpts{Limit: 200, Newest: true})
	if err != nil {
		t.Fatalf("List newest: %v", err)
	}
	if len(got) != 200 {
		t.Fatalf("newest: got %d tasks, want 200", len(got))
	}
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
		t.Fatalf("postgres newest:true did NOT return the most-recent task-%04d", n-1)
	}
}

func TestTaskList_DefaultOrder_StaysAscendingForPagination_Postgres(t *testing.T) {
	r, ctx := newTaskRepoPG(t)

	base := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		if err := r.Save(ctx, &persistence.Task{
			ID: fmt.Sprintf("task-%04d", i), RunnerSID: "runner", State: "working",
			Method: "message/send", CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	// Default (Newest=false) ascending id.
	got, err := r.List(ctx, persistence.TaskListOpts{Limit: 100})
	if err != nil {
		t.Fatalf("List default: %v", err)
	}
	if len(got) < 2 || got[0].ID != "task-0000" {
		t.Fatalf("postgres default order must be ascending id (got[0]=%v)", got[0].ID)
	}

	// SinceID cursor must advance: id > cursor, ascending, no overlap.
	page1, err := r.List(ctx, persistence.TaskListOpts{Limit: 4})
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(page1) != 4 || page1[3].ID != "task-0003" {
		t.Fatalf("postgres page1 must end at task-0003, got %v", page1)
	}
	cursor := page1[3].ID
	page2, err := r.List(ctx, persistence.TaskListOpts{Limit: 4, SinceID: cursor})
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(page2) != 4 || page2[0].ID != "task-0004" {
		t.Fatalf("postgres page2 (SinceID=%s) must start at task-0004, got %v", cursor, page2)
	}
	for _, x := range page2 {
		if x.ID <= cursor {
			t.Fatalf("postgres SinceID leaked non-advancing id %q (<= %q)", x.ID, cursor)
		}
	}
}

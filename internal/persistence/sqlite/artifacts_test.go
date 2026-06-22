package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/agenity-org/agenity/internal/persistence"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := NewStore(ctx, filepath.Join(t.TempDir(), "h3.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedTask(t *testing.T, s *Store, taskID string) {
	t.Helper()
	if err := s.Tasks().Save(context.Background(), &persistence.Task{
		ID: taskID, RunnerSID: "runner-1", State: "working", Method: "message/send",
	}); err != nil {
		t.Fatalf("seed task %q: %v", taskID, err)
	}
}

func TestArtifacts_SaveGetRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedTask(t, s, "task-1")
	ctx := context.Background()
	in := &persistence.Artifact{
		ID:       "art-1",
		TaskID:   "task-1",
		Name:     "result-summary",
		Parts:    []byte(`[{"kind":"text","text":"hello world"}]`),
		Metadata: []byte(`{"source":"agent","confidence":0.95}`),
	}
	if err := s.Artifacts().Save(ctx, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Artifacts().Get(ctx, "art-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != in.ID || got.TaskID != in.TaskID || got.Name != in.Name {
		t.Errorf("roundtrip mismatch: got %+v, want %+v", got, in)
	}
	if string(got.Parts) != string(in.Parts) {
		t.Errorf("Parts roundtrip: got %s, want %s", got.Parts, in.Parts)
	}
	if string(got.Metadata) != string(in.Metadata) {
		t.Errorf("Metadata roundtrip: got %s, want %s", got.Metadata, in.Metadata)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt not stamped")
	}
}

func TestArtifacts_ListByTask(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedTask(t, s, "task-A")
	seedTask(t, s, "task-B")
	ctx := context.Background()
	for _, art := range []persistence.Artifact{
		{ID: "a1", TaskID: "task-A", Name: "n1"},
		{ID: "a2", TaskID: "task-A", Name: "n2"},
		{ID: "b1", TaskID: "task-B", Name: "n3"},
	} {
		a := art
		if err := s.Artifacts().Save(ctx, &a); err != nil {
			t.Fatalf("Save %q: %v", a.ID, err)
		}
	}
	listA, err := s.Artifacts().List(ctx, "task-A")
	if err != nil {
		t.Fatalf("List A: %v", err)
	}
	if len(listA) != 2 {
		t.Errorf("List(task-A) len = %d, want 2: %+v", len(listA), listA)
	}
	listB, err := s.Artifacts().List(ctx, "task-B")
	if err != nil {
		t.Fatalf("List B: %v", err)
	}
	if len(listB) != 1 {
		t.Errorf("List(task-B) len = %d, want 1", len(listB))
	}
}

func TestArtifacts_FKCascadeOnTaskDelete(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedTask(t, s, "task-X")
	ctx := context.Background()
	if err := s.Artifacts().Save(ctx, &persistence.Artifact{
		ID: "art-X", TaskID: "task-X", Name: "to-be-cascaded",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// SQLite needs PRAGMA foreign_keys=ON to enforce FK cascade. Open
	// (in sqlite.Open) sets this.
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	if err := s.Tasks().Delete(ctx, "task-X"); err != nil {
		t.Fatalf("Tasks.Delete: %v", err)
	}
	if _, err := s.Artifacts().Get(ctx, "art-X"); err == nil {
		t.Error("art-X still retrievable after task-X deleted — FK CASCADE not working")
	}
}

func TestArtifacts_SaveValidation(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	cases := []struct {
		name string
		a    *persistence.Artifact
	}{
		{"nil", nil},
		{"empty ID", &persistence.Artifact{TaskID: "t-1"}},
		{"empty TaskID", &persistence.Artifact{ID: "a-1"}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if err := s.Artifacts().Save(ctx, c.a); err == nil {
				t.Errorf("Save(%v) = nil, want error", c.a)
			}
		})
	}
}

func TestArtifacts_GetUnknownReturnsError(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if _, err := s.Artifacts().Get(context.Background(), "no-such-id"); err == nil {
		t.Error("Get(unknown) = nil err, want not-found")
	}
}

func TestArtifacts_DeleteIdempotent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedTask(t, s, "task-D")
	ctx := context.Background()
	if err := s.Artifacts().Save(ctx, &persistence.Artifact{ID: "del-1", TaskID: "task-D"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Artifacts().Delete(ctx, "del-1"); err != nil {
		t.Fatalf("Delete first: %v", err)
	}
	// Second delete is a no-op, not an error.
	if err := s.Artifacts().Delete(ctx, "del-1"); err != nil {
		t.Errorf("Delete second: %v (want idempotent nil)", err)
	}
}

func TestArtifacts_SaveDefaultsEmptyJSONColumns(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedTask(t, s, "task-J")
	ctx := context.Background()
	if err := s.Artifacts().Save(ctx, &persistence.Artifact{
		ID: "j-1", TaskID: "task-J",
		// Parts + Metadata left nil — Save coerces to '[]' and '{}'.
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Artifacts().Get(ctx, "j-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Parts) != "[]" {
		t.Errorf("default Parts = %q, want \"[]\"", got.Parts)
	}
	if string(got.Metadata) != "{}" {
		t.Errorf("default Metadata = %q, want \"{}\"", got.Metadata)
	}
}

func TestArtifacts_UpsertOnConflict(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedTask(t, s, "task-U")
	ctx := context.Background()
	if err := s.Artifacts().Save(ctx, &persistence.Artifact{
		ID: "u-1", TaskID: "task-U", Name: "v1",
		Parts: []byte(`[{"text":"v1"}]`),
	}); err != nil {
		t.Fatalf("Save v1: %v", err)
	}
	if err := s.Artifacts().Save(ctx, &persistence.Artifact{
		ID: "u-1", TaskID: "task-U", Name: "v2",
		Parts: []byte(`[{"text":"v2"}]`),
	}); err != nil {
		t.Fatalf("Save v2: %v", err)
	}
	got, err := s.Artifacts().Get(ctx, "u-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "v2" {
		t.Errorf("upsert Name = %q, want v2", got.Name)
	}
	if string(got.Parts) != `[{"text":"v2"}]` {
		t.Errorf("upsert Parts = %s, want v2", got.Parts)
	}
}

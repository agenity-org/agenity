// internal/runtimehttp/tasks_endpoint_test.go — pins the GET
// /api/v1/tasks endpoint shape introduced for the #225 row G2 A2A
// Inbox dashboard tab. Asserts:
//   - GET returns 200 + {"tasks": []} when no TaskStore is wired
//   - GET returns 200 + {"tasks": [...]} with the seeded task view
//     when TaskStore is wired
//   - Non-GET returns 405
//
// Refs #225 row G2.
package runtimehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/chepherd/chepherd/internal/persistence"
	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

func TestTasksList_NoStoreReturnsEmpty(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/tasks")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	tasks, _ := body["tasks"].([]any)
	if len(tasks) != 0 {
		t.Errorf("tasks = %v, want empty", tasks)
	}
}

func TestTasksList_ReturnsSeededTasks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "g2.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	if err := store.Tasks().Save(ctx, &persistence.Task{
		ID: "task-seeded", RunnerSID: "runner-1", State: "TASK_STATE_WORKING",
		Method: "message/send",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := httptest.NewServer((&Server{TaskStore: store.Tasks()}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/tasks")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	tasks, _ := body["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("tasks len = %d, want 1: %v", len(tasks), tasks)
	}
	first, _ := tasks[0].(map[string]any)
	if first["id"] != "task-seeded" || first["method"] != "message/send" {
		t.Errorf("task fields = %v, want id=task-seeded method=message/send", first)
	}
}

func TestTasksList_RejectsNonGET(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/v1/tasks", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

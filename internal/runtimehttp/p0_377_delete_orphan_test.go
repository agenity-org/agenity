// internal/runtimehttp/p0_377_delete_orphan_test.go — pins #377 P0:
// DELETE /api/v1/sessions/<name> on a persisted-but-not-live row must
// remove the row + return 200 idempotently. Pre-fix the 410-Gone
// short-circuit fired for ALL methods, so the DELETE handler was
// unreachable for orphans → operator's 58 orphan rows could not be
// cleared from the dashboard.
//
// GET on the same row continues to return 410 with canResume=true
// (preserves #357 Resume UI). The fix is method-gated, not a wholesale
// removal of the 410 path.
//
// Refs #377 P0 #357 P0 #225.
package runtimehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"net/http/httptest"

	"github.com/agenity-org/agenity/internal/persistence/sqlite"
)

// TestP0_377_DeleteOrphan_CleansStore_Returns200 is the contract:
// seed a persisted-but-not-live row, DELETE → 200 + {ok:true,cleaned:true,
// wasLive:false}, second DELETE → 404 (row gone, idempotent).
func TestP0_377_DeleteOrphan_CleansStore_Returns200(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "p0377.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	defer store.Close()
	if err := store.Sessions().Save(ctx, "orphan-378",
		map[string]any{"name": "orphan-378", "agent": "claude-code"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	srv := httptest.NewServer((&Server{SessionStore: store.Sessions()}).Handler())
	defer srv.Close()

	// First DELETE — cleans store, 200.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/sessions/orphan-378", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first DELETE status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["ok"] != true {
		t.Errorf("ok = %v, want true", body["ok"])
	}
	if body["cleaned"] != true {
		t.Errorf("cleaned = %v, want true", body["cleaned"])
	}
	if body["wasLive"] != false {
		t.Errorf("wasLive = %v, want false", body["wasLive"])
	}

	// Row must be gone from store.
	if state, err := store.Sessions().Get(ctx, "orphan-378"); err == nil && len(state) > 0 {
		t.Errorf("store still has row after DELETE: %+v", state)
	}

	// Second DELETE — definitively gone, 404.
	req2, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/sessions/orphan-378", nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("second DELETE: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("second DELETE status = %d, want 404 (idempotent)", resp2.StatusCode)
	}
}

// TestP0_377_GetOrphan_Still410_PreservesResumeUI locks the
// non-regression contract: GET on a persisted-but-not-live row MUST
// still return 410 with canResume=true so #357's dashboard Resume UI
// keeps working. The #377 fix is method-gated; GET behavior is
// unchanged.
func TestP0_377_GetOrphan_Still410_PreservesResumeUI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "p0377-get.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	defer store.Close()
	if err := store.Sessions().Save(ctx, "stale-get",
		map[string]any{"name": "stale-get", "agent": "claude-code"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	srv := httptest.NewServer((&Server{SessionStore: store.Sessions()}).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions/stale-get")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("GET status = %d, want 410 (preserves #357)", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["canResume"] != true {
		t.Errorf("canResume = %v, want true (Resume UI hint)", body["canResume"])
	}

	// Critically: GET must NOT have cleared the row. Resume UI relies
	// on the row staying until the user explicitly resumes or deletes.
	if state, err := store.Sessions().Get(ctx, "stale-get"); err != nil || len(state) == 0 {
		t.Errorf("store row missing after GET — Resume UI broken: err=%v state=%v", err, state)
	}
}

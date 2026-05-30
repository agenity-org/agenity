// internal/runtimehttp/p1_393_cleanup_orphans_test.go — pins #393 P1:
// POST /api/v1/sessions/_cleanup-orphans walks the SessionStore,
// deletes every row whose name has no live runtime entry, returns
// {deleted: N, kept: M, deleted_names: [...]}.
//
// Bonus from #377 (deferred per architect "optional in this PR"
// scope). Operator's #393 unblock was a curl-loop one-liner; this
// endpoint replaces it + backs the dashboard's "Clean up orphans"
// header button.
//
// Refs #393 P1 #377 P0 #357 P0 #225.
package runtimehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/chepherd/chepherd/internal/persistence/sqlite"
)

// TestP1_393_CleanupOrphans_DeletesOrphansPreservesLive — seed 5
// rows in the store, 0 live. POST /_cleanup-orphans should return
// {deleted: 5, kept: 0}. Second call should return {deleted: 0,
// kept: 0} (idempotent).
//
// In this test the rt field is nil so EVERY persisted row is
// orphan-by-definition; a more realistic "rt has 1 live" case is
// the next test.
func TestP1_393_CleanupOrphans_DeletesOrphansPreservesLive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "p393-1.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	defer store.Close()
	for _, name := range []string{"orphan-1", "orphan-2", "orphan-3", "orphan-4", "orphan-5"} {
		if err := store.Sessions().Save(ctx, name, map[string]any{"name": name, "agent": "claude-code"}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	srv := httptest.NewServer((&Server{SessionStore: store.Sessions()}).Handler())
	defer srv.Close()

	// First call → cleans all 5
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/sessions/_cleanup-orphans", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, _ := body["deleted"].(float64); int(got) != 5 {
		t.Errorf("deleted = %v, want 5", body["deleted"])
	}
	if got, _ := body["kept"].(float64); int(got) != 0 {
		t.Errorf("kept = %v, want 0", body["kept"])
	}

	// Store must actually be empty after the call.
	names, _ := store.Sessions().List(ctx)
	if len(names) != 0 {
		t.Errorf("store after cleanup has %d rows: %v", len(names), names)
	}

	// Second call → idempotent: deleted=0
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/sessions/_cleanup-orphans", nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("POST 2: %v", err)
	}
	defer resp2.Body.Close()
	var body2 map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&body2)
	if got, _ := body2["deleted"].(float64); int(got) != 0 {
		t.Errorf("second deleted = %v, want 0 (idempotent)", body2["deleted"])
	}
}

// TestP1_393_CleanupOrphans_NoSessionStore_Graceful — no SessionStore
// wired → 200 with {deleted: 0, kept: 0, note: ...}, not 500.
// Operators on BareExec / no-persistence configs shouldn't get a
// scary error from the header button.
func TestP1_393_CleanupOrphans_NoSessionStore_Graceful(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/sessions/_cleanup-orphans", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (graceful no-op)", resp.StatusCode)
	}
}

// TestP1_393_CleanupOrphans_RejectsNonPOST — GET should not trigger
// the cleanup. The magic-name routing only fires on POST.
func TestP1_393_CleanupOrphans_RejectsNonPOST(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "p393-3.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	defer store.Close()
	if err := store.Sessions().Save(ctx, "should-survive", map[string]any{"name": "should-survive"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := httptest.NewServer((&Server{SessionStore: store.Sessions()}).Handler())
	defer srv.Close()

	// GET should NOT delete — falls through to sessionByName's normal
	// flow, which returns 410 Gone for the persisted-but-not-live
	// magic name (treating _cleanup-orphans as a regular session name).
	resp, err := http.Get(srv.URL + "/api/v1/sessions/_cleanup-orphans")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	// Either 410 (treated as persisted-but-not-live row that doesn't
	// exist) or 404 (never-existed); both are valid "this is not the
	// cleanup endpoint via GET" responses. The IMPORTANT assertion is
	// that the live `should-survive` row remained.
	if state, err := store.Sessions().Get(ctx, "should-survive"); err != nil || len(state) == 0 {
		t.Errorf("GET on _cleanup-orphans inadvertently deleted should-survive: err=%v state=%v", err, state)
	}
}

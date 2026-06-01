// internal/runtimehttp/p2_646_claude_login_cancel_test.go pins #646:
// claudeLoginCancel must delete the persisted SessionStore row after
// Stop, otherwise listSessionsMerged keeps surfacing the orphan with
// live=false and operators see the cancelled oauth-capture-* session
// permanently in the topbar.
package runtimehttp

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/chepherd/chepherd/internal/persistence/sqlite"
	"github.com/chepherd/chepherd/internal/runtime"
)

func TestP2_646_ClaudeLoginCancel_DeletesPersistedRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Real sqlite-backed SessionRepository — matches production wiring.
	dbPath := filepath.Join(t.TempDir(), "p2-646.db")
	store, err := sqlite.NewStore(ctx, dbPath)
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	defer store.Close()

	// Pre-seed an orphan oauth-capture row (simulating the state the
	// claudeLoginBegin leaves behind for a SessionStore-backed deploy).
	const orphanName = "oauth-capture-1700000000000"
	if err := store.Sessions().Save(ctx, orphanName, map[string]any{"name": orphanName}); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	// Minimal server with the real session store + a real runtime
	// (so Stop's unknown-session error path is exercised cleanly).
	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	srv := &Server{
		rt:           rt,
		SessionStore: store.Sessions(),
	}

	req := httptest.NewRequest("POST", "/api/v1/claude-tokens/login-cancel/"+orphanName, nil)
	rr := httptest.NewRecorder()
	srv.claudeLoginCancel(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	// After cancel: the persisted row MUST be gone. sqlite Get returns
	// (empty map, nil) on missing rows, so check via List which returns
	// only existing session_ids.
	ids, err := store.Sessions().List(ctx)
	if err != nil {
		t.Fatalf("list after cancel: %v", err)
	}
	for _, id := range ids {
		if id == orphanName {
			t.Errorf("orphan row still present after cancel — #646 regression")
		}
	}
}

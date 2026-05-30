// internal/runtimehttp/p0_357_attach_410_test.go — pins #357 P0:
// /api/v1/sessions/<name> distinguishes persisted-but-not-live (410
// Gone, canResume=true) from never-existed (404). Stops the dashboard
// 5s attach-reconnect loop on stale-cache sessions.
//
// Refs #357 P0 #314 (D4).
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

func TestP0_357_GetSession_PersistedButNotLive_Returns410(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "p0357.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	defer store.Close()
	if err := store.Sessions().Save(ctx, "stale-session",
		map[string]any{"name": "stale-session", "agent": "claude-code"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	srv := httptest.NewServer((&Server{SessionStore: store.Sessions()}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/sessions/stale-session")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want 410 Gone", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["canResume"] != true {
		t.Errorf("canResume = %v, want true", body["canResume"])
	}
	if body["live"] != false {
		t.Errorf("live = %v, want false", body["live"])
	}
	if body["name"] != "stale-session" {
		t.Errorf("name = %v, want stale-session", body["name"])
	}
}

func TestP0_357_GetSession_NeverExisted_Returns404(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "p0357-2.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	defer store.Close()
	srv := httptest.NewServer((&Server{SessionStore: store.Sessions()}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/sessions/never-existed")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (never existed → definitive)", resp.StatusCode)
	}
}

func TestP0_357_AttachWS_PersistedButNotLive_Returns410(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "p0357-3.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	defer store.Close()
	if err := store.Sessions().Save(ctx, "stale", map[string]any{"name": "stale"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	srv := httptest.NewServer((&Server{SessionStore: store.Sessions()}).Handler())
	defer srv.Close()
	// Attach sub-path hits the same sessionByName flow — 410 for
	// persisted-but-not-live before WebSocket upgrade is attempted.
	resp, err := http.Get(srv.URL + "/api/v1/sessions/stale/attach")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want 410 (attach on stale session)", resp.StatusCode)
	}
}

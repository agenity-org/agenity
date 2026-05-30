// internal/runtimehttp/d4_sessions_merged_test.go — pins #314 (D4):
// GET /api/v1/sessions returns merged live + persisted sessions with
// a live boolean so the dashboard's reconciler stops 404-hammering
// non-live sessions.
//
// Refs #314 (D4).
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

func TestSessionsRoot_GET_IncludesPersistedSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.NewStore(ctx, filepath.Join(t.TempDir(), "d4.db"))
	if err != nil {
		t.Fatalf("sqlite.NewStore: %v", err)
	}
	defer store.Close()

	// Seed two persisted sessions; they're NOT in the live runtime.
	for _, name := range []string{"persisted-1", "persisted-2"} {
		if err := store.Sessions().Save(ctx, name, map[string]any{
			"trust_band": "trusted",
			"agent":      "claude-code",
		}); err != nil {
			t.Fatalf("seed %q: %v", name, err)
		}
	}

	srv := httptest.NewServer((&Server{
		SessionStore: store.Sessions(),
	}).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	sessions, _ := body["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("sessions len = %d, want 2 (both persisted, both not live): %+v", len(sessions), sessions)
	}
	for _, s := range sessions {
		m, _ := s.(map[string]any)
		if live, _ := m["live"].(bool); live {
			t.Errorf("persisted session marked live=true: %+v", m)
		}
		if name, _ := m["name"].(string); name != "persisted-1" && name != "persisted-2" {
			t.Errorf("unexpected session name: %q", name)
		}
	}
}

func TestSessionsRoot_GET_NilRtAndNilStoreReturnsEmpty(t *testing.T) {
	t.Parallel()
	// Both rt and SessionStore nil → endpoint returns 200 + empty list,
	// no panic. The nil-rt guard added in #314 D4 is load-bearing here.
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/sessions")
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
	sessions, _ := body["sessions"].([]any)
	if len(sessions) != 0 {
		t.Errorf("sessions = %v, want empty", sessions)
	}
}

func TestSessionsRoot_GET_RespectsNonGET(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()
	// PUT is not allowed on /api/v1/sessions root. Should NOT return our
	// list path (which would crash on nil rt); should hit the
	// method-mismatch branch.
	resp, err := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/sessions", nil)
	if err != nil {
		t.Fatalf("build req: %v", err)
	}
	r, err := http.DefaultClient.Do(resp)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer r.Body.Close()
	// 405 method-not-allowed or 404 are both acceptable — what matters is
	// no panic and the list-merged path isn't hit on a non-GET.
	if r.StatusCode == http.StatusOK {
		t.Errorf("PUT returned 200, want non-OK")
	}
}

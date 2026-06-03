// internal/runtimehttp/p2_694_session_icon_test.go pins #694: the
// operator-set identity icon round-trips through PATCH
// /api/v1/sessions/{name}/icon, persists on the SessionInfo record
// (json.Marshal struct — no handcoded field maps, #356 lesson), and an
// empty PATCH clears the override.
//
// Refs #694 (parent #690).
package runtimehttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chepherd/chepherd/internal/runtime"
)

func iconTestServer(t *testing.T) (*httptest.Server, *runtime.Runtime) {
	t.Helper()
	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	t.Cleanup(rt.Close) // #684 — quiesce team-event fan-out before tempdir cleanup
	rt.UpsertSessionInfoForTest(&runtime.SessionInfo{ID: "sid-ic", Name: "iconic", Team: "t", Role: runtime.RoleWorker})
	srv := httptest.NewServer((&Server{rt: rt}).Handler())
	t.Cleanup(srv.Close)
	return srv, rt
}

func patchIcon(t *testing.T, srv *httptest.Server, name, icon string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"icon": icon})
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/v1/sessions/"+name+"/icon", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH icon: %v", err)
	}
	return resp
}

func TestP2_694_IconPatch_RoundTripsThroughSessionsList(t *testing.T) {
	srv, _ := iconTestServer(t)

	if resp := patchIcon(t, srv, "iconic", "🦊"); resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200", resp.StatusCode)
	}

	// The icon must surface in GET /api/v1/sessions (the wire the
	// dashboard renders from), via the struct's json tag.
	gr, err := http.Get(srv.URL + "/api/v1/sessions")
	if err != nil {
		t.Fatalf("GET sessions: %v", err)
	}
	defer gr.Body.Close()
	var list struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.NewDecoder(gr.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got string
	for _, s := range list.Sessions {
		if s["name"] == "iconic" {
			got, _ = s["icon"].(string)
		}
	}
	if got != "🦊" {
		t.Fatalf("sessions list icon = %q, want 🦊", got)
	}
}

func TestP2_694_IconPatch_EmptyClears(t *testing.T) {
	srv, rt := iconTestServer(t)
	if resp := patchIcon(t, srv, "iconic", "🦊"); resp.StatusCode != http.StatusOK {
		t.Fatalf("set status = %d, want 200", resp.StatusCode)
	}
	// Assert the set actually landed before testing the clear — without
	// this the test false-passes when both PATCHes 404 (caught during
	// development: the route was unreachable for info-only sessions).
	var set bool
	for _, info := range rt.List() {
		if info.Name == "iconic" && info.Icon == "🦊" {
			set = true
		}
	}
	if !set {
		t.Fatal("icon was never set — clear test would be vacuous")
	}
	if resp := patchIcon(t, srv, "iconic", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("clear status = %d, want 200", resp.StatusCode)
	}
	for _, info := range rt.List() {
		if info.Name == "iconic" && info.Icon != "" {
			t.Fatalf("icon after clear = %q, want empty", info.Icon)
		}
	}
}

func TestP2_694_IconPatch_Validation(t *testing.T) {
	srv, _ := iconTestServer(t)
	if resp := patchIcon(t, srv, "iconic", "this-is-way-too-long-for-an-icon"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversize icon status = %d, want 400", resp.StatusCode)
	}
	if resp := patchIcon(t, srv, "no-such-agent", "🦊"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown agent status = %d, want 404", resp.StatusCode)
	}
}

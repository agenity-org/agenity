// internal/runtimehttp/p0_404_p02_peer_status_endpoint_test.go —
// pins #404 P0.2 HTTP surface:
//   GET /api/v1/sessions/<name>/peer-status
// returns the peer's live PeerStatus JSON.
//
// Refs #404 P0.2 #225.
package runtimehttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chepherd/chepherd/internal/runtime"
)

func TestP0_404_P02_PeerStatusEndpoint_404OnUnknownSession(t *testing.T) {
	t.Parallel()
	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	srv := httptest.NewServer((&Server{rt: rt}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/sessions/no-such/peer-status")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestP0_404_P02_PeerStatusEndpoint_RejectsNonGET(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer((&Server{}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/sessions/x/peer-status", strings.NewReader(""))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (POST falls through to default)", resp.StatusCode)
	}
}

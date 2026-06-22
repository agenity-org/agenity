// internal/runtimehttp/p2_693_healthz_federation_test.go pins the #693
// review catch: /healthz must surface the daemon's hub-mesh connection
// (federation.hub_url + org_id) when hub-connected, so Settings ▸ Mesh
// shows the REAL state instead of a false-negative "start with
// --hub-url" hint; and must omit the block entirely when mesh is off.
package runtimehttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agenity-org/agenity/internal/runtime"
)

func healthzFor(t *testing.T, hubURL, orgID string) map[string]any {
	t.Helper()
	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	t.Cleanup(rt.Close)
	s := &Server{rt: rt, HubURL: hubURL, OrgID: orgID}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestP2_693_Healthz_SurfacesHubFederation(t *testing.T) {
	out := healthzFor(t, "https://signal.openova.io", "openova-hq")
	fed, ok := out["federation"].(map[string]any)
	if !ok {
		t.Fatalf("healthz missing federation block: %v", out)
	}
	if fed["hub_url"] != "https://signal.openova.io" || fed["org_id"] != "openova-hq" {
		t.Fatalf("federation = %v, want hub_url+org_id", fed)
	}
}

func TestP2_693_Healthz_OmitsFederationWhenMeshOff(t *testing.T) {
	out := healthzFor(t, "", "")
	if _, present := out["federation"]; present {
		t.Fatalf("federation block present with mesh off: %v", out)
	}
}

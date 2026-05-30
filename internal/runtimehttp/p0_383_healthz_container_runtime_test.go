// internal/runtimehttp/p0_383_healthz_container_runtime_test.go —
// pins #383 P0 healthz diagnostic: /healthz must surface the actual
// container runtime name ("podman" | "docker" | "bare") so operators
// can distinguish a legit podman-sidecar spawn from a silent
// BareExec fallback when chepherd-agent:latest is missing.
//
// Pre-#383 the only signal was `profile.spawner=podman-sidecar` —
// hardcoded in LocalRuntimeSpawner.Mode() regardless of underlying
// cr. Operators bisected unrelated PRs trying to find a spawn-path
// regression when the actual cause was a missing image (env-level).
//
// Refs #383 P0 #225.
package runtimehttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chepherd/chepherd/internal/runtime"
)

func TestP0_383_Healthz_ContainerRuntimeField(t *testing.T) {
	t.Parallel()
	rt, err := runtime.New(t.TempDir())
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	srv := httptest.NewServer((&Server{rt: rt}).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	cr, ok := body["container_runtime"]
	if !ok {
		t.Fatalf("healthz missing container_runtime field (#383): body=%+v", body)
	}
	name, ok := cr.(string)
	if !ok {
		t.Fatalf("container_runtime = %v (%T), want string", cr, cr)
	}
	// Whatever the host has, it must be one of the known values. CI
	// usually has podman; dev envs without podman/docker fall to bare.
	switch name {
	case "podman", "docker", "bare":
		// OK
	default:
		t.Errorf("container_runtime = %q, want one of podman|docker|bare", name)
	}
}

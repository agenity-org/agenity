// internal/runtime/d6_sidecar_test.go — pins #316 (D6): every agent
// Pod manifest built by PodRunner.buildPodManifest contains a
// chepherd-mcp sidecar container.
//
// Refs #316 (D6) #312 (D1 substrate).
package runtime

import (
	"testing"
)

func TestBuildPodManifest_IncludesMCPSidecar(t *testing.T) {
	t.Parallel()
	manifest := buildPodManifest("chepherd", SpawnSpec{
		Name: "agent-x", AgentSlug: "claude-code", Team: "default",
		Role: RoleWorker, Cwd: "/workspace",
	})
	spec, _ := manifest["spec"].(map[string]any)
	containers, _ := spec["containers"].([]map[string]any)
	if len(containers) != 2 {
		t.Fatalf("containers = %d, want 2 (agent + chepherd-mcp sidecar)", len(containers))
	}
	if name, _ := containers[0]["name"].(string); name != "claude-code" {
		t.Errorf("containers[0].name = %q, want claude-code", name)
	}
	if name, _ := containers[1]["name"].(string); name != "chepherd-mcp" {
		t.Errorf("containers[1].name = %q, want chepherd-mcp", name)
	}
}

func TestBuildPodManifest_AgentEnvHasMCPListen(t *testing.T) {
	t.Parallel()
	manifest := buildPodManifest("ns", SpawnSpec{Name: "x", AgentSlug: "y"})
	spec, _ := manifest["spec"].(map[string]any)
	containers, _ := spec["containers"].([]map[string]any)
	agent := containers[0]
	env, _ := agent["env"].([]map[string]any)
	found := false
	for _, e := range env {
		if name, _ := e["name"].(string); name == "CHEPHERD_MCP_LISTEN" {
			if val, _ := e["value"].(string); val == "127.0.0.1:9090" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("agent env missing CHEPHERD_MCP_LISTEN=127.0.0.1:9090: %+v", env)
	}
}

func TestBuildPodManifest_MCPSidecarHasResourceLimits(t *testing.T) {
	t.Parallel()
	manifest := buildPodManifest("ns", SpawnSpec{Name: "x", AgentSlug: "y"})
	spec, _ := manifest["spec"].(map[string]any)
	containers, _ := spec["containers"].([]map[string]any)
	sidecar := containers[1]
	resources, ok := sidecar["resources"].(map[string]any)
	if !ok {
		t.Fatal("chepherd-mcp sidecar missing resources block")
	}
	if _, ok := resources["limits"]; !ok {
		t.Error("chepherd-mcp sidecar missing resources.limits")
	}
	if _, ok := resources["requests"]; !ok {
		t.Error("chepherd-mcp sidecar missing resources.requests")
	}
}

func TestBuildPodManifest_MCPSidecarDialsControlPlane(t *testing.T) {
	t.Parallel()
	manifest := buildPodManifest("ns", SpawnSpec{Name: "x", AgentSlug: "y"})
	spec, _ := manifest["spec"].(map[string]any)
	containers, _ := spec["containers"].([]map[string]any)
	sidecar := containers[1]
	env, _ := sidecar["env"].([]map[string]any)
	found := false
	for _, e := range env {
		if name, _ := e["name"].(string); name == "CHEPHERD_CONTROL_PLANE_URL" {
			if val, _ := e["value"].(string); val == "http://chepherd.chepherd.svc.cluster.local:80" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("sidecar env missing CHEPHERD_CONTROL_PLANE_URL pointing at in-cluster service: %+v", env)
	}
}

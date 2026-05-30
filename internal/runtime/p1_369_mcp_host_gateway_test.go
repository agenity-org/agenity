// internal/runtime/p1_369_mcp_host_gateway_test.go — pins #369 P1:
// (a) PodmanRuntime.SpawnArgs emits --add-host host.containers.internal:host-gateway
// (b) The agent's mcpURL default is ws://host.containers.internal:9090/mcp/ws
//     so claude-code's MCP bridge subprocess can dial chepherd via Podman's
//     host-gateway DNS shim from inside slirp4netns / bridge / host network.
//
// Refs #369 P1 #365 #225.
package runtime

import (
	"testing"
)

func TestP1_369_SpawnArgs_AddsHostGatewayShim(t *testing.T) {
	t.Parallel()
	r := &PodmanRuntime{}
	argv, _ := r.SpawnArgs("agent-369", "/tmp/home", "/tmp/secrets", "/tmp/cwd",
		[]string{"claude"}, nil)
	// Find --add-host immediately followed by host.containers.internal:host-gateway
	for i, a := range argv {
		if a == "--add-host" {
			if i+1 >= len(argv) {
				t.Fatal("--add-host has no value")
			}
			if argv[i+1] != "host.containers.internal:host-gateway" {
				t.Errorf("--add-host value = %q, want host.containers.internal:host-gateway", argv[i+1])
			}
			return
		}
	}
	t.Error("--add-host flag absent from argv — agent will fail to resolve host.containers.internal")
}

// internal/runtime/p1_369_mcp_host_gateway_test.go — pins #372 P0
// regression: SpawnArgs MUST NOT emit --add-host
// host.containers.internal:host-gateway because Podman rejects the
// literal "host-gateway" with "invalid IP address in add-host" exit 125.
// Podman 4.x+ auto-provides the host.containers.internal DNS entry
// under slirp4netns + bridge modes.
//
// This test was inverted from its original #369 form (which asserted
// PRESENCE) after #372 surfaced the Podman incompatibility.
//
// Refs #372 P0 #369 #370 (reverted by #372).
package runtime

import (
	"testing"
)

func TestP0_372_SpawnArgs_NoAddHostHostGateway(t *testing.T) {
	t.Parallel()
	r := &PodmanRuntime{}
	argv, _ := r.SpawnArgs("agent-372", "/tmp/home", "/tmp/secrets", "/tmp/cwd",
		[]string{"claude"}, nil)
	for i, a := range argv {
		if a == "--add-host" {
			if i+1 < len(argv) && argv[i+1] == "host.containers.internal:host-gateway" {
				t.Errorf("--add-host host.containers.internal:host-gateway present in argv; "+
					"Podman rejects 'host-gateway' literal with exit 125. "+
					"Rely on Podman's auto-injected host.containers.internal under slirp4netns/bridge.")
			}
		}
	}
}

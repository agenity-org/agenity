// internal/runtime/p0_365_network_mode_test.go — pins #365 P0:
// spawn argv defaults to slirp4netns (rootless-friendly) instead of
// bridge (which requires CNI plugin + breaks rootless podman with
// exit 127 'failed to mount netns directory'). CHEPHERD_CONTAINER_NETWORK
// env var overrides for operators on rootful podman or who need
// explicit bridge / host / none modes.
//
// Refs #365 P0 #364 (#363 LastOutput captured the RCA).
package runtime

import (
	"testing"
)

func TestP0_365_AgentNetworkMode_DefaultsToSlirp4netns(t *testing.T) {
	t.Setenv("CHEPHERD_CONTAINER_NETWORK", "")
	got := agentNetworkMode()
	want := "slirp4netns:port_handler=slirp4netns"
	if got != want {
		t.Errorf("agentNetworkMode() = %q, want %q (rootless-friendly default per #365)", got, want)
	}
}

func TestP0_365_AgentNetworkMode_RespectsEnvOverride(t *testing.T) {
	cases := []struct {
		envVal string
		want   string
	}{
		{"host", "host"},
		{"bridge", "bridge"},
		{"none", "none"},
		{"slirp4netns:port_handler=rootlesskit", "slirp4netns:port_handler=rootlesskit"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.want, func(t *testing.T) {
			t.Setenv("CHEPHERD_CONTAINER_NETWORK", c.envVal)
			if got := agentNetworkMode(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestP0_365_SpawnArgs_UsesNetworkMode(t *testing.T) {
	t.Setenv("CHEPHERD_CONTAINER_NETWORK", "host")
	r := &PodmanRuntime{}
	argv, _ := r.SpawnArgs("agent-x", "/tmp/home", "/tmp/secrets", "/tmp/cwd",
		[]string{"claude", "--dangerously-skip-permissions"}, nil)
	// Find --network in argv; assert value is "host"
	for i, a := range argv {
		if a == "--network" {
			if i+1 >= len(argv) {
				t.Fatal("--network has no value")
			}
			if argv[i+1] != "host" {
				t.Errorf("--network value = %q, want host (env CHEPHERD_CONTAINER_NETWORK=host)", argv[i+1])
			}
			return
		}
	}
	t.Error("--network flag absent from argv")
}

func TestP0_365_SpawnArgs_DefaultsToSlirp4netns(t *testing.T) {
	t.Setenv("CHEPHERD_CONTAINER_NETWORK", "")
	r := &PodmanRuntime{}
	argv, _ := r.SpawnArgs("agent-y", "/tmp/home", "/tmp/secrets", "/tmp/cwd",
		[]string{"claude"}, nil)
	for i, a := range argv {
		if a == "--network" {
			if i+1 >= len(argv) {
				t.Fatal("--network has no value")
			}
			if argv[i+1] != "slirp4netns:port_handler=slirp4netns" {
				t.Errorf("--network value = %q, want slirp4netns:port_handler=slirp4netns", argv[i+1])
			}
			return
		}
	}
	t.Error("--network flag absent from argv")
}

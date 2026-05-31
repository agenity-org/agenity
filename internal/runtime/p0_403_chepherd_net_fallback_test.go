// internal/runtime/p0_403_chepherd_net_fallback_test.go — pins #403 P0:
// when scripts/start.sh detects a podman network backend missing the
// required plugins (CNI without /opt/cni/bin/bridge etc., or
// "unknown" backend), it passes CHEPHERD_CONTAINER_NETWORK +
// CHEPHERD_MCP_URL env to fall back to slirp4netns + host-loopback.
// The Runtime side MUST honor those env vars; this test pins that
// contract so a future refactor doesn't accidentally break the
// fallback path (which is the only thing keeping Ubuntu 22.04 +
// Podman 3.x hosts able to run chepherd).
//
// Architect's #403 repro: bash scripts/start.sh on Ubuntu 22.04
// host with no containernetworking-plugins package →
//   "Error: failed to mount netns directory for rootless cni: no
//   such file or directory"
// chepherd container fails to start at all.
//
// Refs #403 P0 #398 P0 v2 #225.
package runtime

import (
	"os"
	"testing"
)

// TestP0_403_AgentNetworkMode_RespectsFallbackEnv pins that when
// scripts/start.sh detects CNI unavailable and propagates
// CHEPHERD_CONTAINER_NETWORK=slirp4netns:port_handler=slirp4netns,
// agentNetworkMode returns that value. Without this, the runtime
// would still default to chepherd-net which would error on agent
// spawn (network doesn't exist).
func TestP0_403_AgentNetworkMode_RespectsFallbackEnv(t *testing.T) {
	t.Setenv("CHEPHERD_CONTAINER_NETWORK", "slirp4netns:port_handler=slirp4netns")
	got := agentNetworkMode()
	want := "slirp4netns:port_handler=slirp4netns"
	if got != want {
		t.Errorf("agentNetworkMode() = %q, want %q (fallback env from #403 scripts/start.sh)", got, want)
	}
}

// TestP0_403_ChepherdNetActive_EnvOverridesDefault — symmetric test:
// when scripts/start.sh detects CNI/netavark available and passes
// CHEPHERD_CONTAINER_NETWORK=chepherd-net explicitly (the
// AGENT_NETWORK_ENV path), the runtime still honors the explicit
// value (matches the default but the explicit pass-through guards
// against a future default change that the start.sh fallback path
// can't predict).
func TestP0_403_ChepherdNetActive_EnvOverridesDefault(t *testing.T) {
	t.Setenv("CHEPHERD_CONTAINER_NETWORK", "chepherd-net")
	got := agentNetworkMode()
	if got != "chepherd-net" {
		t.Errorf("explicit env = chepherd-net but got %q", got)
	}
}

// TestP0_403_StartShFallbackBranches verifies the bash script's
// detection logic by reading the file + asserting the canonical
// fallback strings exist. Catches accidental deletions (e.g., a
// future "cleanup pass" removing the warning block) without
// requiring shell-level execution.
func TestP0_403_StartShFallbackBranches(t *testing.T) {
	data, err := os.ReadFile("../../scripts/start.sh")
	if err != nil {
		t.Fatalf("read scripts/start.sh: %v", err)
	}
	src := string(data)
	// Post-#414: agent containers SHARE chepherd netns
	// (--network=container:chepherd). The chepherd-container's own
	// network (chepherd-net vs slirp4netns fallback) is still gated
	// by the #403/#406 detection, but agents bypass that entirely.
	// Test asserts both the chepherd-side fallback machinery + the
	// new agent-side shared-netns env propagation.
	required := []string{
		`NETWORK_BACKEND="$(podman info`, // backend detection
		`USE_CHEPHERD_NET=0`,              // explicit fallback flag
		`/opt/cni/bin /usr/lib/cni /usr/libexec/cni`, // CNI multi-path probe (#406 follow-up)
		`$cni_dir/bridge`,                            // loop body checks bridge
		`$cni_dir/firewall`,                          // loop body checks firewall
		`#403 P0`,                         // citation
		`CHEPHERD_CONTAINER_NETWORK=container:chepherd`, // #414 shared-netns default
		`CHEPHERD_MCP_URL=ws://127.0.0.1:9090/mcp/ws`,   // #414 loopback default
		`#414 architectural fix`,                        // citation for the shared-netns architecture
	}
	for _, sub := range required {
		if !contains(src, sub) {
			t.Errorf("scripts/start.sh missing %q — #403 fallback path may have regressed", sub)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

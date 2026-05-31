// internal/runtime/p0_595_mcp_url_topology_test.go is the regression
// guard for #595 — spawn-time .mcp.json URL must reflect the actual
// MCP listen port + the deployment topology, NOT the legacy
// `ws://chepherd:9090/mcp/ws` hardcode.
//
// Pre-#595 fallback: hardcoded ":9090" + assumed `chepherd` hostname.
// Broke every host-direct deploy (no `chepherd` container to resolve)
// AND every deploy that bound MCP on a non-9090 port.
//
// Post-#595: deriveAgentMCPURL picks port from r.mcpListenAddr +
// hostname from os.Getenv("HOSTNAME") — `chepherd` → container-pod
// (chepherd-net DNS); anything else → host.containers.internal.
//
// Refs #595 #398 #414.
package runtime

import (
	"testing"
)

func TestP0_595_DeriveAgentMCPURL_HostDirectDeploy(t *testing.T) {
	// Operator boot: `chepherd run --mcp-listen 127.0.0.1:9095`. The
	// process is host-direct (no `chepherd` HOSTNAME). Pre-#595 the
	// spawn flow would write `ws://chepherd:9090/mcp/ws` which agents
	// cannot resolve. Post-#595 it should pick the actual port +
	// host.containers.internal hostname.
	r := &Runtime{mcpListenAddr: "127.0.0.1:9095"}
	t.Setenv("HOSTNAME", "openova-laptop") // simulate host-direct
	got := r.deriveAgentMCPURL()
	want := "ws://host.containers.internal:9095/mcp/ws"
	if got != want {
		t.Errorf("host-direct deploy: got %q want %q", got, want)
	}
}

func TestP0_595_DeriveAgentMCPURL_ContainerPodDeploy(t *testing.T) {
	// Canonical deploy: scripts/start.sh boots chepherd as the
	// 'chepherd' container in chepherd-net. HOSTNAME==chepherd inside
	// the container. Agents in chepherd-net resolve `chepherd` by name.
	r := &Runtime{mcpListenAddr: "0.0.0.0:9090"}
	t.Setenv("HOSTNAME", "chepherd")
	got := r.deriveAgentMCPURL()
	want := "ws://chepherd:9090/mcp/ws"
	if got != want {
		t.Errorf("container-pod deploy: got %q want %q", got, want)
	}
}

func TestP0_595_DeriveAgentMCPURL_ContainerPodCustomPort(t *testing.T) {
	// Container-pod deploy but operator bound MCP on a non-default
	// port. The chepherd-net hostname is correct but the port must
	// be the actual bound port, not the legacy 9090.
	r := &Runtime{mcpListenAddr: "0.0.0.0:9099"}
	t.Setenv("HOSTNAME", "chepherd")
	got := r.deriveAgentMCPURL()
	want := "ws://chepherd:9099/mcp/ws"
	if got != want {
		t.Errorf("container-pod custom port: got %q want %q", got, want)
	}
}

func TestP0_595_DeriveAgentMCPURL_LegacyFallback_NoListenAddrSet(t *testing.T) {
	// Pre-SetMCPListenAddr code paths (unit tests, legacy boots that
	// don't yet call SetMCPListenAddr) fall back to ":9090" so they
	// keep working unchanged. Hostname still resolves correctly.
	r := &Runtime{mcpListenAddr: ""}
	t.Setenv("HOSTNAME", "chepherd")
	got := r.deriveAgentMCPURL()
	want := "ws://chepherd:9090/mcp/ws"
	if got != want {
		t.Errorf("legacy fallback: got %q want %q", got, want)
	}
}

func TestP0_595_MCPPortFromListenAddr(t *testing.T) {
	cases := []struct {
		addr string
		want string
	}{
		{"127.0.0.1:9095", "9095"},
		{"0.0.0.0:9090", "9090"},
		{":8080", "8080"},
		{"localhost:5555", "5555"},
		{"", "9090"},        // empty fallback
		{"no-colon", "9090"}, // malformed fallback
	}
	for _, c := range cases {
		if got := mcpPortFromListenAddr(c.addr); got != c.want {
			t.Errorf("mcpPortFromListenAddr(%q): got %q want %q", c.addr, got, c.want)
		}
	}
}

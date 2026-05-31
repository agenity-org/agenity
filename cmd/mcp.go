// cmd/mcp.go — `chepherd mcp` stdio→WebSocket bridge subcommand.
//
// When an agent's MCP config lists chepherd as an MCP server with stdio
// transport, the agent spawns `chepherd mcp` as a subprocess and exchanges
// JSON-RPC over its stdio. This subcommand is a thin bridge: it dials the
// chepherd runtime's HTTP/WebSocket endpoint (CHEPHERD_MCP_URL env or
// --url flag) and shuttles bytes between agent-stdio and the WS. The
// actual tool handlers live in the runtime (one process; no duplicated
// state).
//
// Transport changed in v0.8: Unix socket → HTTP/WS so the same binary
// works on hobby Podman, multi-cluster Kubernetes, and an OpenOva
// instance without changes. Closes #124.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/chepherd/chepherd/internal/mcpserver"
)

var mcpFlagURL string

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "MCP stdio→WebSocket bridge to chepherd's runtime (used by agent MCP configs; not interactive)",
	Long: `chepherd mcp is the MCP stdio→WebSocket bridge. Agents are configured to
spawn it as a subprocess via their .mcp.json. It dials chepherd's runtime
HTTP/WebSocket endpoint and proxies stdio JSON-RPC over the WS.

URL precedence:
  1. --url flag
  2. CHEPHERD_MCP_URL env var
  3. ws://127.0.0.1:9090/mcp/ws  (chepherd-net podman default, #398 v2)

Not intended for interactive use. The chepherd 'run' command emits the
agent MCP config pointing here automatically when it spawns peers.`,
	RunE: runMCPCmd,
}

func init() {
	mcpCmd.Flags().StringVar(&mcpFlagURL, "url", "", "chepherd MCP WebSocket URL (default: $CHEPHERD_MCP_URL or ws://127.0.0.1:9090/mcp/ws — #398 v2)")
	rootCmd.AddCommand(mcpCmd)
}

// m3DeprecationNotice is the stderr warning every `chepherd mcp`
// invocation emits per #479 Wave M3. Kept as a package-level const
// so the test can assert verbatim presence.
//
// The bridge stays FUNCTIONAL (won't break existing chepherd-v0.5
// agent configs); the warning just nudges operators to migrate. A
// future release removes the subcommand entirely — by then every
// supported chepherd version will have shipped with the M2 HTTP
// transport as the canonical path.
//
// Suppress via CHEPHERD_MCP_DEPRECATION_SILENT=1 (some CI runs the
// bridge on every spawn and don't need the noise).
const m3DeprecationNotice = "WARNING: 'chepherd mcp' stdio bridge is DEPRECATED — use MCP HTTP transport via /run/chepherd/mcp.sock per V0.9.2-ARCH §22 (M2 #525). This subcommand will be removed in a future release. Set CHEPHERD_MCP_DEPRECATION_SILENT=1 to suppress this warning.\n"

func runMCPCmd(_ *cobra.Command, _ []string) error {
	// #479 Wave M3 — deprecation warning. Emit BEFORE dialing the
	// bridge URL so even fast-failing invocations see it.
	if os.Getenv("CHEPHERD_MCP_DEPRECATION_SILENT") != "1" {
		fmt.Fprint(os.Stderr, m3DeprecationNotice)
	}
	url := mcpFlagURL
	if url == "" {
		url = os.Getenv("CHEPHERD_MCP_URL")
	}
	if url == "" {
		// #398 P0 v2 — chepherd-net container-name DNS default.
		// scripts/start.sh attaches chepherd + every agent to the
		// `chepherd-net` user-defined podman network so agents reach
		// the MCP server by container name without host-loopback
		// gymnastics. Bare-host dev mode (chepherd outside chepherd-net):
		// override via CHEPHERD_MCP_URL=ws://host.containers.internal:9090/mcp/ws
		// + run with --network slirp4netns... etc. K8s in-cluster
		// matches by coincidence (chepherd Service DNS).
		url = "ws://127.0.0.1:9090/mcp/ws"
	}
	if err := mcpserver.BridgeStdioToHTTP(url); err != nil {
		return fmt.Errorf("chepherd mcp: %w", err)
	}
	return nil
}

// cmd/mcp.go — `chepherd mcp` stdio bridge subcommand.
//
// When an agent's MCP config lists chepherd as an MCP server with stdio
// transport, the agent spawns `chepherd mcp` as a subprocess and exchanges
// JSON-RPC over its stdio. This subcommand is a thin bridge: it dials the
// chepherd runtime's Unix socket and shuttles bytes between agent-stdio
// and the socket. The actual tool handlers live in the runtime (one
// process; no duplicated state).
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/chepherd/chepherd/internal/mcpserver"
)

var mcpFlagSock string

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "MCP stdio bridge to chepherd's runtime (used by agent MCP configs; not interactive)",
	Long: `chepherd mcp is the MCP stdio bridge. Agents are configured to spawn it as
a subprocess via their ~/.claude.json / ~/.qwen/settings.json / .mcp.json.
It dials chepherd's runtime Unix socket and proxies stdio JSON-RPC.

Not intended for interactive use. The chepherd 'run' command emits the
agent MCP config pointing here automatically when it spawns peers.`,
	RunE: runMCPCmd,
}

func init() {
	mcpCmd.Flags().StringVar(&mcpFlagSock, "sock", "", "runtime socket path (default: ~/.local/state/chepherd-v05/runtime.sock)")
	rootCmd.AddCommand(mcpCmd)
}

func runMCPCmd(_ *cobra.Command, _ []string) error {
	sock := mcpFlagSock
	if sock == "" {
		home, _ := os.UserHomeDir()
		sock = filepath.Join(home, ".local", "state", "chepherd-v05", "runtime.sock")
	}
	if _, err := os.Stat(sock); err != nil {
		return fmt.Errorf("chepherd mcp: runtime socket not found at %s — is `chepherd run` active?", sock)
	}
	return mcpserver.BridgeStdioToSocket(sock)
}

package cmd

import (
	"github.com/spf13/cobra"

	"github.com/agenity-org/agenity/internal/tui"
)

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Open the interactive TUI dashboard (default action)",
	Long: `Launches the k9s-style terminal UI showing all watched sessions, their
scorecards, bands, recent verdicts, and the live supervisor log.

The dashboard auto-discovers sessions from ~/.local/state/chepherd/ (Go
daemon) and ~/.local/state/workflow/ (legacy Python supervisor) — runs
read-only against either backend.

Keyboard shortcuts visible in the footer; press ? for the full overlay.

This is the default action: running 'chepherd' with no subcommand is
identical to 'chepherd dashboard'.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		app := tui.New()
		return app.Run()
	},
}

func init() {
	rootCmd.AddCommand(dashboardCmd)
	// Make this the default when 'chepherd' is run with no subcommand.
	rootCmd.RunE = dashboardCmd.RunE
}

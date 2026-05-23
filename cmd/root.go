// Package cmd implements the chepherd CLI surface.
//
// The root command (no subcommand) opens the interactive TUI dashboard.
// Subcommands provide non-interactive operations: status snapshot,
// daemon lifecycle, per-session pause/unpause, and the init wizard.
package cmd

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "chepherd",
	Short: "TUI supervisor for parallel AI coding agents",
	Long: `chepherd watches every Claude Code session running in tmux on this host,
scores each session on goal-clarity / velocity / focus / end-state-proximity,
and coaches them when they drift using your own ~/.claude/CLAUDE.md as the rubric.

Run with no arguments to open the interactive dashboard. Use subcommands for
non-interactive operations.`,
	SilenceUsage: true,
	// Run is intentionally left nil here — when a subcommand isn't given,
	// cobra falls through to the dashboard command (wired in dashboard.go).
}

// Execute is the package entry point called from main().
func Execute() error {
	return rootCmd.Execute()
}

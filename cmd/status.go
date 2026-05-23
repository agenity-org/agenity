package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/chepherd/chepherd/internal/state"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "One-shot text snapshot of all watched sessions",
	Long: `Reads the live state files at ~/.local/state/chepherd/sessions/*.json
(or the legacy Python supervisor at ~/.local/state/workflow/sessions/*.json)
and prints a compact table of each session's current band + last scorecard +
last verdict + next-tick time.

Read-only — does not contact the daemon, does not call any API.`,
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	sessions, err := state.LoadAllSessions()
	if err != nil {
		return fmt.Errorf("load sessions: %w", err)
	}
	if len(sessions) == 0 {
		fmt.Fprintln(os.Stderr, "no session state found")
		fmt.Fprintln(os.Stderr, "checked:")
		for _, d := range state.DefaultStateDirs() {
			fmt.Fprintf(os.Stderr, "  %s\n", d)
		}
		return nil
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].TmuxName < sessions[j].TmuxName
	})

	const headerFmt = "%-16s %-10s %-7s %-12s %-22s %-8s\n"
	const rowFmt = "%-16s %-10s %-7s %-12s %-22s %-8s\n"
	fmt.Printf(headerFmt, "tmux", "band", "G/V/F/E", "last_verdict", "last_intervention_at", "intervs")
	fmt.Printf(headerFmt,
		"────────────────",
		"──────────",
		"───────",
		"────────────",
		"──────────────────────",
		"───────")
	for _, s := range sessions {
		score := s.FormatScorecard()
		fmt.Printf(rowFmt,
			s.TmuxName,
			s.Band,
			score,
			truncate(s.LastVerdict, 12),
			truncate(s.LastInterventionAt, 22),
			fmt.Sprintf("%d", s.InterventionCount),
		)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return s[:n-1] + "…"
}

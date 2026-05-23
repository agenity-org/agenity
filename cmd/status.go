package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/chepherd/chepherd/internal/state"
)

var (
	statusJSON    bool
	statusWatchN  int
	statusBand    string
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
	statusCmd.Flags().BoolVar(&statusJSON, "json", false,
		"emit JSON instead of the human table (for piping to jq)")
	statusCmd.Flags().IntVar(&statusWatchN, "watch", 0,
		"refresh every N seconds (0 = one-shot)")
	statusCmd.Flags().StringVar(&statusBand, "filter-band", "",
		"only show sessions in this band (trusted/standard/concerned/crisis/paused)")
}

func runStatus(cmd *cobra.Command, args []string) error {
	if statusWatchN > 0 {
		// Watch loop — clear screen + redraw every N sec.
		for {
			fmt.Print("\033[H\033[2J") // clear screen + home
			fmt.Printf("chepherd status — refreshing every %ds — press Ctrl+C to exit\n\n",
				statusWatchN)
			if err := renderStatus(); err != nil {
				return err
			}
			time.Sleep(time.Duration(statusWatchN) * time.Second)
		}
	}
	return renderStatus()
}

func renderStatus() error {
	sessions, err := state.LoadAllSessions()
	if err != nil {
		return fmt.Errorf("load sessions: %w", err)
	}
	if statusBand != "" {
		filtered := sessions[:0]
		for _, s := range sessions {
			if s.Band == statusBand {
				filtered = append(filtered, s)
			}
		}
		sessions = filtered
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

	if statusJSON {
		// Compact one-session-per-line JSON — pipe-friendly.
		enc := json.NewEncoder(os.Stdout)
		for _, s := range sessions {
			_ = enc.Encode(s)
		}
		return nil
	}

	const headerFmt = "%-16s %-10s %-7s %-12s %-22s %-8s\n"
	fmt.Printf(headerFmt, "tmux", "band", "G/V/F/E", "last_verdict", "last_intervention_at", "intervs")
	fmt.Printf(headerFmt,
		strings.Repeat("─", 16),
		strings.Repeat("─", 10),
		strings.Repeat("─", 7),
		strings.Repeat("─", 12),
		strings.Repeat("─", 22),
		strings.Repeat("─", 7))
	for _, s := range sessions {
		score := s.FormatScorecard()
		fmt.Printf(headerFmt,
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

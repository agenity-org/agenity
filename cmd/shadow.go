package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/chepherd/chepherd/internal/daemon"
	stylepkg "github.com/chepherd/chepherd/internal/style"
)

var (
	shadowOnce    bool
	shadowSession string
)

var shadowCmd = &cobra.Command{
	Use:   "shadow",
	Short: "Shadow daemon — runs verdicts alongside Python supervisor, NEVER injects",
	Long: `Runs the Go daemon in dry-run shadow mode: computes verdicts using the
same judge.md prompt + same signals as the Python supervisor, persists state
to ~/.local/state/chepherd-shadow/ (separate from Python's ~/.local/state/workflow/),
and NEVER calls tmux send-keys.

Used during the dual-daemon period to compare verdict agreement between
implementations before cutting over. Once agreement is high enough, the
Python supervisor stops and the Go daemon (without --shadow) takes over.

Flags:
  --once          run a single discovery+tick cycle then exit
  --session NAME  restrict to one tmux session (for targeted A/B testing)`,
	RunE: runShadow,
}

func init() {
	rootCmd.AddCommand(shadowCmd)
	shadowCmd.Flags().BoolVar(&shadowOnce, "once", false,
		"Run one tick across all sessions then exit")
	shadowCmd.Flags().StringVar(&shadowSession, "session", "",
		"Restrict to one tmux session (e.g. iogrid-8)")
}

func runShadow(cmd *cobra.Command, args []string) error {
	cfg := daemon.DefaultJudgeConfig()
	if cfg.SystemPromptPath == "" {
		return fmt.Errorf("could not locate judge.md — set ~/.config/chepherd/judge.md")
	}
	stateDir := daemon.DefaultStateDir()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	fmt.Printf("%s using judge.md=%s state-dir=%s\n",
		stylepkg.Sprint(stylepkg.Logo, "chepherd shadow"),
		cfg.SystemPromptPath, stateDir)

	if shadowOnce {
		return tickOnce(cfg, stateDir, shadowSession)
	}

	// Continuous loop — pick due session every minute (adaptive cadence).
	for {
		if err := tickOnce(cfg, stateDir, shadowSession); err != nil {
			fmt.Fprintln(os.Stderr, "tick error:", err)
		}
		time.Sleep(60 * time.Second)
	}
}

func tickOnce(cfg daemon.JudgeConfig, stateDir, only string) error {
	sessions, err := daemon.DiscoverSessions()
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	if only != "" {
		filtered := sessions[:0]
		for _, s := range sessions {
			if s.TmuxName == only {
				filtered = append(filtered, s)
			}
		}
		sessions = filtered
	}
	fmt.Printf("discovered %d sessions\n", len(sessions))

	now := time.Now().UTC()
	for _, s := range sessions {
		state, err := daemon.LoadState(stateDir, s.UUID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load state %s: %v\n", s.UUID, err)
			continue
		}
		// Adaptive cadence: skip if not due yet (per state's next_tick_at).
		if nt, ok := state["next_tick_at"].(string); ok {
			if dt, err := time.Parse(time.RFC3339, nt); err == nil && dt.After(now) {
				fmt.Printf("  %s skip (next tick at %s)\n", s.TmuxName, dt.Format("15:04:05"))
				continue
			}
		}
		sig, err := daemon.BuildSignals(s, state)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s signals: %v\n", s.TmuxName, err)
			continue
		}
		if sig.PauseDetected {
			fmt.Printf("  %s pause-keyword detected; skipping\n", s.TmuxName)
			continue
		}

		userPrompt := daemon.FormatSignalsForPrompt(s, sig)
		v, err := daemon.CallJudge(cfg, userPrompt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s judge: %v\n", s.TmuxName, err)
			continue
		}

		band, intervalMin := daemon.ComputeBand(state, sig, v)
		state["trust_band"] = string(band)
		state["next_tick_at"] = now.Add(time.Duration(intervalMin) * time.Minute).Format(time.RFC3339)
		daemon.RecordVerdictToState(state, sig, v)
		if err := daemon.SaveState(stateDir, s.UUID, state); err != nil {
			fmt.Fprintf(os.Stderr, "  %s save: %v\n", s.TmuxName, err)
		}

		sc := "?/?/?/?"
		if v.Scorecard != nil {
			sc = fmt.Sprintf("%d/%d/%d/%d", v.Scorecard["G"], v.Scorecard["V"],
				v.Scorecard["F"], v.Scorecard["E"])
		}
		fmt.Printf("  %s %s ref=%s %s cost=$%.4f band=%s next=%dmin\n",
			stylepkg.Sprint(stylepkg.Title, s.TmuxName),
			stylepkg.Sprint(stylepkg.VerdictColor(v.Verdict),
				"verdict="+v.Verdict),
			v.PrincipleRef,
			"G/V/F/E="+sc,
			v.CostUSD,
			band,
			intervalMin)

		// Persist a log line that matches Python supervisor format so the
		// tail/dashboard can read either daemon's output.
		appendShadowLog(s.TmuxName, v, band, intervalMin)
	}
	return nil
}

func appendShadowLog(tmux string, v *daemon.Verdict, band daemon.TrustBand, intervalMin int) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".local", "state", "chepherd-shadow", "chepherd.log")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	now := time.Now().UTC().Format("2006-01-02T15:04:05+00:00")
	sc := "?/?/?/?"
	if v.Scorecard != nil {
		sc = fmt.Sprintf("%d/%d/%d/%d", v.Scorecard["G"], v.Scorecard["V"],
			v.Scorecard["F"], v.Scorecard["E"])
	}
	fmt.Fprintf(f, "[%s] %s: verdict=%s ref=%s G/V/F/E=%s reason=%s\n",
		now, tmux, v.Verdict, v.PrincipleRef, sc, v.Reason)
	if v.ScorecardNote != "" {
		fmt.Fprintf(f, "[%s] %s: scorecard_note: %s\n", now, tmux, v.ScorecardNote)
	}
	fmt.Fprintf(f, "[%s] %s: BAND → %s (next_tick in %dmin)\n", now, tmux, band, intervalMin)
}

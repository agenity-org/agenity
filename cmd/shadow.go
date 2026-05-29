package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/chepherd/chepherd/internal/shepherd"
	"github.com/chepherd/chepherd/internal/daemon/rc"
	"github.com/chepherd/chepherd/internal/daemon/rc/envelope"
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
	cfg := shepherd.DefaultJudgeConfig()
	if cfg.SystemPromptPath == "" {
		return fmt.Errorf("could not locate judge.md — set ~/.config/chepherd/judge.md")
	}
	stateDir := shepherd.DefaultStateDir()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	fmt.Printf("%s using judge.md=%s state-dir=%s\n",
		stylepkg.Sprint(stylepkg.Logo, "chepherd shadow"),
		cfg.SystemPromptPath, stateDir)

	// Start rc Listener in background if rc is enabled.
	rcCtx, rcCancel := context.WithCancel(context.Background())
	defer rcCancel()
	listener := startRCListener(rcCtx)

	if shadowOnce {
		return tickOnce(cfg, stateDir, shadowSession, listener)
	}

	// Continuous loop — pick due session every minute (adaptive cadence).
	for {
		if err := tickOnce(cfg, stateDir, shadowSession, listener); err != nil {
			fmt.Fprintln(os.Stderr, "tick error:", err)
		}
		time.Sleep(60 * time.Second)
	}
}

// startRCListener reads rc.toml + spawns the Listener if enabled. Returns
// nil when rc is disabled — the rest of the daemon treats nil as no-op
// (all rc.Listener methods are nil-safe).
func startRCListener(ctx context.Context) *rc.Listener {
	cfg, err := rc.LoadConfig(rc.DefaultConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "rc config: %v (continuing with rc disabled)\n", err)
		return nil
	}
	if !cfg.Enabled {
		return nil
	}
	l, err := rc.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rc new: %v (continuing with rc disabled)\n", err)
		return nil
	}
	fmt.Printf("  %s rc enabled — mode=%s relay=%s bastion=%s\n",
		stylepkg.Sprint(stylepkg.BandTrusted, "✓"),
		cfg.Mode, cfg.RelayURL, cfg.BastionID)
	go func() {
		if err := l.Run(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "rc listener: %v\n", err)
		}
	}()
	return l
}

func tickOnce(cfg shepherd.JudgeConfig, stateDir, only string, listener *rc.Listener) error {
	sessions, err := shepherd.DiscoverSessions()
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
		state, err := shepherd.LoadState(stateDir, s.UUID)
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
		sig, err := shepherd.BuildSignals(s, state)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s signals: %v\n", s.TmuxName, err)
			continue
		}
		if sig.PauseDetected {
			fmt.Printf("  %s pause-keyword detected; skipping\n", s.TmuxName)
			continue
		}

		userPrompt := shepherd.FormatSignalsForPrompt(s, sig)
		v, err := shepherd.CallJudge(cfg, userPrompt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s judge: %v\n", s.TmuxName, err)
			continue
		}

		band, intervalMin := shepherd.ComputeBand(state, sig, v)
		state["trust_band"] = string(band)
		state["next_tick_at"] = now.Add(time.Duration(intervalMin) * time.Minute).Format(time.RFC3339)
		shepherd.RecordVerdictToState(state, sig, v)
		if err := shepherd.SaveState(stateDir, s.UUID, state); err != nil {
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

		// Publish to rc peers (no-op when rc disabled).
		if listener != nil {
			listener.PublishVerdict(envelope.VerdictPayload{
				Session:       s.TmuxName,
				Verdict:       v.Verdict,
				PrincipleRef:  v.PrincipleRef,
				Scorecard:     v.Scorecard,
				ScorecardNote: v.ScorecardNote,
				Message:       v.Message,
				CostUSD:       v.CostUSD,
				Injected:      false, // shadow mode never injects
			})
		}
	}
	return nil
}

func appendShadowLog(tmux string, v *shepherd.Verdict, band shepherd.TrustBand, intervalMin int) {
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

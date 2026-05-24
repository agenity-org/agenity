package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/chepherd/chepherd/internal/daemon"
	"github.com/chepherd/chepherd/internal/daemon/rc"
	"github.com/chepherd/chepherd/internal/daemon/rc/envelope"
	stylepkg "github.com/chepherd/chepherd/internal/style"
)

// daemonCmd is the LIVE supervisor — same judge + same signals as
// `chepherd shadow`, but actually pastes coach messages into the
// tmux pane when the judge says verdict='coach' or 'intervene'.
//
// This is the binary chepherd/chepherd#16 cuts over to from the
// legacy Python supervisor. State lives in ~/.local/state/chepherd-go/
// (separate from Python's ~/.local/state/workflow/ + from shadow's
// ~/.local/state/chepherd-shadow/) so the three implementations can
// coexist without state collision during the dual-daemon period.

var (
	daemonOnce     bool
	daemonInterval time.Duration
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Live Go supervisor — judge + inject (replaces Python supervisor.py)",
	Long: `Runs the Go daemon in LIVE mode: discovers tmux sessions, computes
verdicts via the judge.md prompt, and PASTES coach/intervene messages
into the target session's tmux pane (the action Python's
supervisor.py performs today).

State lives in ~/.local/state/chepherd-go/ — distinct from Python's
~/.local/state/workflow/ + shadow's ~/.local/state/chepherd-shadow/
so the three implementations can coexist during the cutover window.

Cutover runbook: docs/RUNBOOK-cutover-py-to-go.md (chepherd/chepherd#16).

Flags:
  --once          run a single discovery+tick cycle then exit
  --interval D    sleep between cycles (default 60s — matches Python)`,
	RunE: runDaemon,
}

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.Flags().BoolVar(&daemonOnce, "once", false,
		"Run one tick across all sessions then exit")
	daemonCmd.Flags().DurationVar(&daemonInterval, "interval", 60*time.Second,
		"Sleep between cycles (adaptive cadence per session honoured inside the cycle)")
}

func runDaemon(cmd *cobra.Command, args []string) error {
	cfg := daemon.DefaultJudgeConfig()
	if cfg.SystemPromptPath == "" {
		return fmt.Errorf("could not locate judge.md — set ~/.config/chepherd/judge.md")
	}
	stateDir := liveStateDir()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	fmt.Printf("%s LIVE — judge.md=%s state-dir=%s\n",
		stylepkg.SprintBold(stylepkg.Logo, "chepherd daemon"),
		cfg.SystemPromptPath, stateDir)

	// Start rc Listener in background if rc is enabled (same wiring
	// as `chepherd shadow`, reusing the same helper). Coach injections
	// publish via listener.PublishVerdict so connected web/mobile
	// clients see them in real-time.
	rcCtx, rcCancel := context.WithCancel(context.Background())
	defer rcCancel()
	listener := startRCListener(rcCtx)

	if daemonOnce {
		return daemonTickOnce(cfg, stateDir, listener)
	}
	for {
		if err := daemonTickOnce(cfg, stateDir, listener); err != nil {
			fmt.Fprintln(os.Stderr, "tick error:", err)
		}
		time.Sleep(daemonInterval)
	}
}

// liveStateDir is the canonical state directory for the LIVE daemon
// (~/.local/state/chepherd-go/). Distinct from shadow + Python paths.
func liveStateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "chepherd-go")
}

// daemonTickOnce is the same shape as shadow.tickOnce but ACTUALLY
// injects coach/intervene messages into the target tmux pane.
func daemonTickOnce(cfg daemon.JudgeConfig, stateDir string, listener *rc.Listener) error {
	sessions, err := daemon.DiscoverSessions()
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	fmt.Printf("discovered %d sessions\n", len(sessions))

	now := time.Now().UTC()
	for _, s := range sessions {
		state, err := daemon.LoadState(stateDir, s.UUID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load state %s: %v\n", s.UUID, err)
			continue
		}
		// Adaptive cadence: skip if not due yet.
		if nt, ok := state["next_tick_at"].(string); ok {
			if dt, err := time.Parse(time.RFC3339, nt); err == nil && dt.After(now) {
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

		injected := false
		if v.Verdict == "coach" || v.Verdict == "intervene" {
			if v.Message == "" {
				fmt.Fprintf(os.Stderr, "  %s judge said %s but message empty; not injecting\n",
					s.TmuxName, v.Verdict)
			} else if err := tmuxPaste(s.TmuxName, v.Message); err != nil {
				fmt.Fprintf(os.Stderr, "  %s inject failed: %v\n", s.TmuxName, err)
			} else {
				injected = true
				cnt, _ := state["intervention_count"].(int)
				state["intervention_count"] = cnt + 1
				state["last_intervention_at"] = now.Format(time.RFC3339)
			}
		}

		if err := daemon.SaveState(stateDir, s.UUID, state); err != nil {
			fmt.Fprintf(os.Stderr, "  %s save: %v\n", s.TmuxName, err)
		}

		sc := "?/?/?/?"
		if v.Scorecard != nil {
			sc = fmt.Sprintf("%d/%d/%d/%d",
				v.Scorecard["G"], v.Scorecard["V"],
				v.Scorecard["F"], v.Scorecard["E"])
		}
		injectedMark := ""
		if injected {
			injectedMark = stylepkg.Sprint(stylepkg.Injected, " ✓injected")
		}
		fmt.Printf("  %s %s ref=%s G/V/F/E=%s cost=$%.4f band=%s next=%dmin%s\n",
			stylepkg.Sprint(stylepkg.Title, s.TmuxName),
			stylepkg.Sprint(stylepkg.VerdictColor(v.Verdict),
				"verdict="+v.Verdict),
			v.PrincipleRef, sc, v.CostUSD, band, intervalMin, injectedMark)

		appendLiveLog(s.TmuxName, v, band, intervalMin, injected)

		if listener != nil {
			listener.PublishVerdict(envelope.VerdictPayload{
				Session:       s.TmuxName,
				Verdict:       v.Verdict,
				PrincipleRef:  v.PrincipleRef,
				Scorecard:     v.Scorecard,
				ScorecardNote: v.ScorecardNote,
				Message:       v.Message,
				CostUSD:       v.CostUSD,
				Injected:      injected,
			})
		}
	}
	return nil
}

// tmuxPaste — same approach as rc/handler.LocalCommandHandler.Inject:
// load-buffer reads stdin → paste-buffer pastes → send-keys Enter
// submits. Duplicated here so the live daemon doesn't have to spin
// up a full LocalCommandHandler instance just to call one method.
func tmuxPaste(tmuxName, message string) error {
	c1 := exec.Command("tmux", "load-buffer", "-")
	stdin, err := c1.StdinPipe()
	if err != nil {
		return err
	}
	if err := c1.Start(); err != nil {
		return err
	}
	if _, err := stdin.Write([]byte(message)); err != nil {
		_ = c1.Wait()
		return err
	}
	_ = stdin.Close()
	if err := c1.Wait(); err != nil {
		return fmt.Errorf("tmux load-buffer: %w", err)
	}
	if err := exec.Command("tmux", "paste-buffer", "-t", tmuxName).Run(); err != nil {
		return fmt.Errorf("tmux paste-buffer: %w", err)
	}
	if err := exec.Command("tmux", "send-keys", "-t", tmuxName, "Enter").Run(); err != nil {
		return fmt.Errorf("tmux send-keys: %w", err)
	}
	return nil
}

func appendLiveLog(tmux string, v *daemon.Verdict, band daemon.TrustBand, intervalMin int, injected bool) {
	path := filepath.Join(liveStateDir(), "chepherd.log")
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
	if injected {
		fmt.Fprintf(f, "[%s] %s: INJECTED coach message into tmux pane\n", now, tmux)
	}
	fmt.Fprintf(f, "[%s] %s: BAND → %s (next_tick in %dmin)\n", now, tmux, band, intervalMin)
}

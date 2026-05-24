// cmd/run.go — `chepherd run` v0.5 entrypoint
//
// This is the new pty-host-based runtime. The legacy `chepherd dashboard`
// and `chepherd daemon` paths (tmux-based) are left UNTOUCHED so existing
// users keep working while v0.5 stabilizes.
//
// `chepherd run` boots the runtime, spawns Adam (and Chepherd if monitored
// mode is on), wires the @target relay, and tails to stdout. For v0.5.0
// this is a headless harness — the TUI client refactor is tracked
// separately as chepherd/chepherd#55.
//
// Usage:
//
//	chepherd run                          # default: spawn Adam + Chepherd
//	chepherd run --unmonitored            # spawn Adam only (no shepherd)
//	chepherd run --agent qwen-code        # use qwen-code as default agent
//	chepherd run --cwd ~/repos/myproject  # set Adam's CWD
package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/chepherd/chepherd/internal/messagebus"
	"github.com/chepherd/chepherd/internal/prompts"
	"github.com/chepherd/chepherd/internal/runtime"
	"github.com/chepherd/chepherd/internal/runtimetui"
)

var (
	runFlagAgent       string
	runFlagCwd         string
	runFlagUnmonitored bool
	runFlagStateDir    string
	runFlagHeadless    bool
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "v0.5 runtime — pty-host-based, replaces the tmux supervisor (under active development)",
	Long: `'chepherd run' starts the new chepherd v0.5 runtime: pty-host hosting agents,
@target in-band relay for agent-to-agent messaging, runtime registry with tribe/role
metadata.

By default it spawns two sessions:
  - Adam      (worker role; the user-facing primary agent)
  - Chepherd  (shepherd role; the meta-supervisor watching Adam)

Use --unmonitored to spawn only Adam (no shepherd).

This is the v0.5 development entrypoint. The legacy 'chepherd dashboard' and
'chepherd daemon' (tmux-based) are LEFT UNTOUCHED so existing users keep working.

When the TUI refactor lands (chepherd/chepherd#55), the dashboard client will
target this runtime instead of tmux.`,
	RunE: runRunCmd,
}

func init() {
	runCmd.Flags().StringVar(&runFlagAgent, "agent", "claude-code", "default agent CLI slug (claude-code, qwen-code, aider, ...)")
	runCmd.Flags().StringVar(&runFlagCwd, "cwd", "", "Adam's working directory (default: current)")
	runCmd.Flags().BoolVar(&runFlagUnmonitored, "unmonitored", false, "spawn Adam only; no Chepherd (4-eyes off)")
	runCmd.Flags().StringVar(&runFlagStateDir, "state-dir", "", "runtime state dir (default: ~/.local/state/chepherd-v05)")
	runCmd.Flags().BoolVar(&runFlagHeadless, "headless", false, "skip TUI; print runtime status + sleep (for testing / systemd)")
	rootCmd.AddCommand(runCmd)
}

func runRunCmd(cmd *cobra.Command, args []string) error {
	stateDir := runFlagStateDir
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, ".local", "state", "chepherd-v05")
	}
	cwd := runFlagCwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	fmt.Printf("chepherd run — v0.5 runtime\n")
	fmt.Printf("  state-dir: %s\n", stateDir)
	fmt.Printf("  agent:     %s\n", runFlagAgent)
	fmt.Printf("  cwd:       %s\n", cwd)
	fmt.Printf("  monitored: %v\n\n", !runFlagUnmonitored)

	rt, err := runtime.New(stateDir)
	if err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	relay := messagebus.New(rt)

	// Spawn Adam
	adamInfo, adamSession, err := rt.Spawn(runtime.SpawnSpec{
		Name:         "adam",
		AgentSlug:    runFlagAgent,
		Tribe:        "default",
		Role:         runtime.RoleWorker,
		Cwd:          cwd,
		SystemPrompt: prompts.Adam,
	})
	if err != nil {
		return fmt.Errorf("spawn adam: %w", err)
	}
	fmt.Printf("✓ Spawned Adam (%s) — id=%s tribe=%s role=%s\n",
		runFlagAgent, adamInfo.ID, adamInfo.Tribe, adamInfo.Role)
	if err := relay.Watch(adamSession, "adam"); err != nil {
		return fmt.Errorf("watch adam: %w", err)
	}

	// Spawn Chepherd if monitored
	if !runFlagUnmonitored {
		chepInfo, chepSession, err := rt.Spawn(runtime.SpawnSpec{
			Name:         "chepherd",
			AgentSlug:    runFlagAgent,
			Tribe:        "default",
			Role:         runtime.RoleShepherd,
			Cwd:          cwd,
			SystemPrompt: prompts.Shepherd,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: spawn chepherd: %v (continuing unmonitored)\n", err)
		} else {
			fmt.Printf("✓ Spawned Chepherd (%s) — id=%s tribe=%s role=%s shepherding=%v\n",
				runFlagAgent, chepInfo.ID, chepInfo.Tribe, chepInfo.Role, chepInfo.Shepherding)
			if err := relay.Watch(chepSession, "chepherd"); err != nil {
				fmt.Fprintf(os.Stderr, "warn: watch chepherd: %v\n", err)
			}
		}
	}

	fmt.Println()
	fmt.Println("Runtime up. Sessions in registry:")
	for _, info := range rt.List() {
		fmt.Printf("  - %s (%s, %s) in tribe %s\n", info.Name, info.Role, info.AgentSlug, info.Tribe)
	}
	fmt.Println()

	// Graceful shutdown plumbing — fires whether TUI exits naturally or
	// SIGINT/SIGTERM arrives.
	shutdown := func() {
		fmt.Println("\nShutting down...")
		relay.Stop()
		for _, info := range rt.List() {
			_ = rt.Stop(info.Name)
		}
	}

	if runFlagHeadless {
		fmt.Println("Headless mode. Press Ctrl-C to stop.")
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		heartbeat := time.NewTicker(30 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-sig:
				shutdown()
				return nil
			case <-heartbeat.C:
				fmt.Printf("[%s] alive sessions: %d\n", time.Now().UTC().Format("15:04:05"), len(rt.List()))
			}
		}
	}

	// Launch the v0.5 TUI (separate package from the legacy internal/tui).
	app := runtimetui.New(rt)
	// Background SIGINT/SIGTERM handler — calls Stop() to break out of TUI.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		app.Stop()
	}()
	err = app.Run()
	shutdown()
	return err
}

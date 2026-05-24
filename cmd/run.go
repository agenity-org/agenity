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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/chepherd/chepherd/internal/mcpserver"
	"github.com/chepherd/chepherd/internal/messagebus"
	"github.com/chepherd/chepherd/internal/prompts"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
	"github.com/chepherd/chepherd/internal/runtime"
	"github.com/chepherd/chepherd/internal/runtimehttp"
	"github.com/chepherd/chepherd/internal/runtimetui"
)

var (
	runFlagAgent       string
	runFlagCwd         string
	runFlagUnmonitored bool
	runFlagStateDir    string
	runFlagHeadless    bool
	runFlagListen      string
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
	runCmd.Flags().StringVar(&runFlagListen, "listen", "127.0.0.1:8080", "HTTP/WS listen addr (set to '' to disable; for web/mobile clients)")
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
	// Auto-watch every session spawned by the runtime — including dynamic
	// MCP `chepherd.spawn` invocations. Without this, only the initial
	// Adam/Chepherd would have their output scanned for @target lines.
	rt.AddSpawnHook(func(s *session.Session, name string) {
		if err := relay.Watch(s, name); err != nil {
			fmt.Fprintf(os.Stderr, "warn: relay.Watch %s: %v\n", name, err)
		}
	})

	// MCP server on Unix socket — `chepherd mcp` subprocess (used by agents)
	// dials this socket and proxies JSON-RPC. One server per runtime.
	mcpSrv := mcpserver.New(rt, mcpserver.DefaultSockPath(stateDir))
	if err := mcpSrv.Start(); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	fmt.Printf("✓ MCP server listening on %s\n", mcpserver.DefaultSockPath(stateDir))

	// HTTP/WS server — for web (chepherd-rc-web), mobile (rc-ios/android),
	// and remote-TUI clients. Disabled when --listen "".
	var httpSrv *http.Server
	if runFlagListen != "" {
		rs := runtimehttp.New(rt)
		hs, err := rs.ServeOn(runFlagListen)
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		httpSrv = hs
		fmt.Printf("✓ HTTP/WS server listening on http://%s (web/mobile clients)\n", runFlagListen)
	}

	// No auto-spawn. The operator opens the dashboard, sees zero sessions,
	// and clicks "+ spawn agent" to create what they want. A shepherd is
	// opt-in via the web UI (or the chepherd.spawn MCP tool with
	// role=shepherd) — never default.
	_ = prompts.Worker   // exposed via runtimehttp for "spawn worker w/ default prompt"
	_ = prompts.Shepherd // exposed via runtimehttp for "spawn shepherd w/ default prompt"
	fmt.Println("\nRuntime up. Open http://" + runFlagListen + " (dashboard) to spawn sessions.")
	fmt.Println()

	// Graceful shutdown plumbing — fires whether TUI exits naturally or
	// SIGINT/SIGTERM arrives.
	shutdown := func() {
		fmt.Println("\nShutting down...")
		if httpSrv != nil {
			_ = httpSrv.Close()
		}
		mcpSrv.Stop()
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

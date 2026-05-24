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
//	chepherd run                          # default: zero workers, one shepherd
//	chepherd run --no-shepherd            # zero workers, zero shepherds (opt out)
//	chepherd run --agent qwen-code        # use qwen-code as default agent
//	chepherd run --cwd ~/repos/myproject  # initial cwd for any session that omits it
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
	runFlagNoShepherd  bool
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

By default it starts with ZERO workers and ONE shepherd (the meta-supervisor
watching the "default" tribe). Workers are spawned on demand by the operator
via the dashboard's "+ spawn agent" button. Pass --no-shepherd to start
completely empty (4-eyes off).

This is the v0.5 development entrypoint. The legacy 'chepherd dashboard' and
'chepherd daemon' (tmux-based) are LEFT UNTOUCHED so existing users keep working.

When the TUI refactor lands (chepherd/chepherd#55), the dashboard client will
target this runtime instead of tmux.`,
	RunE: runRunCmd,
}

func init() {
	runCmd.Flags().StringVar(&runFlagAgent, "agent", "claude-code", "default agent CLI slug (claude-code, qwen-code, aider, ...)")
	runCmd.Flags().StringVar(&runFlagCwd, "cwd", "", "fallback working directory (default: current)")
	runCmd.Flags().BoolVar(&runFlagNoShepherd, "no-shepherd", false, "skip the default shepherd (4-eyes off)")
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
	fmt.Printf("  shepherd:  %v\n\n", !runFlagNoShepherd)

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

	// Zero workers by default — the operator opens the dashboard and
	// spawns what they want. ONE shepherd is auto-spawned to watch the
	// "default" tribe so 4-eyes coverage is on by default; pass
	// --no-shepherd to opt out (or stop it from the dashboard).
	_ = prompts.Worker // exposed via runtimehttp for explicit worker spawns w/ default prompt
	if !runFlagNoShepherd {
		_, shepSess, err := rt.Spawn(runtime.SpawnSpec{
			Name:         "shepherd",
			AgentSlug:    runFlagAgent,
			Tribe:        "default",
			Role:         runtime.RoleShepherd,
			Cwd:          cwd,
			SystemPrompt: prompts.Shepherd,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: default shepherd failed (continuing): %v\n", err)
		} else {
			fmt.Println("✓ Default shepherd spawned (4-eyes on; --no-shepherd to opt out)")
			// Boot the shepherd: accept the trust prompt + kick off its
			// watch loop with an initial mission prompt. Without this the
			// shepherd just sits at "Yes, I trust this folder" forever
			// because Claude TUI is reactive — no operator means no input.
			go bootstrapShepherd(rt, shepSess)
		}
	}
	fmt.Println("\nRuntime up. Open http://" + runFlagListen + " (dashboard) to spawn workers.")
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

// bootstrapShepherd brings a freshly-spawned shepherd session into its
// watch cycle: accept the Claude-Code trust prompt + send a mission
// prompt + then keep poking it on a 5-minute tick so it actually runs
// list_sessions/read_pane periodically rather than going idle forever.
//
// Claude's TUI is reactive — without these pokes the shepherd would sit
// at the trust prompt and then at an empty input line indefinitely,
// which is exactly the symptom the operator reported on #79.
func bootstrapShepherd(rt *runtime.Runtime, sess *session.Session) {
	// Wait for the Claude TUI to render the trust prompt + welcome.
	time.Sleep(6 * time.Second)
	// Accept trust ("Yes, I trust this folder" — Enter).
	_, _ = sess.Write([]byte("\r"))
	time.Sleep(5 * time.Second)
	// Kick off the watch cycle. send_to_session adds Enter so this lands
	// as a real prompt submission (per the #76 kitty-kbd fix in mcpserver).
	kickoff := "Begin your shepherd duties. Use chepherd.list to see active sessions, then chepherd.read_pane(name, 40) on each non-paused session to assess what they're doing. Report a one-line status per session to the dashboard's human inbox via chepherd.alert_human if you spot anything noteworthy. Stay silent otherwise. After your first sweep, wait for the next tick I'll send you."
	_, _ = sess.Write([]byte(kickoff))
	time.Sleep(120 * time.Millisecond)
	_, _ = sess.Write([]byte("\r"))

	// Periodic ticks. Every 5 min, ask shepherd to do another sweep.
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for range tick.C {
		// Re-check session is still alive; rt.Get returns nil if it was stopped.
		live, _ := rt.Get("shepherd")
		if live == nil || live != sess {
			return
		}
		_, _ = sess.Write([]byte("Tick: do another sweep of active sessions and report anything that drifted since last tick."))
		time.Sleep(120 * time.Millisecond)
		_, _ = sess.Write([]byte("\r"))
	}
}

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
	"github.com/chepherd/chepherd/internal/vault"
)

var (
	runFlagAgent      string
	runFlagCwd        string
	runFlagNoShepherd bool
	runFlagStateDir   string
	runFlagHeadless   bool
	runFlagListen     string
	runFlagWebDir     string
	runFlagMCPListen  string
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
	runCmd.Flags().BoolVar(&runFlagNoShepherd, "no-shepherd", true, "skip the default shepherd (4-eyes off); use --no-shepherd=false to enable")
	runCmd.Flags().StringVar(&runFlagStateDir, "state-dir", "", "runtime state dir (default: ~/.local/state/chepherd-v05)")
	runCmd.Flags().BoolVar(&runFlagHeadless, "headless", false, "skip TUI; print runtime status + sleep (for testing / systemd)")
	runCmd.Flags().StringVar(&runFlagListen, "listen", "127.0.0.1:8080", "HTTP/WS listen addr (set to '' to disable; for web/mobile clients)")
	runCmd.Flags().StringVar(&runFlagWebDir, "web-dir", "", "serve Astro static build from this dir (production mode; empty = dev-proxy mode)")
	runCmd.Flags().StringVar(&runFlagMCPListen, "mcp-listen", "", "MCP HTTP/WS listen addr (default: $CHEPHERD_MCP_LISTEN or 0.0.0.0:9090)")
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

	// MCP server on HTTP/WebSocket — `chepherd mcp` subprocess (used by
	// agents) dials this endpoint and proxies JSON-RPC over the WS. One
	// server per runtime. Works on local Podman, multi-cluster K8s, and
	// the OpenOva Sovereign without any code change. Closes #124.
	mcpListen := runFlagMCPListen
	if mcpListen == "" {
		mcpListen = os.Getenv("CHEPHERD_MCP_LISTEN")
	}
	if mcpListen == "" {
		mcpListen = mcpserver.DefaultListenAddr
	}
	mcpSrv := mcpserver.New(rt)
	if err := mcpSrv.StartHTTP(mcpListen); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	fmt.Printf("✓ MCP server (HTTP/WS) listening on http://%s/mcp/ws\n", mcpListen)

	// HTTP/WS server — for web (chepherd-rc-web), mobile (rc-ios/android),
	// and remote-TUI clients. Disabled when --listen "".
	var httpSrv *http.Server
	if runFlagListen != "" {
		rs := runtimehttp.New(rt)
		rs.WebDir = runFlagWebDir
		// Vault — open (or create) in the state directory
		if vlt, err := vault.Open(filepath.Join(stateDir, "vault.json")); err != nil {
			fmt.Fprintf(os.Stderr, "warn: vault: %v (credential vault disabled)\n", err)
		} else {
			rs.Vault = vlt
			// Wire vault into the runtime so /run/secrets/claude-credentials
			// is sourced from the vault on every spawn (TV1 / R4).
			rt.SetVault(newRuntimeVaultAdapter(vlt))
		}
		hs, err := rs.ServeOn(runFlagListen)
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		httpSrv = hs
		if runFlagWebDir != "" {
			fmt.Printf("✓ HTTP/WS server + web UI on http://%s (web-dir: %s)\n", runFlagListen, runFlagWebDir)
		} else {
			fmt.Printf("✓ HTTP/WS server listening on http://%s (web/mobile clients)\n", runFlagListen)
		}
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
			Team:         "default",
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

// runtimeVaultAdapter adapts *vault.Vault to runtime.VaultProvider — the
// runtime package can't import vault directly without an import cycle, so
// we adapt at the cmd layer.
type runtimeVaultAdapter struct{ v *vault.Vault }

func newRuntimeVaultAdapter(v *vault.Vault) *runtimeVaultAdapter { return &runtimeVaultAdapter{v: v} }

func (a *runtimeVaultAdapter) ListByProvider(provider string) []runtime.VaultCredMeta {
	src := a.v.ListByProvider(provider)
	out := make([]runtime.VaultCredMeta, len(src))
	for i, c := range src {
		out[i] = runtime.VaultCredMeta{
			ID: c.ID, Provider: c.Provider, ProviderLabel: c.ProviderLabel,
			Label: c.Label, EnvVar: c.EnvVar,
		}
	}
	return out
}

func (a *runtimeVaultAdapter) GetValue(id string) (string, error) { return a.v.GetValue(id) }

// bootstrapShepherd brings a freshly-spawned shepherd session into its
// watch cycle: accept the Claude-Code trust prompt + send a mission
// prompt + then poke it on every spawn event AND on a regular tick so
// it actually runs list/read_pane periodically rather than going idle.
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
	// Kick off the watch cycle.
	const kickoff = "Begin the tick loop from your system brief. For every non-paused worker, call chepherd.list then chepherd.read_pane(name, 60), then chepherd.set_scorecard(name, G, V, F, E, D, note) with the 5-axis evaluation AND chepherd.record_verdict(name, verdict, message). Use baseline scores of 5/5/5/5/5 with note 'first observation; baseline scores' for any worker you haven't observed before. Each tick poke means: re-list, re-read, re-score, re-verdict every worker."
	pokeShepherd(sess, kickoff)

	// Event-driven: every new spawn (other than shepherd itself) triggers
	// an immediate sweep so the operator sees shepherd react in real time.
	rt.AddSpawnHook(func(_ *session.Session, name string) {
		if name == "shepherd" {
			return
		}
		// Give the new agent ~3s to print its initial pane content so
		// shepherd's read_pane has something to actually observe.
		go func(n string) {
			time.Sleep(3 * time.Second)
			live, _ := rt.Get("shepherd")
			if live == nil || live != sess {
				return
			}
			pokeShepherd(sess, "A new session was just spawned: '"+n+"'. Do an immediate chepherd.list + chepherd.read_pane('"+n+"', 40) to see what it's doing, then report one short status line via chepherd.alert_human.")
		}(name)
	})

	// Periodic baseline tick — 60s. Catches drift between explicit spawn
	// events (e.g. an existing agent that's been silent or stuck).
	// Anti-rot: after maxTicks the shepherd is retired and a fresh one
	// is spawned with anchored summary of the previous shepherd's state.
	const maxTicksBeforeRefresh = 50
	tickCount := 0
	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()
	for range tick.C {
		live, _ := rt.Get("shepherd")
		if live == nil || live != sess {
			return
		}
		tickCount++
		if tickCount >= maxTicksBeforeRefresh {
			// Anti-rot: fresh shepherd. Capture the current shepherd's
			// pane as the anchored handoff summary, then retire it +
			// spawn replacement. The MCP socket + dashboard see no
			// discontinuity — same name, same membership, same role.
			rt.RecordEvent(runtime.Event{
				Kind: "shepherd_refresh", Actor: "runtime",
				Body: "shepherd hit tick limit (50); refreshing for anti-rot",
			})
			pokeShepherd(sess, "FINAL TICK before refresh: write a 5-line summary of the current state of your watch (workers + their latest scorecard + any open coaching threads + open questions) via chepherd.record_event(kind='shepherd_handoff', body='<summary>'). I'll spawn a replacement shepherd in 10s with this summary as its boot context.")
			time.Sleep(15 * time.Second)
			_ = rt.Stop("shepherd")
			time.Sleep(2 * time.Second)
			// Respawn (skip cycle; new bootstrapShepherd starts its own loop)
			_, newSess, err := rt.Spawn(runtime.SpawnSpec{
				Name: "shepherd", AgentSlug: "claude-code", Team: "default",
				Role: runtime.RoleShepherd, Cwd: "/home/openova",
				SystemPrompt: prompts.Shepherd,
			})
			if err == nil {
				go bootstrapShepherd(rt, newSess)
			}
			return
		}
		pokeShepherd(sess, "Tick: chepherd.list + read_pane each non-paused worker. Then chepherd.set_scorecard + chepherd.record_verdict for each — update scores based on what changed since last tick. Stay quiet unless alert_human is needed.")
	}
}

// pokeShepherd writes a body to the shepherd's PTY then a separate \r.
// Two writes are necessary so kitty-keyboard-aware Claude treats the
// Enter as a distinct keypress event (the same #76 fix as the MCP
// send_to_session path).
func pokeShepherd(sess *session.Session, body string) {
	_, _ = sess.Write([]byte(body))
	time.Sleep(120 * time.Millisecond)
	_, _ = sess.Write([]byte("\r"))
}

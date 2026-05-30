// cmd/run.go — `chepherd run` v0.9.2 single canonical entrypoint.
//
// chepherd run wires the integrated control plane: runtime spawn-
// lifecycle + shepherd intelligence + persistence layer + A2A
// endpoint + MCP HTTP. The legacy `chepherd daemon` + `chepherd
// shadow` cobra verbs (tmux-based Python-supervisor parity paths)
// were retired in #208 — chepherd v0.9.2 has one canonical CLI
// entry per docs/V0.9.2-ARCHITECTURE.md.
//
// `chepherd run` boots the runtime, spawns Adam, and tails to stdout.
//
// Usage:
//
//	chepherd run                          # default: zero workers, one shepherd
//	chepherd run --no-shepherd            # zero workers, zero shepherds (opt out)
//	chepherd run --agent qwen-code        # use qwen-code as default agent
//	chepherd run --cwd ~/repos/myproject  # initial cwd for any session that omits it
package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/chepherd/chepherd/internal/a2a"
	"github.com/chepherd/chepherd/internal/auth"
	"github.com/chepherd/chepherd/internal/federation"
	"github.com/chepherd/chepherd/internal/mcpserver"
	"github.com/chepherd/chepherd/internal/persistence/sqlite"
	"github.com/chepherd/chepherd/internal/keychain"
	"github.com/chepherd/chepherd/internal/profile"
	"github.com/chepherd/chepherd/internal/prompts"
	"github.com/chepherd/chepherd/internal/ptyhost/session"
	"github.com/chepherd/chepherd/internal/runtime"
	"github.com/chepherd/chepherd/internal/runtimehttp"
	"github.com/chepherd/chepherd/internal/runtimetui"
	"github.com/chepherd/chepherd/internal/scrummaster"
	"github.com/chepherd/chepherd/internal/vault"
)

var (
	runFlagAgent                 string
	runFlagCwd                   string
	runFlagNoShepherd            bool
	runFlagStateDir              string
	runFlagHeadless              bool
	runFlagListen                string
	runFlagWebDir                string
	runFlagMCPListen             string
	runFlagFederationRegistryURL    string // #225 row C1 — hosted peer registry
	runFlagFederationPublicURL      string // #225 row C1 — this chepherd's public URL for announcements
	runFlagFederationOutboundBearer string // #225 §DoD walk — shared bearer for FederatedDeliverer outbound POST
	runFlagIOgridEndpoint        string
	runFlagKeychainBackend       string // #322 H6.1 — keychain backend (default = auto)
	runFlagOpenBaoAddr           string // #322 H6.1 — OpenBao server URL
	runFlagOpenBaoTokenFile      string // #322 H6.1 — OpenBao auth token file // #318 (#225 row E1) — iogrid recipe-dispatch endpoint URL
	runFlagScrumMasterName       string // #225 row F4 — name for the auto-spawned Scrum Master (back-compat default: "shepherd")
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

chepherd run is the single canonical entrypoint for v0.9.2 — it integrates
runtime, shepherd tier, persistence layer, A2A endpoint, and MCP HTTP into
one process per docs/V0.9.2-ARCHITECTURE.md.`,
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
	// #225 row C1 — federation peer registry. Empty string disables;
	// when set, this chepherd announces itself + polls for peers + caches
	// each peer's agent-card via AgentCardRepository. PublicURL is what
	// peers will use to reach us (defaults to listen addr; override for
	// reverse-proxy + DNS-name setups).
	runCmd.Flags().StringVar(&runFlagFederationRegistryURL, "federation-registry-url", "",
		"hosted peer registry URL (empty = disabled). Peer discovery POSTs /announce + GETs /peers here.")
	runCmd.Flags().StringVar(&runFlagKeychainBackend, "keychain-backend", "",
		"explicit keychain backend (empty = auto-select per platform: macos | secret-tool | file). Set to 'openbao' to use OpenBao HA backend.")
	runCmd.Flags().StringVar(&runFlagOpenBaoAddr, "openbao-addr", "",
		"OpenBao server URL (required when --keychain-backend=openbao).")
	runCmd.Flags().StringVar(&runFlagOpenBaoTokenFile, "openbao-token-file", "",
		"File path containing the OpenBao auth token (required when --keychain-backend=openbao).")
	runCmd.Flags().StringVar(&runFlagIOgridEndpoint, "iogrid-endpoint", "",
		"iogrid recipe-dispatch endpoint URL — empty disables the iogrid extension on agent-card.json. When set, peers can discover this chepherd's iogrid surface via /.well-known/agent-card.json's x-iogrid block.")
	runCmd.Flags().StringVar(&runFlagFederationPublicURL, "federation-public-url", "",
		"this chepherd's public URL announced to peers (default: derived from --listen).")
	runCmd.Flags().StringVar(&runFlagFederationOutboundBearer, "federation-outbound-bearer", "",
		"shared bearer token sent on every cross-instance SendMessage POST (use B3 trust-list + ES256 JWT in production; this flag is the §DoD walk-friendly bootstrap path).")
	runCmd.Flags().StringVar(&runFlagScrumMasterName, "scrummaster-name", "shepherd",
		"name for the auto-spawned Scrum Master session (back-compat default: 'shepherd'; set to 'scrummaster' for canonical naming).")
	rootCmd.AddCommand(runCmd)
}

func runRunCmd(cmd *cobra.Command, args []string) error {
	// #322 H6.1 — explicit keychain backend selection. When the
	// operator passes --keychain-backend=openbao, install the OpenBao
	// backend ahead of any subsystem that reads secrets via
	// keychain.{Set,Get,Delete}. Empty flag → default platform chain.
	if runFlagKeychainBackend == "openbao" {
		bao, err := keychain.NewOpenBaoBackendFromFlags(
			runFlagOpenBaoAddr, runFlagOpenBaoTokenFile, "secret")
		if err != nil {
			return fmt.Errorf("--keychain-backend=openbao: %w", err)
		}
		keychain.Install(bao)
	}

	stateDir := runFlagStateDir
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, ".local", "state", "chepherd-v05")
	}
	cwd := runFlagCwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	prof := profile.Resolve()

	fmt.Printf("chepherd run — v0.8 runtime\n")
	fmt.Printf("  state-dir: %s\n", stateDir)
	fmt.Printf("  agent:     %s\n", runFlagAgent)
	fmt.Printf("  cwd:       %s\n", cwd)
	fmt.Printf("  shepherd:  %v\n", !runFlagNoShepherd)
	fmt.Printf("  profile:   %s (spawner=%s auth=%s storage=%s tls=%s)\n\n",
		profileNameOrDefault(prof.Name), prof.Spawner, prof.AuthMode, prof.StorageType, prof.TLSMode)

	// v0.9.2 (#208): open SQLite persistence.Store + thread into Runtime so
	// the agent registry (and incrementally other state) is read/written
	// through the Repository contract from PR #209 rather than file-on-disk.
	// Store stays open for the lifetime of the chepherd process.
	persistDB := filepath.Join(stateDir, "chepherd.db")
	store, err := sqlite.NewStore(context.Background(), persistDB)
	if err != nil {
		return fmt.Errorf("runtime: open persistence store %q: %w", persistDB, err)
	}
	defer func() { _ = store.Close() }()
	rt, err := runtime.NewWithStore(stateDir, store)
	if err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	// #270 — surface the instance UUID in the boot banner so operators
	// can confirm two chepherd binaries have distinct fingerprints +
	// won't cross-kill each other's agents at startup.
	fmt.Printf("  instance:  %s (#270 — derived from state-dir abs path)\n\n", rt.InstanceUUID())
	// #273 — verify the chepherd-agent:latest image's entrypoint script
	// matches what this binary expects. Loud warning + rebuild
	// instructions on mismatch. Best-effort — boot does NOT block on
	// the check so dev builds without -X ldflags + bastions without
	// podman still proceed normally.
	runtime.VerifyAgentEntrypointSHA(rt.ContainerRuntime())
	// #258 — reap orphan sibling agent containers BEFORE the HTTP
	// surface comes up. #270 — the listing is now instance-scoped so
	// a parallel chepherd binary on the same host has its own pool +
	// can't be cross-killed by this reap pass.
	_ = rt.ReapOrphanContainers()
	// #393 P2 — log orphan session-row count on boot. An "orphan" is
	// a persisted SessionStore row whose name has no live runtime
	// entry (typically: agent died between chepherd restarts, or the
	// row was abandoned). Loud number in the boot banner so operators
	// notice without having to scroll the dashboard. Set
	// CHEPHERD_CLEANUP_ORPHANS_ON_START=true to also delete them
	// automatically — useful for CI/test harnesses + operators who
	// want a clean slate on every bounce. Defaults to log-only so
	// existing operators aren't surprised by data loss on first
	// upgrade.
	if store != nil {
		ctx := context.Background()
		if ids, err := store.Sessions().List(ctx); err == nil {
			var orphans []string
			for _, name := range ids {
				if sess, _ := rt.Get(name); sess == nil {
					orphans = append(orphans, name)
				}
			}
			if len(orphans) > 0 {
				fmt.Printf("[chepherd-boot] %d orphan session-row(s) in store (no live container)\n", len(orphans))
				if os.Getenv("CHEPHERD_CLEANUP_ORPHANS_ON_START") == "true" {
					var deleted int
					for _, name := range orphans {
						if err := store.Sessions().Delete(ctx, name); err == nil {
							deleted++
						}
					}
					fmt.Printf("[chepherd-boot] auto-cleaned %d orphan row(s) (CHEPHERD_CLEANUP_ORPHANS_ON_START=true)\n", deleted)
				} else {
					fmt.Printf("[chepherd-boot]   → click 'Clean up orphans' in dashboard sessions pane, OR set CHEPHERD_CLEANUP_ORPHANS_ON_START=true to auto-clean at boot\n")
				}
			}
		}
	}
	// v0.9.2 (#208): internal/messagebus/relay.go (337 LOC + 4 Runtime
	// SessionRegistry methods) deleted in this sub-branch. A2A
	// SendMessage supersedes the regex @-line PTY relay entirely;
	// cross-agent conversation now goes through the A2A JSON-RPC
	// endpoint or the chepherd.send_to_session shim (which itself
	// translates onto A2A SendMessage via the Deliverer wired below).

	// MCP server on HTTP/WebSocket — `chepherd mcp` subprocess (used by
	// agents) dials this endpoint and proxies JSON-RPC over the WS. One
	// server per runtime. Works on local Podman, multi-cluster K8s, and
	// the OpenOvan OpenOva instance without any code change. Closes #124.
	mcpListen := runFlagMCPListen
	if mcpListen == "" {
		mcpListen = os.Getenv("CHEPHERD_MCP_LISTEN")
	}
	if mcpListen == "" {
		mcpListen = mcpserver.DefaultListenAddr
	}
	// v0.9.2 (#208): build the A2A PTY Deliverer once + thread to the
	// legacy MCP server (chepherd.send_to_session shim translates onto
	// A2A SendMessage). The same Deliverer instance is also consumed
	// by the runner-side A2A HTTPS endpoint via a2a.Router.WireDeliverer
	// when that endpoint is stood up.
	a2aDeliverer := runtime.NewA2ADeliverer(rt)
	mcpSrv := mcpserver.NewWithDeliverer(rt, a2aDeliverer)
	if err := mcpSrv.StartHTTP(mcpListen); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}

	// v0.9.2 (#208): wire the shepherd tier. Constructs ScrumMaster from
	// the same persistence.Store the runtime uses; attaches via
	// Runtime.WithShepherd so RecordEvent broadcasts reach Observe;
	// kicks off the periodic tick loop in a goroutine bound to the
	// process-lifetime context so ctrl-C cleanly shuts it down.
	shepCfg := scrummaster.Config{JudgeCfg: scrummaster.DefaultJudgeConfig()}
	shep := scrummaster.NewWithStore(store, shepCfg)
	rt.WithShepherd(shep)
	shepCtx, shepCancel := context.WithCancel(context.Background())
	defer shepCancel()
	go func() {
		if err := shep.Run(shepCtx); err != nil && err != context.Canceled {
			fmt.Fprintf(os.Stderr, "shepherd Run: %v\n", err)
		}
	}()
	fmt.Printf("✓ MCP server (HTTP/WS) listening on http://%s/mcp/ws\n", mcpListen)

	// HTTP/WS server — for web (chepherd-rc-web), mobile (rc-ios/android),
	// and remote-TUI clients. Disabled when --listen "".
	var httpSrv *http.Server
	if runFlagListen != "" {
		rs := runtimehttp.New(rt)
		rs.WebDir = runFlagWebDir
		rs.Profile = &prof
		rs.AgentCardStore = store.AgentCards()
		rs.TaskStore = store.Tasks()
		rs.SessionStore = store.Sessions()

		// #225 row C1 — federation peer registry. Boot Federation when
		// `--federation-registry-url` is set; cmd/run.go derives the
		// announce-URL from --listen if --federation-public-url wasn't
		// passed. The Federation runs in a goroutine for the chepherd
		// process lifetime; ctx cancellation on SIGTERM stops it
		// cleanly + flushes any in-flight fetches.
		if runFlagFederationRegistryURL != "" {
			selfURL := runFlagFederationPublicURL
			if selfURL == "" {
				selfURL = "http://" + runFlagListen
			}
			fed := federation.New(store.AgentCards())
			fed.Register(&federation.HostedRegistryDiscoverer{
				RegistryURL: runFlagFederationRegistryURL,
				SelfSID:     rt.InstanceUUID(),
				SelfURL:     selfURL,
			})
			fedCtx, fedCancel := context.WithCancel(context.Background())
			defer fedCancel()
			go fed.Run(fedCtx)
			fmt.Printf("✓ Federation peer discovery via %s (announce as %s)\n",
				runFlagFederationRegistryURL, selfURL)
			rs.Federation = fed
		}

		// v0.9.2 (#208 follow-up): expose A2A on the same HTTP server the
		// dashboard uses. The Deliverer constructed above is reused — the
		// MCP-shim path (chepherd.send_to_session) and the A2A JSON-RPC
		// endpoint both translate onto the SAME PTY-writing Deliverer.
		// AgentCard URL points at the canonical /jsonrpc surface so A2A
		// clients can discover-then-call without out-of-band knowledge.
		a2aRouter := a2a.NewRouter()
		// v0.9.3 #225 row C2 — wrap the local PTY Deliverer with the
		// FederatedDeliverer so SendMessage with `@<peer-sid>/<rest>`
		// ContextID forwards to the peer's /jsonrpc. Local fallback
		// when no `@` prefix (or @<self-sid>/) preserves v0.9.2
		// semantics. AgentCard cache (#225 row C1) provides the peer-
		// URL resolution.
		var routedDeliverer a2a.Deliverer = a2aDeliverer
		if store.AgentCards() != nil {
			routedDeliverer = &federation.FederatedDeliverer{
				Local:          a2aDeliverer,
				Cards:          store.AgentCards(),
				SelfSID:        rt.InstanceUUID(),
				OutboundBearer: runFlagFederationOutboundBearer,
				// In production: B3 TrustListValidator on peer side
				// accepts ES256-signed JWTs minted by this instance's
				// #225 B2 keypair. The OutboundBearer flag supports a
				// shared-secret bootstrap mode for the §DoD walk +
				// pre-trust-list deploys.
			}
		}
		if err := a2aRouter.WireDeliverer(routedDeliverer); err != nil {
			return fmt.Errorf("a2a: wire deliverer: %w", err)
		}
		rs.A2ACard = newAgentCard(runFlagListen)
		// v0.9.3 #277 — wire the remaining 10 A2A method bodies. The
		// MethodBodies struct registers concrete handlers that read
		// and write the TaskRepository + PushNotificationConfigRepository
		// via the persistence.Store. RunnerSID is the chepherd-instance
		// UUID so cross-runner ListTasks queries filter correctly when
		// the same SQLite DB is shared across multi-host setups.
		// SubscribeFn is nil for now — SSE streaming binding lands in a
		// follow-up; SendStreamingMessage + ResubscribeTask return -32004
		// until that wiring is complete.
		// v0.9.3 #225 row A2 — SSE broker for streaming methods. When
		// wired, SendStreamingMessage + ResubscribeTask return a
		// streamID + the SSE GET /a2a/stream/<streamID> path delivers
		// task state transitions. nil disables streaming (returns
		// -32004).
		streamBroker := a2a.NewStreamBroker()
		rs.StreamBroker = streamBroker
		// #225 row A3 — wire the broker into the A2ADeliverer so
		// PTY output for each delivered task flows through SSE
		// subscribers. When SendStreamingMessage caller subscribes
		// to the returned streamID, they see incremental artifact
		// events as the agent's PTY produces output.
		a2aDeliverer.SetBroker(streamBroker)
		// #225 row A4 — wire TaskRepository so each Deliver call
		// persists the issued Task. GetTask/ListTasks then return
		// real history; before A4 the Tasks table stayed empty
		// because nobody called Save in the delivery path.
		a2aDeliverer.SetTaskStore(store.Tasks(), rt.InstanceUUID())
		methodBodies := &a2a.MethodBodies{
			Store:       store,
			AgentCardFn: func() a2a.AgentCard { return *newAgentCard(runFlagListen) },
			RunnerSID:   rt.InstanceUUID(),
			SubscribeFn: streamBroker.SubscribeFn(),
		}
		if err := methodBodies.Register(a2aRouter); err != nil {
			return fmt.Errorf("a2a: register method bodies: %w", err)
		}
		rs.A2ARouter = a2aRouter
		// v0.9.3 #225 row B2 — ES256 keypair lifecycle. Load (or mint
		// on first boot) the instance's signing key from
		// AuthSecretRepository + publish the public half at
		// /.well-known/jwks.json so peers can verify inbound JWTs
		// without out-of-band key sharing. Failure is non-fatal — when
		// persistence is unreachable, JWKS endpoint stays unmounted
		// and B3 per-peer JWT signing simply falls back to the
		// unsigned bearer (the same path as today).
		if priv, err := auth.LoadOrCreateES256(context.Background(), store.AuthSecrets()); err != nil {
			fmt.Fprintf(os.Stderr, "warn: es256: %v (JWKS endpoint disabled)\n", err)
		} else if jwks, err := auth.PublicJWK(priv); err != nil {
			fmt.Fprintf(os.Stderr, "warn: es256 jwks marshal: %v\n", err)
		} else {
			rs.JWKSBody = jwks
			rs.ES256Priv = priv
			fmt.Printf("✓ ES256 signing key loaded; JWKS public at /.well-known/jwks.json (#225 B2)\n")
		}
		// Vault — open (or create) in the state directory
		if vlt, err := vault.Open(filepath.Join(stateDir, "vault.json")); err != nil {
			fmt.Fprintf(os.Stderr, "warn: vault: %v (credential vault disabled)\n", err)
		} else {
			rs.Vault = vlt
			// Wire vault into the runtime so /run/secrets/claude-credentials
			// is sourced from the vault on every spawn (TV1 / R4).
			rt.SetVault(newRuntimeVaultAdapter(vlt))
		}
		// Auth provider — sourced from resolved profile (#129). The
		// per-knob env vars are already applied by profile.Resolve, so
		// pass the materialized values here instead of letting auth.New
		// re-read the environment.
		if ap, err := auth.New(prof.AuthMode, stateDir, prof.OIDCIssuer); err != nil {
			fmt.Fprintf(os.Stderr, "warn: auth: %v (server is unauthenticated)\n", err)
		} else {
			rs.Auth = ap
			fmt.Printf("✓ Auth provider: %s\n", ap.Mode())
			if lp, ok := ap.(*auth.LocalProvider); ok {
				// Bootstrap token: issue once, persist, re-use on every
				// boot so agents spawned across restarts keep working.
				tokenPath := filepath.Join(stateDir, "auth.printed")
				var tok string
				if existing, err := os.ReadFile(tokenPath); err == nil && len(existing) > 0 {
					tok = strings.TrimSpace(string(existing))
				} else {
					if t, err := lp.IssueBootstrapToken(nil, "operator", 0); err == nil {
						tok = t
						_ = os.WriteFile(tokenPath, []byte(t), 0o600)
						fmt.Printf("\n  Bootstrap token (operator, 30d):\n  %s\n\n", tok)
					}
				}
				if tok != "" {
					// Wire token into MCP server (#139) + runtime spawn env
					// (#139). Agents inherit CHEPHERD_TOKEN and present it
					// on every WS upgrade. Dashboard requires same Bearer.
					mcpSrv.SetAuthToken(tok)
					rt.SetAgentEnv("CHEPHERD_TOKEN", tok)
					rs.AuthToken = tok
				}
			}
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
		// #350 D4 auto-resume: query persisted sessions w/ claude_session_uuid
	// + Spawn each with --resume <uuid>. Operator's pre-restart state
	// continues seamlessly post-restart. No-op when no persistence wired.
	if resumable, err := rt.ResumableSessions(context.Background()); err == nil {
		for _, spec := range resumable {
			if _, _, err := rt.Spawn(spec); err != nil {
				fmt.Fprintf(os.Stderr, "warn: D4 auto-resume %q failed (continuing): %v\n", spec.Name, err)
			} else {
				fmt.Printf("✓ Auto-resumed session %q (claude UUID prefix %s…)\n",
					spec.Name, firstN(spec.AgentArgs[1], 8))
			}
		}
	}

	if !runFlagNoShepherd {
		_, shepSess, err := rt.Spawn(runtime.SpawnSpec{
			Name:         runFlagScrumMasterName,
			AgentSlug:    runFlagAgent,
			Team:         "default",
			Role:         runtime.RoleShepherd,
			Cwd:          cwd,
			SystemPrompt: prompts.ScrumMaster,
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

func profileNameOrDefault(n string) string {
	if n == "" {
		return "(auto)"
	}
	return n
}

// newAgentCard builds the v0.9.2 A2A Agent Card served at
// /.well-known/agent-card.json. listenAddr is the chepherd run
// HTTP/WS listen address (e.g. "127.0.0.1:8080"); it determines
// the canonical URL advertised on the card so A2A clients hit the
// correct /jsonrpc endpoint.
//
// All three capabilities are advertised; SendMessage is wired to
// the PTY Deliverer (the other 10 methods still return scaffold
// errors until S5-S7 sub-branches). All 5 securitySchemes from
// V0.9.2-ARCHITECTURE.md §6 are listed; runners pick which to
// require via per-deployment policy.
//
// Refs #208.
func newAgentCard(listenAddr string) *a2a.AgentCard {
	return &a2a.AgentCard{
		ProtocolVersion: "1.0",
		Name:            "chepherd",
		Description:     "chepherd v0.9.3 control-plane Agent — PTY-host runtime + Scrum Master intelligence + A2A endpoint",
		URL:             "http://" + listenAddr + "/jsonrpc",
		Version:         "0.9.3",
		Capabilities: a2a.AgentCapabilities{
			Streaming:         true,
			PushNotifications: true,
			ExtendedCard:      true,
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []a2a.AgentSkill{
			{
				ID:          "send-message",
				Name:        "Send PTY message",
				Description: "Deliver a text message into a chepherd PTY session keyed by contextId (= chepherd session ID).",
			},
		},
		Security: []map[string][]string{
			{"mtls": {}},
			{"httpAuth": {}},
			{"apiKey": {}},
			{"oauth2": {}},
			{"oidc": {}},
		},
		SecuritySchemes: map[string]a2a.SecurityScheme{
			"mtls":     {Type: "mutualTLS"},
			"httpAuth": {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
			"apiKey":   {Type: "apiKey", In: "header", Name: "X-API-Key"},
			"oauth2":   {Type: "oauth2"},
			"oidc":     {Type: "openIdConnect"},
		},
		XChepherdP2P: a2a.DefaultExtension(),
		XIOgrid:      iogridExtension(),
	}
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

func (a *runtimeVaultAdapter) UpdateValue(id, plaintext string) error {
	return a.v.UpdateValue(id, plaintext)
}

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
		if name == runFlagScrumMasterName {
			return
		}
		// Give the new agent ~3s to print its initial pane content so
		// the Scrum Master's read_pane has something to actually observe.
		go func(n string) {
			time.Sleep(3 * time.Second)
			live, _ := rt.Get(runFlagScrumMasterName)
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
		live, _ := rt.Get(runFlagScrumMasterName)
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
			_ = rt.Stop(runFlagScrumMasterName)
			time.Sleep(2 * time.Second)
			// Respawn (skip cycle; new bootstrapShepherd starts its own loop)
			_, newSess, err := rt.Spawn(runtime.SpawnSpec{
				Name: runFlagScrumMasterName, AgentSlug: "claude-code", Team: "default",
				Role: runtime.RoleShepherd, Cwd: "/home/openova",
				SystemPrompt: prompts.ScrumMaster,
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


// iogridExtension returns the AgentCard's x-iogrid extension shape.
// Returns nil when --iogrid-endpoint is unset (extension omitted from
// the marshalled agent-card.json). When set, defaults are populated
// via a2a.DefaultIOgridExtension() and the Endpoint URL is taken from
// the operator flag.
//
// Refs #318 (#225 row E1).
func iogridExtension() *a2a.IOgridExtension {
	if runFlagIOgridEndpoint == "" {
		return nil
	}
	ext := a2a.DefaultIOgridExtension()
	ext.Endpoint = runFlagIOgridEndpoint
	return ext
}

// firstN returns the first n runes of s, or s when shorter.
func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

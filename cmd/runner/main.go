// Package main is the chepherd-runner binary entrypoint per
// docs/V0.9.2-ARCHITECTURE.md §5 #3 + §22 + issue #453 (Wave R).
//
// chepherd-runner is the data-plane process that runs as PID 1 inside
// each agent container. It owns:
//
//   - per-session A2A endpoint at /a2a/{sid}/jsonrpc (+ Agent Card at
//     /a2a/{sid}/.well-known/agent-card.json)
//   - MCP HTTP server bound to a local Unix socket inside the
//     container (default /run/chepherd/mcp.sock), replacing the
//     stdio→WS bridge previously used to reach chepherd-central
//   - PTY master ownership for the agent (chepherd-runner is the
//     agent process's parent; claude-code etc. is exec'd as its
//     child with PTY allocation)
//   - outbound WS to chepherd-daemon for registration, command
//     intake, audit egress
//
// chepherd-daemon (control plane, separate process: cmd/run.go) owns
// JWT mint + Agent Card directory + operator API + dashboard + audit
// aggregator. The daemon spawns containers via podman; the spawn
// command is `chepherd-runner` with the flags below; daemon hands the
// runner the daemon URL + agent metadata via flags + env, and the
// runner dials back over WS for the lifecycle.
//
// This file is touch-point 1 of Wave R: scaffold + Unix-socket MCP
// listener + flag parsing. Touch points 2-4 wire per-session A2A
// hosting, strip the daemon's A2A/Deliverer/MCP integration, and
// land the cutover with e2e tests against real runners. Each
// touch-point check-in is filed as a gh issue comment on #453.
//
// Refs #453 #208 docs/V0.9.2-ARCHITECTURE.md §5 #3 §22.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/chepherd/chepherd/internal/auth"
	"github.com/chepherd/chepherd/internal/mcpserver"
	"github.com/chepherd/chepherd/internal/runtime"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "chepherd-runner: %v\n", err)
		os.Exit(1)
	}
}

// runnerConfig collects all the flags / env the runner accepts. Each
// field documents whether it's required + how it's used. Defaults
// match the in-container conventions production deployment will use.
type runnerConfig struct {
	// SID is the chepherd session ID this runner manages. Assigned
	// by chepherd-daemon at spawn time + passed via --sid (or
	// CHEPHERD_SID env). REQUIRED — the runner serves
	// /a2a/{sid}/jsonrpc against this ID.
	sid string

	// DaemonURL is the chepherd-daemon's base URL for the outbound
	// registration WS + audit egress. Empty disables daemon
	// registration (dev / unit-test mode).
	daemonURL string

	// AgentSlug is the agent flavor this runner will launch as its
	// child (e.g. "claude-code", "sovereign-shell"). Resolved via
	// internal/ptyhost/agentcatalog. REQUIRED for the spawn step
	// (touch point 4); optional for touch point 1 scaffold mode.
	agentSlug string

	// MCPSocket is the filesystem path for the MCP-over-Unix-socket
	// listener. Defaults to /run/chepherd/mcp.sock. Agent's MCP
	// config (the claude-code container entrypoint writes one)
	// points at this path.
	mcpSocket string

	// MCPTCPListen is the localhost-only TCP bind address for the
	// agent-facing MCP HTTP transport (#478 Wave M2). Empty disables
	// the TCP listener (Unix-socket only — back-compat with R1+M1
	// scaffold). Set to "127.0.0.1:0" to bind a random free port +
	// have the runner discover it for .mcp.json templating.
	//
	// Why both Unix + TCP: claude-code's HTTP MCP transport requires
	// a TCP URL (verified empirically; http+unix URLs fail in
	// `claude mcp list`). The Unix socket stays as the canonical
	// non-agent transport (audit / monitoring / legacy consumers).
	mcpTCPListen string

	// Name is the operator-visible @-handle (e.g. "iogrid-1"). Empty
	// at register-time is allowed — daemon may echo back from spawn
	// intent in a later Wave. Set via --name or CHEPHERD_RUNNER_NAME.
	name string

	// A2ABaseURL is the scheme://host:port (NOT host:port) the runner
	// will host its per-session A2A endpoint on. Used by daemon to
	// template the §12.1 well-known Agent Card URI
	// `<a2a_base_url>/a2a/<sid>/.well-known/agent-card.json`. Empty
	// for R1 (Wave R2 lights the A2A endpoint + populates this).
	a2aBaseURL string

	// A2AListen is the runner's per-session A2A endpoint TCP bind
	// address (host:port). Empty disables the endpoint entirely
	// (back-compat with R1 scaffold mode + the daemon-register-only
	// e2e test). When set + --sid is non-empty, the runner mounts
	// internal/a2a.Router at /a2a/<sid>/jsonrpc serving all 11 A2A
	// methods. Set via --a2a-listen or CHEPHERD_A2A_LISTEN. #463.
	a2aListen string

	// requireJWT enables Wave T1 #486 JWT verification on the runner's
	// /a2a/<sid>/jsonrpc endpoint. Set via --require-jwt or
	// CHEPHERD_REQUIRE_JWT=1. Production deploys MUST set it; dev/
	// scaffold/e2e tests typically leave it off so they can hit the
	// endpoint unauthenticated.
	requireJWT bool

	// StateDir is the per-runner state directory inside the
	// container. Defaults to /var/lib/chepherd/runner.
	stateDir string

	// AuthToken is the bearer token the daemon issues to this
	// runner at spawn time. Used both for outbound WS to daemon
	// and for the local MCP socket's auth check.
	authToken string

	// AgentArgs are the argv tail handed to the agent process when
	// the runner exec's it. Empty defaults to the agentcatalog
	// flavor's DefaultArgs.
	agentArgs []string

	// Headless (#499 Wave H1) is the per-task ephemeral lifecycle
	// mode: skip daemon-WS registration + MCP socket; read ONE task
	// from --task-json / --task-file / stdin; run via claude --print;
	// write A2A Task envelope to --result-file or stdout; exit.
	headless headlessConfig
}

func run() error {
	cfg, err := parseFlags()
	if err != nil {
		return err
	}

	// #499 Wave H1 — headless / per-task ephemeral lifecycle.
	// Branch BEFORE the daemon-WS register + MCP socket setup
	// because headless mode needs neither + the iogrid HTTP API
	// (Wave H2) spawns one --headless invocation per request.
	if cfg.headless.enabled {
		hc := cfg.headless
		hc.agentSlug = cfg.agentSlug
		exitCode, herr := runHeadless(context.Background(), &hc)
		if herr != nil {
			fmt.Fprintln(os.Stderr, herr.Error())
		}
		os.Exit(exitCode)
	}

	// Touch point 1 scaffold: enough plumbing to start the MCP
	// socket server + log the config. Spawn, A2A endpoint, daemon-
	// outbound, PTY ownership wire in touch points 2-4.
	log.Printf("[chepherd-runner] starting sid=%q name=%q agent=%q mcp-socket=%q a2a-base-url=%q daemon=%q",
		cfg.sid, cfg.name, cfg.agentSlug, cfg.mcpSocket, cfg.a2aBaseURL, cfg.daemonURL)

	// State dir + MCP socket dir prep. mkdir -p with restrictive
	// modes; the socket itself ends up 0600 after listen.
	if err := os.MkdirAll(cfg.stateDir, 0o700); err != nil {
		return fmt.Errorf("mkdir state-dir %q: %w", cfg.stateDir, err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.mcpSocket), 0o700); err != nil {
		return fmt.Errorf("mkdir mcp-socket parent %q: %w", filepath.Dir(cfg.mcpSocket), err)
	}

	// #504 CI follow-up — DON'T init internal/runtime.Runtime here.
	// runtime.New triggers podman/docker capability probes
	// (chepherd-runtime-detect → imageExists chepherd-agent:latest →
	// `podman image exists` subprocess) which take 5-10s on cold
	// CI runners that don't have the agent image. That blew past
	// the e2e test's 5s polling deadline → daemon never saw the
	// runner row → red CI.
	//
	// For R1 the runner doesn't NEED a Runtime — it doesn't spawn
	// containers, doesn't manage multiple sessions, doesn't host
	// the chepherd MCP tool catalog. Wave R4 (PTY ownership
	// cutover) will introduce a stripped-down per-session manager
	// inside the runner; the daemon's full Runtime stays where it
	// is.
	//
	// MCP server is constructed with rt=nil. The healthz/info paths
	// don't touch rt; the JSON-RPC tool catalog DOES — until the
	// runner's R2+ scope adds a runner-specific tool surface, any
	// tools/call hitting the runner's MCP socket NPEs. That's fine
	// for R1: agents inside the container talk to the daemon via
	// the existing /mcp/rpc surface, NOT the local socket yet. The
	// local socket exists for R2 to wire onto.
	mcp := mcpserver.New(nil)
	if cfg.authToken != "" {
		mcp.SetAuthToken(cfg.authToken)
	}
	if err := mcp.StartHTTP("unix://" + cfg.mcpSocket); err != nil {
		return fmt.Errorf("mcp StartHTTP: %w", err)
	}
	log.Printf("[chepherd-runner] MCP listening on unix://%s", cfg.mcpSocket)
	// #478 Wave M2 — add the localhost TCP listener for the agent
	// container's claude-code MCP HTTP transport. claude-code can't
	// dial http+unix:// URLs (verified empirically); a localhost
	// TCP bind inside the container's network namespace gives the
	// agent a dialable URL with the same security profile as a
	// container-internal Unix socket. Empty mcpTCPListen disables
	// the second listener (Unix socket only — back-compat with R1).
	if cfg.mcpTCPListen != "" {
		if err := mcp.AddHTTPListener(cfg.mcpTCPListen); err != nil {
			return fmt.Errorf("mcp AddHTTPListener %q: %w", cfg.mcpTCPListen, err)
		}
		if addrs := mcp.ExtraListenerAddrs(); len(addrs) > 0 {
			log.Printf("[chepherd-runner] MCP also listening on http://%s/mcp for agent-facing transport", addrs[len(addrs)-1])
		}
	}

	// #504 — outbound WS registration with chepherd-daemon. Empty
	// daemon-url skips registration entirely (dev mode + the
	// scaffold unit test stay green without a daemon).
	var dc *daemonClient
	if cfg.daemonURL != "" {
		req := runnerRegisterReq{
			SID:           cfg.sid,
			Name:          cfg.name,
			AgentSlug:     cfg.agentSlug,
			RunnerVersion: runnerSelfVersion,
			A2ABaseURL:    cfg.a2aBaseURL,
			MCPSocket:     cfg.mcpSocket,
			Capabilities:  []string{"pty", "audit-stream"},
		}
		client, resp, err := registerWithDaemon(cfg.daemonURL, cfg.authToken, req)
		if err != nil {
			return fmt.Errorf("daemon register: %w", err)
		}
		dc = client
		log.Printf("[chepherd-runner] registered with daemon: assigned-sid=%s audit-topic=%s daemon-version=%s",
			resp.SID, resp.AuditTopic, resp.DaemonVersion)
		// If daemon assigned a sid AND caller didn't pre-set one, adopt
		// the daemon's. Otherwise the operator's --sid wins (test mode).
		if cfg.sid == "" {
			cfg.sid = resp.SID
		}
		// #504 — "registered" audit event is emitted on the DAEMON
		// side synchronously inside handleRunnerRegister (see
		// internal/runtimehttp/runners_register.go). Previously this
		// line client-side-emitted via fire-and-forget SendAudit,
		// which raced SIGTERM in CI. Per V0.9.2-ARCH §5 #8 the daemon
		// owns the audit log; "registered" is a daemon observation
		// not a runner-uploaded event. Wave AU (later) wires runner-
		// uploaded audits for SendMessage call events.
	}

	// #465 Wave R4 — spawn the agent PTY-session FIRST so it can be
	// threaded into the A2A endpoint's runnerDeliverer for
	// SendMessage-drives-PTY behavior. nil session is OK (no --agent
	// flag, or agentcatalog miss) — the deliverer falls back to R2's
	// persist-only path.
	ptySession, err := spawnAgentSession(cfg)
	if err != nil {
		return fmt.Errorf("agent PTY-session spawn: %w", err)
	}
	if ptySession != nil {
		// R1 contract — fan PTY bytes to daemon as audit events when
		// --daemon-url is wired. Coexists with R4's broker fan-out
		// via session's multi-subscriber model.
		pumpSessionToAudit(ptySession, dc)
	}

	// #463 Wave R2 — per-session A2A endpoint. Off by default for
	// back-compat with R1 scaffold mode; activates when --a2a-listen
	// is non-empty AND --sid is non-empty (the URL path /a2a/<sid>
	// depends on the sid).
	var a2aSrv *a2aEndpoint
	if cfg.a2aListen != "" && cfg.sid != "" {
		// #486 T1 + #465 R4 + #488 AU1 — JWT + PTY + audit-emitter
		// all threaded into startA2AEndpoint.
		var jwtCfg *auth.RunnerJWTMiddlewareConfig
		if cfg.requireJWT {
			jwtCfg = &auth.RunnerJWTMiddlewareConfig{
				RunnerSID:  cfg.sid,
				JWKSClient: auth.NewJWKSClient(nil, 0),
			}
		}
		var emitter runtime.AuditEmitter
		if dc != nil {
			emitter = dc
		}
		srv, err := startA2AEndpoint(cfg.a2aListen, cfg.sid, cfg.name, cfg.a2aBaseURL, cfg.daemonURL, cfg.stateDir, ptySession, jwtCfg, emitter)
		if err != nil {
			return fmt.Errorf("a2a endpoint: %w", err)
		}
		a2aSrv = srv
	}

	// Wait for SIGINT / SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("[chepherd-runner] received %s; shutting down", sig)
	if dc != nil {
		_ = dc.SendAudit("event", "[chepherd-runner] shutdown signal="+sig.String())
		dc.Close()
	}
	mcp.Stop()
	a2aSrv.Close()
	if ptySession != nil {
		_ = ptySession.Close()
	}
	return nil
}

// runnerSelfVersion is the chepherd-runner build identifier reported
// in the daemon register frame. Mirrors daemonRunnerVersion in the
// daemon package; the two are not coupled — Wave R5+ may decouple via
// ldflags.
const runnerSelfVersion = "0.9.4-R1"

// parseFlags reads flags + env into a runnerConfig.
func parseFlags() (*runnerConfig, error) {
	fs := flag.NewFlagSet("chepherd-runner", flag.ContinueOnError)
	cfg := &runnerConfig{}
	fs.StringVar(&cfg.sid, "sid", envOr("CHEPHERD_SID", ""),
		"chepherd session ID this runner manages (assigned by daemon)")
	fs.StringVar(&cfg.daemonURL, "daemon-url", envOr("CHEPHERD_DAEMON_URL", ""),
		"chepherd-daemon base URL for outbound registration + audit (empty = dev / unit-test mode)")
	fs.StringVar(&cfg.agentSlug, "agent", envOr("CHEPHERD_AGENT_SLUG", ""),
		"agent flavor to launch (claude-code, sovereign-shell, ...)")
	fs.StringVar(&cfg.mcpSocket, "mcp-socket", envOr("CHEPHERD_MCP_SOCKET", "/run/chepherd/mcp.sock"),
		"MCP HTTP-over-Unix-socket path inside the container")
	fs.StringVar(&cfg.mcpTCPListen, "mcp-tcp-listen", envOr("CHEPHERD_MCP_TCP_LISTEN", "127.0.0.1:0"),
		"localhost-only TCP bind for the agent-facing MCP HTTP transport (#478 Wave M2). Empty = Unix socket only. 127.0.0.1:0 picks a free port + the runner discovers it for .mcp.json templating.")
	fs.StringVar(&cfg.name, "name", envOr("CHEPHERD_RUNNER_NAME", ""),
		"operator-visible @-handle for this runner (e.g. \"iogrid-1\"). Empty fine; daemon may echo back from spawn intent.")
	fs.StringVar(&cfg.a2aBaseURL, "a2a-base-url", envOr("CHEPHERD_A2A_BASE_URL", ""),
		"scheme://host:port the runner serves its A2A endpoint on. Daemon templates the §12.1 well-known URI off this. Empty for R1; Wave R2 lights it.")
	fs.StringVar(&cfg.a2aListen, "a2a-listen", envOr("CHEPHERD_A2A_LISTEN", ""),
		"per-session A2A endpoint TCP bind address (host:port). Empty disables. When set + --sid non-empty, mounts /a2a/<sid>/jsonrpc serving all 11 A2A methods. (#463 Wave R2)")
	fs.BoolVar(&cfg.requireJWT, "require-jwt", envOr("CHEPHERD_REQUIRE_JWT", "") == "1",
		"enable Wave T1 JWT verification on /a2a/<sid>/jsonrpc. Off by default for dev/scaffold; production deploys MUST enable. (#486 Wave T1)")
	fs.StringVar(&cfg.stateDir, "state-dir", envOr("CHEPHERD_RUNNER_STATE", "/var/lib/chepherd/runner"),
		"per-runner state directory inside the container")
	fs.StringVar(&cfg.authToken, "auth-token", envOr("CHEPHERD_TOKEN", ""),
		"bearer token (issued by daemon at spawn time) for outbound WS + local MCP auth")
	// #499 Wave H1 — headless / per-task ephemeral mode flags.
	fs.BoolVar(&cfg.headless.enabled, "headless", false,
		"per-task ephemeral lifecycle (#499 Wave H1). Skips daemon-WS-register + MCP socket; reads ONE task from --task-json / --task-file / stdin; runs via the agent's non-interactive --print mode; writes A2A Task envelope to --result-file or stdout; exits.")
	fs.StringVar(&cfg.headless.taskJSON, "task-json", "",
		"#499 Wave H1 — inline A2A SendMessage params JSON for --headless. Precedence: --task-json > --task-file > stdin.")
	fs.StringVar(&cfg.headless.taskFile, "task-file", "",
		"#499 Wave H1 — path to file containing A2A SendMessage params JSON for --headless.")
	fs.StringVar(&cfg.headless.resultFile, "result-file", "",
		"#499 Wave H1 — output path for the A2A Task envelope. Empty = write to stdout.")
	fs.DurationVar(&cfg.headless.timeout, "task-timeout", 5*time.Minute,
		"#499 Wave H1 — wall-clock cap for the agent process under --headless. Zero disables (not recommended for batch consumers).")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, err
	}
	if cfg.sid == "" {
		// Touch point 1 scaffold allows empty sid for local-only
		// runs; touch point 2 enforces non-empty when A2A endpoint
		// activates.
		log.Printf("[chepherd-runner] WARN: --sid empty; A2A endpoint will not start (scaffold mode)")
	}
	cfg.agentArgs = fs.Args()
	return cfg, nil
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

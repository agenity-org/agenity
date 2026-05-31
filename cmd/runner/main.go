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
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

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

	// A2AListen is the TCP host:port for the per-session A2A
	// endpoint. Defaults to "0.0.0.0:9091" (distinct from daemon's
	// 9090). Sibling runners reach this via the daemon's directory.
	a2aListen string

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
}

func run() error {
	cfg, err := parseFlags()
	if err != nil {
		return err
	}

	// Touch point 1 scaffold: enough plumbing to start the MCP
	// socket server + log the config. Spawn, A2A endpoint, daemon-
	// outbound, PTY ownership wire in touch points 2-4.
	log.Printf("[chepherd-runner] starting sid=%q agent=%q mcp-socket=%q a2a-listen=%q daemon=%q",
		cfg.sid, cfg.agentSlug, cfg.mcpSocket, cfg.a2aListen, cfg.daemonURL)

	// State dir + MCP socket dir prep. mkdir -p with restrictive
	// modes; the socket itself ends up 0600 after listen.
	if err := os.MkdirAll(cfg.stateDir, 0o700); err != nil {
		return fmt.Errorf("mkdir state-dir %q: %w", cfg.stateDir, err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.mcpSocket), 0o700); err != nil {
		return fmt.Errorf("mkdir mcp-socket parent %q: %w", filepath.Dir(cfg.mcpSocket), err)
	}

	// Bring up a minimal Runtime for this single-session runner.
	// In the daemon today Runtime manages MANY sessions; in the
	// runner it manages exactly ONE (this runner's). The same
	// type backs both for now — touch points 2-3 narrow this so
	// per-runner state stays bounded.
	rt, err := runtime.New(cfg.stateDir)
	if err != nil {
		return fmt.Errorf("runtime.New: %w", err)
	}

	// MCP server. The runner doesn't yet have a Deliverer (touch
	// point 2 wires the per-session A2A endpoint; until then the
	// send_to_session shim returns the descriptive -32000 error
	// the New() path already emits). The local Unix-socket
	// transport is the operator-visible deliverable of this
	// touch point.
	mcp := mcpserver.New(rt)
	if cfg.authToken != "" {
		mcp.SetAuthToken(cfg.authToken)
	}
	if err := mcp.StartHTTP("unix://" + cfg.mcpSocket); err != nil {
		return fmt.Errorf("mcp StartHTTP: %w", err)
	}
	log.Printf("[chepherd-runner] MCP listening on unix://%s", cfg.mcpSocket)

	// Wait for SIGINT / SIGTERM. The agent child process (touch
	// point 4) will be reaped + the runner exits with the agent's
	// exit code so the container exit code reflects agent status.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("[chepherd-runner] received %s; shutting down", sig)
	mcp.Stop()
	_ = rt
	_ = cfg.agentArgs
	return nil
}

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
	fs.StringVar(&cfg.a2aListen, "a2a-listen", envOr("CHEPHERD_A2A_LISTEN", "0.0.0.0:9091"),
		"per-session A2A endpoint TCP bind (host:port). Sibling runners reach this via daemon directory.")
	fs.StringVar(&cfg.stateDir, "state-dir", envOr("CHEPHERD_RUNNER_STATE", "/var/lib/chepherd/runner"),
		"per-runner state directory inside the container")
	fs.StringVar(&cfg.authToken, "auth-token", envOr("CHEPHERD_TOKEN", ""),
		"bearer token (issued by daemon at spawn time) for outbound WS + local MCP auth")

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

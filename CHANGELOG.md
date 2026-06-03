# Changelog

All notable changes to chepherd. Format based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); chepherd follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Work toward **v0.9.4** (QA complete, not yet tagged).

### Added

- **Cross-host federation mesh** — independent chepherd parties discover each other via the `chepherd-hub` rendezvous (`/v1/registry/{announce,peers}`) and exchange A2A over hub-relayed WebRTC with zero inbound ports. STUN P2P + TURN relay live at `signal.openova.io` ([#672](https://github.com/chepherd/chepherd/issues/672)). Hub-only peers route through `HubDeliverer` over WebRTC.

### Deprecated

- `chepherd mcp` stdio bridge subcommand ([#479](https://github.com/chepherd/chepherd/issues/479)). Use MCP HTTP transport via `/run/chepherd/mcp.sock` per [V0.9.2-ARCHITECTURE.md](docs/V0.9.2-ARCHITECTURE.md) §22 ([#525](https://github.com/chepherd/chepherd/issues/525)) instead. The subcommand stays functional in v0.9.4 but emits a deprecation warning to stderr on every invocation; suppress via `CHEPHERD_MCP_DEPRECATION_SILENT=1`. Slated for removal in a future release.

## [0.9.2] — 2026-05-29

Latest released daemon tag. A2A-compliant runtime: the runner becomes its own process and IS an A2A endpoint.

### Added

- **Persistence abstraction** — `internal/persistence/` with 13 Repository interfaces; SQLite (`modernc.org/sqlite`) + PostgreSQL (`jackc/pgx`) implementations behind `database/sql`, with a backend-equivalence test framework (testcontainers). One-time migration tool from `~/.local/state/chepherd` JSON → Store ([#208](https://github.com/chepherd/chepherd/issues/208)).
- **A2A runner** — `runtime.Runner` interface (Process + Pod variants); A2A agent-card + JSON-RPC routes wired into `cmd/run.go`'s HTTP server. `chepherd.send_to_session` MCP shim now routes through A2A `SendMessage`. A2A `contextId` accepts session ID OR @-name.
- **Spawn credential resolution** — `Runtime.Spawn` propagates `vault.claude-oauth` → `CLAUDE_CODE_OAUTH_TOKEN` for claude-code workers; T+30s liveness gate + walk-script auth preflight ([#218](https://github.com/chepherd/chepherd/issues/218)).
- **End-to-end walk** — in-process regression test (`go test ./internal/e2e/...`) + operator-walk script (`scripts/v092-e2e-walk.sh`) closing epic [#208](https://github.com/chepherd/chepherd/issues/208).

### Changed

- `internal/shepherd` replaces the retired algorithmic-judge loop; `shepherd.Run` tick loop wired via `cmd/run.go`.

### Removed

- Retired the legacy Python-era `cmd/daemon` + `cmd/shadow` CLI verbs and `internal/messagebus`.

## [0.9.1] — 2026-05-28

### Added

- **Runtime TUI polish** — per-pane workspace tabs, `Ctrl+Arrow` pane focus, empty-tab center card picker, right-click cascade, role-logo agent cards, uniform-width member cards ([#114](https://github.com/chepherd/chepherd/issues/114), [#179](https://github.com/chepherd/chepherd/issues/179), [#180](https://github.com/chepherd/chepherd/issues/180)).

## [0.9.0] — 2026-05-27

### Added

- **Skill library + templates** — skill registry, agent templates, and team/role bootstrapping; openova-MCP integration substrate.

## [0.5.0] — 2026-05-24

The architectural pivot release. chepherd transforms from "tmux supervisor for Claude Code" into an agent runtime + multi-agent control room.

### Added

- **Runtime layer** — `chepherd run` subcommand. New `internal/runtime/` package owning session registry, tribe/role metadata, spawn/assign/grant operations. Lifecycle persistence to `~/.local/state/chepherd-v05/sessions/<id>.json`.
- **pty-host** — `internal/ptyhost/` lifted from `openova-io/openova` at tag `pty-server-handoff-1.0` (commit `c65dbdca`). ~1900 LOC of session multiplexer + ring buffer + WS server + agent catalog, zero rewrite. See `internal/ptyhost/LICENSE-NOTICE` for attribution.
- **@target in-band relay** — `internal/messagebus/`. Watches every session's output stream; routes `^@<target>:` lines as PTY writes into addressed sessions. Tribe-aware, rate-limited (10/min/sender), loop-detected (5x in 30s), 4KiB body cap.
- **chepherd MCP server** — `internal/mcpserver/` exposes 8 control-plane tools (`chepherd.spawn`, `assign`, `grant_channel`, `list`, `read_pane`, `send_to_session`, `pause`, `alert_human`) over JSON-RPC on a Unix socket. `chepherd mcp` subcommand is the stdio bridge agents spawn.
- **Adam + Chepherd default bootstrap** — `internal/prompts/{adam,shepherd}.md` embedded via `//go:embed`. `chepherd run` spawns Adam (worker) + Chepherd (shepherd) by default; `--unmonitored` for solo mode. System prompts injected via `--append-system-prompt`; per-session `.mcp.json` auto-written.
- **HTTP/WS server** — `internal/runtimehttp/`. REST endpoints (`/api/v1/sessions`, `/api/v1/inbox`) + WS attach (`/api/v1/sessions/{name}/attach`). Backend for web/mobile clients.
- **Runtime TUI** — `internal/runtimetui/` (separate from legacy `internal/tui/`). Tribe-grouped session list with role icons, live center pane via session subscription, interact mode, log strip, hotkeys (↑↓/Enter-i/n/p/r/q).
- **Provider abstraction** — `internal/provider/`. 6 implementations behind one interface: Claude OAuth, Anthropic API, OpenRouter, OpenAI, OpenOva NewAPI, Ollama. AgentEnv routes per-agent env-var triples correctly.
- **OS keychain** — `internal/keychain/` with 3 backends (macOS `security` CLI, Linux `secret-tool` + libsecret, 0600-mode file fallback for headless Linux).
- **CLI setup wizard** — `chepherd setup` subcommand. 4 steps: folder pick, provider menu, credential paste (keychain-stored), monitored-mode toggle. Writes `~/.config/chepherd/providers.json`.
- **bp-chepherd Blueprint Helm chart** — `blueprint/chart/` packages chepherd as a third-party Blueprint for the OpenOva catalog. StatefulSet with 3 PVCs preserved (`/repo`, `/.claude-memory`, `/.cache`), openova-MCP sidecar bundle, console UI sidebar registration, Cilium Gateway HTTPRoute. `helm lint` PASS.
- **chepherd.io edge infrastructure** — `edge/chart/` Helm package for coturn (STUN+TURN) + discovery service. 'Powered by OpenOva' branding wired into the pairing UI.
- **Goreleaser config** — `.goreleaser.yaml` for multi-OS builds (linux/darwin/windows × amd64/arm64), `.deb` + `.rpm` via nfpms, Homebrew tap (`chepherd/homebrew-tap`), signed checksums. `scripts/systemd/chepherd.service` for auto-start on Linux desktop installs.
- **Web client scaffold** — `web/` Astro 5 + Svelte 5 + xterm.js project. Same codebase serves the chepherd.io marketing landing AND the dashboard browser client. 8 pages built clean (`astro build` PASS): `/`, `/app`, `/brand`, `/docs`, `/download`, `/vs/{tmux,claude-code,openrouter}`.
- **agentcatalog graceful fallback** — `internal/ptyhost/agentcatalog/lookpath.go` adds `exec.LookPath` resolution when the Builtin's `/usr/local/bin/<cli>` doesn't exist. Lets chepherd work on any laptop without env-var-tuning.
- **Daemon pause-sentinel honored** — `cmd/daemon.go` (legacy) now checks `<uuid>.paused` before injecting. Closes founder-reported gap from earlier in the session.

### Changed

- chepherd's product positioning. The README, landing page, and brand kit reflect the new shape: agent runtime + supervisor + multi-agent dashboard. tmux is no longer the central metaphor.
- Default config dir for the new runtime: `~/.local/state/chepherd-v05/` (legacy `~/.local/state/chepherd/` untouched while v0.5 stabilizes).

### Unchanged (intentional)

- `cmd/daemon.go` algorithmic-judge loop, `internal/tui/`, legacy `chepherd dashboard`, `chepherd-daemon.service` systemd unit, `~/.local/bin/chepherd-go` binary path. Per the founder's mandate to leave existing daily-use paths intact during v0.5 stabilization.

### Contracted with OpenOva (`openova-io/openova#2316`)

- Tag `pty-server-handoff-1.0` published on openova-side for clean attribution
- `chepherd.*` MCP namespace claimed; `gitea.*`, `sandbox.db.*`, `k8s.*`, `marketplace.*`, `sandbox.deploy.*`, `sandbox.stripe.*` reserved for openova-MCP
- bp-chepherd preserves the 3-PVC StatefulSet shape so Sandbox's "close laptop, open phone later" semantics survive
- Auth chain: catalyst-api session cookie → Cilium Gateway → `X-Catalyst-User` header → chepherd WS upgrade
- openova-side integration commits (`openova-io/openova` #2310/#2334, #2308, #2312, #2313, #2365) all shipped on the substrate-pivot vector. The `consoleUI.sidebarEntry` CRD field, brand-kit, and `docs/INTEGRATION-OPENOVA-MCP.md` were queued openova-side.

### Issues closed this milestone

`#27, #37, #47, #49, #50, #51, #52, #53, #54, #55, #57, #58, #59, #60, #61, #62, #65, #66, #67, #68, #69, #70, #71`.

### Still open (deferred)

- `#28, #29, #63, #64` — iOS + Android native builds (Swift + Kotlin work, v0.7)
- `#56` — delete legacy tmux deps (held intact during v0.5 stabilization per founder mandate)
- `#72` — pricing / business model (founder decision pending)
- `#73` — demo loop screen recording (founder records)
- `#74, #75` — legacy TUI polish (founder-filed for the legacy daily-use track)

# chepherd

[![daemon release](https://img.shields.io/badge/daemon-v0.9.2-green)](https://github.com/agenity-org/agenity/releases/tag/v0.9.2)
[![license](https://img.shields.io/github/license/chepherd/chepherd)](LICENSE)
[![chepherd-rc client](https://img.shields.io/badge/chepherd--rc%20client-v0.2.0--rc3-blue)](https://chepherd.org)

> A multi-agent runtime + dashboard for parallel AI coding agents.

`chepherd` (pronounced *shepherd*, spelled with a `c` — intentional) is a Go daemon + interactive dashboard that watches every Claude Code session you run across all your repos, scores each on goal/velocity/focus/end-state-proximity, and coaches them when they drift — using your own `CLAUDE.md` as the rubric. It also spawns and orchestrates teams of agents that talk to each other (and across hosts) over A2A.

Inspired by the [k9s](https://k9scli.io) experience for Kubernetes operators, but for the parallel-AI-agent operator.

> **Status:** The chepherd **daemon** (this Go binary) is at released tag **v0.9.2**; **v0.9.4** is in active development (QA complete, not yet tagged). The **chepherd-rc clients** (web, iOS, Android, relay) are a separately versioned component line at **v0.2.0-rc3** (pre-release). The two version lines are independent — do not conflate the daemon version with the client version. The original architecture was [validated in Python](https://github.com/dynolabs-io/workflow); this repo is the Go implementation.

## What it does

```
chepherd ───────────────────────── reads ──────────► tmux sessions running `claude`
   │                                                  │
   │                                                  ├── openova-27 (claude in ~/repos/openova)
   │                                                  ├── iogrid-8   (claude in ~/repos/iogrid)
   │                                                  ├── ping-5     (claude in ~/repos/ping)
   │                                                  └── …
   │
   │  every N minutes (adaptive cadence) per session:
   ▼
┌───────────────────────────────────────────────────────────────┐
│  read last 20 events from ~/.claude/projects/<uuid>.jsonl     │
│  fetch in-progress + backlog counts via `gh issue list`       │
│  compute: quiet_ratio, banned-phrase hits, addressed_last_coach│
│  ↓                                                            │
│  Call Claude Sonnet (subscription-billed via claude-agent-sdk)│
│  as a JUDGE — emits {verdict, scorecard G/V/F/E, message}     │
│  ↓                                                            │
│  if coach/intervene: tmux paste-buffer + Enter the message    │
│  into the target session's prompt.                            │
└───────────────────────────────────────────────────────────────┘
```

The receiving Claude Code session reads the injection as a normal user message, acknowledges in 2-4 sentences, and ships its next tool call.

## Why it exists

Running 3+ parallel Claude Code sessions across multiple repos exposes failure patterns that don't appear in a single session:

- Sessions end turns with status summaries instead of tool calls (P21 violation)
- Sessions wait for async tasks idle instead of doing parallel inline work (D10/D15)
- Sessions get stuck on the same blocker for hours
- Sessions claim 30+ in-progress issues simultaneously (no focus)
- Sessions ship "theater commits" after coaching without addressing the actual blocker
- Sessions stop maintaining the TRACKER ledger
- Operators can't watch all panes at once

`chepherd` catches these via an LLM-judge that has the same ~/.claude/CLAUDE.md context as the sessions, applies it as a checklist, and intervenes when divergence is real.

## Install (when releases land)

```bash
# Linux/macOS — single binary, no runtime deps
brew install chepherd/chepherd/chepherd                          # macOS via homebrew tap (soon)
curl -fsSL https://chepherd.org/install.sh | sh                  # universal installer (soon)

# from source
go install github.com/agenity-org/agenity@latest
```

## Quick start

```bash
chepherd init           # detect tmux sessions + write ~/.config/chepherd/config.toml
chepherd start          # start the daemon (systemd --user unit)
chepherd                # open the interactive TUI dashboard
chepherd status         # one-shot text status of all sessions
```

## Architecture

| Layer | Tech |
|---|---|
| TUI | [rivo/tview](https://github.com/rivo/tview) (same as k9s) |
| CLI | [spf13/cobra](https://github.com/spf13/cobra) |
| Config | TOML at `~/.config/chepherd/config.toml` |
| State | JSON per session at `~/.local/state/chepherd/sessions/<uuid>.json` |
| Log | Plain text at `~/.local/state/chepherd/chepherd.log` |
| Judge | [Anthropic Claude Code SDK](https://docs.anthropic.com/en/docs/agent-sdk) (subprocess shell-out for now; native Go SDK when Anthropic ships one) |
| A2A mesh | Cross-host agent-to-agent over hub-relayed WebRTC (STUN P2P + TURN relay) via the `signal.openova.io` rendezvous hub |
| Distribution | [goreleaser](https://goreleaser.com) for cross-platform binaries |

See [docs/V0.9.2-ARCHITECTURE.md](docs/V0.9.2-ARCHITECTURE.md) for the full design (v0.9.2 canon; v0.9.4 in development).

## v0.9.2 ship gate — end-to-end walk

The v0.9.2 release closes via the canonical 9-step walk on a fresh provision
(epic [#208](https://github.com/agenity-org/agenity/issues/208)). The in-process
regression gate runs in CI on every commit:

```bash
go test ./internal/e2e/...
```

The operator-facing full walk (curl + dashboard screenshot + sub-agent reviewer)
runs against a real `chepherd run` process:

```bash
chepherd run &                                            # step 1
scripts/v092-e2e-walk.sh http://127.0.0.1:8083 <session>  # steps 3, 4, 6
```

Steps 2 (spawn), 5 (≥60s shepherd-tick wait), 7 (Playwright dashboard
screenshot), 8 (epic comment), and 9 (4-eyes sub-agent review) are operator-driven.
See `scripts/v092-e2e-walk.sh` for the exact contract.

### Anthropic credential resolution for spawned agents (#218)

chepherd spawns each agent (worker, shepherd) as a `claude-code` PTY child
inside a podman sidecar container. The claude binary inside the container
reads its credentials in this priority order at spawn time:

1. **Per-session vault entry** — when `SpawnSpec.ClaudeTokenID` references a
   specific `claude-oauth` credential in chepherd's vault.
2. **Most-recently-updated vault `claude-oauth`** — single shared credential
   used across all agent spawns (R4 default).
3. **Host fallback** — `~/.claude/.credentials.json` on the chepherd-run
   host. Preserves the v0.5-v0.7 "you already ran `claude login`, it just
   works" behavior.

The credential gets materialized into a per-spawn `/run/secrets/claude-credentials`
bind-mount; the chepherd-agent image entrypoint links it to
`~/.claude/.credentials.json` inside the container. A short-lived access
token is auto-refreshed via Anthropic's OAuth `/token` endpoint at spawn
time and the new pair is written back to the vault. If NONE of the three
resolve, the spawned claude-code hits the OAuth login screen, idles, and
its PTY eventually closes — `scripts/v092-e2e-walk.sh` fails fast at the
preflight stage with a clear remediation in that case.

The `TestV092Walk_ShepherdPTYAliveAtT30s` integration test in
`internal/e2e/` is the regression gate: spawn a shepherd, wait 30s,
SendMessage — assert a real Task result (NOT `error.code=-32603 "session:
closed"`).

## Roadmap

Shipped (daemon):

- **v0.5** — Architectural pivot from tmux supervisor to agent runtime + multi-agent control room (`chepherd run`, MCP control plane, web client scaffold, provider abstraction)
- **v0.9.0–v0.9.2** — A2A-compliant runtime: runner-as-A2A-endpoint, ES256 JWT auth, credential vault, dashboard backend; **v0.9.2 is the latest released tag**

In development:

- **v0.9.4** — QA-hardening pass + cross-host federation mesh (STUN P2P + TURN relay via `signal.openova.io`); see the `[Unreleased]` section of [CHANGELOG.md](CHANGELOG.md)

Planned:

- Adapter abstraction for non-Claude-Code agents (Aider, Cline, Cursor) — contributions from those communities welcome
- Hardened TURN relay on Kubernetes (currently STUN-P2P-primary)

## License

MIT — see [LICENSE](LICENSE).

## Related

- The original [Python proof-of-concept](https://github.com/dynolabs-io/workflow) — same architecture, single-file
- [Wink paper (arXiv 2602.17037)](https://arxiv.org/abs/2602.17037) — academic precedent for asynchronous self-intervention systems
- [k9s](https://k9scli.io) — UX inspiration

## Contributing

Issues + PRs welcome via the [GitHub issue tracker](https://github.com/agenity-org/agenity/issues).

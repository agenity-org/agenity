# Free / mixed-flavor agents in chepherd (#741)

chepherd spawns heterogeneous agent CLIs so a team can mix models/flavors that
cross-check each other. This is the operator reference for the free (mostly
no-credit-card) options, how each authenticates, and how to add them.

> Free-tier numbers shift constantly and are region-dependent — treat the RPD
> column as "as of early 2026, verify at signup", not a contract.

## Consolidated provider table

| Agent CLI | Model (free tier) | Good role fit | Auth → env/dir | Free tier (≈ RPD) | Credit card? | Signup / key URL |
|---|---|---|---|---|---|---|
| **Claude Code** | Claude Opus / Sonnet | lead, architect, hard reasoning | OAuth dir `~/.claude` | Pro/Max plan (not RPD) | **Yes** (subscription) | https://claude.ai → `/code` |
| **Gemini CLI** | Gemini 2.5 Pro (→ Flash on quota) | architect, big-context analysis | OAuth (gmail) `~/.gemini` *or* `GEMINI_API_KEY` | ~1,000/day (OAuth); AI Studio key per-model | **No** | https://aistudio.google.com/app/apikey |
| **Qwen Code** | Qwen3-Coder (Plus) | developer (coding-specialised) | OAuth (qwen.ai) `~/.qwen` *or* `DASHSCOPE_API_KEY` | ~2,000/day (OAuth) | **No** for OAuth; Aliyun acct for key | OAuth: https://chat.qwen.ai · key: https://dashscope.console.aliyun.com |
| **GitHub Copilot CLI** | Copilot pool (GPT-4.1 / Claude / Gemini) | developer, reviewer | GitHub OAuth `~/.config/gh` | Copilot Free (~50 chat + 2k completions/mo) | **No** (Copilot Free) | https://github.com/settings/copilot |
| **opencode + Groq** | Llama-3.3-70B, GPT-OSS-120B, Kimi-K2 | fast worker, tester | `GROQ_API_KEY` | generous daily token/req limits | **No** | https://console.groq.com/keys |
| **opencode + Cerebras** | Llama-3.3-70B, Qwen3-235B | fast/heavy worker | `CEREBRAS_API_KEY` | free daily limits (fastest inference) | **No** | https://cloud.cerebras.ai |

*(Unlimited, fully-local, zero-account fallback: `opencode`/`aider` + Ollama — your hardware.)*

## Auth patterns (two shapes)

- **OAuth-directory mount** (claude-code, gemini-cli, qwen-code, copilot): a one-time
  login on the host produces a creds dir (`~/.claude` / `~/.gemini` / `~/.qwen` /
  `~/.config/gh`); chepherd mounts that person's dir into their agent. One human →
  one account → one agent. (Multi-accounting one workload across several accounts to
  pool quota violates provider ToS — don't.)
- **API-key → vault → env** (Groq, Cerebras; optional for Gemini/Qwen): paste the key
  in the spawn wizard's **"+ Add key"** (or `POST /api/v1/vault {provider, env_var,
  label, value}`); it's injected as the env var at spawn.

Vault provider ids: `google-api`→`GEMINI_API_KEY`, `groq-api`→`GROQ_API_KEY`,
`cerebras-api`→`CEREBRAS_API_KEY`, `dashscope-api`→`DASHSCOPE_API_KEY`, plus the
OAuth creds-file providers `gemini-oauth`/`qwen-oauth`/`copilot-oauth`.

## Deployed-vs-pending status (as of this branch)

**Done + verified:**
- All 5 flavors builtin (agentcatalog), CLIs installed in the agent image (node 22),
  guided no-credit-card signup in the spawn wizard.
- Flavor-aware **briefing + embedded skills + team canon + MCP config** written to each
  CLI's native file (`CLAUDE.md`/`GEMINI.md`/`QWEN.md`/`AGENTS.md`/`copilot-instructions.md`;
  `.mcp.json`/`settings.json`/`opencode.json`/`mcp-config.json`) — verified with real bytes.
- Keys validated live (Gemini/Groq/Cerebras → HTTP 200).
- MCP server reachable: claude + opencode `initialize` + `tools/list` (27 tools).
- **Agent→agent messaging proven (send side):** claude autonomously ran
  `get_task → send_to_session→<peer> → send_to_session→operator`.

**Pending:**
- Bidirectional free-agent↔free-agent autonomous loop: opencode received a peer knock
  but didn't auto-reply; gemini-cli stalls on its onboarding screen (lazy MCP). Gap is
  each CLI's knock-handling / onboarding-readiness, not the channel.

## Operator tooling note

`chepherd.alert_human` (and the other `chepherd.*` MCP tools) are **not available in
this Claude Code session** — `/mcp` is not surfaced here (ToolSearch returns none), so
escalations were delivered in-chat rather than via the dashboard inbox. If those tools
should be reachable from this session, that's a chepherd MCP transport gap to wire.

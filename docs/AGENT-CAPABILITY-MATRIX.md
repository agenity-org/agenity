# chepherd Agent Capability Matrix

Two **orthogonal** axes, never mixed:
- **Capability** = *binary* — does the agent complete the MCP round-trip at all? Works once ⇒ **✅ WORKS**. Rate limits do **not** make a capable agent "fail".
- **Capacity** = *quantitative* — the measured rate limits (TPM / RPM / RPD) that bound *how much* a working agent can do. Reported in their own columns.

> **Last full live walk: 2026-06-19 18:45 UTC.** Every runnable row re-verified on the *current* daemon (fresh spawn → operator knock → autonomous round-trip), evidence from the daemon's own MCP tool-call log.

| Agent · model | MCP | Round-trip | TPM | RPM | RPD | Access | Limiting factor (quantitative) |
|---|:--:|:--:|--:|--:|--:|---|---|
| claude-code · Opus 4.8 | ✅ | ✅ | sub | sub | sub | paid sub | none — subscription tier |
| lean-coder · gpt-oss-120b | ✅ | ✅ | 30k | 5 | n/d | Cerebras free | req ~3k ≪ 30k TPM → not rate-bound in practice |
| lean-coder · llama-3.3-70b | ✅ | ✅ | ~12k | n/d | n/d | Groq free | req ~3k ≪ 12k TPM |
| lean-coder · gemini-2.5-flash | ✅ | ✅ | per-tok | n/d | ~1,500 | Google free | high free quota |
| lean-coder · qwen3-32b | ✅ | ✅ | ~Groq | n/d | n/d | Groq free | req ~3k |
| gemini-cli · gemini-3.5-flash | ✅ | ✅ | per-tok | n/d | **20** | Google free | **RPD = 20/day** caps volume (works until spent) |
| copilot · Copilot (GPT-4o/Claude) | ✅ | ✅ | n/d | n/d | n/d | PAT + Copilot Free | Copilot Free premium-request allowance |
| opencode · gpt-oss-120b | ✅ | ❌ | 30k | 5 | n/d | Cerebras free | **per-turn 15–40k tok > 30k TPM** → never completes on free (would work on a higher-TPM/paid tier) |
| qwen-code · — | ✅ | n/r | — | — | — | no key | not run — no DashScope key |
| aider · — | ❌ | — | — | — | — | — | no MCP support |
| little-coder · — | ❌ | — | — | — | — | — | no MCP support |

### Reading the columns
- **MCP** — speaks chepherd's MCP protocol (`initialize`+`tools/list`). Binary.
- **Round-trip** — completed an **autonomous** knock → `get_task` → reply **at least once**. Binary capability. ✅ = proven works; ❌ = never completes; `n/r` = not run; — = n/a (no MCP).
- **TPM / RPM / RPD** — measured rate limits (tokens-per-min / requests-per-min / requests-per-day). These bound *throughput*, not *capability*. `sub` = subscription-governed (not a free cap); `per-tok` = billed per-token, no per-minute cap; `n/d` = vendor doesn't disclose.
- **Access** — credential/tier in use.
- **Limiting factor** — the *quantitative* thing that bounds this combo (or "none").

**Provenance:** Round-trip/MCP = live-measured this walk (daemon log). TPM/RPM/RPD = measured from this account's provider response headers where shown as numbers; `n/d` where undisclosed. Note the measured-vs-published drift: Google *publishes* ~1,500 RPD free, but this account's gemini-cli `3.5-flash` fallback **measured 20 RPD**.

### Capability vs. capacity — worked example
**gemini-cli is `✅ WORKS`** — it ran a full autonomous round-trip (`get_task`+`send_to_session`+`alert_human` → OK) this walk. Its 20/day is a **capacity** number in the **RPD** column, *not* a capability downgrade. It works every time **until** the 20 daily requests are spent, then returns `429 limit:20` until reset. Capability ✅, RPD = 20. (To lift the cap: pin `gemini-2.5-flash`, commit `c9ff5d0` — a daemon redeploy.)

**opencode is the only `❌`** — and it's a pure **capacity** failure, not a capability one: it's MCP-capable, but a single turn packs 15–40k tokens, exceeding the 30k TPM cap on request #1, so it never reaches a tool call on free tier. It would work on a higher-TPM/paid tier.

### Sustained-use rule
Capability ✅ is necessary; to run *continuously* on a tier you also need: `tokens/turn < TPM` **and** `turns/day < RPD`. lean-coder satisfies both on every free tier (1 small ~3k req/turn); gemini-cli satisfies capability but is RPD-bound to ~20 turns/day; opencode fails the TPM term.

### Bottom line
**7 agents WORK** (claude-code · lean-coder×{Cerebras, Groq-llama, Gemini-2.5, Qwen3} · gemini-cli · copilot), all with fresh autonomous round-trips 2026-06-19. **1 fails** (opencode — capacity). gemini-cli is fully working but volume-capped at 20/day; copilot runs on a Copilot Free allowance.

### Sources (model specs)
[gpt-oss-120b](https://arxiv.org/pdf/2508.10925) · [Llama 3.3 70B](https://console.groq.com/docs/model/llama-3.3-70b-versatile) · [Qwen3-32B](https://huggingface.co/Qwen/Qwen3-32B) · [Gemini Flash limits](https://pecollective.com/tools/gemini-free-tier-guide/)

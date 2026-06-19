# chepherd Agent Capability Matrix

**Comprehensive + transposed:** every *attribute* is a row, every *agent* a column.
Nothing is dropped when columns are added — a new attribute is just a new row.
Two axes kept strictly separate: **capability** (binary — does it work at all?) and
**capacity** (quantitative — rate limits that bound *how much*).

> **Last full live walk: 2026-06-19** — every column re-verified on the *current* daemon (fresh spawn → operator knock → autonomous round-trip), evidence from the daemon's own MCP tool-call log. opencode re-walked 20:18 UTC on the rebuilt image. `LC` = lean-coder.

| Attribute | claude-code | LC · Cerebras | LC · Groq | LC · Gemini | LC · Qwen | gemini-cli | copilot | opencode |
|---|---|---|---|---|---|---|---|---|
| **Model** | Opus 4.8 | gpt-oss-120b | llama-3.3-70b | gemini-2.5-flash | qwen3-32b | gemini-3.5-flash | GPT-4o/Claude | gemini-2.5-flash |
| **Params** (total/active) | n/d | 117B/5.1B | 70B | n/d | 32.8B | n/d | n/d | n/d |
| **Context** | 200k | 131k | 131k | 1.05M | 131k | ~1M | n/d | 1.05M |
| **Provider** | Anthropic | Cerebras | Groq | Google | Groq | Google | GitHub | Google |
| **Access** | paid sub | free | free | free | free | free | PAT+Free | free |
| *— capability (binary) —* | | | | | | | | |
| **MCP** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Knock recv** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **LLM call** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **get_task** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Reply tool** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Round-trip** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| *— capacity (quantitative) —* | | | | | | | | |
| **TPM** | sub | 30k | ~12k | per-tok | ~Groq | per-tok | n/d | ~250k |
| **RPM** | sub | 5 | n/d | n/d | n/d | n/d | n/d | ~10 |
| **RPD** | sub | n/d | n/d | ~1,500 | n/d | **20** | Free-alw. | ~1,500 |
| **Tokens/turn** | heavy | ~3k | ~3k | ~3k | ~3k | ~15k | ~med | 15–40k |
| *— outcome —* | | | | | | | | |
| **Limiting factor** | none | none | none | none | none | RPD 20/day | Free alw. | none on Gemini |
| **Status** | WORKS | WORKS | WORKS | WORKS | WORKS | WORKS | WORKS | **WORKS** |

**Not in the table (degenerate — fail at MCP / no credential):** `qwen-code` = MCP-capable but **not run** (no DashScope key) · `aider` = **no MCP support** · `little-coder` = **no daemon MCP config**.

### How to read it
- **Capability rows** (MCP → Round-trip) are **binary**: ✅ = proven works (≥1 live success this walk), ❌ = never completes, — = n/a because an earlier stage failed. **Rate limits never turn a ✅ into ❌.**
- **Capacity rows** (TPM/RPM/RPD/Tokens-per-turn) are **quantities** that bound throughput. `sub` = subscription-governed · `per-tok` = billed per-token · `Free-alw.` = Copilot Free premium-request allowance · `n/d` = vendor undisclosed.
- **Status** = WORKS (capability ✅) / FAILS (capability ❌) — a *capability* verdict, independent of how fast/much.

### Worked examples (capability ≠ capacity)
- **opencode = WORKS (resolved 2026-06-19).** It **FAILS on Cerebras/Groq** — a pure *capacity* failure: one turn = 15–40k tokens > the 30k (Cerebras) / 12k (Groq) TPM cap, so it dies at the LLM call (`AI_APICallError: Tokens per minute limit exceeded`). Moved to **Gemini (~250k TPM)** where the turn fits, fixed two daemon bugs — (a) the Google key is now injected as `GOOGLE_GENERATIVE_AI_API_KEY`, (b) the `#743` tools-slim that wrongly disabled all chepherd tools was removed — and it now does a **full autonomous round-trip**: `get_task`+`alert_human`+`send_to_session→operator` → OK. So opencode's failure was *capacity + config*, never capability.
- **gemini-cli = WORKS.** Full autonomous round-trip; `RPD = 20` is a **capacity** number — works until 20 daily requests are spent, then `429 limit:20`. ✅ capability, low RPD.
- **copilot = WORKS.** With the `Copilot Requests` PAT permission (added 2026-06-19) it autonomously fired `get_task → OK` then `send_to_session→operator → OK`.

### Sustained-use rule
Capability ✅ is necessary; to run *continuously* on a tier you also need: `tokens/turn < TPM` **and** `turns/day < RPD`. lean-coder satisfies both on every free tier; gemini-cli is RPD-bound to ~20 turns/day; opencode now fits on Gemini (15–40k ≪ 250k TPM).

### Provenance
Capability + per-turn tokens = live-measured this walk (daemon log). TPM/RPM/RPD = measured from this account's provider response headers where a number is shown; `n/d` where undisclosed. Measured beats published: Google *publishes* ~1,500 RPD free, but this account's gemini-cli `3.5-flash` fallback **measured 20 RPD**. opencode's Gemini model/key/tool-fix ship via vault entries (`OPENCODE_MODEL`, `GOOGLE_GENERATIVE_AI_API_KEY`) + the daemon rebuild (`runtime.go` tools-slim removed).

### Bottom line
**8 agents WORK** — claude-code, lean-coder×{Cerebras, Groq-llama, Gemini-2.5, Qwen3}, gemini-cli, copilot, **and opencode (on Gemini)**, all with live autonomous round-trips 2026-06-19. **Zero capability failures remain.** gemini-cli is volume-capped at 20/day; copilot runs on a Copilot Free allowance; opencode requires the Gemini provider (busts Cerebras/Groq TPM).

### Sources (model specs)
[gpt-oss-120b](https://arxiv.org/pdf/2508.10925) · [Llama 3.3 70B](https://console.groq.com/docs/model/llama-3.3-70b-versatile) · [Qwen3-32B](https://huggingface.co/Qwen/Qwen3-32B) · [Gemini Flash limits](https://pecollective.com/tools/gemini-free-tier-guide/)

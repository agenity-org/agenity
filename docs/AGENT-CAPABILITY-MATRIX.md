# chepherd Agent Capability Matrix

**Comprehensive + transposed:** every *attribute* is a row, every *agent* a column.
Nothing is dropped when columns are added — a new attribute is just a new row.
Two axes kept strictly separate: **capability** (binary — does it work at all?) and
**capacity** (quantitative — rate limits that bound *how much*).

> **Last full live walk: 2026-06-19 18:45 UTC** — every column re-verified on the *current* daemon (fresh spawn → operator knock → autonomous round-trip), evidence from the daemon's own MCP tool-call log. `LC` = lean-coder.

| Attribute | claude-code | LC · Cerebras | LC · Groq | LC · Gemini | LC · Qwen | gemini-cli | copilot | opencode |
|---|---|---|---|---|---|---|---|---|
| **Model** | Opus 4.8 | gpt-oss-120b | llama-3.3-70b | gemini-2.5-flash | qwen3-32b | gemini-3.5-flash | GPT-4o/Claude | gpt-oss-120b |
| **Params** (total/active) | n/d | 117B/5.1B | 70B | n/d | 32.8B | n/d | n/d | 117B/5.1B |
| **Context** | 200k | 131k | 131k | 1.05M | 131k | ~1M | n/d | 131k |
| **Provider** | Anthropic | Cerebras | Groq | Google | Groq | Google | GitHub | Cerebras |
| **Access** | paid sub | free | free | free | free | free | PAT+Free | free |
| *— capability (binary) —* | | | | | | | | |
| **MCP** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Knock recv** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **LLM call** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| **get_task** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | — |
| **Reply tool** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | — |
| **Round-trip** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| *— capacity (quantitative) —* | | | | | | | | |
| **TPM** | sub | 30k | ~12k | per-tok | ~Groq | per-tok | n/d | 30k |
| **RPM** | sub | 5 | n/d | n/d | n/d | n/d | n/d | 5 |
| **RPD** | sub | n/d | n/d | ~1,500 | n/d | **20** | Free-alw. | n/d |
| **Tokens/turn** | heavy | ~3k | ~3k | ~3k | ~3k | ~15k | ~med | 15–40k |
| *— outcome —* | | | | | | | | |
| **Limiting factor** | none | none | none | none | none | RPD 20/day | Free alw. | req>TPM |
| **Status** | WORKS | WORKS | WORKS | WORKS | WORKS | WORKS | WORKS | FAILS |

**Not in the table (degenerate — fail at MCP / no credential):** `qwen-code` = MCP-capable but **not run** (no DashScope key) · `aider` = **no MCP support** · `little-coder` = **no daemon MCP config**.

### How to read it
- **Capability rows** (MCP → Round-trip) are **binary**: ✅ = proven works (≥1 live success this walk), ❌ = never completes, — = n/a because an earlier stage failed. **Rate limits never turn a ✅ into ❌.**
- **Capacity rows** (TPM/RPM/RPD/Tokens-per-turn) are **quantities** that bound throughput. `sub` = subscription-governed · `per-tok` = billed per-token (no per-minute cap) · `Free-alw.` = Copilot Free premium-request allowance · `n/d` = vendor undisclosed.
- **Status** = WORKS (capability ✅) / FAILS (capability ❌) — a *capability* verdict, independent of how fast/much.

### Worked examples (capability ≠ capacity)
- **gemini-cli = WORKS.** Ran a full autonomous round-trip this walk (`get_task`+`send_to_session`+`alert_human` → OK). Its `RPD = 20` is a **capacity** number — it works every time until 20 daily requests are spent, then `429 limit:20` until reset. ✅ capability, low RPD. (Lift the cap by pinning gemini-2.5-flash, commit `c9ff5d0`.)
- **opencode = FAILS**, and it's a **pure capacity** failure: MCP ✅, but one turn = 15–40k tokens > the 30k TPM cap, so it dies at the LLM call before any tool call. Would work on a higher-TPM/paid tier.
- **copilot = WORKS.** With the `Copilot Requests` PAT permission (added 2026-06-19) it autonomously fired `get_task → OK` then `send_to_session→operator → OK`.

### Provenance
Capability + per-turn tokens = live-measured this walk (daemon log). TPM/RPM/RPD = measured from this account's provider response headers where a number is shown; `n/d` where undisclosed. Measured beats published: Google *publishes* ~1,500 RPD free, but this account's gemini-cli `3.5-flash` fallback **measured 20 RPD**.

### Sources (model specs)
[gpt-oss-120b](https://arxiv.org/pdf/2508.10925) · [Llama 3.3 70B](https://console.groq.com/docs/model/llama-3.3-70b-versatile) · [Qwen3-32B](https://huggingface.co/Qwen/Qwen3-32B) · [Gemini Flash limits](https://pecollective.com/tools/gemini-free-tier-guide/)

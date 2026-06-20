# chepherd Agent Capability Matrix

**Comprehensive + transposed:** every *attribute* is a row, every *agent* a column.
Nothing is dropped when columns are added — a new attribute is just a new row.
Two axes kept strictly separate: **capability** (binary — does it work at all?) and
**capacity** (quantitative — rate limits that bound *how much*).

> **Last full live walk: 2026-06-19** — every column re-verified on the *current* daemon (fresh spawn → operator knock → autonomous round-trip), evidence from the daemon's own MCP tool-call log. opencode re-walked 20:18 UTC on the rebuilt image. `LC` = lean-coder.
>
> **2026-06-20 update:** (1) **copilot** — second startup gate found + fixed: the "Confirm folder trust" modal swallowed the knock; `COPILOT_ALLOW_ALL=true` skips it (`50f332d`), qa(copilot) round-trip confirmed live. (2) **Gemini RPD re-measured directly** = **20/day per-project-per-model** (not the published ~1,500); opencode + LC·Gemini + gemini-cli all draw from the same per-model 20/day pool. Capability rows unchanged (rate limits never flip a ✅).

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
| **TPM** | sub | 30k | ~12k | ~250k | ~Groq | n/d | n/d | ~250k |
| **RPM** | sub | 5 | n/d | n/d | n/d | n/d | n/d | ~10 |
| **RPD** | sub | n/d | n/d | **20** | n/d | **20** | Free-alw. | **20** |
| **Tokens/turn** | heavy | ~3k | ~3k | ~3k | ~3k | ~15k | ~med | 15–40k |
| **Reqs/round-trip** | n/a | 1 | 1 | 1 | 1 | 1 | 1 | **3** |
| **Round-trips/day** (RPD÷reqs) | sub | n/d | n/d | **20** | n/d | **20** | Free-alw. | **~6** |
| *— outcome —* | | | | | | | | |
| **Limiting factor** | none | TPM 30k | TPM 12k | RPD 20/day | TPM~Groq | RPD 20/day | Free alw. | **RPD 20/day** |
| **Status** | WORKS | WORKS | WORKS | WORKS | WORKS | WORKS | WORKS | **WORKS** |

**Not in the table (degenerate — fail at MCP / no credential):** `qwen-code` = MCP-capable but **not run** (no DashScope key) · `aider` = **no MCP support** · `little-coder` = **no daemon MCP config**.

### How to read it
- **Capability rows** (MCP → Round-trip) are **binary**: ✅ = proven works (≥1 live success this walk), ❌ = never completes, — = n/a because an earlier stage failed. **Rate limits never turn a ✅ into ❌.**
- **Capacity rows** (TPM/RPM/RPD/Tokens-per-turn) are **quantities** that bound throughput. `sub` = subscription-governed · `Free-alw.` = Copilot Free premium-request allowance · `n/d` = vendor undisclosed.
- **Gemini's TPM is real, not absent.** Gemini free enforces a **per-minute TPM (~250k, published)** — ~8× Cerebras's 30k. That high ceiling is *why opencode fits on Gemini but busts Cerebras's 30k*; it is **not** "no per-minute cap." On Gemini the binding limit shifts to **RPD = 20/day** (measured 2026-06-20, see below) and RPM.
- **RPD = 20/day is per-project-per-model (measured).** Direct probe 2026-06-20: `gemini-2.5-flash` → `429 RESOURCE_EXHAUSTED, quotaId GenerateRequestsPerDayPerProjectPerModel-FreeTier, limit: 20`. The cap is keyed on (project, model), so **every agent sharing one key + model draws from the SAME 20/day pool.** Consequence: opencode at 3 reqs/round-trip = ~6 round-trips/day on `gemini-2.5-flash`, and **two opencode on one key share that pool** (they don't each get 20). Sibling models have their *own* 20/day buckets — probe results: `gemini-2.5-flash-lite` ✅ available, `gemini-2.0-flash` 429 (per-minute TPM), `gemini-2.0-flash-lite` 429 (RPD), `gemini-2.5-pro` 429 (per-day input-tokens), `gemini-3-flash-preview` ✅ single / 429 under 2-agent load.
- **Status** = WORKS (capability ✅) / FAILS (capability ❌) — a *capability* verdict, independent of how fast/much.

### Worked examples (capability ≠ capacity)
- **opencode = WORKS (capability ✅, round-trip proven live; resolved 2026-06-19, RPD re-measured 2026-06-20).** It **FAILS on Cerebras/Groq** — a pure *capacity* failure: one turn = 15–40k tokens > the 30k (Cerebras) / 12k (Groq) TPM cap, so it dies at the LLM call (`AI_APICallError: Tokens per minute limit exceeded`). Moved to **Gemini (~250k TPM)** where the turn fits, fixed two daemon bugs — (a) the Google key is now injected as `GOOGLE_GENERATIVE_AI_API_KEY`, (b) the `#743` tools-slim that wrongly disabled all chepherd tools was removed — and it does a **full autonomous round-trip**: `get_task`+`alert_human`+`send_to_session→operator` → OK (architect + full-stack both round-tripped live 2026-06-20). So opencode's failure mode was *capacity + config*, never capability.
  - **Capacity correction (measured 2026-06-20):** opencode's Gemini RPD is **20/day, not the published ~1,500** — same measured-beats-published case as gemini-cli. At **3 reqs/round-trip** that is **~6 round-trips/day** on `gemini-2.5-flash`. Because the 20/day pool is **per-project-per-model**, two concurrent opencode on one key **share** it. So a *single* opencode round-trip is deterministic until the pool drains; running *two* concurrently is bounded by 20 reqs/day shared + RPM — a **quantity**, not a capability change. Levers: daily reset, a 2nd free key (own pool), a sibling model's own 20/day bucket, or lean-coder (1 req/round-trip = 20 round-trips/day on the same cap).
- **gemini-cli = WORKS.** Full autonomous round-trip; `RPD = 20` is a **capacity** number — works until 20 daily requests are spent, then `429 limit:20`. ✅ capability, low RPD.
- **copilot = WORKS.** With the `Copilot Requests` PAT permission (added 2026-06-19) it autonomously fired `get_task → OK` then `send_to_session→operator → OK`.

### Sustained-use rule
Capability ✅ is necessary; to run *continuously* on a tier you also need: `tokens/turn < TPM` **and** `reqs/day < RPD`. lean-coder satisfies both on every free tier (1 req/round-trip → 20 round-trips/day on Gemini's 20/day cap); gemini-cli is RPD-bound to ~20 turns/day; opencode fits Gemini's TPM (15–40k ≪ 250k) but at 3 reqs/round-trip is RPD-bound to **~6 round-trips/day per key** on `gemini-2.5-flash`, **shared** across concurrent opencode on the same key+model.

### Provenance
Capability + per-turn tokens = live-measured this walk (daemon log). TPM/RPM/RPD = measured from this account's provider response headers where a number is shown; `n/d` where undisclosed. Measured beats published: Google *publishes* ~1,500 RPD free, but this account's gemini-cli `3.5-flash` fallback **measured 20 RPD**. opencode's Gemini model/key/tool-fix ship via vault entries (`OPENCODE_MODEL`, `GOOGLE_GENERATIVE_AI_API_KEY`) + the daemon rebuild (`runtime.go` tools-slim removed).

### Bottom line
**8 agents WORK** — claude-code, lean-coder×{Cerebras, Groq-llama, Gemini-2.5, Qwen3}, gemini-cli, copilot, **and opencode (on Gemini)**, all with live autonomous round-trips 2026-06-19. **Zero capability failures remain.** gemini-cli is volume-capped at 20/day; copilot runs on a Copilot Free allowance; opencode requires the Gemini provider (busts Cerebras/Groq TPM).

### Sources (model specs)
[gpt-oss-120b](https://arxiv.org/pdf/2508.10925) · [Llama 3.3 70B](https://console.groq.com/docs/model/llama-3.3-70b-versatile) · [Qwen3-32B](https://huggingface.co/Qwen/Qwen3-32B) · [Gemini Flash limits](https://pecollective.com/tools/gemini-free-tier-guide/)

# Agent Capability Matrix — chepherd mixed-team (scientific decomposition)

Each combination broken into **independent, measurable axes**. No verdict words —
every cell is one objective value with a provenance tag.

## Methodology / provenance

| Tag | Meaning |
|---|---|
| `M` | **Measured live** this session — chepherd daemon logs (`podman logs chepherd \| grep [chepherd-mcp]`) or the provider's own HTTP response headers seen by the daemon. |
| `S` | **Vendor spec** — provider/model documentation (sources at bottom). |
| `n/d` | Vendor does **not disclose**. |
| `n/r` | **Not run** — no credential provisioned. |
| `est` | Estimated from observed request bodies (order-of-magnitude). |

> **Authority rule:** for rate limits, `M` (this account's measured headers) **overrides**
> `S` (published tier) — published tiers routinely differ from what an account receives.
> Demonstrated drift: published free RPD = **1,500**; this account's gemini-cli fallback
> measured **20 RPD** (see Table B note).

---

## Table A — Runtime / protocol capability (model-independent, chepherd-measured)

| Runtime | Auth method | MCP handshake (`initialize`+`tools/list`) | Transport | Multi-turn loop | Tool-call emission *demonstrated* | Tools exposed | Requests / turn | ~Input tokens / req |
|---|---|---|---|---|---|---|---|---|
| claude-code | OAuth (sub) | ✅ `M` | Streamable-HTTP | ✅ | ✅ `M` (`get_task`,`alert_human`,`send_to_session`) | 27 `M` | 1 (+reasoning) | heavy `n/d` |
| lean-coder | API key (env) | ✅ `M` | HTTP | ✅ (knock→1 call→reply) | ✅ `M` | 27 `M` | **1** `M` | **~2–5k** `est` |
| gemini-cli | API key | ✅ `M` | HTTP | ✅ (ReAct) | ✅ `M` (`chepherd.list`,`get_task`) | 27 `M` | multi | ~10–20k `est` |
| copilot | GitHub PAT | ✅ `M` | HTTP | ✅ | ✅ by design — **not reached** (auth fails first) | 27 `M` | multi | ~medium `est` |
| opencode | API key | ✅ `M` | HTTP | ✅ | ❌ **never reached** — TPM-fails on req #1 | 27 `M` | multi (build+title+per-tool) | **~15–40k** `M` |
| qwen-code | API key / OAuth | ✅ (engine = gemini-cli) | HTTP | ✅ | `n/r` | 27 | multi | ~10–20k `est` |
| aider | API key | ❌ **no MCP** `M` | — | — | — | — | — | — |
| little-coder | API key | ❌ **no daemon MCP cfg** `M` | — | — | — | — | — | — |

---

## Table B — Model + provider economics / limits

Model specs `S` (web-verified); rate limits `M` (this account's measured headers, 2026-06-16).

| Model | Params (total / active) | Context (tok) | Max output | Provider | Access | TPM | RPM | RPD |
|---|---|---|---|---|---|---|---|---|
| Claude (Opus 4.8) | `n/d` | 200k `S` (≤1M ext.) | `n/d` | Anthropic | **paid sub** | sub-tier | sub-tier | — |
| gpt-oss-120b | 117B / **5.1B** (MoE, 4-of-128) `S` | **131,072** `S` | — | Cerebras | free | **30,000** `M` | **5** `M` | `n/d` |
| llama-3.3-70b | 70B dense `S` | **131,072** `S` | — | Groq | free | **~12,000** `M` | `n/d` | `n/d` |
| llama-3.1-8b | 8B dense `S` | 131,072 `S` | — | Groq | free | **6,000** `M` | — | — |
| qwen3-32b | 32.8B / 31.2B non-emb `S` | 32,768 native / **131,072** YaRN `S` | — | Groq | free | ~Groq-tier | — | — |
| gemini-2.5-flash | `n/d` | **1,048,576** `S` | 65,535 `S` | Google | free | per-token | — | > 3.5-flash |
| gemini-3.5-flash *(gemini-cli fallback)* | `n/d` | ~1M `S` | `n/d` | Google | free | per-token | — | **20** `M` ⚠ (published 1,500 `S`) |
| Copilot backends (GPT-4o / Claude / …) | `n/d` | `n/d` | `n/d` | GitHub | PAT / sub | `n/d` | — | — |

> ⚠ **Measured/published drift:** Google publishes ~1,500 RPD for the current free Flash
> model, but the gemini-cli `gemini-3.5-flash` *fallback* on this account measured **20 RPD**
> — the single biggest reason gemini-cli is free-tier-flaky.

---

## Table C — Combination outcome (chepherd live-measured, model-independent of opinion)

Each cell is a daemon-log fact. "binding constraint" is the *measured* limiter.

| Runtime × Model × Provider | knock recv | `get_task`→OK | reply tool→OK | full round-trip | turn completes on tier | **Binding constraint (measured)** |
|---|---|---|---|---|---|---|
| claude-code × Claude × Anthropic sub | ✅ `M` | ✅ `M` | ✅ `M` | ✅ `M` | ✅ | none (paid) |
| lean-coder × gpt-oss-120b × Cerebras free | ✅ `M` | ✅ `M` | ✅ `M` | ✅ `M` | ✅ | footprint ~3k ≪ 30k TPM → fits |
| lean-coder × llama-3.3-70b × Groq free | ✅ `M` | ✅ `M` | ✅ `M` | ✅ `M` | ✅ | ~3k ≪ 12k TPM → fits |
| lean-coder × gemini-2.5-flash × Google free | ✅ `M` | ✅ `M` | ✅ `M` | ✅ `M` | ✅ | single req → fits |
| lean-coder × qwen3-32b × Groq free | ✅ `M` | ✅ `M` | ✅ `M` | ✅ `M` | ✅ | fits (`<think>` handled) |
| gemini-cli × gemini-3.5-flash × Google free | ✅ `M` | ✅ `M` | ✅ `M` (when slot free) | ✅ flaky `M` | **intermittent** | **RPD = 20/day** (limiter) |
| copilot × Copilot × GitHub PAT | ✅ `M` | ❌ | ❌ | ❌ | n/a | **PAT missing `Copilot Requests` scope** (auth, not a limit) |
| opencode × gpt-oss-120b × Cerebras free | ✅ `M` | ❌ | ❌ | ❌ | ❌ | **req ~15–40k > 30k TPM / >5 RPM** |
| opencode × * × Groq free | ✅ `M` | ❌ | ❌ | ❌ | ❌ | req footprint > 6–12k TPM |
| qwen-code × * | `n/r` | `n/r` | `n/r` | `n/r` | `n/r` | no DashScope key |
| aider / little-coder × * | — | — | — | ❌ | — | no MCP support |

---

## Derived viability rule (the science)

A combination is **free-tier viable** iff **all four** hold:

```
MCP-capable
  ∧ tool-call-emitting
  ∧ (input_tokens_per_req × reqs_per_turn  <  provider_TPM)     ← throughput gate
  ∧ (turns_per_day                          <  provider_RPD)     ← daily-quota gate
```

Applying the measured numbers:

| Combination | term 1 MCP | term 2 emits | term 3 throughput | term 4 daily | viable |
|---|---|---|---|---|---|
| lean-coder × any free | ✅ | ✅ | ~3k < 6–30k ✅ | 1 ≪ quota ✅ | ✅ |
| gemini-cli × Gemini free | ✅ | ✅ | ✅ | turns vs **20 RPD** ❌ | ⚠ intermittent |
| opencode × any free | ✅ | n/a | **15–40k > 30k ❌** | — | ❌ |
| copilot × GitHub | ✅ | ✅ | n/a | n/a | ⛔ auth scope gate (orthogonal) |
| aider / little-coder | ❌ | — | — | — | ❌ |

**Why lean-coder is the only off-the-shelf-beating answer:** it forces `reqs_per_turn → 1`
and `input_tokens → ~3k` by construction (one knock → one tiny LLM call → one reply tool),
so it passes term 3 and term 4 under *every* measured free ceiling. opencode fails term 3
structurally; gemini-cli fails term 4 intermittently (fixable by pinning 2.5-flash → higher RPD,
commit `c9ff5d0`, deploy-gated).

---

## Sources (model specs)
- gpt-oss-120b: [OpenAI model card (arXiv 2508.10925)](https://arxiv.org/pdf/2508.10925), [Groq model docs](https://console.groq.com/docs/model/openai/gpt-oss-120b)
- Llama 3.3 70B: [Groq model docs](https://console.groq.com/docs/model/llama-3.3-70b-versatile), [llm-stats](https://llm-stats.com/models/llama-3.3-70b-instruct)
- Qwen3-32B: [Qwen/Qwen3-32B (Hugging Face)](https://huggingface.co/Qwen/Qwen3-32B)
- Gemini 2.5/3 Flash: [Gemini context-window analysis](https://www.datastudios.org/post/google-gemini-context-window-token-limits-model-comparison-and-workflow-strategies-for-late-2025), [Gemini free-tier guide](https://pecollective.com/tools/gemini-free-tier-guide/)
- Rate limits tagged `M`: measured from this account's provider response headers via the chepherd daemon, 2026-06-16. Authoritative over published tiers per the methodology note.

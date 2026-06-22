# Provider / Agent Strategy — chepherd free mesh

Decision record (2026-06-21). Companion to `AGENT-CAPABILITY-MATRIX.md` (which holds
the measured capability/capacity facts). This doc answers: **which agent runs on which
provider, and why.**

## Decision
- **opencode = the standard agent for raw OpenAI-compatible free providers** (Cerebras,
  Groq) — providers that are pure inference APIs with no first-party agent CLI. We invest
  in optimizing opencode to fit their TPM rather than maintaining N agents.
- **Native first-party CLIs** are used for providers that ship one (they're tuned for
  their own backend, and in copilot's case it's the *only* way to reach the hosted model).

## Mapping (which agent per provider)
| Provider | Agent | Type | Rationale |
|---|---|---|---|
| Cerebras (gpt-oss-120b) | **opencode** | generic adapter | raw inference API, no first-party CLI |
| Groq (llama-3.3-70b) | **opencode** | generic adapter | raw inference API, no first-party CLI |
| Google / Gemini | **gemini-cli** | native | Google's own CLI (operator-fixed) |
| Anthropic / Claude | **claude-code** | native | Anthropic's official CLI; runs on the sub |
| GitHub | **copilot** | native | GitHub-hosted model — only its own CLI reaches it |
| Qwen | **qwen-code** | native | Qwen's own fork (when a key is added) |

**Rule:** first-party agent exists → use it; pure inference API (Cerebras/Groq) → opencode.
(opencode also runs on Gemini's 250k-TPM headroom if we ever unify, but Gemini stays on
gemini-cli per the operator's call.)

## Measured free-tier limits (▸ = measured this session; others published/model-spec)
| Provider (free) | TPM | RPM | RPD / allowance | Notes |
|---|---|---|---|---|
| Cerebras | **30k** ▸ | **5** ▸ | generous (not measured) | biggest per-request budget → best for tool loops |
| Groq | ~**6–12k** ▸ | high | high (~14k/day pub.) | small TPM → cap tool outputs; short loops |
| Gemini (AI Studio) | ~250k (pub.) | ~10 | **20/day per model** ▸ | RPD counts **LLM calls** (each tool-step = 1 request); sweet spot = a FEW HUGE single-shot calls (~1M context), not chatty loops |
| GitHub Copilot Free | n/a (hosted) | n/a | ~50 premium req/MONTH (pub., not measured) | GitHub eats inference (loss-leader → upsell Pro $10/mo); lowest free *volume*, highest per-call capability |

## Why "free = quantity, not capability"
All four free model APIs (Cerebras gpt-oss-120b, Groq llama-3.3-70b, Gemini 2.5-flash,
Qwen3) **emit native tool calls** — proven live this session with raw `tool_calls`
evidence. The free constraint is throughput (RPD/TPM), never capability. Heavy
off-the-shelf agents (opencode, goose, …) bust free TPM purely because of their
**per-turn weight** (system prompt + tool schemas + context), not because tool-calling
costs money — and "reputable" says nothing about token footprint.

## opencode optimization plan (to fit free TPM) — IN PROGRESS
Problem: opencode's turn is ~15–40k tokens + multiple requests/turn → busts Cerebras 30k
/ Groq 12k. Targets:
1. Slim MCP tool schemas to essentials (correct `tools` allow-list; the old `#743`
   slim was buggy — it disabled ALL chepherd tools).
2. Disable the per-session "title" generation request.
3. Trim system prompt + AGENTS.md + auto file-context.
4. Cap tool-result echo size.
5. **Measure real tokens/turn after each trim**; prove a live round-trip within Cerebras
   30k, then Groq 12k.
Status: optimization spike dispatched 2026-06-21 (background); results to be folded into
the matrix once measured.

## Session fixes that this strategy rests on
- opencode **per-agent model pick** now honored (was hardcoded to vault gemini): `209c6a5` + `7cc3675`.
- copilot's three blockers closed: PAT auth, folder-trust modal (`50f332d`), SSE-idle crash (`c3fd774`).
- Talk transcript: routing (`d001251`) + newest-tasks (`3328222`); Terminal-tab auto-count (`5547875`); Talk-tab simplification (`991e700`).

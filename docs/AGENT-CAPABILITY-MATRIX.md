# chepherd Agent Capability Matrix

Every agent's round-trip is broken into its **5 pipeline stages**. A working agent
must pass all 5. The "Breaks at" column names the exact stage + the literal error,
so "working / flaky / failing" is never a vague label — it's *which stage failed*.

### The 5 stages (what has to happen for one message round-trip)
1. **MCP** — agent connects to chepherd's tool server (`initialize` + `tools/list`).
2. **Knock** — agent receives the operator's message.
3. **LLM** — the model actually answers (no `429`/`503`/auth error).
4. **get_task** — agent calls `chepherd.get_task` to read the task body.
5. **Reply** — agent calls a reply tool (`send_to_session`/`alert_human`).

Stages 3–5 need the model; stages 1–2 don't. So an agent can connect + receive a
knock yet still fail because its model call dies.

| Agent · model · tier | MCP | Knock | LLM | get_task | Reply | Breaks at · exact error | Status |
|---|:--:|:--:|:--:|:--:|:--:|---|---|
| claude-code · Claude Opus 4.8 · paid sub | ✅ | ✅ | ✅ | ✅ | ✅ | — | **WORKS** |
| lean-coder · gpt-oss-120b · Cerebras free | ✅ | ✅ | ✅ | ✅ | ✅ | — | **WORKS** |
| lean-coder · llama-3.3-70b · Groq free | ✅ | ✅ | ✅ | ✅ | ✅ | — | **WORKS** |
| lean-coder · gemini-2.5-flash · Google free | ✅ | ✅ | ✅ | ✅ | ✅ | — | **WORKS** |
| lean-coder · qwen3-32b · Groq free | ✅ | ✅ | ✅ | ✅ | ✅ | — | **WORKS** |
| gemini-cli · gemini-3.5-flash · Google free | ✅ | ✅ | ⚠ | ⚠ | ⚠ | **Stage 3 (LLM)** — after ~20 calls/day: `429 Quota exceeded … limit: 20, model: gemini-3.5-flash`. Stages 3–5 run *only* while a daily slot is free. | **FLAKY** |
| copilot · GitHub Copilot · fine-grained PAT | ✅ | ✅ | ❌ | ❌ | ❌ | **Stage 3 (LLM/auth)** — `Authentication failed … ensure the 'Copilot Requests' permission is enabled`. Fails before the model ever answers. | **BLOCKED** (operator) |
| opencode · gpt-oss-120b · Cerebras free | ✅ | ✅ | ❌ | ❌ | ❌ | **Stage 3 (LLM)** — `Tokens per minute limit exceeded` on request #1: its turn sends 15–40k tokens > the 30k TPM cap. | **FAILS** |
| qwen-code · (no key) | ✅ | — | — | — | — | not run — no DashScope key in vault | **NOT RUN** |
| aider · any | ❌ | — | — | — | — | **Stage 1** — aider has no MCP support | **NO MCP** |
| little-coder · any | ❌ | — | — | — | — | **Stage 1** — no daemon MCP config | **NO MCP** |

**Symbols:** ✅ passes every time · ⚠ passes *only when quota is available* · ❌ fails every time · — n/a (an earlier stage already failed).

### What each status word means (no vague labels)
- **WORKS** — all 5 stages pass on every attempt. Reliable mesh member.
- **FLAKY** — stages 1–2 always pass, but the model-dependent stages (3–5) pass *only while a free-tier quota slot remains*. **gemini-cli's** free fallback model allows ~**20 model calls per day**; once spent, every turn returns `429 limit: 20` until the next daily reset. So it's neither broken nor dependable — it completes a round-trip *sometimes*, bounded by 20/day. (Fix: pin `gemini-2.5-flash`, which has far higher quota — commit `c9ff5d0`, needs a daemon redeploy.)
- **BLOCKED** — chepherd side is 100% done; it fails at an external gate only you can open. **copilot's** PAT authenticates to GitHub (API 200) but lacks the **`Copilot Requests`** permission, so the model call is refused before stage 3. One toggle at github.com/settings/personal-access-tokens fixes it.
- **FAILS** — a structural limit, not fixable on the free tier. **opencode** packs 15–40k tokens into one request (big system prompt + 27 tool schemas + context); that exceeds Cerebras's 30k tokens/minute cap on the very first call.
- **NOT RUN / NO MCP** — no credential, or the tool can't speak MCP at all (fails stage 1).

### Why the working ones work (the rule)
A combo passes stage 3 on a free tier iff `tokens_per_request × requests_per_turn < TPM` **and** `turns_per_day < RPD`.
**lean-coder** is built to send **one ~3k-token request per turn**, so it stays far under every free cap → passes on Cerebras, Groq, Gemini, and Qwen. opencode violates the token term; gemini-cli violates the daily term.

### Bottom line
Reliable today: **claude-code (paid) + lean-coder × {Cerebras, Groq, Gemini, Qwen} (free)**. gemini-cli works ~20×/day then rate-limits; copilot needs your GitHub scope toggle; opencode/aider/little-coder are out for the measured reasons above.

### Sources (model specs)
[gpt-oss-120b](https://arxiv.org/pdf/2508.10925) · [Llama 3.3 70B](https://console.groq.com/docs/model/llama-3.3-70b-versatile) · [Qwen3-32B](https://huggingface.co/Qwen/Qwen3-32B) · [Gemini Flash limits](https://pecollective.com/tools/gemini-free-tier-guide/)

# chepherd Agent Capability Matrix

**One table. Every agent × model × provider combination, every measurable axis.**
This is the single source of truth for agent/model/subscription status.

| # | Agent | Model | Params (total/active) | Context | Provider · Tier | Limits (TPM / RPM / RPD) | MCP | Tool-calls | Auth | Round-trip (live) | Viable | Binding constraint |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | claude-code | Claude Opus 4.8 | n/d | 200k (≤1M) | Anthropic · **paid sub** | subscription | ✅ | ✅ | OAuth | ✅ | ✅ | none (paid) |
| 2 | lean-coder | gpt-oss-120b | 117B / 5.1B (MoE) | 131k | Cerebras · free | 30k / 5 / n/d | ✅ | ✅ | API key | ✅ | ✅ | ~3k req ≪ 30k TPM |
| 3 | lean-coder | llama-3.3-70b | 70B | 131k | Groq · free | ~12k / n/d / n/d | ✅ | ✅ | API key | ✅ | ✅ | ~3k req ≪ 12k TPM |
| 4 | lean-coder | gemini-2.5-flash | n/d | 1.05M | Google · free | per-token | ✅ | ✅ | API key | ✅ | ✅ | single small req fits |
| 5 | lean-coder | qwen3-32b | 32.8B | 32k→131k (YaRN) | Groq · free | Groq free-tier | ✅ | ✅ | API key | ✅ | ✅ | fits (`<think>` handled) |
| 6 | gemini-cli | gemini-3.5-flash | n/d | ~1M | Google · free | per-token · **20 RPD** | ✅ | ✅ | API key | ✅ flaky | ⚠ | **20 req/day** cap |
| 7 | copilot | Copilot (GPT-4o/Claude) | n/d | n/d | GitHub · PAT | n/d | ✅ | ✅ ¹ | fine-grained PAT | ❌ | ⛔ | **PAT lacks `Copilot Requests` scope** |
| 8 | opencode | gpt-oss-120b | 117B / 5.1B (MoE) | 131k | Cerebras · free | 30k / 5 / n/d | ✅ | ❌ ² | API key | ❌ | ❌ | **15–40k req > 30k TPM** |
| 9 | qwen-code | — | — | — | (no key) | — | ✅ ³ | n/r | DashScope/OAuth | n/r | n/r | no credential in vault |
| 10 | aider | — | — | — | any | — | ❌ | — | API key | ❌ | ❌ | no MCP support |
| 11 | little-coder | — | — | — | any | — | ❌ | — | API key | ❌ | ❌ | no daemon MCP config |

¹ copilot emits tool-calls by design, but the turn never starts — auth fails first.
² opencode is MCP-capable but TPM-fails on request #1, before any tool-call is emitted.
³ qwen-code shares gemini-cli's engine (MCP-capable); not run — no key.

---

### How to read this table

**Symbols:** ✅ works · ⚠ works-but-flaky · ❌ fails · ⛔ blocked (non-quantitative) · n/d vendor-undisclosed · n/r not-run (no credential).

**Provenance, by column** (so "scientific" means traceable):
- **Params, Context** — vendor specification, sources below.
- **Provider·Tier, Auth** — chepherd configuration fact.
- **Limits** — *measured live* from this account's provider HTTP response headers (2026-06-16). Authoritative over published tiers — e.g. Google *publishes* ~1,500 RPD free but this account's gemini-cli fallback *measured* **20 RPD**.
- **MCP, Tool-calls, Round-trip, Viable, Binding constraint** — *measured live* from the chepherd daemon log (`podman logs chepherd | grep [chepherd-mcp]`). "Round-trip" = a real operator knock produced `get_task → OK` **and** a reply tool (`alert_human`/`send_to_session`) `→ OK`.

**Viability law** — a combination is viable iff all four hold:
```
MCP-capable ∧ emits-tool-calls ∧ (tokens_per_req × reqs_per_turn < TPM) ∧ (turns_per_day < RPD)
```
- lean-coder (rows 2–5) passes by construction: it forces reqs/turn→1 and tokens→~3k, slipping under every free ceiling.
- opencode (row 8) fails the throughput term structurally.
- gemini-cli (row 6) fails the daily term intermittently (fix: pin 2.5-flash, commit `c9ff5d0`, deploy-gated).
- copilot (row 7) is blocked by an auth scope toggle, not a limit — operator adds `Copilot Requests = Read` to the PAT.

### Bottom line
**Working live today:** claude-code (paid) + lean-coder × {Cerebras, Groq, Gemini, Qwen} (all free) — a real mixed team over the mesh. **gemini-cli** works but is 20-req/day-flaky. **copilot** is one GitHub toggle away. **opencode / aider / little-coder / qwen-code** are ruled out for the measured reasons above.

### Sources (model specs)
- gpt-oss-120b — [model card, arXiv 2508.10925](https://arxiv.org/pdf/2508.10925) · [Groq docs](https://console.groq.com/docs/model/openai/gpt-oss-120b)
- Llama 3.3 70B — [Groq docs](https://console.groq.com/docs/model/llama-3.3-70b-versatile)
- Qwen3-32B — [Hugging Face model card](https://huggingface.co/Qwen/Qwen3-32B)
- Gemini Flash context/limits — [context-window analysis](https://www.datastudios.org/post/google-gemini-context-window-token-limits-model-comparison-and-workflow-strategies-for-late-2025) · [free-tier guide](https://pecollective.com/tools/gemini-free-tier-guide/)

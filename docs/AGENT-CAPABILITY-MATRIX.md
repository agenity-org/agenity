# chepherd Agent Capability Matrix

**One table, fits on screen.** Every agent × model × provider, every measurable axis.
Composite cells keep it to 5 columns so it reads without horizontal scrolling.

| Agent | Model · params · context | Provider · tier · limits | Round-trip (live) | Verdict · binding constraint |
|---|---|---|---|---|
| claude-code | Claude Opus 4.8 · n/d · 200k | Anthropic · **paid sub** | ✅ | ✅ working · MCP+tools ✅ · none (paid) |
| lean-coder | gpt-oss-120b · 117B/5.1B · 131k | Cerebras · free · 30k TPM / 5 RPM | ✅ | ✅ working · req ~3k ≪ 30k TPM |
| lean-coder | llama-3.3-70b · 70B · 131k | Groq · free · ~12k TPM | ✅ | ✅ working · req ~3k ≪ 12k TPM |
| lean-coder | gemini-2.5-flash · n/d · 1.05M | Google · free · per-token | ✅ | ✅ working · single small req fits |
| lean-coder | qwen3-32b · 32.8B · 131k | Groq · free | ✅ | ✅ working · `<think>` handled |
| gemini-cli | gemini-3.5-flash · n/d · ~1M | Google · free · **20 RPD** | ⚠ flaky | ⚠ MCP+tools ✅ · capped at 20 req/day |
| copilot | Copilot (GPT-4o/Claude) · n/d | GitHub · fine-grained PAT | ❌ | ⛔ MCP+tools ✅ · **PAT lacks `Copilot Requests` scope** |
| opencode | gpt-oss-120b · 117B/5.1B · 131k | Cerebras · free · 30k TPM | ❌ | ❌ MCP ✅ but req 15–40k **> 30k TPM** (no tool-call reached) |
| qwen-code | — · — · — | no key in vault | n/r | ⏭ MCP ✅ · not run (no DashScope key) |
| aider | — · — · — | any | ❌ | ❌ no MCP support |
| little-coder | — · — · — | any | ❌ | ❌ no daemon MCP config |

**Symbols:** ✅ works · ⚠ flaky · ❌ fails · ⛔ blocked (non-quantitative) · n/d undisclosed · n/r not-run.
**Round-trip** = a real knock produced `get_task → OK` **and** a reply tool (`alert_human`/`send_to_session`) `→ OK` in the daemon log.
**Provenance:** params/context = vendor spec (sources below); limits + MCP/tools/round-trip = measured live (chepherd daemon logs + this account's provider headers, 2026-06-16). Measured limits override published tiers — e.g. Google *publishes* ~1,500 RPD free, this account *measured* **20 RPD** on the gemini-cli fallback.

### Viability law
A combination works on free tier iff: `MCP-capable ∧ emits-tool-calls ∧ (tokens/req × reqs/turn < TPM) ∧ (turns/day < RPD)`.
- **lean-coder** passes by construction (1 req/turn, ~3k tokens) → works on every free tier.
- **opencode** fails the throughput term (15–40k > 30k TPM) — structural.
- **gemini-cli** fails the daily term (20 RPD) — fixable by pinning 2.5-flash (`c9ff5d0`, deploy-gated).
- **copilot** is an auth-scope gate, not a limit — operator adds `Copilot Requests = Read`.

### Bottom line
Live today: **claude-code (paid) + lean-coder × {Cerebras, Groq, Gemini, Qwen} (free)** — a real mixed team. gemini-cli works but is 20/day-flaky; copilot is one GitHub toggle away; the rest are ruled out for the measured reasons above.

### Sources (model specs)
[gpt-oss-120b](https://arxiv.org/pdf/2508.10925) · [Llama 3.3 70B](https://console.groq.com/docs/model/llama-3.3-70b-versatile) · [Qwen3-32B](https://huggingface.co/Qwen/Qwen3-32B) · [Gemini Flash limits](https://pecollective.com/tools/gemini-free-tier-guide/)

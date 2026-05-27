# Protocol layer comparison — MCP vs PTY-relay vs A2A

**What this is.** A foundational analysis of the three candidate agent-runtime protocol surfaces (Model Context Protocol, PTY-relay, Agent-to-Agent Protocol), scored by best-fit per capability, with chepherd's current usage highlighted. Used to decide whether to migrate, replace, or layer.

**Authority.** Architectural reasoning doc. Authored 2026-05-27 in response to operator question "MCP vs A2A — competitors or complementors, what's the right horse for our course?". Refs [#186](https://github.com/chepherd/chepherd/issues/186).

**Pointer.** Companion to [`docs/PROTOCOL.md`](./PROTOCOL.md) (which specs the chepherd-relay wire details). This doc is the *protocol-choice rationale*, not the spec.

---

## TL;DR

**Complementary, not competitors.** Three protocols cover three orthogonal axes. Chepherd today correctly uses **MCP for the worker → runtime control plane** and **PTY-relay for internal agent ↔ agent messaging + universal CLI wrapping**. **A2A** is not used today and should be added later as an **edge / federation layer** when the first cross-trust-domain use case lands — never as a replacement for MCP or PTY.

---

## Capability matrix

**Legend:** 🟢 = chepherd uses this protocol today for this capability · **bold** = best-fit (highest score) for this capability.

| # | Capability | MCP | PTY-relay | A2A |
|---:|---|---:|---:|---:|
| 1 | Tool invocation from an LLM (typed function call) | 🟢 **95** | 25 | 60 |
| 2 | Peer-to-peer agent messaging (agent ↔ agent text) | 20 | 🟢 90 | **95** |
| 3 | Capability discovery (what can the other side do?) | 🟢 90 | 5 | **95** |
| 4 | Cross-trust-domain federation (across orgs / hosts) | 35 | 5 | **95** |
| 5 | Schema enforcement / typed contracts | 🟢 **95** | 0 | 85 |
| 6 | Structured artifacts (JSON, files, binary blobs) | 🟢 80 | 25 | **95** |
| 7 | Multi-modal data (images, audio, attachments) | 🟢 75 | 20 | **90** |
| 8 | Streaming responses (partial output over time) | 75 | 🟢 **90** | 85 |
| 9 | Long-running task lifecycle (submitted → working → done) | 30 | 20 | **95** |
| 10 | Async task handoff (submit now, fetch later) | 35 | 50 | **90** |
| 11 | Universal CLI agent wrapping (agent unaware of protocol) | 10 | 🟢 **100** | 5 |
| 12 | Human-observable transport (operator reads raw stream) | 40 | 🟢 **100** | 45 |
| 13 | Live dashboard pane streaming | 35 | 🟢 **100** | 60 |
| 14 | Pause / resume / supervise running agent | 30 | 🟢 **75** | 70 |
| 15 | Auth & authz (identity, scope, delegation) | 🟢 55 | 10 | **80** |
| 16 | Low protocol overhead / latency | 🟢 70 | 🟢 **95** | 65 |
| 17 | Local-first / works offline | 🟢 90 | 🟢 **100** | 50 |
| 18 | Heterogeneous agent ecosystem (Claude + Gemini + OpenAI + custom) | 60 | 🟢 **95** | 75 |
| 19 | Standardization maturity (adoption, tooling, age) | 🟢 90 | 🟢 **100** | 55 |
| 20 | Already implemented in chepherd v0.8 (low cost) | 🟢 90 | 🟢 **100** | 30 |

---

## How to read it — three clusters

### Cluster A: 🟢 + **bold** align — right horse, right course, don't touch

*Rows 1, 5, 8, 11, 12, 13, 14, 16, 17, 18, 19, 20 — 12 of 20 capabilities.*

The dominant majority of chepherd's foundational capabilities are already on the best-fit protocol. No migration anywhere in this cluster. The chepherd thesis (universal-CLI shepherd with MCP control plane) is mechanically locked in here.

### Cluster B: 🟢 present but NOT bold — adequate today, room to grow

*Rows 2, 3, 6, 7, 15 — 5 capabilities.*

| Row | What we use | Optimal | Reading |
|---|---|---|---|
| 2 Peer messaging | PTY 90 | A2A 95 | PTY wins on universal-CLI thesis (row 11) — using "second-best" here is actually correct. Keep. |
| 3 Capability discovery | MCP 90 | A2A 95 | MCP discovers runtime tools fine; A2A would add peer-agent discovery — only matters on federation day. |
| 6 Structured artifacts | MCP 80 | A2A 95 | Acceptable today; richer artifact model with A2A. |
| 7 Multi-modal | MCP 75 | A2A 90 | Acceptable today; dashboard mostly streams text. |
| 15 Auth | MCP 55 | A2A 80 | Real weakness as soon as cross-trust enters the picture. |

### Cluster C: no 🟢 anywhere — actual gaps, A2A is the only good answer

*Rows 4, 9, 10 — 3 capabilities chepherd doesn't carry today at all.*

| Row | Capability | Best | Why a gap |
|---|---|---|---|
| 4 | Cross-trust-domain federation | A2A 95 | No way today for an agent at another org to delegate work into chepherd. |
| 9 | Long-running task lifecycle | A2A 95 | Chepherd has no formal task state machine — peer status is ad-hoc text parsing. |
| 10 | Async task handoff | A2A 90 | "Submit now, fetch later" works only loosely via PTY persistence. |

These 3 rows are the **entire case for A2A** — and they're the only rows where A2A is the *only* protocol scoring above 90.

---

## Architect's verdict

- **MCP** — locked for the worker → chepherd-runtime control plane (rows 1, 3, 5). 95-grade fit, already implemented, no challenger.
- **PTY-relay** — locked for internal agent ↔ agent messaging + dashboard streaming + universal CLI wrapping (rows 8, 11–14, 18). The chepherd thesis lives here.
- **A2A** — not needed for any current capability we carry, but is the *only* answer for the 3 gap rows (4, 9, 10) and the row-15 auth weakness. Add it as an **edge / federation layer** when the first cross-trust use case materializes — never as a replacement.

**Two horses already on the right courses; a third horse to add when a new course (federation) opens.**

---

## Implementation implications for v0.8 → v1.0

1. **No protocol replacement.** Don't migrate MCP → A2A. Don't migrate PTY → A2A. The matrix scores both as dominant in their lanes.
2. **Internalize task-lifecycle semantics now** (cheap). Model chepherd-internal task states (`submitted` / `working` / `input-required` / `completed` / `failed`) and structured artifacts on every peer session, even before any A2A wire surface exists. Dashboard gains a real state machine; federation day finds the data model already shaped right.
3. **Defer A2A wire surface** to v0.9 / v1.0, gated on a concrete external-federation use case (e.g., a customer wants their Gemini agent to delegate work into a chepherd worker). When that lands, expose each chepherd session as an A2A endpoint with an Agent Card + inbound task adapter that translates A2A → PTY input and PTY output → A2A artifacts.
4. **Re-score row 18 (heterogeneous ecosystem) yearly.** PTY dominates today because it works with any CLI. As more agents ship native A2A endpoints, A2A's score will rise. If it crosses PTY, the federation case strengthens further.

## Failure modes to reject

- **"Migrate from MCP to A2A"** — false dichotomy; they aren't substitutable layers.
- **"Replace PTY-relay with A2A internally"** — destroys the universal-CLI-shepherd thesis (row 11 collapses from 100 → 5).
- **"Wait for one protocol to win"** — there is no winner-takes-all dynamic. The industry is settling on both, at different layers, and chepherd should follow the same shape.

# Protocol layer comparison — MCP vs PTY-relay vs A2A

**What this is.** A foundational analysis of the three candidate agent-runtime protocol surfaces (Model Context Protocol, PTY-relay, Agent-to-Agent Protocol), scored by best-fit per capability, with chepherd's current usage highlighted. Used to decide whether to migrate, replace, or layer.

**Authority.** Architectural reasoning doc. Authored 2026-05-27, revised 2026-05-28 after deeper deliberation walked back the original "add A2A as edge layer in v0.9" recommendation. Refs [#186](https://github.com/chepherd/chepherd/issues/186).

**Pointer.** Companion to [`docs/PROTOCOL.md`](./PROTOCOL.md) (which specs the chepherd-relay wire details). This doc is the *protocol-choice rationale*, not the spec.

---

## TL;DR (revised conclusion)

**Today's architecture is sound. The only inter-agent fix needed is retiring the regex `@-relay` via a system-prompt edit** (tracked in [#203](https://github.com/chepherd/chepherd/issues/203)) — routing agents to the already-existing `chepherd.send_to_session` MCP tool instead.

- **MCP** stays as the worker → chepherd-runtime control plane (no change).
- **PTY** stays as the substrate + visibility channel (no change).
- **Regex `@-relay`** retires: it was a pre-MCP-tool workaround that calcified into the architecture; the cure is a system-prompt edit, not a new protocol.
- **A2A** is **NOT added in v0.9**. It is parked until a real trigger fires (multi-pod chepherd, cross-cluster federation, or an external 3rd-party agent that needs to call into a chepherd-managed agent). Inside a single chepherd-pod, A2A adds zero value — both endpoints would be localhost components in the same process, and standing up an HTTP server to call ourselves is ceremony over an in-process function call.

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

## Architectural facts the matrix sits on top of

A walk through the actual code (`internal/mcpserver/server.go`, `internal/messagebus/relay.go`, `internal/runtime/runtime.go`, `internal/runtime/container.go`) confirms the topology:

- **Chepherd-daemon** runs as a single Go process; binds the MCP server on TCP **9090** (HTTP + WebSocket, JSON-RPC 2.0, Bearer auth via `CHEPHERD_TOKEN`).
- **Each agent** runs in its own rootless-podman container. Inside that container:
  - **Process 1 (foreground)**: the agent itself (e.g., `claude-code`)
  - **Process 2 (child)**: the `chepherd` binary in `mcp` mode, forked by the agent via its `.mcp.json` config — acts as the agent's MCP bridge over stdio, forwards to the daemon over WebSocket
- **PTY**: chepherd-daemon owns the PTY master FD for every agent. The slave side is bound to the agent container's stdin/stdout via `podman run -t`. All visibility (dashboard pane) and inter-agent delivery (writes to peer's stdin) happen through these master FDs — kernel syscalls, no network.

**Two inter-agent messaging mechanisms exist today** (both shipped):

| Mechanism | Code | Used by |
|---|---|---|
| Regex `@target:` relay | `internal/messagebus/relay.go` | Worker peers (per `internal/prompts/worker.md:12-14`) |
| `chepherd.send_to_session(name, body)` MCP tool | `internal/mcpserver/server.go:235,496` | Shepherd advising Adam (per `internal/prompts/shepherd.md:13,61,74`) |

**The MCP-tool path is structured and reliable. The regex path is fragile** — false matches on quoted strings (`@author:`, `@deprecated:`, shell prompts), approximate ANSI stripping, no delivery feedback, no task correlation. The fix is to flip the worker system prompt to use the MCP tool too (#203).

---

## How to read the matrix — three clusters

### Cluster A: 🟢 + **bold** align — right horse, right course, don't touch

Rows 1, 5, 8, 11, 12, 13, 14, 16, 17, 18, 19, 20 — **12 of 20 capabilities**. The dominant majority of chepherd's foundational capabilities are already on the best-fit protocol. The chepherd thesis (universal-CLI shepherd with MCP control plane + PTY-as-substrate) is mechanically locked in here.

### Cluster B: 🟢 present but NOT bold — adequate today, room to grow

Rows 2, 3, 6, 7, 15 — 5 capabilities. Detail:

| Row | What we use | Optimal | Reading |
|---|---|---|---|
| 2 Peer messaging | PTY 90 (today via regex; after #203 via MCP tool) | A2A 95 | Single-pod doesn't need A2A; PTY substrate + MCP tool is the right shape until federation arrives. |
| 3 Capability discovery | MCP 90 | A2A 95 | MCP discovers runtime tools fine; A2A's Agent Card only matters when external agents need to discover us. |
| 6 Structured artifacts | MCP 80 | A2A 95 | Acceptable today; would gain on federation day. |
| 7 Multi-modal | MCP 75 | A2A 90 | Acceptable today; dashboard mostly streams text. |
| 15 Auth | MCP 55 | A2A 80 | A real weakness — but only matters once cross-trust enters the picture. |

### Cluster C: no 🟢 anywhere — actual gaps, A2A is the only good answer

Rows 4, 9, 10 — 3 capabilities chepherd doesn't carry today at all:

| Row | Capability | Best | Why a gap |
|---|---|---|---|
| 4 | Cross-trust-domain federation | A2A 95 | No way today for an agent at another org to delegate work into chepherd. |
| 9 | Long-running task lifecycle | A2A 95 | No formal task state machine — peer status is in-band conversational text. |
| 10 | Async task handoff | A2A 90 | "Submit now, fetch later" works only loosely via session persistence. |

These 3 rows are **the entire case for A2A** — and they are the only rows where A2A is the *only* protocol scoring above 90. **None of them are needed in v0.9.** A2A activates when a trigger fires.

---

## Verdict (revised)

- **MCP** — locked for the worker → chepherd-runtime control plane (rows 1, 3, 5). 95-grade fit, already implemented.
- **PTY** — locked for substrate + dashboard streaming + universal CLI wrapping (rows 8, 11–14, 18). The chepherd thesis lives here.
- **Regex `@-relay`** — retire via system-prompt edit in [#203](https://github.com/chepherd/chepherd/issues/203). Replace with the already-existing `chepherd.send_to_session` MCP tool plus a server-side auto-envelope (`[@<sender>] ...`) so receivers always know the source. Tool already exists; the change is mechanical, not architectural.
- **A2A** — parked. Add only when one of these triggers fires:
  1. Chepherd splits across multiple pods (scaling beyond a single chepherd-pod's agent capacity).
  2. A second chepherd instance wants to federate (cross-cluster, cross-org).
  3. An external 3rd-party agent runtime wants to call into a chepherd-managed agent.
  4. A flagship coding agent ships a native A2A client and wants to skip chepherd's MCP path.

Until one of those fires, A2A is YAGNI. File a fresh issue at that time.

---

## Failure modes to reject (architect anti-patterns from this deliberation)

- **"Migrate from MCP to A2A"** — false dichotomy; they aren't substitutable layers.
- **"Add A2A internally for future-proofing"** — pure ceremony; both endpoints would be localhost components in the same process. Future-proofing is achieved by stable API shape (`chepherd.send_to_session` as the tool contract), not by speculative wire choice.
- **"Replace regex relay with a new send_to_peer MCP tool with task IDs, attachments, reply_to"** — YAGNI. The existing `send_to_session` already does the delivery; richer envelope features wait until a real use case needs them.
- **"Build a 'lean Activity view' over Claude Code hooks/OTel"** — invented problem. The TUI is a feature, not a bug. If a real user-pain emerges, file a fresh scoped issue then.
- **"Wait for one protocol to win"** — there is no winner-takes-all dynamic. MCP and A2A solve different layers; both will coexist.

## Stable API shape (this is the actual future-proofing)

The chepherd-managed contract that protects against future migration is:

```
Agent calls: chepherd.send_to_session(name, body)
Returns: success / error
Effect:   the body (with [@<sender>] envelope) is delivered to the named session
```

What's BEHIND that tool is implementation detail:
- **Today**: chepherd-daemon writes to the named session's PTY master FD locally.
- **Tomorrow (multi-pod)**: chepherd-daemon-A makes an A2A `tasks/send` POST to chepherd-daemon-B, which then writes to its local PTY.
- **Day after (federation)**: same as multi-pod, but across cluster trust boundaries with mTLS + Agent Card.

Agents never know which. That stable API is the actual future-proofing — not adopting A2A speculatively.

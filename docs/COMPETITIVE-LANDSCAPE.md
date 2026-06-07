# chepherd — competitive landscape (functional scorecard)

**As of 2026-06-07.** This compares chepherd to adjacent multi-agent / agent-runtime
projects on **functionality only**. Maturity, adoption, funding, version, and
reputation are deliberately **excluded** — a capability that exists in prototype
form still counts as *present* here. The question this answers is *"what can each
system functionally do,"* not *"how production-ready or popular is it."*

> The projects address different areas (frameworks vs orchestrators vs personal
> assistants vs mesh infra). The table is normalized onto a shared set of
> agent-mesh capabilities so they can be compared on the axes that matter for
> chepherd's positioning.

## Capability legend (each 0–100 = has this capability, and how completely)

| Code | Capability |
|---|---|
| **PeerA2A** | *Symmetric* agent↔agent messaging (not just orchestrator→subagent) |
| **Dist** | Distributed / cross-host A2A |
| **Fed** | Cross-**org** federation (independent parties discover + talk) |
| **0In** | Zero-inbound / NAT traversal (no exposed ports) |
| **Wrap** | Make a **non-A2A agent A2A-aware** (give a plain CLI an A2A endpoint + inbound delivery) |
| **LiveIn** | Deliver a message into a **running interactive** agent (live session, not batch) |
| **Disc** | Peer discovery / registry / capability cards |
| **Std** | A2A spec compliance + interop (Agent Card, JSON-RPC methods, well-known) |
| **MCP** | MCP tool/data integration |
| **Iso** | Agent isolation / sandbox (containers / pods) |
| **Orch** | Multi-agent orchestration (spawn, fan-out, workflows, durable tasks) |
| **HITL** | Human / operator control (dashboard, live steering) |
| **CLIs** | Agent-flavor agnostic (claude / codex / aider / gemini / qwen / opencode …) |

## Scorecard

| Project | PeerA2A | Dist | Fed | 0In | Wrap | LiveIn | Disc | Std | MCP | Iso | Orch | HITL | CLIs | **FUNC** |
|---|--|--|--|--|--|--|--|--|--|--|--|--|--|--|
| **chepherd** | 92 | 90 | 90 | 92 | 95 | 95 | 85 | 75 | 85 | 75 | 70 | 88 | 90 | **86** |
| **AGNTCY** (SLIM + Agent Directory) | 80 | 85 | 90 | 85 | 50 | 40 | 88 | 88 | 85 | 80 | 50 | 35 | 40 | **69** |
| **kagent** (CNCF, k8s-native) | 80 | 75 | 70 | 25 | 55 | 30 | 80 | 90 | 90 | 85 | 80 | 70 | 45 | **67** |
| **CrewAI / LangGraph** (framework layer) | 65 | 50 | 35 | 10 | 40 | 30 | 55 | 85 | 88 | 55 | 90 | 80 | 40 | **56** |
| **OpenHands** | 40 | 70 | 30 | 20 | 35 | 40 | 40 | 55 | 80 | 90 | 75 | 75 | 30 | **52** |
| **Devin / Factory** (commercial fleets) | 30 | 80 | 25 | 20 | 20 | 50 | 30 | 35 | 60 | 80 | 85 | 80 | 15 | **47** |
| **OpenClaw** (opencla) — *personal assistant, diff. category* | 25 | 25 | 20 | 30 | 30 | 55 | 30 | 40 | 80 | 60 | 40 | 80 | 30 | **42** |
| **Cursor Cloud Agents** | 10 | 80 | 10 | 20 | 10 | 45 | 10 | 35 | 70 | 80 | 60 | 85 | 20 | **41** |
| **claude-flow / ruflo** | 55 | 25 | 15 | 10 | 25 | 35 | 30 | 40 | 80 | 35 | 80 | 50 | 50 | **41** |
| **Claude Code** (subagents + Dynamic Workflows) | 25 | 20 | 5 | 10 | 15 | 60 | 20 | 30 | 95 | 50 | 85 | 85 | 5 | **39** |
| **Worktree orchestrators** (Conductor / claude-squad / Vibe Kanban / uzi / Sculptor) | 5 | 15 | 5 | 5 | 10 | 30 | 5 | 10 | 50 | 60 | 60 | 80 | 85 | **32** |

`FUNC` = holistic functional breadth across all 13 capabilities (not a flat average — rewards genuine coverage, penalizes gaps).

## Reading the table

- **chepherd leads on functional breadth** because it is purpose-built across exactly these axes — it is the only entry non-zero on all 13, and best-in-class on six: **PeerA2A, Dist, Fed, 0In, Wrap, LiveIn**. No other project combines symmetric peer messaging + cross-org federation + zero-inbound + agent-wrapping + live inbound delivery.
- **The two chepherd-defining columns that are near-empty elsewhere:**
  - **Wrap (95)** — taking a stock CLI that knows nothing about A2A and giving it a real A2A endpoint + inbound mailbox. kagent/CrewAI wrap *framework* agents (ADK/LangGraph objects), not arbitrary CLIs; the worktree class runs plain CLIs but never makes them A2A-addressable.
  - **LiveIn (95)** — pushing a peer message into a *running interactive* agent (the knock). Most systems dispatch a new task/process; very few interrupt a live session. Claude Code's shared task list (60) is the nearest, and it's poll-a-list, not push-into-a-live-pane.
- **chepherd's genuine functional gaps (not a whitewash):**
  - **Std (75)** — A2A-compliant endpoints, but custom extensions (knock marker, WebRTC-as-transport) aren't in the A2A core spec, so a vanilla third-party A2A client needs chepherd-side glue to fully interop. kagent (90) / AGNTCY (88) are cleaner standards citizens.
  - **Orch (70)** — spawn/teams/task-persistence exist, but no durable workflow engine (no Temporal-style replay, no LangGraph typed graphs, no on-demand decomposition like Claude Code Dynamic Workflows). CrewAI/LangGraph (90) and Claude Code (85) are deeper here.
  - **Iso (75)** — containerized + non-root + zero-inbound, but pod-per-agent K8s isolation is the design, not uniformly the running reality. OpenHands (90, Docker-by-default) and kagent (85, k8s-native) are more complete.
- **Closest functional rivals are NOT the popular ones:** **AGNTCY** matches the mesh shape (Fed/0In/Disc/Std) but has no operator console (HITL 35) and no agent-wrapping/live-inbound; **kagent** is the broadest infra runtime but empty on zero-inbound (0In 25) and weak on wrapping arbitrary CLIs. The entire Claude-Code family is functionally *narrow* — deep on Orch/HITL/MCP/CLIs, near-zero on the whole A2A/Fed/0In/Wrap left half.

## Deep-dive: chepherd vs kagent (+ complementary positioning)

**kagent uses A2A directly and natively** (verified from kagent docs): *"every AI
agent created with kagent implements the A2A protocol and can be invoked by an A2A
client."* It serves standard Agent Cards at `/.well-known/agent.json`, exposes an
A2A JSON-RPC endpoint (port 8083 on the kagent-controller), and any external A2A
client can call it. So chepherd and kagent **speak the same A2A wire** — that
shared protocol is the basis for complementarity. They differ on what "an agent"
*is*:

| Axis | **kagent** | **chepherd** |
|---|---|---|
| What an agent is | Declarative **k8s CRD** — programmatic agent on Google ADK / LangGraph / CrewAI | **Wrapped interactive CLI** (`claude`, codex, aider), A2A-unaware; runner makes it A2A-capable |
| A2A endpoint | controller-hosted, `/.well-known/agent.json`, :8083 | per-agent **runner**, `/a2a/<sid>/jsonrpc` + Agent Card |
| A2A transport | HTTP / JSON-RPC (standard, in-cluster) | HTTP/JSON-RPC **+ WebRTC DataChannel** (P2P, non-spec extension) |
| Reachability | k8s Service / Gateway / Ingress — assumes reachability | **zero-inbound**, outbound-only, hub-relayed signaling, NAT traversal |
| Cross-org | multi-cluster (Enterprise-gated), still HTTP-reachable | **independent orgs, nothing exposed** |
| Inbound delivery | invoke a (usually stateless) endpoint | **knock into a live interactive session** |
| Human role | dashboard + OTel/Prometheus observability | **operator console: live panes + steering + alerts** |

**One-line diff:** kagent is the in-cluster runtime for programmatic agents you
author as code; chepherd is the edge wrapper for interactive agents humans drive,
federated peer-to-peer across trust boundaries.

### Positioning: complementary, not competitive

Competing with kagent (CNCF, Solo.io) is a losing fight. Complementary is honest
and defensible because chepherd does two things kagent structurally does not:

1. **chepherd wraps the agents kagent can't** — stock interactive CLIs (`claude-code`)
   with a live A2A inbox. *kagent for autonomous service-agents; chepherd for the
   interactive coding agents humans operate — on the same A2A bus.*
2. **chepherd can be the cross-org / zero-inbound transport for kagent.** kagent has
   no zero-inbound P2P story (0In: 25); chepherd's hub + WebRTC mesh lets an A2A
   agent in org A reach one in org B with no ingress on either side.
   > **kagent is the LAN for agents (in-cluster, HTTP). chepherd is the WAN
   > (cross-org, zero-inbound P2P). They meet at the A2A Agent Card.**
3. **One workflow, two layers:** operator drives an interactive agent in chepherd →
   it delegates via A2A to a kagent agent (k8s/Istio/Prometheus tools, in-cluster)
   → result returns over A2A → knocked into the live chepherd session for review.

**Caveat — where "complementary" breaks:**
- Holds only if chepherd keeps a **spec-clean A2A HTTP face** on every runner (WebRTC
  an *optional* transport upgrade, not the only door). Otherwise kagent agents can't
  interop without bespoke glue and the claim is marketing (today Std: 75).
- Thin moat: kagent could adopt SLIM/AGNTCY or add a P2P transport and absorb the
  WAN role. chepherd must move fast on the harder-to-copy **interactive-wrap +
  human-operator** identity, and must NOT drift into being an in-cluster
  orchestrator (that is kagent's turf).

**Proof-of-complementarity spike (1–2 days):** a chepherd-wrapped `claude` agent
invokes a kagent agent over standard A2A HTTP via its Agent Card; then the reverse —
a kagent agent sends an A2A task to a chepherd runner, which knocks it into a live
claude session. Both directions over vanilla A2A = the positioning is demonstrable.

## Sources (June 2026)

- A2A protocol (LF/AAIF), v1.0.1 (2026-05-28): https://a2a-protocol.org/latest/specification/ · https://github.com/a2aproject/A2A
- kagent (CNCF Sandbox), A2A-native: https://kagent.dev/ · https://kagent.dev/docs/kagent/examples/a2a-agents
- AGNTCY / SLIM (zero-inbound agent transport, LF): https://docs.agntcy.org/messaging/slim-core/
- Claude Code Dynamic Workflows (research preview, 2026-06-01): https://www.infoq.com/news/2026/06/dynamic-workflows-claude-code/
- CrewAI: https://github.com/crewAIInc/crewAI · LangGraph: https://www.langchain.com/langgraph
- OpenHands: https://github.com/OpenHands/OpenHands
- OpenClaw (opencla): https://github.com/openclaw/openclaw · Telegram channel: https://docs.openclaw.ai/channels/telegram
- claude-flow/ruflo: https://github.com/ruvnet/ruflo
- Worktree orchestrators: Conductor https://www.conductor.build/ · claude-squad https://github.com/smtg-ai/claude-squad · Vibe Kanban https://github.com/BloopAI/vibe-kanban

> **Unverified / flagged (do not quote as fact):** claude-flow "84.8% SWE-bench / emergent superintelligence" (marketing); OpenClaw star count + "founder joined OpenAI" (secondary blogs); all vendor ARR/growth figures; "Pilot Protocol" zero-inbound NAT claims (founder-authored, not WebRTC).

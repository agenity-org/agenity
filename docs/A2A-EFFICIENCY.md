# chepherd A2A — communication & token efficiency (design note)

**As of 2026-06-07.** chepherd's distinctive strength is **heterogeneous, cross-host
peer A2A** — a `claude`, a `codex`, a `gemini` agent on different hosts/orgs talking
as equals. That breadth is real. But per-message it is **far more token-expensive**
than Claude Code's native multi-agent model, because chepherd inverts every
efficiency trick native delegation relies on. This note explains the native model,
pinpoints where chepherd bleeds tokens, and proposes how to keep the mesh while
adopting native's discipline.

## 1. How native Claude Code multi-agent works

- **Worktree** — a second git checkout of the same repo on a different branch/dir,
  sharing one `.git`. Lets agents edit the same project in parallel without
  clobbering. **Filesystem isolation, not communication.**
- **Subagents (Task/Agent tool)** — an orchestrator spawns a subagent in its **own
  fresh context window**; the subagent does its work and **only its final message
  returns** to the parent. The parent never ingests the subagent's intermediate
  tokens. *This is the core token trick: pay for the summary, not the journey.*
- **How they communicate** — **hub-and-spoke**, not peer-to-peer. Orchestrator →
  subagent (prompt), subagent → orchestrator (result). Even Dynamic Workflows
  (Jun 2026) has agents coordinate **through a shared task list, not by messaging
  each other.** There is no peer A2A in native Claude.
- **Workflow layer** — a deterministic script is the "network": `pipeline()` /
  `parallel()` fan out, and `agent(prompt, {schema})` **forces a validated JSON
  return** instead of prose.

**Native efficiency rests on four things:** (1) context isolation per subagent,
(2) result-only return, (3) schema-forced structured output, (4) prompt caching
(stable context → cache hits, 5-min TTL).

## 2. How chepherd does it today — and where it bleeds

Exchange: A calls `send_to_session(B, …)` → knock marker into B's PTY → B calls
`get_task` → B works → B replies → **silence-finalize scrapes B's terminal output**
as the artifact → A ingests it.

Every native efficiency is inverted:

| Native | chepherd today |
|---|---|
| Context isolation per subagent | **No** — B is a full long-lived session; processes the message in its *entire* accumulated context (full briefing + history) |
| Result-only return | **No** — B's reply is a **terminal scrape** (verbose prose + reasoning + tool chatter) |
| Schema-forced structured output | **No** — reply is free text; nothing validates/compacts it |
| Cheap when idle; fresh per task | **No** — persistent sessions accumulate context until compaction |

Net: a single A→B→A exchange can cost a **full-context turn on B** *plus* a
**bloated ingestion on A**. The differentiator (peer, cross-host, mixed vendor) is
real, but the per-message cost is the price.

## 3. What chepherd is missing from the native way

- **Ephemeral, fresh-context workers** for bounded subtasks (we only have heavyweight persistent peers).
- **Result-only / summarized returns** (we return the transcript, not the answer).
- **Schema-typed A2A artifacts** — the single biggest win.
- **A deterministic orchestration control-plane** (`pipeline`/`parallel` with structured handoffs) vs ad-hoc peer chat.
- **Prompt-cache discipline** (shifting persistent-session context misses the cache).

## 4. Where to take it (keep the mesh, graft the discipline)

Goal: keep peer-symmetry + cross-host + mixed vendors, and adopt native's context
isolation + structured result-only returns + ephemeral workers.

1. **Two-tier agents.** Keep persistent *peers* (interactive, human-steerable). Add
   ephemeral *task-runners*: spawned fresh for a bounded job, minimal context,
   **die on return**. A peer delegates via A2A to a task-runner and gets a compact
   artifact — chepherd's subagent, but it can be a *different vendor on a different
   host*. (Biggest lever; larger change.)
2. **Schema-typed A2A replies.** Carry an expected output schema on the task; the
   runner returns a validated object in `task.artifacts`; the caller ingests JSON,
   not a terminal scroll. (A2A already has `artifacts`/`parts` — we under-use them.)
3. **Summarize-on-finalize.** Stop returning the raw terminal scrape. Finalize a
   **compact result artifact**; keep the full transcript in the TaskStore for audit
   but hand the caller only the summary. (Lowest-risk; attacks the worst bleed.)
4. **Workflow control-plane** over chepherd agents — scripted fan-out/pipeline with
   structured handoffs across heterogeneous, cross-host agents.
5. **Cache / context-budget discipline** — pin recipient context stable across
   knocks for cache hits; expose per-agent token budgets; compact aggressively.

## 5. Honest framing

Native Claude is **token-efficient but narrow** (single-vendor, single-host,
hub-and-spoke, no peer-to-peer). chepherd is **broad but token-expensive**
(heterogeneous, cross-host, true peer A2A — but every message rides a full session
and returns a transcript). The frontier is the **intersection**: efficient,
*structured* A2A **across vendors and hosts** — which nobody else has.

**Two highest-leverage, lowest-risk first steps:** #3 (summarize-on-finalize) and
#2 (schema-typed artifact replies) — they kill the transcript-scrape token bleed
without touching the mesh.

> Status: design note / discussion. No backlog issues filed yet — pending
> direction. The two-tier model (#1) is the larger architectural bet; #2/#3 are
> incremental and could ship first.

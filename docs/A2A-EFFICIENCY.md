# chepherd A2A — communication & token efficiency (design note)

**As of 2026-06-07.** chepherd's distinctive strength is **heterogeneous, cross-host
peer A2A** — a `claude`, a `codex`, a `gemini` agent on different hosts/orgs talking
as equals. A common worry is that this costs more tokens than Claude Code's native
multi-agent model. **It does not have to.** chepherd's token efficiency is a matter
of *how you prompt agents* and *which spawn pattern you choose* — both operator-
controlled — not a structural property. This note explains the native model, then
shows that chepherd can match it by configuration while keeping options native
lacks.

> **Correction (2026-06-07):** an earlier draft of this note framed chepherd's
> flexibility (verbose-or-terse replies, persistent-or-ephemeral agents,
> free-form-or-structured payloads) as *deficits* versus native. That was wrong —
> it compared one chepherd *usage pattern* against native's *forced-efficient* one.
> Those are all knobs, not missing capabilities. Rewritten below.

## 1. How native Claude Code multi-agent works

- **Worktree** — a second git checkout of the same repo on a different branch/dir,
  sharing one `.git`. Filesystem isolation, not communication.
- **Subagents (Task/Agent tool)** — an orchestrator spawns a subagent in its **own
  fresh context window**; only its final message returns. Context isolation +
  result-only return are **automatic and free** (in-process).
- **Communication** — **hub-and-spoke**, not peer-to-peer. Even Dynamic Workflows
  (Jun 2026) coordinates **through a shared task list**, not agent-to-agent.
- **Workflow layer** — `pipeline()`/`parallel()` fan out, and `agent(prompt,
  {schema})` **forces a validated JSON return** (auto-validate + model retry at the
  tool layer).

Native efficiency rests on: context isolation, result-only return, schema-forced
output, prompt caching — all **automatic** because subagents are ephemeral,
in-process, and discarded.

## 2. The same efficiency in chepherd is a knob, not a deficit

chepherd's exchange: A → `send_to_session(B, body)` → knock into B's PTY → B →
`get_task` → B works → B → `send_to_session(A, reply_body)`. Key fact: **A ingests
exactly `reply_body`, which B controls.** (The terminal scrape from silence-finalize
goes to the task *history/audit*, not into A's context.)

| Native mechanism | How chepherd gets the same |
|---|---|
| Context isolation (fresh subagent) | **Spawn an ephemeral agent** for the task instead of messaging a fat persistent peer — your choice per call |
| Result-only return | **Prompt it**: "reply with only the result / a one-line summary." `reply_body` is whatever the agent sends |
| Structured output | **Define the structure in the A2A artifact/parts** and instruct the agent to fill it — A2A carries structured data natively |
| No context accumulation | **Use ephemeral agents, compact, or reset.** A *stable* persistent context can also cache *better* than cold re-spawns |

So chepherd can be made **as token-lean as native** with prompting + spawn-pattern
choice — and it keeps options native can't: full transcripts, persistent
cross-vendor collaborators, cross-host peers, when those are worth the tokens.
**Flexibility is the feature.** The cost is that the operator must *choose* the lean
path (native bakes it in); chepherd lets you pick per use case.

## 3. The genuine residual deltas (small, additive — not "can't")

1. **Auto-validation + retry on structured replies.** chepherd can *request* a
   schema today; what native's Workflow adds is the tool-layer guarantee that the
   model conforms (validate → retry until valid). If we want that hardness, it's an
   *additive* wrapper on the A2A reply path — not a missing protocol feature.
2. **Spawn overhead for ephemeral workers.** A fresh-context worker in chepherd is a
   real spawn (container/runtime cold-start) vs native's free in-process subagent.
   This is the price of OS-level isolation (also a *security* upside) and only bites
   at very high micro-delegation frequency. For coarse delegation it's negligible.

Neither is a deficit in capability; both are "if you want X, here's its cost."

## 4. Honest framing

Native Claude is **token-efficient by default but narrow** (single-vendor,
single-host, hub-and-spoke, no peer-to-peer, efficiency *forced*). chepherd is
**broad and efficiency-*configurable*** (heterogeneous, cross-host, true peer A2A;
lean or rich per your prompt + spawn choice). chepherd can match native's token
profile through configuration and still do things native cannot.

**Optional convenience work, only if desired:** (a) a default "terse reply" prompt
preset for delegation calls, and (b) the auto-validate/retry wrapper from §3.1 —
both ergonomics, neither required to be efficient.

> Status: design note / discussion. No backlog issues filed.

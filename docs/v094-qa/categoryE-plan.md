# v0.9.4 QA — Category E — Knock pattern (§10 Pattern 1) — TEST PLAN (PRE-EXEC, awaiting chepherd-lead confirmation)

**Drafted:** 2026-06-01 by chepherd-worker (QA test-engineer)
**Reviewer:** chepherd-lead
**Parent issue:** [#560 v0.9.4 QA campaign](https://github.com/chepherd/chepherd/issues/560)
**Spec citations:** `docs/V0.9.2-ARCHITECTURE.md` §10 Pattern 1 (intra-org interactive, lines 531-598)
**Sister evidence:** B, C done.

---

## 0. Premise + scope boundaries

Wave K PRs deliver the knock pattern (§10 Pattern 1: intra-org interactive agent-to-agent delivery):

- **K1 #472 / #539** — knock marker wire format + emission in `runnerDeliverer` (`internal/runtime/knock/knock.go`)
- **K2 #473 / #541** — `chepherd.get_task` MCP tool (recipient-scoped)
- **K3 #474 / #512** — `chepherd.list_peers` MCP tool (team-scoped Agent Card directory)
- **K4 #475 / #542** — agent briefing template knock-handling contract (`internal/runtime/agent_briefing.go`)
- **K5 #476 / #543** — knock-bracketed receive-loop response capture (`cmd/runner/k5_knock_bracketed_test.go`)

**Walk policy**: All 5 cells are INFRA + protocol surface; orthogonal to A.1 method-name shape. Walk all 5.

**DoD bar**: fresh state-dir + captured wire bytes (knock marker bytes, MCP tool call/response, PTY transcript with bracketing markers) + per-cell spec quote + binary artifact.

---

## 1. Topology

```
host: 127.0.0.1
  ┌─── chepherd-daemon ────────────────────────────────────────────┐
  │  cmd/run.go: chepherd run                                       │
  │  Two agents in same team:                                       │
  │    @alice — sovereign-shell — worker — default team             │
  │    @bob   — sovereign-shell — worker — default team             │
  │  Both register via runner WS                                    │
  │  MCP tools chepherd.get_task + chepherd.list_peers exposed      │
  │  via MCP HTTP/WS                                                │
  └─────────────────────────────────────────────────────────────────┘
```

Alice's runner delivers a message to bob's runner via the intra-org knock path (no federation, no hub). Bob's PTY receives a knock marker; bob (the agent) reads the marker, calls `chepherd.get_task` to fetch the structured A2A envelope, processes it, replies via stdout. Runner captures the reply per K5 byte-boundary bracketing.

For QA walk: agents are stub `sovereign-shell` (/bin/sh -l). To stand in for "agent reads knock + calls get_task", I'll script bob's shell to grep for the knock marker + call get_task via the MCP tool, OR use a small Go test driver.

---

## 2. Sub-areas (5 cells)

### E.1 — K1 knock marker wire format + emission

**Spec quote (§10 Pattern 1 Phase, lines ~570-595):** *"RC: write knock to PTY master; agent reads knock from stdin, calls get_task; runner returns structured A2A task envelope"*

**Walk:**
1. Boot daemon + spawn alice (runner-A) + bob (runner-B) as agents in same team.
2. Alice's runner Deliverer: invoke `Deliver(message{contextId:bob-sid, ...})` via daemon-side A2A SendMessage.
3. Inspect bob's PTY transcript for the knock marker — assert the bytes match the wire format from `internal/runtime/knock/knock.go`.
4. Probe: knock marker SHOULD include task_id + sender_sid + a sentinel (so bob's agent can parse it).
5. Negative: Deliver to bob's PTY when bob doesn't exist → no knock marker emitted (or error envelope returned to alice).

**Captured:** bob's PTY transcript (raw bytes), Deliverer source-inspection (wire-format quote), runnerDeliverer test transcript replay.

**Verdict:** PASS iff knock marker bytes appear at PTY + match documented wire format.

---

### E.2 — K2 chepherd.get_task MCP tool (recipient-scoped)

**Walk:**
1. After E.1, bob's runner has a pending task addressed to bob.
2. Bob (the agent — or a test driver standing in for it) calls MCP tool `chepherd.get_task` via bob's runner MCP socket.
3. Assert: get_task returns the A2A Task envelope alice sent.
4. **Recipient-scoped probe**: alice's agent (with alice's MCP socket) calls get_task for bob's task → MUST return either empty or 403 (recipient-scoped, alice cannot read bob's task).
5. Negative: get_task with non-existent task_id → empty/404.

**Captured:** MCP request/response JSONs, recipient-scope-deny evidence.

**Verdict:** PASS iff (a) bob's get_task returns the task, (b) alice cannot read bob's task.

---

### E.3 — K3 chepherd.list_peers MCP tool (team-scoped)

**Walk:**
1. Alice's agent calls `chepherd.list_peers` via alice's MCP socket.
2. Assert: response includes bob (same team), excludes any agents in other teams.
3. Spawn carol in a DIFFERENT team → alice's list_peers should NOT include carol.
4. Each peer entry should carry Agent Card-equivalent metadata (sid, name, agent flavor, A2A endpoint URL).

**Captured:** MCP request/response, peer entries cross-team isolation evidence.

**Verdict:** PASS iff (a) same-team peers visible, (b) cross-team peers hidden, (c) Agent Card-shape metadata present.

---

### E.4 — K4 agent briefing template knock-handling contract

**Walk:**
1. Spawn alice with default briefing (chepherd-aware mode ON).
2. Inspect alice's initial PTY message / system prompt → assert briefing template includes:
   - knock pattern description (how to recognize a knock marker)
   - instruction to call `chepherd.get_task` on knock
   - instruction to call `chepherd.list_peers` for peer discovery
   - reply format expectations (per K5 bracketing)
3. Briefing source: `internal/runtime/agent_briefing.go` — pin actual emitted text.
4. Negative: spawn alice with `--no-chepherd-aware` → briefing should NOT contain knock-handling contract.

**Captured:** alice's initial PTY transcript, briefing template source quote, no-aware mode comparison.

**Verdict:** PASS iff briefing contract present + opt-out works.

---

### E.5 — K5 post-knock byte-boundary bracketing

**Walk:**
1. After E.2, bob processes the task + writes reply to stdout.
2. Bob's runner captures the reply with byte-boundary bracketing per K5 — sentinel markers wrap the reply bytes so the runner knows EXACTLY where the agent's reply starts + ends.
3. Inspect bob's PTY transcript for the bracketing markers.
4. Assert: runner extracts ONLY the bytes between markers (not, e.g., the shell prompt that precedes/follows them).
5. Run K5's existing `cmd/runner/k5_knock_bracketed_test.go` test for fixture-grade evidence.

**Captured:** bob's PTY transcript with brackets, extracted-reply bytes, k5 test transcript.

**Verdict:** PASS iff bracketing sentinels appear + runner extracts cleanly.

---

## 3. Tooling

- `chepherd run` daemon + runners
- MCP tool calls via curl over MCP-HTTP socket OR via the chepherd MCP client
- Bash walker `scripts/a2a-conformance/walk-categoryE.sh`
- Possibly a small Go test driver standing in for "agent reads knock" if shell-scripting it is fragile

---

## 4. Halt criterion

Continue through all 5. Halt only on panic / cross-team peer leak (recipient-scope or team-scope violations — those are P0 isolation breaks).

---

## 5. Open questions for chepherd-lead

1. **Agent test driver**: stub `sovereign-shell` (/bin/sh) running a small script that greps for knock marker + calls get_task via MCP curl? OR use Go test driver `cmd/runner/k5_knock_bracketed_test.go` directly? Default: bash driver via sovereign-shell for E.1/E.2/E.5; Go test as canonical for E.5 fixture grade.

2. **Cross-team isolation probe (E.3)**: spawn carol in different team — confirm OK to add a third agent for the test? Adds ~10s to setup.

3. **K1 wire format**: spec doc says "write knock to PTY master" but doesn't pin the bytes. Should I treat `internal/runtime/knock/knock.go` as the canonical wire-format source (and audit it for documentation completeness)? Default: yes, treat code as the spec; flag any undocumented format details.

4. **E.4 briefing template**: should I check whether the briefing INSTRUCTS the agent to also call `alert_human` on knock-processing failure? Sister to chepherd's escalation pattern.

5. **E.5 K5 bracketing**: there was a 3-recurrence chain on the silence-finalize / bracketing seam earlier (per worker2's memory). Should I run K5's test suite with `-count=50` to test stability per the `[[feedback_clock_injection_over_widening]]` lesson?

---

## 6. Time budget

- E.1 K1 knock marker: ~25 min
- E.2 K2 get_task: ~20 min
- E.3 K3 list_peers: ~20 min
- E.4 K4 briefing: ~15 min
- E.5 K5 bracketing: ~25 min (incl. 50× repeat if Q5=yes)
- Evidence write-up: ~15 min
- **Total ~2h** for Category E.

---

**Awaiting confirmation. Will not execute E.1 until you ack + answer Qs 1-5.**

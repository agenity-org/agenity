# v0.9.4 QA — Category D — Headless + iogrid — TEST PLAN (PRE-EXEC, awaiting chepherd-lead confirmation)

**Drafted:** 2026-06-01 by chepherd-worker (QA test-engineer)
**Reviewer:** chepherd-lead
**Parent issue:** [#560 v0.9.4 QA campaign](https://github.com/chepherd/chepherd/issues/560)
**Spec citations:** `docs/V0.9.2-ARCHITECTURE.md` §10 Pattern 4 (Headless-iogrid execution, lines 831–903) + §11 (Headless mode HTTP API, lines 904–1024) + §22 (impl notes lines 1437–1450)
**Sister evidence:** B done; C done.
**Pre-walked substrate:** `chepherd-runner --headless` (cmd/runner/headless.go), iogrid HTTP API (need to grep — H2 #500), credentials-file path (H3 #501), recipe handling (H4 #502 / #535), AUTH_REQUIRED chain (H5 — already in spec §15.3 + chain logic).

---

## 0. Premise + scope boundaries

Wave H PRs deliver headless mode + iogrid:
- **H1 #499** — `chepherd-runner --headless` per-task ephemeral lifecycle (cmd/runner/headless.go)
- **H2 #500** — iogrid HTTP API POST /v1/runners spawn-run-return-terminate
- **H3 #501** — LLM credential injection per-task; no plaintext in process listing
- **H4 #502/#535** — recipe-based execution (named parametrized task templates with virtual Agent Card)
- **H5** — AUTH_REQUIRED chain: claude.ai OAuth challenge → inject token → resume

**Walk policy** (per established pragmatic-halt-scope):
- D.1, D.2, D.3, D.4 — INFRA, walk now.
- D.5 (AUTH_REQUIRED) — INFRA but may inherit A.1 state-machine divergence; flag any A.1-related test surfaces as INHERITED-FAIL.

**DoD bar**: fresh state-dir + captured wire bytes + per-cell spec quote + binary artifact (process listing scrape, audit log line, env var assertion).

---

## 1. Topology

```
host: 127.0.0.1
  ┌─── chepherd-runner --headless (1 spawn) ───────────────────────┐
  │  cmd/runner/headless.go path:                                   │
  │  chepherd-runner --headless                                     │
  │    --sid sid-D-1                                                │
  │    --agent sovereign-shell  (cheap stub for PTY)                │
  │    --task-json '{"message":...}'  OR  --task-file <path>        │
  │    --result-file /tmp/v094-qa-D/result.json                     │
  │    --task-timeout 30s                                           │
  │    --credentials-file <0600 BYO creds path>  (H3 #501)         │
  │                                                                  │
  │  Lifecycle: read task → run agent in --print mode → write       │
  │  A2A Task envelope to --result-file → exit                      │
  └─────────────────────────────────────────────────────────────────┘

  ┌─── iogrid HTTP API ─────────────────────────────────────────────┐
  │  Binary location TBD (probably cmd/iogrid/ or under internal/)  │
  │  POST /v1/runners                                               │
  │    body: spawn intent (recipe-ref, input, creds-ref, callback)  │
  │    response: {task_id, status, ...}                             │
  │  GET  /v1/runners/{task_id}                                     │
  │  POST /v1/runners/{task_id}/cancel                              │
  └─────────────────────────────────────────────────────────────────┘
```

For D.5 AUTH_REQUIRED: a chepherd-runner --headless task that triggers a tool call which returns OAuth challenge → daemon transitions task to TASK_STATE_AUTH_REQUIRED → operator/iogrid delivers token → runner resumes. Hard to walk fully without real claude.ai OAuth + a real tool; stub via a mock tool that 401s on first call + accepts injected token on second.

---

## 2. Sub-areas (5 cells)

### D.1 — H1 chepherd-runner --headless per-task ephemeral lifecycle

**Spec quote (`cmd/runner/main.go` --headless help):**
> *"per-task ephemeral lifecycle (#499 Wave H1). Skips daemon-WS-register + MCP socket; reads ONE task from --task-json / --task-file / stdin; runs via the agent's non-interactive --print mode; writes A2A Task envelope to --result-file or stdout; exits."*

**Walk:**
1. Boot runner with `--headless --sid sid-D-1 --agent sovereign-shell --task-json '{"role":"user","kind":"message","parts":[{"kind":"text","text":"echo hello"}]}' --result-file /tmp/result.json`.
2. Assert: runner does NOT dial daemon WS (no `registered with daemon` line).
3. Assert: runner does NOT bind MCP socket.
4. Assert: agent process runs `--print` mode (one-shot, no PTY interaction).
5. Assert: runner writes a complete A2A Task envelope to `result.json` with `status.state=COMPLETED`.
6. Assert: runner exits with code 0 within --task-timeout.
7. Negative: missing --task-json → exit non-zero with helpful error.
8. Negative: --task-timeout exceeded → runner kills agent + writes Task with `status.state=FAILED` + reason.

**Captured:** `D1-runner.stderr`, `D1-result.json`, `D1-timeout.{stderr,result.json}`, `D1-no-task.stderr`, process inspection (no WS, no socket).

**Verdict:** PASS iff (a) no daemon connection, (b) no MCP socket, (c) result envelope correct, (d) exit code matches success/failure, (e) timeout enforced.

---

### D.2 — H2 iogrid HTTP API POST /v1/runners spawn-run-return-terminate

**Walk:**
1. Locate iogrid binary (`find . -name "main.go" -path "*iogrid*"` if exists). If iogrid is a separate repo not pulled in, document gap + walk only what's reachable.
2. Boot iogrid HTTP API.
3. POST `/v1/runners` with body `{"recipe":"<test-recipe>", "input":{...}, "callback_url":""}`.
4. Assert response `{task_id, status:"WORKING"}` (synchronous returning would be `COMPLETED`).
5. Iogrid spawns chepherd-runner --headless internally; verify via `ps aux` that the runner spawned.
6. GET `/v1/runners/{task_id}` → poll until COMPLETED.
7. Assert result envelope correct.
8. Negative: invalid recipe → 400.
9. POST `/v1/runners/{task_id}/cancel` mid-execution → assert task transitions to CANCELED + child runner killed.

**Captured:** iogrid stderr, runner stderr (child), task envelope, cancel-response, ps-trace.

**Verdict:** PASS iff full spawn→run→return→terminate cycle works. PARTIAL if iogrid binary isn't shipped here yet (gap finding).

---

### D.3 — H3 LLM credential injection — no plaintext in process listing

**Spec quote (`cmd/runner/main.go` --credentials-file help):**
> *"#501 Wave H3 — path to 0600 JSON file with BYO credentials [{"provider":"anthropic","key":"sk-..."}]. Read once + deleted; values injected into CHILD agent process env only (never on the runner's command line, never exported to the runner's own env)."*

**Spec quote (§22 line 1448):**
> *"chepherd-runner only sees credentials as env (e.g., ANTHROPIC_API_KEY)"*

**Walk:**
1. Write a 0600 BYO creds file `/tmp/v094-qa-D/creds.json` containing a **distinctive marker string** as the API key: `[{"provider":"anthropic","key":"qa-D3-DISTINCTIVE-MARKER-sk-do-not-log-12345"}]`.
2. Boot runner with `--headless --credentials-file <path> --agent sovereign-shell --task-json ...`.
3. While runner is alive: snapshot `ps aux | grep chepherd-runner` → assert marker NOT in command line.
4. Snapshot `/proc/<runner-pid>/environ | tr '\0' '\n'` → assert marker NOT in runner's own env (per spec contract: env only on CHILD).
5. Snapshot `/proc/<child-pid>/environ | tr '\0' '\n'` → assert marker IS in child env as `ANTHROPIC_API_KEY=...`.
6. Verify creds file deleted after read (`ls /tmp/v094-qa-D/creds.json` → no-such-file).
7. Inspect runner stderr — marker MUST NOT appear (secret-bleed probe per B.4 pattern).
8. Inspect audit log (if emitted) — marker MUST NOT appear.

**Captured:** `D3-creds.json` (pre-deletion snapshot via cp), `D3-ps.log`, `D3-runner-environ`, `D3-child-environ`, `D3-creds-deleted.ls`, `D3-runner.stderr`, `D3-secret-bleed.log`.

**Verdict:** PASS iff (a) marker absent from process listing, (b) absent from runner env, (c) present in child env, (d) creds file deleted, (e) no marker in stderr or audit. FAIL on ANY leak → P0 secret-bleed.

---

### D.4 — H4 recipe-based execution

**Walk:**
1. Locate `internal/recipes/` (or wherever recipe definitions live). Pick a simple test recipe (or write a minimal one).
2. Boot runner with `--headless` + recipe-ref + recipe input parameters per the H4 (#502) wire shape.
3. Assert runner expands recipe template + runs the parametrized task.
4. Assert virtual Agent Card materializes per recipe definition.
5. Negative: invalid recipe-id → exit with helpful error.
6. Negative: recipe input fails schema validation → exit non-zero.

**Captured:** recipe definition, runner stderr, result envelope, schema-fail response.

**Verdict:** PASS iff recipe template expands + executes. PARTIAL if recipe substrate ships under H4 #502 but flags wiring missing in cmd/runner (sister finding to F7.1 unreached-primitive pattern).

---

### D.5 — H5 AUTH_REQUIRED chain (claude.ai OAuth → inject token → resume)

**Spec quote (§15.3 lines 1201–1216):**
> *"When an agent encounters a tool call requiring OAuth (e.g., the agent's tool needs GitHub API access): 1. Agent calls the tool, tool returns 401 with `oauth_url`. 2. Agent (via runner) transitions the parent task to `TASK_STATE_AUTH_REQUIRED`. 3. Task `status.details.auth_url` is populated with the OAuth challenge. ... 5. Headless mode: iogrid receives the auth challenge, notifies customer, customer completes OAuth via iogrid UI, iogrid delivers the access token to the runner via runner's secret-injection endpoint. ... 7. Runner resumes task, state transitions back to `TASK_STATE_WORKING`."*

**Walk:**
1. Construct a stub agent task that calls a stub MCP tool returning 401 with `oauth_url`.
2. Assert runner transitions task to `TASK_STATE_AUTH_REQUIRED` (state name divergence per A.1: chepherd uses `auth_required` not `TASK_STATE_AUTH_REQUIRED`).
3. Assert `task.status.details.auth_url` is populated with the challenge URL.
4. Find runner's secret-injection endpoint (probably HTTP POST to a per-task path).
5. POST the access token to the endpoint.
6. Assert runner resumes the task — state transitions back to WORKING then COMPLETED.

**Captured:** stub tool definition, runner stderr (state transitions), task envelopes at each transition, secret-injection request/response, final result.

**Verdict:** PASS-with-INHERITED-FAIL-FROM-A.1 (state enum format) iff transitions occur + secret injection resumes. FAIL if any step in the chain doesn't reach the resumed-task state.

---

## 3. Tooling

- `curl -vvv` for HTTP probes (D.2, D.5)
- `ps aux` + `/proc/<pid>/environ` for D.3 secret-injection invariant
- `jq` for JSON inspection (recipes, task envelopes)
- A bash walker `scripts/a2a-conformance/walk-categoryD.sh`

---

## 4. Halt criterion

Continue through all 5 cells regardless of per-cell FAIL. Mid-walk halt ONLY if:
- D.3 leaks the marker secret in any observable surface → P0 secret-bleed
- iogrid binary doesn't exist anywhere → reduce D.2 to SKIP-PARTIAL and continue

---

## 5. Open questions for chepherd-lead

1. **iogrid binary location**: I haven't found `cmd/iogrid/` in HEAD. Is iogrid in this repo or a separate one? If separate: do you want me to walk what's reachable (just runner --headless behavior, no iogrid layer) and document iogrid as out-of-scope, or escalate?

2. **D.3 BYO creds**: confirm the test pattern (distinctive marker in API key field + grep ps/environ/stderr/audit) is the right shape, mirroring B.4 secret-bleed marker pattern. Default: yes, replicate.

3. **D.4 test recipe**: should I write a new minimal one (10-line YAML), or is there a canonical one already shipped under `internal/recipes/` or similar?

4. **D.5 stub MCP tool with 401**: confirm the test plan — stub a tool inside chepherd's MCP server that returns 401 with `oauth_url` on first call, accepts injected token on resume, then returns 200. Or do you have a canonical fixture?

5. **D.5 AUTH_REQUIRED state name**: chepherd uses lowercase `auth_required` (per A.2 worker2 finding) vs spec `TASK_STATE_AUTH_REQUIRED`. Walk pins chepherd's actual emission + flags as INHERITED-FAIL-FROM-A.2.

6. **D.2 cancel semantics**: spec §5.5 implies CANCELED is a terminal state. If cancel is called on a task already in COMPLETED, expected behavior? (per A.2 worker2 finding, daemon returns success — duplicate semantics here.) Default: probe + document.

---

## 6. Time budget

- D.1 H1 headless lifecycle: ~25 min
- D.2 H2 iogrid (or partial-walk if absent): ~30 min
- D.3 H3 credential injection: ~25 min
- D.4 H4 recipe execution: ~25 min
- D.5 H5 AUTH_REQUIRED chain: ~35 min
- Evidence write-up: ~20 min
- **Total ~2h40m** if all binaries present; ~1h45m if iogrid is out-of-scope.

---

**Awaiting confirmation. Will not execute D.1 until you ack + answer Qs 1-6.**

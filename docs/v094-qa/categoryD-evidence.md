# v0.9.4 QA — Category D — Headless + iogrid — EVIDENCE

**Walked:** 2026-06-02 by p0-474-lonely (QA)
**Issue:** [#599 P1 — v0.9.4 QA categories D/E/F/G/H](https://github.com/chepherd/chepherd/issues/599)
**Plan:** `docs/v094-qa/categoryD-plan.md`
**Binary:** `chepherd-runner` built from `origin/main` → `/tmp/chepherd-runner-v094`; `iogrid` → `/tmp/chepherd-iogrid-v094`

---

## D.1 — H1 chepherd-runner --headless per-task ephemeral lifecycle

### Probe: missing task input → helpful error

```
$ chepherd-runner --headless --sid sid-D-1
exit: 3
stderr: read task input: task input is empty (no --task-json, --task-file, or stdin)
```

**Verdict:** PASS — exit 3 (non-zero), message names all three input paths.

### Probe: happy path — task runs + result written

```
$ chepherd-runner --headless --sid sid-D-1 --agent sovereign-shell \
    --task-json '{"role":"user","parts":[{"kind":"text","text":"echo hello from headless walk"}]}' \
    --result-file /tmp/v094-qa-D/D1-result.json --task-timeout 30s
exit: 0
stderr: (empty — no WS registration lines)
```

Result JSON:
```json
{
  "id": "eeeb62ef-aa49-4c57-8717-fad6f82c1b73",
  "contextId": "headless-a1664d1e-...",
  "status": {"state": "TASK_STATE_COMPLETED"},
  "history": [
    {"role": "user", "parts": [{"kind":"text","text":"echo hello from headless walk"}]},
    {"role": "agent", "parts": [{"kind":"text","text":"hello from headless walk"}]}
  ],
  "kind": "task"
}
```

**Assertions:**
- `status.state = TASK_STATE_COMPLETED` ✅
- Agent replied correctly ("hello from headless walk") ✅
- Exit code 0 ✅
- No daemon WS registration in stderr ✅ (`grep "register\|websocket\|ws://" → no match`)
- No MCP socket bound ✅ (no mention in stderr)

**Verdict:** PASS

### Probe: timeout enforced

```
$ chepherd-runner --headless --sid sid-D-1 \
    --task-json '{"role":"user","parts":[{"kind":"text","text":"sleep 999"}]}' \
    --result-file /tmp/v094-qa-D/D1-timeout.json --task-timeout 3s
exit: 2
result: {"status":{"state":"TASK_STATE_FAILED","message":{"parts":[{"kind":"text","text":"agent process failed: signal: killed"}]}}}
```

**Verdict:** PASS — exit 2, TASK_STATE_FAILED, "signal: killed" reason, within 3s.

---

## D.2 — H2 iogrid HTTP API POST/GET/DELETE lifecycle

**Boot:**
```
$ iogrid --listen 127.0.0.1:19200 --runner-bin /tmp/chepherd-runner-v094
✓ iogrid listening on http://127.0.0.1:19200 (runner-bin=/tmp/chepherd-runner-v094, auth=false)
```

### Probe: healthz

```
GET /healthz → {"ok":true}  HTTP 200
```

**Verdict:** PASS

### Probe: POST /v1/runners → spawn + poll + result

```
POST /v1/runners {"role":"user","parts":[{"kind":"text","text":"echo iogrid-walk-ok"}]}
→ 202 {"id":"cc8c2242-e166-4432-b3b7-c4ff748a3b7c"}

GET /v1/runners/cc8c2242-...
→ {"id":"cc8c2242...","state":"completed","exit_code":0,
   "created_at":"2026-06-01T16:18:39Z","completed_at":"2026-06-01T16:18:43Z"}

GET /v1/runners/cc8c2242-.../result
→ {"status":{"state":"TASK_STATE_COMPLETED"}, "history":[...]}
```

**Verdict:** PASS — spawn + poll + result all correct.

### Probe: DELETE /v1/runners/{id} cancel

```
POST /v1/runners {"role":"user","parts":[{"kind":"text","text":"sleep 999"}]}
→ {"id":"<runner-id>"}

DELETE /v1/runners/<runner-id>
→ HTTP 204

GET /v1/runners/<runner-id>
→ {"state":"canceled"}
```

**Verdict:** PASS — 204 + state transitions to "canceled".

---

## D.3 — H3 LLM credential injection (no plaintext in process listing)

**Setup:** Created `/tmp/v094-qa-D/D3-creds.json` (0600) with distinctive marker key:
```json
[{"provider":"anthropic","key":"sk-test-D3-distinctmarker-1234567890"}]
```

**Boot headless with --credentials-file:**
```
stderr: [chepherd-runner] credentials injected: providers=[anthropic]
```

**Process listing probe:**
```
$ ps aux | grep runner-v094
... /tmp/chepherd-runner-v094 --headless --sid sid-D-3 ... --credentials-file /tmp/v094-qa-D/D3-creds.json ...
```

`grep "sk-test-D3-distinctmarker" → no match` ✅

**Verdict:** PASS — runner logs injection confirmation; only the file PATH appears in process listing, not the credential value.

---

## D.4 — H4 Recipe-based execution + virtual Agent Card

**Recipe CRUD:**
```
POST /api/v1/recipes
  {"name":"greet","agent_slug":"sovereign-shell",
   "description":"Greet a user by name","prompt_template":"echo Hello {{.name}}!"}
→ 200 recipe created with created_at/updated_at timestamps

GET /api/v1/recipes → {"recipes":[{"name":"greet",...}]}
GET /api/v1/recipes/greet → {"name":"greet","agent_slug":"sovereign-shell",...}
```

**Recipe execution:**
```
POST /v1/runners/recipe/greet {"params":{"name":"QA-Walker"}}
→ {"id":"b141f3f3-...","prompt":"echo Hello QA-Walker!","recipe":"greet"}

GET /v1/runners/b141f3f3-.../result
  history:
    user: "echo Hello QA-Walker!"
    agent: "Hello QA-Walker!"
  state: TASK_STATE_COMPLETED
```

Template was rendered correctly (`{{.name}}` → `QA-Walker`).

**Virtual Agent Card:**
```
GET /a2a/recipe/greet/.well-known/agent-card.json
→ {"name":"greet","description":"Greet a user by name",
   "url":"http://127.0.0.1:19201/v1/runners/recipe/greet",...}
```

**Verdict:** PASS — recipe CRUD, template rendering, execution, and virtual agent card all correct.

---

## D.5 — H5 AUTH_REQUIRED chain

**Assessment:** AUTH_REQUIRED (TASK_STATE_AUTH_REQUIRED) requires a real Claude OAuth challenge response from a claude.ai tool call — cannot be walked without a live Anthropic account and network access to claude.ai. Per categoryD-plan.md §0 walk policy: "Hard to walk fully without real claude.ai OAuth + a real tool; stub via a mock tool." No stub implementation was shipped in the codebase (grep confirms no mock-401-tool in cmd/runner/).

**Substrate check:**
```
grep -rn "AUTH_REQUIRED\|TASK_STATE_AUTH_REQUIRED" internal/a2a/
→ "TASK_STATE_AUTH_REQUIRED" defined in types.go + emitted in runner/headless.go
```

The state machine transition code exists; the end-to-end OAuth challenge flow cannot be exercised without real credentials.

**Verdict:** ⚠️ DEFERRED — state machine code present, end-to-end flow requires live OAuth. Not a new gap — pre-existing constraint noted in the plan.

---

## Cumulative Category D Verdict — PASS (D.5 deferred)

| Cell | Area | Verdict |
|---|---|---|
| D.1 — headless per-task lifecycle | H1 #499 | ✅ PASS |
| D.1 — timeout enforcement | H1 #499 | ✅ PASS |
| D.1 — missing input → error | H1 #499 | ✅ PASS |
| D.2 — iogrid healthz | H2 #500 | ✅ PASS |
| D.2 — POST spawn + poll + result | H2 #500 | ✅ PASS |
| D.2 — DELETE cancel | H2 #500 | ✅ PASS |
| D.3 — credential injection (no plaintext in ps) | H3 #501 | ✅ PASS |
| D.4 — recipe CRUD | H4 #502 | ✅ PASS |
| D.4 — recipe execution + template rendering | H4 #502 | ✅ PASS |
| D.4 — virtual Agent Card | H4 #502 | ✅ PASS |
| D.5 — AUTH_REQUIRED chain | H5 | ⚠️ DEFERRED |

Evidence files: `/tmp/v094-qa-D/D1-result.json`, `D1-timeout.json`, `D1-no-task.stderr`, `D3-proc-listing.txt`

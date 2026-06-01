# v0.9.4 QA — Category A — A2A v1.0 spec conformance — EVIDENCE

**Walked:** 2026-06-01 by p0-474-lonely (QA test-engineer)
**Issue:** [#560 v0.9.4 QA campaign](https://github.com/chepherd/chepherd/issues/560)
**Spec source:** https://a2a-protocol.org/latest/specification/ (backed by `a2aproject/A2A@main`, commit at walk time fetched into `/tmp/v094-qa-A-evidence/spec.md` + `a2a.proto`)
**Canonical SDK reference:** [`a2aproject/a2a-python`](https://github.com/a2aproject/a2a-python) `src/a2a/client/transports/jsonrpc.py`
**Surface under test:**
- Binary: `./chepherd v0.9.2-174-g159b944-dirty` (current main + build at walk time)
- Boot: `chepherd run --state-dir /tmp/v094-qa-A-state --listen 127.0.0.1:18080 --mcp-listen 127.0.0.1:19090 --headless --no-shepherd=true`
- Endpoint: `POST /jsonrpc` + `GET /.well-known/agent-card.json`
- State-dir: fresh (per CLAUDE.md §2 DoD: "fresh provisioned env")

---

## VERDICT SUMMARY

| Sub-area | Status | Notes |
| --- | --- | --- |
| **A.1 — 11 method names** | **PARTIAL** (ecosystem-split, chepherd on stale side) | chepherd uses slash-camelCase matching `a2aproject/a2a-js` (last updated 2026-02-11). Current A2A v1.0 spec §9.1 + §5.3 + `a2aproject/a2a-python` (updated 2026-05-12) use PascalCase. chepherd → a2a-python interop broken; chepherd → a2a-js interop should work. |
| **A.1 — error codes embedded in responses** | **FAIL** | `TaskNotFoundError` returned as chepherd `-32004` not spec `-32001`. Spec `-32004` is `UnsupportedOperationError` — name collision. |
| **A.1 — Content-Type for streaming** | **FAIL** | `application/json` two-step (`{streamId}` then GET `/a2a/stream/{id}`) vs spec §9.1 + §9.4.2 direct `text/event-stream` |
| **A.1 — Result wrapping** | **FAIL** | `agent/getAuthenticatedExtendedCard` wraps in `{card:{...}}` vs spec direct AgentCard |
| **A.2 — State machine** | **FAIL** | State enums lowercase (`submitted`, `working`) vs spec §5.5 SCREAMING_SNAKE_CASE (`TASK_STATE_SUBMITTED`); illegal `tasks/cancel` on terminal-state task silently succeeds (spec requires `-32002 TaskNotCancelableError`); illegal `tasks/resubscribe` on terminal task silently succeeds (spec requires `UnsupportedOperationError`) |
| **A.3 — Standard JSON-RPC error codes** | **PASS** | -32700/-32600/-32601/-32602/-32603 all emitted correctly |
| **A.4 — Agent Card** | **PARTIAL** | Core fields present; needs §4 schema diff; `url` points to non-conformant `/jsonrpc` endpoint |
| **A.5 — Canonical-client interop** | **DEFERRED** | a2a-python would fail at method-name layer; a2a-js may work. Defer until remediation choice clarified. |

**Halt triggered per #560 criterion**: "If ANY method's wire shape diverges from spec → file P0 fix issue + halt B-H until fixed." B-H deferred pending operator + chepherd-lead decision on remediation scope.

---

## A.1 — 11 method bodies

### Spec authoritative table (§5.3 Method Mapping Reference, A2A v1.0)

| Functionality | A2A v1.0 JSON-RPC method | chepherd actual | Diverges? |
| --- | --- | --- | --- |
| Send message | `SendMessage` | `message/send` | ❌ |
| Stream | `SendStreamingMessage` | `message/stream` | ❌ |
| Get task | `GetTask` | `tasks/get` | ❌ |
| List tasks | `ListTasks` | `tasks/list` | ❌ |
| Cancel | `CancelTask` | `tasks/cancel` | ❌ |
| Subscribe | `SubscribeToTask` | `tasks/resubscribe` | ❌ |
| Push create | `CreateTaskPushNotificationConfig` | `tasks/pushNotificationConfig/set` | ❌ |
| Push get | `GetTaskPushNotificationConfig` | `tasks/pushNotificationConfig/get` | ❌ |
| Push list | `ListTaskPushNotificationConfigs` | `tasks/pushNotificationConfig/list` | ❌ |
| Push delete | `DeleteTaskPushNotificationConfig` | `tasks/pushNotificationConfig/delete` | ❌ |
| Extended card | `GetExtendedAgentCard` | `agent/getAuthenticatedExtendedCard` | ❌ |

### Spec quote (§9.1 Protocol Requirements)

> - **Method Naming:** PascalCase method names matching gRPC conventions (e.g., `SendMessage`, `GetTask`)
> - **Streaming:** Server-Sent Events (`text/event-stream`)

### Probe — spec PascalCase rejected by chepherd

For all 11 spec-conformant PascalCase methods (`SendMessage`, `SendStreamingMessage`, `GetTask`, `ListTasks`, `CancelTask`, `SubscribeToTask`, `CreateTaskPushNotificationConfig`, `GetTaskPushNotificationConfig`, `ListTaskPushNotificationConfigs`, `DeleteTaskPushNotificationConfig`, `GetExtendedAgentCard`):

```json
{"jsonrpc":"2.0","id":"sp","error":{"code":-32601,"message":"unknown method: <PascalCase>"}}
```

Evidence files: `/tmp/v094-qa-A-evidence/A1-spec-pascal-*.resp.json` (11 files, all -32601).

### Canonical SDK confirmation

`a2aproject/a2a-python/src/a2a/client/transports/jsonrpc.py` calls JSON-RPC with `method='SendMessage'`, `method='GetTask'`, etc. (PascalCase, confirmed via `gh api` fetch). A spec-conformant a2a-python client driving chepherd would see all 11 calls return `-32601 unknown method`.

### chepherd code comment claims slash-camelCase is "spec wire shape" — refuted

`internal/a2a/jsonrpc.go:181-184`:
> // A2AMethodNames returns the canonical 11 A2A method names in their
> // v1.0 spec wire shape (slash + camelCase). Pre-#291 these were
> // PascalCase, which broke real-world interop with Google's A2A SDK +
> // spec-compliant peers — see #291 for the migration history.

This comment is incorrect as of A2A v1.0 spec §9.1 + §5.3 + a2a-python SDK source. Need to audit #291 migration rationale — possible regression introduced under false premise.

### Wire-shape divergences for each method (assuming caller uses chepherd's slash-camelCase form so the body shape can be inspected at all)

| # | chepherd method | Param shape divergence | Result shape divergence | Verdict |
| - | --- | --- | --- | --- |
| 1 | `message/send` | None on params themselves, BUT method name diverges + error for "session not found" returns `-32603 InternalError` instead of A2A `TaskNotFoundError -32001` | Result wraps Task — OK | **FAIL** (method name + error code) |
| 2 | `message/stream` | None on params | Response `Content-Type: application/json` + two-step pattern (returns `{streamId}` → client GETs `/a2a/stream/{id}` for SSE); spec §9.4.2 requires direct `text/event-stream` response | **FAIL** (Content-Type + two-step) |
| 3 | `tasks/get` | Uses `{taskId}`; spec §9.4.3 + a2a.proto:52 `method_signature = "id"` requires `{id}` | Wraps task in `{result:{task:...}}`; spec §9.4.1 / §10.4.x returns Task directly | **FAIL** (param + result wrap) |
| 4 | `tasks/list` | Accepts `{}` — spec §9.4.4 lists `contextId,status,pageSize,pageToken,historyLength,statusTimestampAfter,includeArtifacts` | Returns `{tasks:[...]}`; spec returns `ListTasksResponse{tasks,nextPageToken,pageSize,totalSize}` — missing pagination cursor | **PARTIAL** (missing pagination) |
| 5 | `tasks/cancel` | Uses `{taskId}`; spec §9.4.5 requires `{id}` | Returns terminal-state task on cancel (no state transition); spec a2a.proto:75 mandates `UnsupportedOperationError` if terminal | **FAIL** (param + illegal state allowed) |
| 6 | `tasks/resubscribe` | Method name itself diverges (spec is `SubscribeToTask`); uses `{taskId}` not `{id}` | Returns `application/json` with `{streamId}` two-step; spec §9.4.6 requires direct SSE stream; on terminal-state task should error `UnsupportedOperationError`, chepherd returns success | **FAIL** (everything) |
| 7 | `tasks/pushNotificationConfig/set` | Uses flat `{taskId,url,filters}`; spec method `CreateTaskPushNotificationConfig` takes `TaskPushNotificationConfig` object | Returns `{result:{config:{...}}}` wrapper; spec returns `TaskPushNotificationConfig` directly | **FAIL** |
| 8 | `tasks/pushNotificationConfig/get` | Uses `{id}` only; spec §9.4.7 + proto:109 `method_signature="task_id,id"` requires `{taskId,id}` | `{result:{config:{...}}}` wrap; spec direct | **FAIL** |
| 9 | `tasks/pushNotificationConfig/list` | `{taskId}` ✅ matches | `{result:{configs:[...]}}` ✅ — `configs` key matches proto line 808 `repeated TaskPushNotificationConfig configs = 1` | **PARTIAL** (matches modulo method name + missing pagination) |
| 10 | `tasks/pushNotificationConfig/delete` | Uses `{id}` only; spec needs `{taskId,id}` (proto:138 `method_signature="task_id,id"`) | Returns `{result:{ok:true}}`; spec proto:131 returns `google.protobuf.Empty` → JSON `null` or `{}` | **FAIL** |
| 11 | `agent/getAuthenticatedExtendedCard` | No params; spec method is `GetExtendedAgentCard` | Returns `{result:{card:{...}}}`; spec a2a.proto:122 returns `AgentCard` directly | **FAIL** |

### Sample wire bytes (chepherd's actual responses on its slash-camelCase form)

All under `/tmp/v094-qa-A-evidence/`:
- `A1-01.message_send.nosession.resp.json`: `{"error":{"code":-32603,"message":"deliver: a2a.SendMessage: target session \"no-such-session\" not found"}}` — wrong code; should be `-32001 TaskNotFoundError` per §5.4 (or returned as task with status.error per §9.4.1)
- `A1-03.tasks_get.missing.resp.json`: `{"error":{"code":-32004,"message":"task not found: definitely-does-not-exist"}}` — code `-32004` mapped to `UnsupportedOperationError` in spec §5.4; should be `-32001 TaskNotFoundError`
- `A1-05.tasks_cancel.illegal_state.resp.json`: `{"result":{"task":{"id":"...","status":{"state":"failed"},...}}}` — should be `-32002 TaskNotCancelableError` per spec §5.4 + §9.4.5
- `A1-02.message_stream.resp.raw` headers: `Content-Type: application/json` — should be `text/event-stream` per §9.1 + §9.4.2
- `A1-11.agent_getExtendedCard.resp.json`: `{"result":{"card":{"protocolVersion":"1.0",...}}}` — card key wraps the result; spec returns AgentCard directly

---

## A.2 — State machine

### Spec authoritative state names (§5.5 + a2a.proto:187-)

> Enum values MUST be represented according to the ProtoJSON specification ... typically SCREAMING_SNAKE_CASE.
> Examples: `TASK_STATE_INPUT_REQUIRED`, `ROLE_USER`

### chepherd actual state values (observed in `A1-04.tasks_list.resp.json`)

```json
"status":{"state":"failed"}
"status":{"state":"submitted"}
"status":{"state":"working"}
```

**FAIL** — chepherd emits lowercase short forms (`failed`, `submitted`, `working`); spec mandates `TASK_STATE_FAILED`, `TASK_STATE_SUBMITTED`, `TASK_STATE_WORKING`.

### Illegal transitions

| Transition | Spec requirement | chepherd actual |
| --- | --- | --- |
| `failed → canceled` via `tasks/cancel` | `-32002 TaskNotCancelableError` per §5.4 | Returns task success (A1-05.tasks_cancel.illegal_state.resp.json) — **FAIL** |
| `failed → subscribed` via `tasks/resubscribe` | `UnsupportedOperationError` per §9.4.6 + a2a.proto:75 | Returns `{streamId:...}` success — **FAIL** |

Other transitions (working→completed, working→canceled, submitted→working, etc.) not walked because a successful end-to-end requires a live PTY session (deferred to Category C/D walk).

---

## A.3 — JSON-RPC 2.0 + A2A-specific error codes

### Standard JSON-RPC 2.0 codes (chepherd ✅)

| Code | chepherd | Evidence |
| --- | --- | --- |
| `-32700` ParseError | ✅ emitted on invalid JSON | `A3-32700.parse_error.resp.json` |
| `-32600` InvalidRequest | ✅ emitted on missing jsonrpc field | `A3-32600.invalid_request.resp.json` |
| `-32601` MethodNotFound | ✅ emitted on unknown method | `A3-32601.method_not_found.resp.json` |
| `-32602` InvalidParams | ✅ emitted on type-mismatched params | `A3-32602.invalid_params.resp.json` |
| `-32603` InternalError | ✅ emitted on session-not-found (but wrong layer) | `A1-01.message_send.nosession.resp.json` |

### A2A-specific codes (§5.4 table)

| A2A error | Spec code | chepherd code | Status |
| --- | --- | --- | --- |
| `TaskNotFoundError` | `-32001` | `-32004` | ❌ FAIL |
| `TaskNotCancelableError` | `-32002` | not emitted (silent success on terminal cancel) | ❌ FAIL |
| `PushNotificationNotSupportedError` | `-32003` | (untested — feature is supported, so not exercised) | n/a |
| `UnsupportedOperationError` | `-32004` | (chepherd uses `-32004` for "task not found" — NAME COLLISION) | ❌ FAIL |
| `ContentTypeNotSupportedError` | `-32005` | (untested) | n/a |
| `InvalidAgentResponseError` | `-32006` | (untested) | n/a |
| `ExtendedAgentCardNotConfiguredError` | `-32007` | (untested — feature configured) | n/a |
| `ExtensionSupportRequiredError` | `-32008` | (untested) | n/a |
| `VersionNotSupportedError` | `-32009` | (untested) | n/a |

### Auth-required (chepherd-specific)

- Unauthenticated → HTTP 401 + `{"error":{"code":-32001,"message":"authentication required..."}}` — collides with A2A `TaskNotFoundError -32001`. Should use HTTP 401 with `WWW-Authenticate` header, no JSON-RPC body, or use a non-conflicting code in the A2A-unused band.

---

## A.4 — Agent Card schema

### Discovery surface

- `GET /.well-known/agent-card.json` → HTTP 200 ✅
- `GET /.well-known/jwks.json` → HTTP 200 ✅ (per #225 B2)

### chepherd's published card (excerpt)

```json
{
  "protocolVersion": "1.0",
  "name": "chepherd",
  "description": "chepherd v0.9.3 control-plane Agent — PTY-host runtime + Scrum Master intelligence + A2A endpoint",
  "url": "http://127.0.0.1:18080/jsonrpc",
  "version": "0.9.3",
  "capabilities": { "streaming": true, "pushNotifications": true, "extendedCard": true },
  "defaultInputModes": ["text"],
  "defaultOutputModes": ["text"],
  "skills": [{"id":"send-message","name":"Send PTY message","description":"..."}],
  "security": [{"mtls":[]},{"httpAuth":[]},{"apiKey":[]},{"oauth2":[]},{"oidc":[]}],
  "securitySchemes": {
    "apiKey": {"type":"apiKey","in":"header","name":"X-API-Key"},
    "httpAuth": {"type":"http","scheme":"bearer","bearerFormat":"JWT"},
    "mtls": {"type":"mutualTLS"},
    "oauth2": {"type":"oauth2"},
    "oidc": {"type":"openIdConnect"}
  },
  "x-chepherd-p2p": {"version":"0.9.2","supportedDataChannels":["a2a"]}
}
```

### Spec §4 conformance (partial, needs full audit)

- `protocolVersion: "1.0"` ✅
- `name`, `description`, `url`, `version` ✅
- `capabilities.streaming/pushNotifications/extendedCard` ✅
- `defaultInputModes`/`defaultOutputModes` ✅
- `skills[]` minimal ✅
- `securitySchemes` keyed by scheme name ✅
- `x-chepherd-p2p` extension prefixed with `x-` ✅ (acceptable per spec §4.x extension model)
- **Missing/needs audit**: `supportedInterfaces` (§5.8), `signature` field if Card is canonicalized, `additionalInterfaces`, `documentationUrl`, `provider` per §4 schema
- **`url` points to `/jsonrpc` which serves non-spec method names** — clients reading the card and following the URL get a non-conformant endpoint

---

## A.5 — Canonical-client interop (not executed)

Per chepherd-lead's plan addition: drive 3 highest-value methods through `a2a-python` SDK as "state-of-the-art exemplary" proof.

**Not executed** because A.1 found `-32601 unknown method` for every PascalCase call. The SDK constructs requests with `method='SendMessage'` etc.; round-trip will fail at the JSON-RPC method-resolution layer before any further logic runs. No new evidence would be produced beyond what A.1 already shows.

This becomes a **documented gap until chepherd's method-name layer is fixed**. Once fixed, A.5 can be executed properly.

---

## P0 issue to file

**Title**: v0.9.4 spec conformance: A2A v1.0 JSON-RPC method names + error codes + state enums + Content-Type all diverge from spec §5.3/§5.4/§5.5/§9.1

**Body**: ...all findings from this evidence file plus a remediation plan. See `docs/v094-qa/categoryA-evidence.md`.

**Blocks**: #560 (until resolved, Categories B-H deferred — federation interop, runner R2/R3, audit, etc. all depend on a spec-conformant A2A layer).

---

## Appendix — Items 1-7 byte-diff (worker-implementation-ready)

Per chepherd-lead 2026-06-01: build worker-actionable evidence so the moment operator says "go", remediation can ship without re-discovering the deltas. Spec quote + chepherd-actual + spec-required + unified diff for each independent divergence.

### Item 1 — Method names: 11 slash-camelCase → PascalCase

**Spec §9.1 quote** (verbatim):
> **Method Naming:** PascalCase method names matching gRPC conventions (e.g., `SendMessage`, `GetTask`)

**Spec §5.3 Method Mapping Reference** (JSON-RPC column):
`SendMessage`, `SendStreamingMessage`, `GetTask`, `ListTasks`, `CancelTask`, `SubscribeToTask`, `CreateTaskPushNotificationConfig`, `GetTaskPushNotificationConfig`, `ListTaskPushNotificationConfigs`, `DeleteTaskPushNotificationConfig`, `GetExtendedAgentCard`

**chepherd-actual** (`internal/a2a/jsonrpc.go:185-198`):
```go
return []string{
    "message/send", "message/stream",
    "tasks/get", "tasks/list", "tasks/cancel", "tasks/resubscribe",
    "tasks/pushNotificationConfig/set", "tasks/pushNotificationConfig/get",
    "tasks/pushNotificationConfig/list", "tasks/pushNotificationConfig/delete",
    "agent/getAuthenticatedExtendedCard",
}
```

**Proof of non-interop** (single byte-diff per method):
```diff
-{"method":"message/send", ...}                      ← chepherd accepts
+{"method":"SendMessage", ...}                       ← spec + a2a-python
```
Probe at `/tmp/v094-qa-A-evidence/A1-spec-pascal-SendMessage.resp.json`:
```json
{"jsonrpc":"2.0","id":"sp","error":{"code":-32601,"message":"unknown method: SendMessage"}}
```
(Same shape for all 10 other PascalCase names — files `A1-spec-pascal-*.resp.json`.)

**Remediation**: rebind 11 `Register()` calls + `A2AMethodNames()` + all test fixtures. Add optional alias map `{"message/send" → "SendMessage", ...}` for `a2a-js` client compat, documented as `x-chepherd-method-aliases` AgentCard extension.

---

### Item 2 — Param field name: `taskId` → `id` for tasks/get + tasks/cancel + tasks/resubscribe

**Spec §9.4.3 GetTask example** (verbatim):
```json
{"jsonrpc":"2.0","id":2,"method":"GetTask","params":{"id":"task-uuid","historyLength":10}}
```

**Spec §9.4.5 CancelTask example** (verbatim):
```json
{"jsonrpc":"2.0","id":4,"method":"CancelTask","params":{"id":"task-uuid"}}
```

**Spec §9.4.6 SubscribeToTask example** (verbatim):
```json
{"jsonrpc":"2.0","id":5,"method":"SubscribeToTask","params":{"id":"task-uuid"}}
```

**Proto authority** `a2a.proto:52`: `option (google.api.method_signature) = "id";` (single field, named `id`)

**chepherd-actual** (`internal/a2a/method_bodies.go:83`):
```go
type getTaskParams struct {
    TaskID string `json:"taskId"`
}
```

**Unified diff for params**:
```diff
 {
   "method":"GetTask",
-  "params":{"taskId":"019e7f00-..."}   ← chepherd
+  "params":{"id":"019e7f00-..."}       ← spec + a2a-python
 }
```

**Remediation**: rename `TaskID string \`json:"taskId"\`` → `ID string \`json:"id"\`` in `getTaskParams`, `cancelTaskParams`, `subscribeTaskParams`. Optional: accept both via `json:"id,omitempty"` + fallback to `taskId` for one transition window.

---

### Item 3 — TaskPushNotificationConfig param shape for get + delete

**Spec proto authority** (`a2a.proto:109, 138`):
```proto
option (google.api.method_signature) = "task_id,id";   // GetTaskPushNotificationConfig
option (google.api.method_signature) = "task_id,id";   // DeleteTaskPushNotificationConfig
```

**chepherd-actual** (`internal/a2a/method_bodies.go:315-317`):
```go
type getPushConfigParams struct {
    ID string `json:"id"`
}
```

**Diff**:
```diff
 {
   "method":"GetTaskPushNotificationConfig",
-  "params":{"id":"cfg-uuid"}                          ← chepherd
+  "params":{"taskId":"task-uuid","id":"cfg-uuid"}     ← spec proto
 }
```

**Remediation**: add `TaskID string \`json:"taskId"\`` field. Validation: ensure `taskId` matches the persisted `cfg.TaskID` (defense against cross-task ID confusion).

---

### Item 4 — TaskPushNotificationConfig/set shape (flat → spec-nested)

**Spec proto** (`a2a.proto:90`):
```proto
rpc CreateTaskPushNotificationConfig(TaskPushNotificationConfig) returns (TaskPushNotificationConfig)
```
i.e., params is a `TaskPushNotificationConfig` object directly.

**Spec proto:469** (`TaskPushNotificationConfig` shape):
```proto
message TaskPushNotificationConfig {
  string task_id = 1;
  PushNotificationConfig push_notification_config = 2;
}
```
→ JSON (per §5.5 camelCase): `{taskId: ..., pushNotificationConfig: {url, token, authentication, ...}}`

**chepherd-actual** (`internal/a2a/method_bodies.go:279-285`):
```go
type pushConfig struct {
    ID         string   `json:"id,omitempty"`
    TaskID     string   `json:"taskId"`
    URL        string   `json:"url"`
    SigningKey string   `json:"signingKey,omitempty"`
    Filters    []string `json:"filters,omitempty"`
}
```

**Diff**:
```diff
 {
   "method":"CreateTaskPushNotificationConfig",
   "params":{
     "taskId":"task-uuid",
-    "url":"https://example.org/webhook",                            ← chepherd flat
-    "filters":["state.completed"]
+    "pushNotificationConfig":{                                      ← spec nested
+      "url":"https://example.org/webhook",
+      "token":"<optional>",
+      "authentication":{"schemes":["bearer"]}
+    }
   }
 }
```
Probe `A1-07.push_set.spec_nested.resp.json` shows spec-nested form FAILS:
```json
{"error":{"code":-32602,"message":"taskId and url are required"}}
```

**Remediation**: restructure `pushConfig` → `TaskPushNotificationConfig{TaskID, Config: PushNotificationConfig{URL, Token, Authentication}}`. Probably keep an alias decoder for the flat form for one transition window.

---

### Item 5 — Streaming Content-Type: `application/json` two-step → direct `text/event-stream`

**Spec §9.4.2 SendStreamingMessage** (verbatim):
> **Response:** HTTP 200 with `Content-Type: text/event-stream`
> ```text
> data: {"jsonrpc": "2.0", "id": 1, "result": { /* StreamResponse object */ }}
>
> data: {"jsonrpc": "2.0", "id": 1, "result": { /* StreamResponse object */ }}
> ```

**chepherd-actual** (`A1-02.message_stream.resp.raw`):
```http
HTTP/1.1 200 OK
Content-Type: application/json
Date: Sun, 31 May 2026 17:07:49 GMT
Content-Length: 335

{"jsonrpc":"2.0","id":"ms1","result":{"task":{"id":"...","status":{"state":"submitted"},...},"streamId":"589fbeac-..."}}
```

**Diff** (headers + body):
```diff
 HTTP/1.1 200 OK
-Content-Type: application/json
-Content-Length: 335
+Content-Type: text/event-stream
+Cache-Control: no-cache
+Connection: keep-alive

-{"jsonrpc":"2.0","id":"ms1","result":{"task":{...},"streamId":"..."}}
+data: {"jsonrpc":"2.0","id":"ms1","result":{"task":{...}}}
+
+data: {"jsonrpc":"2.0","id":"ms1","result":{"statusUpdate":{...}}}
+
+(connection held open until terminal state)
```

**Remediation**: collapse the two-step `streamId + GET /a2a/stream/{id}` pattern into the spec's direct-streaming form. Reuse existing `StreamBroker` machinery at the HTTP layer of the POST `/jsonrpc` route. Apply to both `SendStreamingMessage` + `SubscribeToTask`.

---

### Item 6 — Result wrapping for GetExtendedAgentCard

**Spec a2a.proto:122** (verbatim):
```proto
rpc GetExtendedAgentCard(GetExtendedAgentCardRequest) returns (AgentCard)
```
→ JSON-RPC result is `AgentCard` directly.

**chepherd-actual** (`A1-11.agent_getExtendedCard.resp.json`):
```json
{"jsonrpc":"2.0","id":"e1","result":{"card":{"protocolVersion":"1.0",...}}}
```

**Diff**:
```diff
 {
   "jsonrpc":"2.0",
   "id":"e1",
-  "result":{"card":{"protocolVersion":"1.0","name":"chepherd",...}}   ← chepherd wraps
+  "result":{"protocolVersion":"1.0","name":"chepherd",...}            ← spec direct
 }
```

**Remediation**: drop the `{card:...}` wrapper. Same check needed for other wrapped results — `GetTask` returns `{task:...}`, `CancelTask` returns `{task:...}`, push CRUD returns `{config:...}` / `{configs:[...]}`. Diff against spec proto return types for each.

---

### Item 7 — Terminal-state behavior on cancel + resubscribe

**Spec §3.1.5 Cancel Task Errors** (verbatim):
> - `TaskNotCancelableError`: The task is not in a cancelable state (e.g., already completed, failed, or canceled).
> - `TaskNotFoundError`: The task ID does not exist or is not accessible.

**Spec §3.1.6 Subscribe to Task Errors** (verbatim):
> - `UnsupportedOperationError`: The operation is attempted on a task that is in a terminal state (`TASK_STATE_COMPLETED`, `TASK_STATE_FAILED`, `TASK_STATE_CANCELED`, `TASK_STATE_REJECTED`).

**Spec §5.4 code mapping**: `TaskNotCancelableError` = `-32002`; `UnsupportedOperationError` = `-32004`.

**chepherd-actual cancel-on-failed** (`A1-05.tasks_cancel.illegal_state.resp.json`):
```json
{"jsonrpc":"2.0","id":"c1","result":{"task":{"id":"019e7f00-...","status":{"state":"failed"},...}}}
```
**Should be**:
```json
{"jsonrpc":"2.0","id":"c1","error":{"code":-32002,"message":"task not in cancelable state: failed"}}
```

**chepherd-actual resubscribe-on-failed** (`A1-06.tasks_resubscribe.resp.raw` body):
```json
{"jsonrpc":"2.0","id":"r1","result":{"task":{"id":"019e7f00-...","status":{"state":"failed"},...},"streamId":"93241f79-..."}}
```
**Should be**:
```json
{"jsonrpc":"2.0","id":"r1","error":{"code":-32004,"message":"task in terminal state: failed"}}
```

**Remediation**: in `handleCancelTask` + `handleResubscribeTask`, check `task.Status.State` against `{completed, failed, canceled, rejected}` (or whatever the post-item-8 enum form is) BEFORE proceeding; return the right error code.

---

### Item 8 — State enum: lowercase → SCREAMING_SNAKE_CASE per ProtoJSON §5.5

**Spec §5.5** (verbatim):
> Enum values MUST be represented according to the ProtoJSON specification, which serializes enums as their string names as defined in the Protocol Buffer definition (typically SCREAMING_SNAKE_CASE).
> **Examples:**
> - Protocol Buffer enum: `TASK_STATE_INPUT_REQUIRED` → JSON value: `"TASK_STATE_INPUT_REQUIRED"`

**Spec proto:187-** (verbatim):
```proto
enum TaskState {
  TASK_STATE_UNSPECIFIED = 0;
  TASK_STATE_SUBMITTED = 1;
  TASK_STATE_WORKING = 2;
  TASK_STATE_COMPLETED = 3;
  TASK_STATE_FAILED = 4;
  TASK_STATE_CANCELED = 5;
  TASK_STATE_REJECTED = 6;
  TASK_STATE_INPUT_REQUIRED = 7;
  TASK_STATE_AUTH_REQUIRED = 8;
}
```

**chepherd-actual** (observed in `A1-04.tasks_list.resp.json`):
```json
"status":{"state":"failed"}
"status":{"state":"submitted"}
```

**Diff**:
```diff
-"status":{"state":"failed"}                ← chepherd
+"status":{"state":"TASK_STATE_FAILED"}     ← spec ProtoJSON
```

**Remediation**: change wire serialization. Internal Go enum values can stay as `TaskStateFailed`, but MarshalJSON must emit ProtoJSON form. Affects every Task wire payload; needs corresponding spec-conformant unmarshal too.

---

### Item 9 — Error code remapping per spec §5.4

| Where | chepherd current | Spec required | Fix |
| --- | --- | --- | --- |
| `tasks/get` missing | `-32004` (UnsupportedOperationError in spec) | `-32001` (TaskNotFoundError) | rename + remap |
| `tasks/cancel` missing | `-32004` | `-32001` | same |
| `tasks/cancel` terminal state | (no error, silent success) | `-32002` (TaskNotCancelableError) | item 7 |
| `tasks/resubscribe` terminal state | (no error, silent success) | `-32004` (UnsupportedOperationError) | item 7 |
| `tasks/pushNotificationConfig/get` missing | `-32004` | `-32001` (TaskNotFoundError) | same |
| auth-required | HTTP 401 + JSON-RPC `-32001` | use distinct chepherd-specific code OR omit JSON-RPC body | avoid `-32001` collision |
| `message/send` no session | `-32603` (InternalError) | `-32001` (TaskNotFoundError) per spec §3.3.2 | remap to A2A code |

---

### Item 10 — Pagination response fields on List methods

**Spec §9.4.4 ListTasks params** include `pageSize, pageToken`.
**Spec proto:705** `ListTasksResponse{tasks, nextPageToken, pageSize, totalSize}`.

**chepherd-actual** (`A1-04.tasks_list.resp.json`):
```json
{"result":{"tasks":[...]}}
```

**Required**:
```json
{"result":{"tasks":[...], "nextPageToken":"", "pageSize":0, "totalSize":2}}
```

Same shape gap for `ListTaskPushNotificationConfigs`.

---

## Evidence files

All under `/tmp/v094-qa-A-evidence/` (45 files, 178 KB):
- `A1-01..A1-11`: 11 method walks (request + response + http-code)
- `A1-spec-pascal-*`: 11 PascalCase rejection probes
- `A3-32xxx`: 5 standard error code probes + auth probe
- `A4.agent-card.*`: card + JWKS
- `spec.md`, `a2a.proto`: canonical A2A v1.0 spec snapshots fetched at walk time

Walk transcript: `/tmp/v094-qa-A-logs/chepherd.log`
Walker script: `scripts/a2a-conformance/walk-categoryA.sh`

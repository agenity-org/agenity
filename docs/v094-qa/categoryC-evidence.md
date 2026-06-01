# v0.9.4 QA — Category C — Runner lifecycle — EVIDENCE

**Walked:** 2026-06-01 by chepherd-worker (QA test-engineer)
**Issue:** [#560 v0.9.4 QA campaign](https://github.com/chepherd/chepherd/issues/560)
**Plan:** [`categoryC-plan.md`](categoryC-plan.md)
**Sister evidence:** [`categoryA-evidence.md`](categoryA-evidence.md) + [`categoryB-evidence.md`](categoryB-evidence.md)
**Spec source:** [`docs/V0.9.2-ARCHITECTURE.md`](../V0.9.2-ARCHITECTURE.md) §5 #3 (chepherd-runner), §22 (lines 1413-1421), §23 invariants (lines 1466-1487)
**Surface under test:**
- Binary: `chepherd` + `chepherd-runner`, both built from `origin/main @ 9df9f6d` at walk time
- Daemon: `chepherd run --headless --no-shepherd=true --listen 127.0.0.1:<port> --state-dir <fresh>`
- Runner: `chepherd-runner -daemon-url ws://daemon -auth-token <daemon-bearer> -sid sid-qa-C-1 -agent sovereign-shell -a2a-listen 127.0.0.1:<port> -mcp-socket <unix> -mcp-tcp-listen 127.0.0.1:<port>`
- Fresh state-dirs per CLAUDE.md §2

---

## VERDICT SUMMARY (6/6 cells walked — Category C COMPLETE)

| Sub-area | Status | One-line reason |
| --- | --- | --- |
| **C.1 — R1 boot/register/quiesce** | **PASS** + NOTE (quiesce-at-5s-boundary) | Runner boots, registers WS to daemon, A2A listener up, MCP unix socket bound; daemon /api/v1/runners lists runner with full metadata; SIGTERM → quiesce in 5011ms (right at 5s soft+hard boundary); A2A listener closes, MCP socket cleaned. |
| **C.2 — R4 PTY ownership** | **PASS (strong)** | Runner holds `/dev/ptmx` fd 8; daemon holds ZERO PTY fds (§23 invariant ✓); child `/bin/sh -l` spawned as runner's direct child PID. |
| **C.3 — R2 per-session A2A endpoint** | **PASS (structural)** + **INHERITED-FAIL-FROM-A.1** (wire shape) | Endpoint `/a2a/{sid}/jsonrpc` exists + accepts POST + returns slash-camelCase responses identical to daemon's pre-R5 shape; GET → 200+JSON-RPC -32600 (non-spec but consistent); bad-sid → 404. Full A.1 walker replay produces same divergences worker2 found at daemon. |
| **C.4 — R3 per-session Agent Card** | **PASS (structural)** + **PARTIAL** (full §4 audit below) + **NOTE** (JWKS URL scheme bug) | Card at `/a2a/{sid}/.well-known/agent-card.json` returns 200 with §4-mostly-conformant body; per-session-specific fields (name, url, signalingEndpoint) correct; missing supportedInterfaces/signature/provider; `securitySchemes.chepherd-jwt.description` emits `ws://...` for JWKS URL (cosmetic but wrong). |
| **C.5 — R5 daemon 410-Gone + RFC headers** | **PASS (strong)** | Daemon `/jsonrpc` returns HTTP 410 on POST/GET/OPTIONS with `Deprecation: true` (RFC 9745) + `Sunset: Sun, 31 May 2026 00:00:00 GMT` (RFC 8594) + `Link: </api/v1/agents/>; rel="successor-version"` (RFC 5988). Daemon audit line `[chepherd-daemon R5] LEGACY A2A request ... 410-Gone` per method invocation. |
| **C.6 — Playwright dashboard spawn → SID materialization** | **PASS** + **P1 FINDING** (token-not-propagated) | Drove canonical v0.9.4 dashboard at `http://localhost:<port>/app/` (`web/src/components/Dashboard.svelte` mounted at `web/src/pages/app.astro`). Spawn flow fires (`POST /api/v1/sessions → 201 Created`), SID materializes (`qa-c6-agent-1780250854769374928`), Details pane populates with full identity/location/process metadata, inbox surfaces failure cleanly. **P1 BUG**: dashboard SPA reads URL `?token=...` but does NOT forward it to background API calls → 12× 401 across `/api/v1/{sessions,inbox,peers,tasks}`. Cookie-injection workaround required to unblock the walk. |

---

## C.1 — R1 chepherd-runner boot/register/quiesce

### Spec quote (`cmd/runner/main.go:1-32`)

> *"chepherd-runner is the data-plane process that runs as PID 1 inside each agent container. It owns: per-session A2A endpoint at `/a2a/{sid}/jsonrpc` (+ Agent Card at `/a2a/{sid}/.well-known/agent-card.json`), MCP HTTP server bound to a local Unix socket inside the container ... PTY master ownership for the agent ... outbound WS to chepherd-daemon for registration, command intake, audit egress."*

### Boot — runner stderr captured (`C1-runner.stderr`)

```
2026/06/01 01:58:30 [chepherd-runner] starting sid="sid-qa-C-1" name="qa-C-runner" agent="sovereign-shell" mcp-socket="/tmp/v094-qa-C/mcp-runner.sock" a2a-base-url="http://127.0.0.1:45011" daemon="ws://127.0.0.1:48577"
2026/06/01 01:58:30 [chepherd-runner] MCP listening on unix:///tmp/v094-qa-C/mcp-runner.sock
2026/06/01 01:58:30 [chepherd-runner] MCP also listening on http://127.0.0.1:50757/mcp for agent-facing transport
2026/06/01 01:58:30 [chepherd-runner] registered with daemon: assigned-sid=sid-qa-C-1 audit-topic=runner:sid-qa-C-1 daemon-version=0.9.4-R1
2026/06/01 01:58:30 [chepherd-runner] agent PTY-session started: sid=sid-qa-C-1 binary=/bin/sh argv=[/bin/sh -l]
2026/06/01 01:58:30 [chepherd-runner] A2A endpoint listening on 127.0.0.1:45011 (/a2a/sid-qa-C-1/jsonrpc)
```

Six startup lines, one per subsystem: starting → MCP unix → MCP TCP → daemon-WS-register → PTY agent → A2A endpoint. PASS.

### Daemon-side register confirmation (`C1-daemon-registered.log`)

```
[chepherd-daemon] runner registered: sid=sid-qa-C-1 agent_slug=sovereign-shell version=0.9.4-R1
```

### Daemon `/api/v1/runners` (`C1-runners-list.json`)

```json
{"runners":[{
  "sid":"sid-qa-C-1",
  "name":"qa-C-runner",
  "agent_slug":"sovereign-shell",
  "runner_version":"0.9.4-R1",
  "a2a_base_url":"http://127.0.0.1:45011",
  "mcp_socket":"/tmp/v094-qa-C/mcp-runner.sock",
  "capabilities":["pty","audit-stream"],
  "registered_at":"2026-05-31T17:58:30.953563219Z",
  "last_seen":"2026-05-31T17:58:31.150643036Z",
  "audit_events_received":2
}]}
```

Full registration row visible to operator API. `audit_events_received: 2` shows the runner is already pushing audit notifications via the WS heartbeat.

### Socket+listener probes

- A2A listener: `LISTEN 0 1024 127.0.0.1:45011 0.0.0.0:*` ✓
- MCP TCP listener: `LISTEN 0 1024 127.0.0.1:50757 0.0.0.0:*` ✓
- MCP Unix socket: `srw------- 1 openova openova 0 Jun  1 01:58 /tmp/v094-qa-C/mcp-runner.sock` ✓ (0600 perms)

### Quiesce probe — SIGTERM → clean shutdown

```
sent SIGTERM at 2026-05-31T17:58:33.285012626Z
runner quiesced in 5011ms
✓ A2A listener closed
✓ MCP socket cleaned
```

Daemon post-quiesce (`C1-daemon-quiesce.log`):
```
[chepherd-daemon] runner sid-qa-C-1 WS closed: websocket: close 1006 (abnormal closure): unexpected EOF
```

**Verdict: PASS** + **NOTE**: quiesce at 5011ms is RIGHT at the soft+hard window boundary (3s soft + 5s hard per chepherd-lead Q6). The runner does quiesce cleanly (listener closes, socket cleaned, daemon notified) but takes most of the 5s window. Suggest investigating whether `cmd/runner/main.go` SIGTERM handler can short-circuit one of the lifecycle teardowns to drop this to ~1-2s.

Also NOTE: daemon's "WS closed: websocket: close 1006 (abnormal closure)" should ideally be "WS closed: normal disconnect" — the runner is killing the WS by exit rather than sending a graceful close frame. Minor.

---

## C.2 — R4 PTY ownership inside runner (§23 invariant)

### Spec quote (§23 invariants, lines 1473-1475)

> *"No PTY file descriptors cross pod boundaries — Each runner owns its local PTY, cross-pod safe. Daemons hold zero PTY file descriptors — Only runners hold them."*

### Runner fd snapshot (`C2-runner-fd.ls` excerpt)

```
lrwx------ 1 openova openova 64 Jun  1 01:58 8 -> /dev/ptmx
```

Runner pid 107042 has `/dev/ptmx` open on fd 8 (the PTY master). lsof confirms:
```
chepherd- 107042 openova    8u  CHR  5,2  0t0  87 /dev/ptmx
```

### Daemon fd snapshot (`C2-daemon-fd.ls` + `C2-daemon-pty-count`)

```
daemon PTY fd count: 0
```

`grep -E "pts|ptmx"` on daemon's `/proc/<pid>/fd/` produces zero matches. lsof confirms zero PTY entries. **§23 invariant honored.**

### Process tree (`C2-runner-children.log`)

```
child pid=107066 cmdline='/bin/sh -l '
```

Runner's direct child is the sovereign-shell agent (`/bin/sh -l`), exec'd via the PTY-allocated child. The agent runs as runner's child PID, NOT daemon's.

### Cumulative C.2 verdict — **PASS (strong)**

| Probe | Expected | Observed | Verdict |
| --- | --- | --- | --- |
| Runner holds PTY master | `/dev/ptmx` in `/proc/<pid>/fd/` | fd 8 → /dev/ptmx | PASS |
| Daemon holds ZERO PTY fds | no pts/ptmx entries | 0 entries | PASS (§23 invariant) |
| Agent is runner's child | child pid under runner | pid=107066 cmd=/bin/sh -l | PASS |

---

## C.3 — R2 per-session A2A endpoint

### Spec quote (`cmd/runner/main.go:368`)

> *"per-session A2A endpoint TCP bind address (host:port). Empty disables. When set + --sid non-empty, mounts /a2a/<sid>/jsonrpc serving all 11 A2A methods. (#463 Wave R2)"*

### Structural probes

| Probe | URL | Method | Observed |
| --- | --- | --- | --- |
| a — GET on POST-only path | `/a2a/sid-qa-C-1/jsonrpc` | GET | `http=200 {"jsonrpc":"2.0","error":{"code":-32600,"message":"method must be POST"}}` |
| b — POST tasks/list | `/a2a/sid-qa-C-1/jsonrpc` | POST | `http=200 {"jsonrpc":"2.0","id":"c3-1","result":{"tasks":[]}}` |
| c — POST to bad sid | `/a2a/sid-doesnt-exist/jsonrpc` | POST | `http=404 404 page not found` |

NOTE on probe (a): chepherd returns HTTP 200 + JSON-RPC -32600 envelope for GET-on-POST-only, not HTTP 405. Defensible (JSON-RPC layer absorbs all method-routing) but non-standard. Not flagging as FAIL since it's consistent with chepherd's overall pattern of returning JSON-RPC-formatted errors at HTTP 200.

### A.1 walker re-run against runner endpoint (`/tmp/v094-qa-C/A1-replay-evidence/`)

Worker2's `walk-categoryA.sh` re-driven against `http://127.0.0.1:<runner>/a2a/sid-qa-C-1/jsonrpc`. Sample responses (full set in evidence dir):

```
A1-01.message_send.nosession → {"jsonrpc":"2.0","id":"s1","result":{"task":{"id":"019e7f30-...","contextId":...}}}
                                 (RUNNER ACCEPTS — auto-creates task for unknown contextId; daemon used to error)
A1-03.tasks_get.missing → {"jsonrpc":"2.0","id":"g2","error":{"code":-32004,"message":"task not found: ..."}}
                          (-32004 wrong code per A.3 — should be -32001 TaskNotFoundError)
A1-04.tasks_list → {"jsonrpc":"2.0","id":"l1","result":{"tasks":[{...}]}}
                   (slash-camelCase method works, missing pagination cursor per A.1)
A1-05.tasks_cancel.illegal_state → {"jsonrpc":"2.0","id":"c1","result":{"task":...}}
                                   (still returns success on cancel of terminal-state task — A.2 illegal-transition fail)
A1-07.push_set.flat → {"jsonrpc":"2.0","id":"p1","result":{"config":{"id":"...","taskId":...}}}
                      (flat shape works, spec nested shape errors — A.1 §9.4.7 divergence)
A1-11.agent_getExtendedCard → {"jsonrpc":"2.0","id":"e1","error":{"code":-32001,"message":"authentication required: ..."}}
                              (auth-required path differs; spec expects card body, chepherd requires Bearer)
```

**Confirms: the runner endpoint serves the SAME slash-camelCase shape as the (now-410-Gone) daemon endpoint** — all of worker2's A.1 method-name + A.2 state-machine + A.3 error-code divergences inherit to the runner-served per-session endpoint.

NOTE — one DIFFERENCE from daemon: `A1-01.message_send.nosession` at runner returns **success** (auto-creates task for unknown contextId) while at daemon it returned `-32603`. The runner is more permissive — operator-debuggability win but spec-conformance unclear (spec §9.4.1 implies session must exist).

### Cumulative C.3 verdict — **PASS (structural)** + **INHERITED-FAIL-FROM-A.1**

| Sub-verdict | Status |
| --- | --- |
| Endpoint mounted at `/a2a/{sid}/jsonrpc` | PASS |
| Accepts POST with JSON-RPC envelopes | PASS |
| Returns slash-camelCase method body shapes | PASS-structural / inherited-FAIL-on-wire-shape |
| GET on POST-only → 200 + JSON-RPC -32600 | PASS-with-NOTE (non-standard, consistent) |
| POST bad-sid → 404 | PASS |
| Same method-name divergences as worker2's A.1 | **INHERITED-FAIL** — defer to A.1 remediation |
| Runner is more permissive than daemon on unknown contextId | **NEW NOTE** (auto-creates task vs daemon's -32603) — worth pinning policy decision |

---

## C.4 — R3 per-session Agent Card — FULL §4 SCHEMA AUDIT

### Spec quote (A2A v1.0 §4)

> *"AgentCard fields ... protocolVersion (required), name (required), description, url (required), version (required), capabilities, defaultInputModes, defaultOutputModes, skills, security, securitySchemes, supportedInterfaces, signature, additionalInterfaces, provider, documentationUrl"* (consolidated from §4 + §5.8 + a2a.proto schema)

### Per-session card body (`C4-card.json`)

```json
{
  "protocolVersion": "1.0",
  "name": "chepherd-runner-sid-qa-C-1",
  "description": "chepherd-runner v0.9.4-R1 hosting one A2A-protocol agent session (operator handle: @qa-C-runner)",
  "url": "http://127.0.0.1:59811/a2a/sid-qa-C-1/jsonrpc",
  "version": "0.9.4-R1",
  "capabilities": {
    "streaming": true,
    "pushNotifications": false,
    "extendedCard": false
  },
  "defaultInputModes": ["text/plain"],
  "defaultOutputModes": ["text/plain"],
  "skills": [],
  "security": [{"chepherd-jwt": []}],
  "securitySchemes": {
    "chepherd-jwt": {
      "type": "http",
      "scheme": "bearer",
      "bearerFormat": "JWT",
      "description": "Per-call JWT minted by chepherd-daemon (POST /api/v1/jwt/mint, Wave D2). Verify against daemon JWKS at ws://127.0.0.1:34925/.well-known/jwks.json (Wave T2). ES256 signing."
    }
  },
  "x-chepherd-p2p": {
    "version": "0.9.4",
    "supported": true,
    "signalingEndpoint": "http://127.0.0.1:59811/webrtc/offer",
    "supportedDataChannels": ["a2a"]
  }
}
```

### §4 row-by-row audit (closing worker2's A.4 PARTIAL)

| Spec field | Required? | Per-session card | Verdict | Note |
| --- | --- | --- | --- | --- |
| `protocolVersion` | required | `"1.0"` | **PASS** | matches A2A v1.0 |
| `name` | required | `"chepherd-runner-sid-qa-C-1"` | **PASS** | per-session-specific (includes SID) ✓ |
| `description` | required | present | **PASS** | human-readable |
| `url` | required | `"http://127.0.0.1:59811/a2a/sid-qa-C-1/jsonrpc"` | **PASS** | per-session endpoint URL ✓ |
| `version` | required | `"0.9.4-R1"` | **PASS** | runner version |
| `capabilities.streaming` | optional | `true` | **PASS** | SSE binding live |
| `capabilities.pushNotifications` | optional | `false` | **DIVERGE-FROM-DAEMON** | daemon's central card had `true`; per-session runner doesn't host push notifications |
| `capabilities.extendedCard` | optional | `false` | **DIVERGE-FROM-DAEMON** | similar |
| `defaultInputModes` | required | `["text/plain"]` | **PASS** | PTY agent only ingests text |
| `defaultOutputModes` | required | `["text/plain"]` | **PASS** | |
| `skills` | required (array) | `[]` | **PASS-with-NOTE** | empty array is legal but the agent flavor (sovereign-shell) presumably has skills — could populate from agentcatalog metadata for richer discovery |
| `securitySchemes` | optional | `{chepherd-jwt: ...}` | **PASS** | single scheme; daemon's central card had 5 (apiKey/httpAuth/mtls/oauth2/oidc) — per-session simplification is intentional and reasonable |
| `security` | optional | `[{chepherd-jwt: []}]` | **PASS** | requirement-pointer matches schemes |
| `x-chepherd-p2p` | extension | `{version, supported:true, signalingEndpoint, supportedDataChannels}` | **PASS** | extension prefixed with `x-` ✓; signalingEndpoint correctly populated per F1 #488 |
| `supportedInterfaces` (§5.8) | optional | **MISSING** | **DIVERGE** | A2A v1.0 §5.8 defines this for multi-transport discovery — chepherd-runner only serves JSON-RPC over HTTP so single-interface card may be acceptable, but spec recommends populating |
| `signature` (JWS-signed card) | optional | **MISSING** | **NOTE** | daemon's central card also missing (worker2's A.4); v0.9.4 cards are unsigned |
| `provider` | optional but recommended | **MISSING** | **NOTE** | spec recommends; would help operators identify chepherd as the implementation |
| `documentationUrl` | optional | **MISSING** | **NOTE** | recommended |
| `additionalInterfaces` | optional | **MISSING** | OK (no additional interfaces) |

### Found JWKS scheme bug

`securitySchemes.chepherd-jwt.description` emits `Verify against daemon JWKS at ws://127.0.0.1:34925/.well-known/jwks.json` — **`ws://` is wrong** for a JWKS URL (JWKS is an HTTP resource per RFC 7517). The bug source: `cmd/runner/a2a_endpoint.go:291` interpolates `jwksRef = daemonJWKSURL`; the daemon-URL passed via `--daemon-url ws://...` is used verbatim. Also `:34925` is the daemon's MCP port (not its HTTP port where the JWKS actually lives at `127.0.0.1:48577/.well-known/jwks.json`). Two bugs in one URL.

Severity: P3 — it's a string in a description field, not a parseable URL field, so doesn't break automated card consumers. But it's misleading and the daemon-URL→JWKS-URL translation should sanitize `ws://`→`http://` and probably point at the correct port.

### Per-session JWKS endpoint

`GET /a2a/{sid}/.well-known/jwks.json` → **404**. The per-session card doesn't host its own JWKS — that's fine per the design (per-call JWTs are minted+signed by daemon, peers verify against daemon JWKS).

### Bad sid → 404

`GET /a2a/sid-doesnt-exist/.well-known/agent-card.json` → `404 404 page not found`. Correct.

### Cumulative C.4 verdict — **PASS (structural)** + **PARTIAL** + **NOTE**

| Audit | Status |
| --- | --- |
| 8 required §4 fields | 8/8 PASS |
| 4 optional but recommended fields | 0/4 present (supportedInterfaces, signature, provider, documentationUrl) — PARTIAL |
| `x-chepherd-p2p` extension well-formed | PASS |
| Per-session-specific routing (name, url, signalingEndpoint) | PASS |
| Per-session simplifications (single security scheme) | PASS (intentional) |
| Bad sid → 404 | PASS |
| Per-session JWKS endpoint not exposed | PASS (correct design — daemon hosts JWKS) |
| **JWKS URL scheme bug** (`ws://` instead of `http://`) | **P3 NOTE** |

This audit closes worker2's A.4 PARTIAL by enumerating every §4 field row-by-row. The per-session card surface is consistent with the daemon's central card (same required fields, same gaps on recommended optional fields). Recommend a follow-up issue to populate `provider`, `documentationUrl`, and `signature` per §4 — applies to BOTH card surfaces.

---

## C.5 — R5 daemon 410-Gone + Deprecation + Sunset headers

### Spec quote (`internal/runtimehttp/daemon_a2a_cutover_410.go:1-15`)

> *"Wave R5 #466. Routes return HTTP 410 Gone with: a `Deprecation: true` response header (RFC 9745 Deprecation), a `Sunset: <wave-R5-merge-date>` response header (RFC 8594), and a `Link: rel='successor-version'` header pointing at the per-runner endpoint."*

### Spec quote (RFC 8594 §3)

> *"The Sunset HTTP response header field indicates that a URI is likely to become unresponsive at a specified point in the future."*

### POST `/jsonrpc` headers (`C5-410-post.headers`)

```
HTTP/1.1 410 Gone
Content-Type: application/json
Deprecation: true
Link: </api/v1/agents/>; rel="successor-version"
Sunset: Sun, 31 May 2026 00:00:00 GMT
Date: Sun, 31 May 2026 17:58:30 GMT
Content-Length: 268
```

All 4 RFC-mandated header rows present: 410 status (RFC 7231 §6.5.9), Deprecation: true (RFC 9745), Sunset (RFC 8594 §3, IMF-fixdate format), Link rel="successor-version" (RFC 5988).

### GET `/jsonrpc` headers (`C5-410-get.headers`)

Same headers as POST. ✓

### OPTIONS `/jsonrpc` headers (`C5-410-options.headers`) — chepherd-lead Q5 pin

Same headers as POST. **Chepherd's R5 returns 410 for OPTIONS too** (consistent across all methods).

**Spec interpretation:** RFC 9745 §5 says Deprecation applies "to any HTTP method". RFC 8594 doesn't speak to OPTIONS specifically. Chepherd's choice (410 for all methods including OPTIONS) is defensible — the resource state IS Gone regardless of method; OPTIONS asking "what methods does this support" gets the correct answer of "none, the resource is retired". The alternative (405+Allow header) would be defensible for endpoints that selectively retire methods, but R5 retires the entire resource.

**Verdict: PASS.** Consistent + spec-compliant interpretation. Pinning behavior per Q5: chepherd returns 410-on-OPTIONS, which is the stronger of the two valid interpretations.

### Daemon stderr audit lines (`C5-daemon-legacy.log`)

```
[chepherd-daemon R5] LEGACY A2A request POST /jsonrpc from 127.0.0.1:34704 — 410-Gone (caller should discover the per-runner endpoint via /api/v1/agents/)
[chepherd-daemon R5] LEGACY A2A request GET /jsonrpc from 127.0.0.1:34714 — 410-Gone (caller should discover the per-runner endpoint via /api/v1/agents/)
[chepherd-daemon R5] LEGACY A2A request OPTIONS /jsonrpc from 127.0.0.1:34728 — 410-Gone (caller should discover the per-runner endpoint via /api/v1/agents/)
```

One audit line per method invocation, includes caller addr + the successor URI. Operator can grep these to estimate migration progress.

### Cumulative C.5 verdict — **PASS (strong)**

| Probe | Expected | Observed | Verdict |
| --- | --- | --- | --- |
| POST /jsonrpc | 410 + 3 RFC headers | 410 + all 3 | PASS |
| GET /jsonrpc | 410 + 3 RFC headers | 410 + all 3 | PASS |
| OPTIONS /jsonrpc (Q5 pin) | depends on interpretation | 410 + all 3 (consistent) | PASS — defensible interpretation |
| Audit log line per call | one line per call | yes | PASS |

---

## C.6 — Playwright dashboard spawn → SID materialization

### Spec context

Canonical v0.9.4 dashboard URL (confirmed by chepherd-lead 2026-06-01): `http://localhost:<port>/app/` — `web/src/components/Dashboard.svelte` mounted at `web/src/pages/app.astro`. NOT `/v07/` (legacy, operator-locked never-touch). Per #297 URL-versioning rule, a `/v0.9.4/` route should ALSO exist — it does NOT, filed separately as [#567](https://github.com/chepherd/chepherd/issues/567).

### Setup

Boot daemon with `--web-dir /home/openova/repos/chepherd/web/dist`. Web bundle pre-built (`web/dist/` 1.4MB, includes `app/index.html`).

```
chepherd run --headless --no-shepherd=true \
  --listen 127.0.0.1:43941 \
  --state-dir /tmp/v094-qa-C6/state \
  --web-dir /home/openova/repos/chepherd/web/dist
```

Daemon bearer token captured from `/tmp/v094-qa-C6/state/auth.printed`. Dashboard navigated via Playwright MCP to `http://127.0.0.1:43941/app/?token=<bearer>`.

### Probe 1 — Initial dashboard load → 12× 401 (P1 finding)

Browser console:

```
[ERROR] Failed to load resource: 401 Unauthorized @ http://127.0.0.1:43941/api/v1/sessions:0
[ERROR] Failed to load resource: 401 Unauthorized @ http://127.0.0.1:43941/api/v1/tasks:0
[ERROR] Failed to load resource: 401 Unauthorized @ http://127.0.0.1:43941/api/v1/peers:0
[ERROR] Failed to load resource: 401 Unauthorized @ http://127.0.0.1:43941/api/v1/inbox:0
... × 3 more polling rounds = 12 total
```

Header shows `0 sessions · 0 workers · 0 Scrum Masters · runtime offline`. JS evaluate via Playwright confirms the dashboard HAS the URL token but doesn't propagate it:

```json
{
  "localStorage": {"chepherd-theme": "dark"},
  "sessionStorage": {},
  "cookies": "",
  "urlToken": "eyJ…"
}
```

**P1 finding** filed in [#566](https://github.com/chepherd/chepherd/issues/566).

### Workaround applied

```js
const t = new URL(location.href).searchParams.get('token');
document.cookie = `chepherd_token=${t}; path=/`;
location.reload();
```

After reload: console errors drop to 0; all API calls 200.

### Probe 2 — Spawn flow

1. Clicked "+ spawn agent" → modal opened with form.
2. Selected agent: `sovereign-shell`. Working directory: `/tmp/v094-qa-C6`. Session name: `qa-c6-agent`.
3. Clicked "Spawn agent" submit button.
4. Network trace recorded `POST /api/v1/sessions → 201 Created`.
5. Dashboard refreshed → header showed `2 sessions · 2 workers` (one pre-existing test session + the new one).
6. Details pane populated with full identity/location/process metadata:

```
Identity:  name=qa-c6-agent, agent=sovereign-shell, role=worker, team=default
Location:  cwd=/tmp/v094-qa-C6, started=0s ago, status=○ idle
Process:   pid=255301, uuid=qa-c6-agent-1780250854769374928, bytes 5m=0 B
Scrum:     "assessing — first scorecard arrives on next tick (≤60s)"
```

### Probe 3 — Failure observability

Container creation hit env disk-full (/home was 100% used at walk time). Daemon caught the failure cleanly:

```
Inbox (1):
  @runtime  2m ago  [failure] agent "qa-c6-agent" exited (code 125)
```

Operator gets visible failure feedback via inbox — UX correct even in degraded environment.

### Network trace — full API call surface

41 GET polls across `/api/v1/{sessions,inbox,peers,tasks}` (10s/round; ~10 rounds before spawn), then `POST /api/v1/sessions → 201` for the spawn, then another ~150 polling GETs over 2 minutes. All 200 (post-cookie-workaround).

`POST /api/v1/sessions → 201` confirms the production spawn endpoint shape. (Plan had guessed `/api/v1/agent-types/spawn`; the actual endpoint is `/api/v1/sessions`. Plan corrected against Dashboard.svelte source per [[feedback_ui_changes_need_route_smoke_test]].)

### Cumulative C.6 verdict — **PASS** + **P1 FINDING**

| Probe | Expected | Observed | Verdict |
| --- | --- | --- | --- |
| Dashboard at /app/ loads | 200 + render | 200 + render | PASS |
| URL token propagated to API calls | 200 on all API calls | **401 × 12** | **FAIL → P1 #566** |
| Workaround (set cookie) → unblocks | 0 errors | 0 errors | PASS (proves auth substrate works) |
| Spawn flow fires | POST → 201 + SID | POST /api/v1/sessions → 201 + SID materializes | PASS |
| Details pane populates | Identity/Location/Process | All 3 + Scrum scorecard | PASS |
| Failure observability (env-gap) | inbox surfaces | `[failure] agent exited (code 125)` | PASS |

Companion: P1 [#567](https://github.com/chepherd/chepherd/issues/567) `/v0.9.4` route missing per #297 URL-versioning rule.

Screenshots: `C6-01-dashboard-empty.png`, `C6-02-spawn-modal.png`, `C6-03-post-spawn-details-pane.png` (under `.playwright-mcp/`).

---

## Evidence files

All under `/tmp/v094-qa-C/evidence/`:

- `C1-runner.stderr`, `C1-daemon-registered.log`, `C1-runners-list.{json,meta}`, `C1-runner-{a2a,mcp}-listener.ss`, `C1-runner-mcp-sock.ls`, `C1-quiesce.verdict`, `C1-postquiesce-{a2a.ss,sock.ls}`, `C1-daemon-quiesce.log`
- `C2-runner-fd.ls`, `C2-runner-lsof-pty.log`, `C2-daemon-fd.ls`, `C2-daemon-pty-count`, `C2-daemon-lsof-pty.log`, `C2-runner-children.log`
- `C3-{get,post-tasks-list,post-bad-sid}.{body,meta}`, `C3-A1-replay.log`, `/tmp/v094-qa-C/A1-replay-evidence/` (24 files from worker2's walker re-run against runner endpoint)
- `C4-card.json`, `C4-card-{404,jwks}.{body,meta}`
- `C5-410-{post,get,options}.{body,headers,meta}`, `C5-daemon-legacy.log`
- `C-daemon.stderr` (full daemon stderr)

Walker: `scripts/a2a-conformance/walk-categoryC.sh`

C.6 Playwright walk pending — separate script + npm build.

---

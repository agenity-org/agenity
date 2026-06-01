# v0.9.4 QA — #598 — Daemon MCP/A2A surface walk — EVIDENCE

**Walked:** 2026-06-02 by p0-474-lonely (QA)
**Issue:** [#598 P1 — chepherd daemon MCP/A2A surface never QA'd](https://github.com/chepherd/chepherd/issues/598)

Binary: fresh build from current `main` branch (commit `fix/get-task-taskstore-wiring` HEAD)
Boot: `chepherd run --state-dir /tmp/chepherd-598-state --listen 127.0.0.1:18083 --mcp-listen 127.0.0.1:19090 --headless --no-shepherd=true`

---

## Surface matrix

| Probe | URL / Method | Expected | Actual | Verdict |
|---|---|---|---|---|
| A1 | `GET /healthz` | HTTP 200 `ok:true` | HTTP 200 `ok:true sessions:0` | ✅ PASS |
| A2 | `GET /api/v1/sessions` (no token) | HTTP 401 | HTTP 401 | ✅ PASS |
| A3 | `GET /api/v1/sessions` (bad token) | HTTP 401 | HTTP 401 | ✅ PASS |
| A4 | `POST /jsonrpc` (Wave R5 cutover) | HTTP 410, `-32601` + diagnostic | HTTP 410, `-32601` + diagnostic → "Discover via /api/v1/agents/" | ✅ PASS |
| A5 | `GET /.well-known/agent-card.json` (Wave R5) | HTTP 410, `-32601` | HTTP 410, `-32601` | ✅ PASS |
| A6 | `GET /.well-known/jwks.json` | HTTP 200, ES256 key | HTTP 200, `alg:ES256 keys:1` | ✅ PASS |
| A7 | `GET /api/v1/agents/` (directory) | HTTP 200, `{agents:[...]}` | HTTP 200, `{agents:[]}` (no sessions yet) | ✅ PASS |
| A8 | `GET /api/v1/tasks` | HTTP 200 JSON | HTTP 200 `application/json` | ✅ PASS |
| A9 | `POST /mcp` tools/list (port 19090) | 25 chepherd tools | 25 tools returned | ✅ PASS |
| A10 | `GET /api/v1/runners` | HTTP 200, `{runners:[]}` | HTTP 200, `runners:0` | ✅ PASS |
| A11 | `GET /api/v1/sessions` (with token) | HTTP 200 JSON | HTTP 200, `total:0 live:0` | ✅ PASS |
| A12 | `GET /a2a/no-such-sid/.well-known/agent-card.json` | HTTP 404 JSON (not SPA HTML) | HTTP 404 `application/json` | ✅ PASS (post-#650 fix) |

---

## A4 — /jsonrpc Wave R5 cutover detail

```
POST /jsonrpc
→ HTTP 410 Gone
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "error": {
    "code": -32601,
    "message": "daemon A2A surface retired by Wave R5 (#466): /jsonrpc no longer serves A2A.
                Discover per-runner endpoints via daemon's Wave D1 directory at /api/v1/agents/,
                then POST to the runner's /a2a/<sid>/jsonrpc."
  }
}
```

**Verdict:** ✅ PASS — 410 Gone with parseable JSON-RPC error body + explicit next-step diagnostic pointing to Wave D1 directory. Matches Wave R5 architecture: daemon de-A2A'd, per-runner `/a2a/<sid>/jsonrpc` is the new endpoint.

---

## A5 — /.well-known/agent-card.json Wave R5 cutover detail

```
GET /.well-known/agent-card.json
→ HTTP 410 Gone
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "error": {
    "code": -32601,
    "message": "daemon A2A surface retired by Wave R5 (#466): /.well-known/agent-card.json no longer serves A2A.
                Discover per-runner endpoints via daemon's Wave D1 directory at /api/v1/agents/"
  }
}
```

**Verdict:** ✅ PASS — 410 with same diagnostic pattern.

---

## A6 — JWKS (Wave T2 #505)

```
GET /.well-known/jwks.json
→ HTTP 200
Content-Type: application/json

{"keys":[{"kty":"EC","alg":"ES256","crv":"P-256","x":"...","y":"..."}]}
```

Keys=1, alg=ES256. JWKS endpoint unaffected by Wave R5 — daemon owns the keystore and JWKS publication; runners verify peer JWTs against this URL.

**Verdict:** ✅ PASS

---

## A9 — MCP tools/list (25 tools)

```
POST http://127.0.0.1:19090/mcp
{"jsonrpc":"2.0","id":1,"method":"tools/list"}

→ {"result":{"tools":[
    {"name":"chepherd.spawn",...},
    {"name":"chepherd.assign",...},
    {"name":"chepherd.grant_channel",...},
    {"name":"chepherd.list",...},
    {"name":"chepherd.list_peers",...},
    ... (25 total)
  ]}}
```

**Verdict:** ✅ PASS — 25 chepherd MCP tools returned.

---

## P1 Finding: #650 — /a2a/<sid>/ routes unregistered

**Pre-fix probe (against production daemon):**

```
GET /api/v1/agents/
→ {"agents":[{"sid":"tech-lead-178...","name":"tech-lead",
    "agent_card_url":"http://127.0.0.1:8083/a2a/tech-lead-178.../.well-known/agent-card.json"}]}

GET /a2a/tech-lead-178.../.well-known/agent-card.json
→ HTTP 200 Content-Type: text/html;charset=utf-8   ← SPA HTML fallback, NOT JSON
```

**Root cause:** The daemon's mux had no `/a2a/` prefix handler. The SPA catchall (`/`) served `index.html` for every unmatched path — including the agent card URLs advertised by Wave D1 directory.

**Fix shipped:** Added `s.a2aSessionCardHandler` registered at `/a2a/` in `server.go`. Handler:
- Parses `/a2a/<sid>/.well-known/agent-card.json`
- Looks up session via `rt.GetByContextID(sid)` (accepts SID or short name)
- Returns `runtime.BuildPeerAgentCard(info)` as `application/json`
- Returns 404 JSON for unknown SIDs, 405 JSON for non-GET methods

**Post-fix probe (A12 above):**

```
GET /a2a/no-such-sid/.well-known/agent-card.json
→ HTTP 404 Content-Type: application/json    ← FIXED
```

**Regression test:** `internal/runtimehttp/p1_650_a2a_session_card_test.go` — 3 cases:
1. Unknown SID → 404 JSON (not SPA HTML)
2. POST → 405 JSON
3. Known session → 200 JSON card with correct shape

All 3 PASS.

**Filed:** [#650](https://github.com/chepherd/chepherd/issues/650)

---

## Delta vs categoryA-evidence.md (#560)

#560 walked a fresh daemon before Wave R5 merge. Key differences:

| Surface | #560 (pre-Wave-R5) | #598 (current main) |
|---|---|---|
| `POST /jsonrpc` | HTTP 200 + A2A methods | HTTP 410 + diagnostic |
| `GET /.well-known/agent-card.json` | HTTP 200 + card JSON | HTTP 410 + diagnostic |
| Agent card discovery | Daemon-level card | Per-runner via `/api/v1/agents/` directory |
| `/a2a/<sid>/...` routing | N/A | Fixed by #650 (was SPA HTML fallback) |
| MCP tools | ≥25 | 25 |
| JWKS | ES256 | ES256 |

All #560 A2A spec conformance findings (method names, state enums, error codes) are **pre-Wave-R5 and do not apply to the current daemon**, which retires the A2A surface entirely at the daemon level. Per-runner A2A conformance is a separate QA scope (Category C/D walk).

---

## Verdict summary

| Area | Verdict |
|---|---|
| Auth (no-token, bad-token) | ✅ PASS |
| Healthz | ✅ PASS |
| Wave R5 /jsonrpc cutover | ✅ PASS — 410 + diagnostic |
| Wave R5 agent-card cutover | ✅ PASS — 410 + diagnostic |
| JWKS (T2 #505) | ✅ PASS |
| D1 directory /api/v1/agents/ | ✅ PASS |
| /api/v1/tasks task store | ✅ PASS |
| /api/v1/runners (sibling-container mode) | ✅ PASS — empty expected |
| MCP :9090 tools/list | ✅ PASS — 25 tools |
| /a2a/<sid>/ routing (pre-fix) | ❌ FAIL → P1 #650 filed + fixed |
| /a2a/<sid>/ routing (post-fix) | ✅ PASS |

**Overall #598 verdict: PASS** — P1 finding caught + fixed (#650). All 12 probes PASS post-fix.

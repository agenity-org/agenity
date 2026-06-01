# v0.9.4 QA — Category F — MCP HTTP transport — EVIDENCE

**Walked:** 2026-06-02 by p0-474-lonely (QA)
**Issue:** [#599 P1 — v0.9.4 QA categories D/E/F/G/H](https://github.com/chepherd/chepherd/issues/599)
**Spec:** `docs/V0.9.2-ARCHITECTURE.md` §22

Wave M PRs:
- **M1 #477** — Unix socket MCP listener (`--mcp-socket`, 0600 perms, `/mcp/healthz`)
- **M2 #478** — Streamable HTTP transport (POST/GET/DELETE at `/mcp`, session ID, SSE stream)
- **M3 #479** — stdio bridge deprecation warning (`chepherd mcp` command)

---

## F.1 — M1 Unix socket MCP listener

**Test:** `cmd/runner/main_smoke_test.go::TestRunner_TP1_ScaffoldStartsAndServesHealthz`

```
$ go test ./cmd/runner/... -run TestRunner_TP1 -v -count=1

=== RUN   TestRunner_TP1_ScaffoldStartsAndServesHealthz
[chepherd-runner] MCP listening on unix:///tmp/.../mcp.sock
[chepherd-runner] MCP also listening on http://127.0.0.1:38711/mcp for agent-facing transport
[chepherd-runner] received terminated; shutting down
--- PASS: TestRunner_TP1_ScaffoldStartsAndServesHealthz (1.06s)
```

**Assertions verified:**
- Runner binary builds from source ✅
- Socket file appears at `--mcp-socket` path within 5s ✅
- `GET /mcp/healthz` over Unix socket → HTTP 200 ✅
- Socket permissions = `0600` (privacy guarantee) ✅
- Runner shuts down cleanly on SIGTERM ✅

**Verdict:** PASS

---

## F.2 — M2 Streamable HTTP transport

**Tests:** `internal/mcpserver/p0_478_streamable_http_test.go` — 6 assertions

```
$ go test ./internal/mcpserver/... -run TestWaveM2 -v -count=1

=== RUN   TestWaveM2_StreamablePOST_ReturnsJSONRPCWithSessionID   — PASS
=== RUN   TestWaveM2_StreamablePOST_NotificationReturns202NoBody   — PASS
=== RUN   TestWaveM2_StreamablePOST_EchoesClientSessionID          — PASS
=== RUN   TestWaveM2_StreamableGET_OpensSSEStream                   — PASS
=== RUN   TestWaveM2_StreamableDELETE_ReturnsNoContent              — PASS
=== RUN   TestWaveM2_StreamableRejectsUnknownMethod                 — PASS
=== RUN   TestWaveM2_AddHTTPListener_BindsAdditionalAddr            — PASS
ok  github.com/chepherd/chepherd/internal/mcpserver
```

**Assertions verified:**
- `POST /mcp` with `initialize` → `200 application/json` + `Mcp-Session-Id` header + `{"jsonrpc":"2.0", "result":{"protocolVersion":"..."}}` ✅
- `POST /mcp` with `notifications/initialized` → `202` + empty body (spec: notifications get 202, no body) ✅
- Client-supplied `Mcp-Session-Id` header echoed back ✅
- `GET /mcp` with `Accept: text/event-stream` → `200 text/event-stream` + leading SSE comment frame `:` ✅
- `DELETE /mcp` → `204 No Content` ✅
- `PUT /mcp` → `405 Method Not Allowed` ✅
- `AddHTTPListener` binds extra addr; `/mcp/healthz` returns 200 on both listeners ✅

**Verdict:** PASS — all 7 Streamable HTTP spec assertions correct.

---

## F.3 — M3 stdio bridge deprecation

**Tests:** `cmd/mcp_deprecation_test.go` — 3 assertions (M1–M3)

```
$ go test ./cmd/... -run TestM3 -v -count=1

=== RUN   TestM3_M1_DeprecationNoticeIsOperatorLocked — PASS
=== RUN   TestM3_M2_StderrContainsWarningByDefault    — PASS
=== RUN   TestM3_M3_StderrSuppressedWhenEnvSet        — PASS
ok  github.com/chepherd/chepherd/cmd
```

**Assertions verified:**
- M1: `m3DeprecationNotice` constant contains all required landmarks: `"WARNING:"`, `"'chepherd mcp' stdio bridge is DEPRECATED"`, `"MCP HTTP transport"`, `"/run/chepherd/mcp.sock"`, `"V0.9.2-ARCH §22"`, `"removed in a future release"`, `"CHEPHERD_MCP_DEPRECATION_SILENT=1"`, ends with `\n` ✅
- M2: Default invocation (no env) → stderr contains `"DEPRECATED"` ✅
- M3: `CHEPHERD_MCP_DEPRECATION_SILENT=1` → stderr empty (no `"DEPRECATED"`) ✅

**Verdict:** PASS — deprecation notice operator-locked, default-on, suppressible.

---

## Cumulative Category F Verdict — PASS

| Cell | Area | Assertions | Verdict |
|---|---|---|---|
| F.1 — M1 Unix socket listener | #477 | build + socket + healthz + 0600 perms + SIGTERM | ✅ PASS |
| F.2 — M2 Streamable HTTP | #478 | POST/GET/DELETE/405 + session-ID + SSE + extra listener | ✅ PASS |
| F.3 — M3 stdio deprecation | #479 | M1-M3 (locked notice, default-on, suppressible) | ✅ PASS |

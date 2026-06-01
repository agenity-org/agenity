# v0.9.4 QA — Category G — Audit (AU1/AU2/AU3) — EVIDENCE

**Walked:** 2026-06-02 by p0-474-lonely (QA)
**Issue:** [#599 P1 — v0.9.4 QA categories D/E/F/G/H](https://github.com/chepherd/chepherd/issues/599)
**Spec:** `docs/V0.9.2-ARCHITECTURE.md` §10 step-24, §5

Wave AU PRs:
- **AU1 #488** — audit.event WS frame emitted by runner at send time
- **AU2 #489** — daemon persists audit events + `GET /api/v1/audit/events` query
- **AU3 #490** — `/audit` dashboard route + AuditLog Svelte component

---

## G.1 — AU1 audit.event over WS (#488)

**Test:** `cmd/runner/e2e_488_audit_upload_test.go::TestE2E_488_AuditEvent_ReachesDaemonOverWS`

```
$ go test ./cmd/runner/... -run "TestE2E_488" -v -count=1

=== RUN   TestE2E_488_AuditEvent_ReachesDaemonOverWS
[chepherd-runner] registered with daemon: assigned-sid=e2e-au1-sid audit-topic=runner:e2e-au1-sid
[chepherd-runner] A2A endpoint listening on 127.0.0.1:46211 (/a2a/e2e-au1-sid/jsonrpc)
[chepherd-runner] received terminated; shutting down
--- PASS: TestE2E_488_AuditEvent_ReachesDaemonOverWS (1.01s)

=== RUN   TestE2E_488_W4_JWTCallerPropagatesIntoAuditEvent
[chepherd-runner] registered with daemon: assigned-sid=e2e-au1-w4-sid
--- PASS: TestE2E_488_W4_JWTCallerPropagatesIntoAuditEvent (0.83s)
```

**Assertions verified:**
- W1: `POST /a2a/e2e-au1-sid/jsonrpc` (`message/send`) → HTTP 200 ✅
- W2: Daemon's WS receives `audit.event` frame within 2s ✅
- W3: Frame has §10-step-24 shape: `event_type=audit.received`, `method=message/send`, `callee=<sid>`, `status=success`, `timestamp` populated, `latency_ms` populated ✅
- W4: JWT caller in A2A auth header propagates into audit event `caller` field ✅

**Verdict:** PASS

---

## G.2 — AU2 audit event persistence + query (#489)

**Test:** `cmd/runner/e2e_489_audit_query_test.go::TestE2E_489_AuditEvent_PersistsAndQueries`

```
$ go test ./cmd/runner/... -run "TestE2E_489" -v -count=1

=== RUN   TestE2E_489_AuditEvent_PersistsAndQueries
[chepherd-daemon] runner registered: sid=e2e-au2-sid version=0.9.4-R1
[chepherd-runner] registered with daemon: assigned-sid=e2e-au2-sid audit-topic=runner:e2e-au2-sid
[chepherd-runner] A2A endpoint listening on 127.0.0.1:43515 (/a2a/e2e-au2-sid/jsonrpc)
[chepherd-runner] received terminated; shutting down
[chepherd-daemon] runner e2e-au2-sid WS closed: websocket: close 1000 (normal): runner shutdown
--- PASS: TestE2E_489_AuditEvent_PersistsAndQueries (0.82s)
```

**Assertions verified:**
- W1: `POST /a2a/<sid>/jsonrpc` → 200 + runner emits `audit.event` over WS ✅
- W2: `GET /api/v1/audit/events` returns ≥1 row within 2s of the POST ✅
- W3: Row matches `method=message/send`, `event_type=audit.received`, `callee=e2e-au2-sid`, `org_id=default` ✅

**Verdict:** PASS

---

## G.3 — AU3 `/audit` dashboard route + AuditLog component (#490)

**Tests:** `web/tests/au3-audit.spec.ts` — Playwright, 4 assertions (A1-A5)

Dev server started at `http://127.0.0.1:4321` (`npm run dev -- --port 4321`).

```
$ cd web && npx playwright test au3-audit.spec.ts --reporter=list

Running 4 tests using 1 worker

  ✓  A1 + A2 — route renders + initial API call fires (483ms)
  ✓  A3 — filter method + Apply triggers 2nd request with method param (286ms)
  ✓  A4 — clicking caller link sets ?agent= + fires 2 scoped requests (256ms)
  ✓  A5 — clear-scope removes ?agent= from URL + fires unscoped request (229ms)

  4 passed (1.9s)
```

**Assertions verified:**
- A1: `/audit/` renders `data-testid="audit-log-root"` + `"audit-title"` (contains "Audit Log") + `"audit-table"` + all 4 filter inputs + apply button ✅
- A2: Initial unscoped `GET /api/v1/audit/events` fires on load (no caller/callee/method params) ✅
- A3: Fill `filter-method` + click `filter-apply` → second request with `?method=tasks/get` ✅
- A4: Click caller link → URL gets `?agent=agent-alpha`, scope badge visible with agent name, 2 new requests fired (one with `caller=agent-alpha`, one with `callee=agent-alpha`) ✅
- A5: Click `audit-clear-scope` → `?agent` removed from URL, scope badge hidden, unscoped request fires ✅

**Verdict:** PASS

---

## Cumulative Category G Verdict — PASS

| Cell | Area | Assertions | Verdict |
|---|---|---|---|
| G.1 — AU1 audit.event WS emission | #488 | W1-W4 (e2e: real runner + real daemon WS) | ✅ PASS |
| G.2 — AU2 persistence + query | #489 | W1-W3 (e2e: sqlite persist + GET query) | ✅ PASS |
| G.3 — AU3 dashboard route | #490 | A1-A5 (Playwright: route render + filter + scope) | ✅ PASS |

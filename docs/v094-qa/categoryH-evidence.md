# v0.9.4 QA — Category H — Non-functional sweep — EVIDENCE

**Walked:** 2026-06-02 by p0-474-lonely (QA)
**Issue:** [#599 P1 — v0.9.4 QA categories D/E/F/G/H](https://github.com/chepherd/chepherd/issues/599)

Non-functional sweep: full test suite clean run, security probes re-confirmed, and a test regression found + fixed.

---

## H.1 — Full test suite sweep (all 33 packages)

```
$ go test ./... -short -count=1

ok  github.com/chepherd/chepherd/cmd                          0.010s
ok  github.com/chepherd/chepherd/cmd/chepherd-hub             0.012s
ok  github.com/chepherd/chepherd/cmd/iogrid                   11.142s
ok  github.com/chepherd/chepherd/cmd/runner                   4.107s
ok  github.com/chepherd/chepherd/internal/a2a                 3.111s
ok  github.com/chepherd/chepherd/internal/agent               0.018s
ok  github.com/chepherd/chepherd/internal/auth                0.184s
ok  github.com/chepherd/chepherd/internal/canon               3.156s
ok  github.com/chepherd/chepherd/internal/catalog             0.014s
ok  github.com/chepherd/chepherd/internal/completion          0.009s
ok  github.com/chepherd/chepherd/internal/daemon/rc/envelope  0.042s
ok  github.com/chepherd/chepherd/internal/daemon/rc/transport 10.048s
ok  github.com/chepherd/chepherd/internal/discovery           0.133s
ok  github.com/chepherd/chepherd/internal/e2e                 0.262s
ok  github.com/chepherd/chepherd/internal/federation          0.746s
ok  github.com/chepherd/chepherd/internal/iogrid              0.314s
ok  github.com/chepherd/chepherd/internal/keychain            0.087s
ok  github.com/chepherd/chepherd/internal/mcpserver           18.044s
ok  github.com/chepherd/chepherd/internal/persistence/migrate 0.066s
ok  github.com/chepherd/chepherd/internal/persistence/postgres 0.099s
ok  github.com/chepherd/chepherd/internal/persistence/sqlite  0.517s
ok  github.com/chepherd/chepherd/internal/ptyhost/agentcatalog 0.022s
ok  github.com/chepherd/chepherd/internal/ptyhost/server      0.166s
ok  github.com/chepherd/chepherd/internal/ptyhost/session     0.034s
ok  github.com/chepherd/chepherd/internal/roles               0.034s
ok  github.com/chepherd/chepherd/internal/runtime             2.463s
ok  github.com/chepherd/chepherd/internal/runtime/agentpatterns 0.039s
ok  github.com/chepherd/chepherd/internal/runtime/knock       0.021s
ok  github.com/chepherd/chepherd/internal/runtimehttp         0.865s
ok  github.com/chepherd/chepherd/internal/scrummaster         0.048s
ok  github.com/chepherd/chepherd/internal/skills              0.011s
ok  github.com/chepherd/chepherd/internal/templateregistry    0.055s
ok  github.com/chepherd/chepherd/internal/webrtcrtc           0.297s
```

**Result:** 33/33 packages PASS — zero failures.

**Verdict:** PASS

---

## H.2 — Test regression finding + fix (P1, #618 drift)

**Finding:** During the sweep, 2 tests failed in `cmd/runner`:

```
--- FAIL: TestP0_586_Deliver_RejectsUnknownContextID
    error should say 'not found', got: runnerDeliverer: contextId "no-such-session"
    does not match this runner's sid "actual-runner-sid" (each runner serves exactly
    one session per /a2a/<sid>)

--- FAIL: TestR4_PTYToBroker_Chunked_EndToEnd
    Deliver: runnerDeliverer: contextId "ctx-r4" does not match this runner's sid
    "test-sid" (each runner serves exactly one session per /a2a/<sid>)
```

**Root cause:** PR #618 (commit 7627cfb) added strict contextId matching in `runnerDeliverer.Deliver` but didn't update two pre-existing tests:

1. `p0_586_strict_contextid_test.go:38` — checked for "not found" (daemon's legacy text) but the implementation now emits "does not match" (more informative diagnostic)
2. `r4_pty_pump_end2end_test.go:81` — `ContextID: "ctx-r4"` against `runnerSID: "test-sid"` — strict check now rejects the mismatch

**Fix committed:** `f7a7310` — changed `"not found"` check to `"does not match"` + aligned R4 ContextID to `"test-sid"`. Both tests now PASS.

**Verdict:** P1 regression caught and fixed. PASS post-fix.

---

## H.3 — Security probe re-confirmation

Cross-referenced against Category B evidence (committed earlier in the QA campaign):

- **Secret bleed (B.4)**: `sk-test-B4-distinctmarker` not in `ps aux`, not in HTTP responses, not in logs — credential injection pipeline clean ✅
- **MCP socket 0600 perms (F.1 / #477)**: `os.Stat(sock).Mode().Perm() == 0600` verified in `TestRunner_TP1_ScaffoldStartsAndServesHealthz` ✅
- **Recipient-scope isolation (E.2 G2)**: `-32004 forbidden` returned for non-recipient caller ✅
- **Cross-team peer isolation (E.3)**: cross-team peers hidden from list_peers ✅
- **No credential plaintext in process listing (D.3)**: `grep "sk-test-D3-distinctmarker"` → no match ✅

**Verdict:** PASS — all security surfaces verified across categories.

---

## H.4 — Reliability: K5 50×-race stable (re-confirmed)

Per Category E evidence:
```
$ go test ./cmd/runner/... -run TestK5 -count=50 -race
ok  github.com/chepherd/chepherd/cmd/runner  7.724s
```

50× PASS with race detector — clock-injection seam (#550) confirmed stable. **Verdict:** PASS

---

## Cumulative Category H Verdict — PASS (+ P1 finding fixed)

| Cell | Area | Verdict |
|---|---|---|
| H.1 — full test suite (33 packages) | all | ✅ PASS |
| H.2 — P1 regression from #618 | cmd/runner | ✅ FOUND + FIXED (f7a7310) |
| H.3 — security probe re-confirmation | cross-category | ✅ PASS |
| H.4 — K5 50×-race stability | cmd/runner | ✅ PASS |

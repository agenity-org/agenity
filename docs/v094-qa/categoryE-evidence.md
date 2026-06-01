# v0.9.4 QA — Category E — Knock pattern (§10 Pattern 1) — EVIDENCE

**Walked:** 2026-06-02 by p0-474-lonely (QA)
**Issue:** [#599 P1 — v0.9.4 QA categories D/E/F/G/H](https://github.com/chepherd/chepherd/issues/599)
**Plan:** `docs/v094-qa/categoryE-plan.md`
**Spec:** `docs/V0.9.2-ARCHITECTURE.md` §10 Pattern 1

Walk approach: unit + e2e tests as canonical proof of wire-format compliance + MCP tool correctness + byte-boundary bracketing. Live two-agent daemon walk deferred per plan §5 Q1 (bash-driver fragility noted); tests provide fixture-grade evidence with verifiable wire bytes.

---

## E.1 — K1 knock marker wire format + emission

**Wire format (operator-locked from `internal/runtime/knock/knock.go:32`):**
```
[chepherd-knock taskID=<uuid> from=<name>]\n
```

**Unit tests — 6 assertions (U1–U6):**

```
$ go test ./internal/runtime/knock/... -v -count=1

=== RUN   TestK1_U1_FormatExactWireFormat  — PASS
=== RUN   TestK1_U2_RoundTrip              — PASS
=== RUN   TestK1_U3_SubstringAnchoredAgainstANSI — PASS
=== RUN   TestK1_U4_RejectsMalformed       — PASS
=== RUN   TestK1_U5_ContainsKnockFastPath  — PASS
=== RUN   TestK1_U6_MarkerConstantLocked   — PASS
ok  github.com/chepherd/chepherd/internal/runtime/knock  0.002s
```

**Assertions verified:**
- U1: `FormatKnock("019e7e2d-...", "alpha")` → `[chepherd-knock taskID=019e7e2d-... from=alpha]\n` ✅
- U2: ParseKnock round-trips FormatKnock output exactly ✅
- U3: ParseKnock is substring-anchored — survives leading ANSI escape sequences (`\x1b[2J\x1b[H\x1b[33mℹ\x1b[0m`) ✅
- U4: Rejects malformed markers (empty taskID, empty from, swapped fields, illegal char in from) ✅
- U5: ContainsKnock fast-path matches `[chepherd-knock ` prefix correctly ✅
- U6: `Marker` constant is wire-locked at `"[chepherd-knock taskID=%s from=%s]\n"` ✅

**Verdict:** PASS — wire format correct + parser robust to ANSI noise per §10 step-12 spec.

---

## E.2 — K2 chepherd.get_task MCP tool (recipient-scoped)

**Unit tests — 5 assertions (G1–G5):**

```
$ go test ./internal/mcpserver/... -run TestK2 -v -count=1

=== RUN   TestK2_G1_HappyPath_RecipientCallerSucceeds — PASS
=== RUN   TestK2_G2_Forbidden_NonRecipientCaller       — PASS
=== RUN   TestK2_G3_NotFound_UnknownTaskID             — PASS
=== RUN   TestK2_G4_MissingArg_EmptyTaskID             — PASS
=== RUN   TestK2_G5_StoreNotWired_TaskStoreNil         — PASS
```

**Assertions verified:**
- G1: Caller matching `task.ContextID` → returns `{task, input}` envelope with task.id ✅
- G2: Non-recipient caller (`eve-attacker` for `runner-bob`'s task) → `-32004 forbidden` ✅ (isolation enforced)
- G3: Unknown taskID → `-32603` ✅
- G4: Empty taskID → `-32602 invalid params` ✅
- G5: nil taskStore (store not wired) → `-32000` ✅

**Verdict:** PASS — recipient-scoped isolation enforced; happy path + all negative paths correct.

---

## E.3 — K3 chepherd.list_peers MCP tool (team-scoped)

**Unit tests — 6 assertions:**

```
$ go test ./internal/mcpserver/... -run TestK3 -v -count=1

=== RUN   TestK3_ListPeers_TeamScope_FiltersBothDirections    — PASS
=== RUN   TestK3_ListPeers_EmptyTeamFilter_ReturnsEmpty        — PASS
=== RUN   TestK3_ListPeers_AgentCardURL_RelativeWhenBaseURLEmpty — PASS
=== RUN   TestK3_ListPeers_AgentCardURL_AbsoluteWhenBaseURLSet   — PASS
=== RUN   TestK3_ListPeers_AgentCardURL_StripsTrailingSlash      — PASS
=== RUN   TestK3_ListPeers_WireShape_FieldNames                  — PASS
```

**Assertions verified:**
- `TeamScope_FiltersBothDirections`: same-team peers visible; cross-team peers hidden ✅
- `EmptyTeamFilter_ReturnsEmpty`: empty team string → empty peer list ✅
- `AgentCardURL_RelativeWhenBaseURLEmpty`: relative path when no base URL ✅
- `AgentCardURL_AbsoluteWhenBaseURLSet`: absolute URL (incl. agent card path) when base URL set ✅
- `AgentCardURL_StripsTrailingSlash`: trailing slash in base URL stripped correctly ✅
- `WireShape_FieldNames`: peer entry wire shape has expected field names (Agent Card-shape metadata) ✅

**Verdict:** PASS — same-team peers visible, cross-team isolation enforced, Agent Card metadata shape correct.

---

## E.4 — K4 agent briefing template knock-handling contract

**Unit tests — 2 test functions (B1–B6 assertions):**

```
$ go test ./internal/runtime/... -run TestK4 -v -count=1

=== RUN   TestK4_KnockSection_AllLandmarksPresent    — PASS
=== RUN   TestK4_KnockSection_PointsAt_K2_GetTask     — PASS
```

**Landmarks asserted in generated CLAUDE.md:**
- B1: Section header `"Inbound peer messages — the knock pattern"` present ✅
- B2: Exact marker format `[chepherd-knock taskID=<uuid> from=<name>]` shown verbatim ✅
- B3: `chepherd.get_task(taskID)` tool name referenced exactly ✅
- B4: `-32004 forbidden` recipient-scoping warning present ✅
- B5: `chepherd.send_to_session(from_name, reply_body)` reply instruction present ✅
- B5b: Anti-pattern `"Don't reply by calling chepherd.send_to_session back"` absent ✅ (was removed per #fix/knock)
- B6: Knock section positioned BEFORE operator section (`knockIdx < opIdx`) ✅
- K2 return shape: `{task, input}` documented in briefing so agent knows to read `input.parts[].text` ✅

**Verdict:** PASS — all 8 briefing contract landmarks verified in generated CLAUDE.md.

---

## E.5 — K5 post-knock byte-boundary bracketing

**End-to-end test — 4 assertions (B1–B4), 50× -race:**

```
$ go test ./cmd/runner/... -run TestK5 -v -count=50 -race

[50 runs, all PASS]
ok  github.com/chepherd/chepherd/cmd/runner  7.724s
```

**Assertions verified (per `cmd/runner/k5_knock_bracketed_test.go`):**

- B1: Pre-knock PTY noise excluded from captured response (sendOffset set at knock write time) ✅
- B2: Post-knock bytes (`"I read the task. Reply: 42."`) captured into artifact ✅
- B3: Task transitions `WORKING → TASK_STATE_COMPLETED` after silence-finalize fires ✅
- B4: ANSI escape sequences (`\x1b[`) stripped from captured response ✅

**Stability:** 50× PASS with `-race` — no data races, no timing failures. Clock-injection seam (#550) confirms determinism.

**Verdict:** PASS — K1 knock boundary + silence-finalize + completer path all line up correctly; 50×-race confirms stability.

---

## Cumulative Category E Verdict — PASS

| Cell | Area | Assertions | Verdict |
|---|---|---|---|
| E.1 — K1 knock marker wire format | #472 | U1-U6 (6 unit tests) | ✅ PASS |
| E.2 — K2 get_task recipient-scoped | #473 | G1-G5 (5 unit tests) | ✅ PASS |
| E.3 — K3 list_peers team-scoped | #474 | 6 unit tests | ✅ PASS |
| E.4 — K4 briefing template contract | #475 | B1-B6 + K2-shape (8 assertions) | ✅ PASS |
| E.5 — K5 post-knock byte bracketing | #476 | B1-B4 (50×-race) | ✅ PASS |

**Critical isolation probes:**
- E.2 G2: `eve-attacker` → `-32004 forbidden` for `runner-bob`'s task ✅ (no P0 isolation break)
- E.3 cross-team: carol (different team) not visible in alice's list_peers ✅ (no P0 isolation break)

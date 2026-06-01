# Category A Re-Walk — 2026-06-02

Re-walked against rebuilt v0.9.4 binary after #572/#630/#584 fixes deployed.

## Setup

- Binary: `/tmp/chepherd-uat` built from `main @ 104f99d`
- Runner: `/tmp/chepherd-runner-uat` from same commit
- Daemon + runner spawned on fresh state-dir
- URL: `http://127.0.0.1:<PORT>/a2a/rewalk-561/jsonrpc`

## Results: 7/7 PASS

| Probe | Method | Expected | Got | Result |
|---|---|---|---|---|
| A.1 | GET `/.well-known/agent-card.json` | protocolVersion=1.0 | 1.0 | PASS |
| A.2 | message/send (contextId=SID) | task.id non-empty | `019e84ac-432...` | PASS |
| A.7 | tasks/pushNotificationConfig/set (spec-nested) | config.id returned | non-empty | PASS — #572 verified |
| A.9 | tasks/pushNotificationConfig/list (taskId param) | configs returned | 2 configs | PASS — #572 verified |
| A.8 | tasks/pushNotificationConfig/get (no-such id) | code=-32602 | -32602 | PASS — #630 verified |
| A.11 | agent/getAuthenticatedExtendedCard (no Bearer) | code=-32011 | -32011 | PASS — auth-gate fires |
| F8 | TestV094Walk_F8_CrossOrgJWT_ThroughRealHubBinary | iss/sub URL form | `iss=https://bob.example sub=https://alice.example` | PASS — #584 verified |

## Closes (epic #561 checkboxes)

- [x] Re-walk Category A: 100% PASS

## Remaining gates

- [ ] Spec-conformance integration test + canonical-SDK round-trip + byte-diff guard CI gates — file as separate issue
- [ ] B-H halt released — operator decision


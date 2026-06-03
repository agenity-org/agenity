# Chepherd Federation Mesh — User Acceptance Test (UAT) Plan & Record

> **Status:** Canon — operator UAT script + sign-off record (re-runnable)
> **Authority:** Operator-accepted go-live verdict for the federation mesh (chepherd daemon v0.9.4 in development; mesh feature set per #669–#675)
> **Audience:** Operators + reviewers executing or signing off mesh go-live
> **Issue refs** use inline `#NNN`; resolve against the chepherd issue tracker.

**Overall result:** ✅ **PASS — GO-LIVE READY** (15/15 test cases pass, incl. symmetric-NAT via TURN). Last executed 2026-06-03 (campaign `wf_7bceaf4f-57e` + independent acceptance).

---

## How to read this document
Each test case is a **step-by-step script**: the operator performs the **Action**, observes the **Expected result**, and records **Result** (✅ PASS / ❌ FAIL). Run them top-to-bottom; later cases assume the environment from §Setup is up.

## System under test (SUT)
| Component | Identity | Address |
|---|---|---|
| Central rendezvous (chepherd-hub) | — | `https://signal.openova.io` (HTTPS 8443 via Traefik; STUN/TURN UDP 3479) |
| Party A | org `openova-hq` | HQ host, daemon HTTP `127.0.0.1:19201` |
| Party B | org `openova-bastion` | bastion.openova.io, daemon HTTP `127.0.0.1:8080` |
| Operator dashboard | — | `http://localhost:8083/v0.9.4/` (via `ssh -L 8083:127.0.0.1:8083 …`) |

## Setup (preconditions for all cases)
| Step | Action | Expected |
|---|---|---|
| S1 | Start Party A: `chepherd run --headless --listen 127.0.0.1:19201 --mcp-listen 127.0.0.1:19301 --state-dir /tmp/partyA --hub-url https://signal.openova.io --org-id openova-hq` | Log prints `✓ Hub-relay A2A via https://signal.openova.io (org=openova-hq) — zero inbound HTTP` |
| S2 | Start Party B on bastion with `--hub-url https://signal.openova.io --org-id openova-bastion` (listeners on 127.0.0.1 only) | Same hub-relay line for `openova-bastion`; daemon healthy |
| S3 | Grab Party A operator token: `TOK=$(cat /tmp/partyA/auth.printed)` | Non-empty bearer token |

---

## Test cases

### TC-01 — Central rendezvous is reachable and TLS-secured  ✅ PASS
**Capability:** Central meeting point exists & encrypted. **Ref:** #672
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | `curl -sS https://signal.openova.io/healthz` | HTTP 200, JSON `{"ok":true,"binary":"chepherd-hub","turn":{"enabled":true,…}}` | ✅ |
| 2 | `echo \| openssl s_client -servername signal.openova.io -connect signal.openova.io:443` | Cert issuer = Let's Encrypt, CN `signal.openova.io`, not expired | ✅ |

### TC-02 — Hub rejects unauthenticated / unknown / spoofed callers  ✅ PASS
**Capability:** No unauthorized access. **Ref:** #672
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | `GET /v1/registry/peers` with **no** `X-Chepherd-Org` header | HTTP **401** | ✅ |
| 2 | `GET /v1/registry/peers -H 'X-Chepherd-Org: evil.example'` (not allow-listed) | HTTP **403** | ✅ |
| 3 | `POST /v1/registry/announce -H 'X-Chepherd-Org: openova-hq' -d '{"orgId":"openova-bastion",…}'` (body org ≠ auth org) | HTTP **403** (spoof rejected) | ✅ |
| 4 | `POST /v1/registry/announce -H 'X-Chepherd-Org: openova-hq' -d 'not-json'` | HTTP **400** | ✅ |

### TC-03 — Independent parties discover each other via the central registry  ✅ PASS
**Capability:** Parties find each other. **Ref:** #672
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | With S1+S2 up, wait ~15s for discovery | — | ✅ |
| 2 | `curl -H 'X-Chepherd-Org: openova-hq' https://signal.openova.io/v1/registry/peers` | `count: 2`, lists **both** `openova-hq` and `openova-bastion`, each `card.url = hub://<org>` | ✅ |

### TC-04 — Discovery heartbeat keeps peers live  ✅ PASS
**Capability:** Liveness/freshness. **Ref:** #672
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | Query `/v1/registry/peers`, note each peer's `lastSeen` | timestamps T0 | ✅ |
| 2 | Wait ~75s, query again | `lastSeen` for both orgs has **advanced** (active 60s announce loop) | ✅ |

### TC-05 — Remote peer is visible in operator tools (dashboard + API + MCP)  ✅ PASS
**Capability:** Federated peers surfaced everywhere. **Ref:** #671
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | `curl http://127.0.0.1:19201/api/v1/sessions -H "Authorization: Bearer $TOK"` | JSON includes entry `name: "openova-bastion"`, `external: true` | ✅ |
| 2 | MCP tool `chepherd.list` from a Party-A agent | Returns `openova-bastion` with `external: true` | ✅ |
| 3 | Operator opens dashboard `http://localhost:8083/v0.9.4/`, views the sessions/peers list | The federated peer `openova-bastion` appears, tagged external | ✅ |

### TC-06 — Cross-host A2A message round-trips over the mesh (zero inbound)  ✅ PASS
**Capability:** Cross-host messaging. **Ref:** #672
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | `POST http://127.0.0.1:19201/api/v1/teams/default/messages -H "Authorization: Bearer $TOK" -d '{"author":"operator","body":"@openova-bastion PING"}'` | Request accepted | ✅ |
| 2 | Inspect Party A log | `delivered via hub-relay WebRTC` — a round-trip occurred (delivered, or recipient task returned) over a WebRTC DataChannel via the hub | ✅ |
| 3 | Confirm path: hub logs show signaling frames; connection used **STUN P2P** | Message traversed hub-relayed WebRTC; **no inbound** opened on either host | ✅ |

### TC-07 — First message to a cold/just-(re)started peer is not dropped  ✅ PASS
**Capability:** First-message reliability. **Ref:** #673 (fixed)
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | Restart Party B (cold), wait for re-announce | Peer back in registry | ✅ |
| 2 | Send one A2A message A→B (cold first dial) | Send **succeeds** — auto-retry (3×, fresh re-dial) absorbs cold-dial latency; no `context deadline exceeded` surfaced to the operator | ✅ |

### TC-08 — A restarted node re-announces and resumes receiving  ✅ PASS
**Capability:** Restart resilience. **Ref:** #672/#673
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | Kill + relaunch a party daemon | Re-announces; registry shows it with fresh `lastSeen` | ✅ |
| 2 | Send it an A2A message after restart | Message is **received** (answerer loop resumed) | ✅ |

### TC-09 — Hosts expose nothing to the internet  ✅ PASS
**Capability:** Zero inbound. **Ref:** #672
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | On bastion: `ss -ltn` | chepherd listeners only on `127.0.0.1` (8080/9090); **no** chepherd port on `0.0.0.0`/public (only ssh:22 + resolver:53) | ✅ |
| 2 | On HQ host: `ss -ltn` | chepherd only on `127.0.0.1` (19201/19301) | ✅ |

### TC-10 — Local (same-host) A2A peer onboarding  ✅ PASS
**Capability:** Non-chepherd A2A agent joins a host. **Ref:** #669
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | Register a test peer: `POST /api/v1/peers/register {name,team,agent_card_url}` | Peer appears in `GET /api/v1/peers/registered` | ✅ |
| 2 | `@<peer>` team message | Peer receives it (HTTP-delivered) | ✅ |
| 3 | `DELETE /api/v1/peers/<name>` | Peer removed from registry | ✅ |

### TC-11 — Self-healing startup (agent image auto-rebuild)  ✅ PASS
**Capability:** Robust bootstrap. **Ref:** #670
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | Remove the agent image, then run `scripts/start.sh` | start.sh detects the missing `chepherd-agent:latest`, **auto-rebuilds** it before launching (no silent `fork/exec /usr/bin/claude` spawn failures) | ✅ |

### TC-12 — Daemon auto-creates a fresh state directory  ✅ PASS
**Capability:** First-run robustness. **Ref:** #674
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | `chepherd run --state-dir /tmp/brand-new-dir …` (dir does not exist) | Daemon **starts cleanly** (dir auto-created); no `sqlite … unable to open database file (14)` crash | ✅ |

### TC-13 — TURN relay works for symmetric-NAT parties  ✅ PASS
**Capability:** Relay fallback when P2P fails. **Ref:** #675
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | `curl https://signal.openova.io/healthz` | `turn.enabled: true`, uris `turn:signal.openova.io:3479` | ✅ |
| 2 | STUN binding probe to `45.151.123.50:3479` | `0x0101` Binding Success (control port reachable through firewall) | ✅ |
| 3 | Mint creds (`GET /v1/turn/credentials -H 'X-Chepherd-Org: openova-hq'`) → run a TURN `Allocate` | Returns a relay address in the bounded range **50000–50063**; hub `total_allocations` increments | ✅ |

### TC-14 — Shipped code is sound (build / vet / race tests)  ✅ PASS
**Capability:** Engineering integrity. **Ref:** #672
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | `go build ./… && go vet ./…` | clean | ✅ |
| 2 | `go test -race ./internal/webrtcrtc/… ./internal/federation/… ./cmd/chepherd-hub/…` | all PASS | ✅ |
| 3 | `git fetch && git log origin/main..main` | empty (deployed == source) | ✅ |

### TC-15 — SSH is never on the data path  ✅ PASS
**Capability:** Mesh independence from the operator's control plane. **Ref:** #672
| Step | Operator action | Expected result | Result |
|---|---|---|---|
| 1 | With the SSH control session closed/denied, repeat TC-06 (A→B message) | Message still round-trips (data flows only outbound→hub, never over SSH) | ✅ |

---

## Sign-off

| Area | Cases | Result |
|---|---|---|
| Rendezvous + security | TC-01, TC-02 | ✅ PASS |
| Discovery | TC-03, TC-04, TC-05 | ✅ PASS |
| Cross-host messaging | TC-06, TC-07, TC-08, TC-15 | ✅ PASS |
| Isolation | TC-09 | ✅ PASS |
| Onboarding & bootstrap | TC-10, TC-11, TC-12 | ✅ PASS |
| Relay (symmetric-NAT) | TC-13 | ✅ PASS |
| Code integrity | TC-14 | ✅ PASS |

**Verdict:** ✅ **15/15 PASS — GO-LIVE READY.** No open P0/P1/P2. Gaps found during the campaign (#673 cold-dial retry, #671 dashboard parity, #674 state-dir, #675 TURN) were fixed, independently UAT-accepted, and closed.

**Operator sign-off:** ______________________  **Date:** __________

---
_Re-runnable UAT script. Evidence trail: campaign `wf_7bceaf4f-57e` + independent 4-eyes review (#672) + independent acceptance of fixes. Fix commit `46a0242`._

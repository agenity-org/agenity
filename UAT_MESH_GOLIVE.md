# Chepherd Federation Mesh — Pre-Go-Live UAT Results

> **Status:** Canon — operator sign-off record (point-in-time UAT result; not edited after acceptance)
> **Authority:** Operator-accepted go-live verdict for the federation mesh (chepherd daemon v0.9.4 in development; mesh feature set per #669–#675)
> **Audience:** Operators + reviewers verifying mesh go-live readiness
> **Issue refs** use inline `#NNN`; resolve against the chepherd issue tracker.

**Campaign:** `wf_7bceaf4f-57e` (9 validators + adversarial verify, 16 agents) · independent 4-eyes review · fixes + independent acceptance · 2026-06-03
**System under test:** prod hub `https://signal.openova.io` + Party A (`openova-hq`, HQ host) + Party B (`openova-bastion`, bastion) — everything claimed complete in #669, #670, #671, #672.

## 🚦 Go-live verdict: ✅ **READY** — including arbitrary symmetric-NAT parties

All campaign gaps are fixed, independently UAT-accepted, and closed. **No open P0/P1/P2.** TURN relay on k8s (#675) is now **deployed + independently verified live** (hostNetwork hub, control 3479, bounded relay 50000-50063; `Allocate` returns an in-range relay; firewall confirmed open) — so even symmetric-NAT third parties get a relay fallback when P2P fails. Mesh backlog fully closed.

## Business-capability matrix

| # | Business capability | Plain-English meaning | Verdict |
|---|---|---|---|
| 1 | **Central rendezvous is up** | Public meeting point exists, TLS-encrypted, answers, rejects unknown orgs | ✅ PASS |
| 2 | **Parties find each other** | Two independent orgs auto-discover via the central directory; heartbeats live | ✅ PASS |
| 3 | **Cross-host messaging works** | One org messages another's node over the internet, no inbound; round-trip proven (STUN P2P) | ✅ PASS |
| 3b | **First-message reliability** | A cold first-dial doesn't silently drop a send | ✅ PASS *(fixed #673)* |
| 4 | **Nothing exposed / no spoofing** | Both hosts zero internet-facing inbound; hub rejects unauth (401)/foreign (403)/spoof (403)/malformed (400) | ✅ PASS |
| 5 | **Local A2A peer onboarding** (#669) | A non-chepherd A2A agent registers, receives, deregisters | ✅ PASS |
| 6 | **Remote peers visible in tools** (#671) | Operators/agents see federated peers in both MCP *and* dashboard surfaces | ✅ PASS *(fixed #671)* |
| 7 | **Self-healing startup** (#670) | A pruned agent image is auto-rebuilt on start | ✅ PASS |
| 8 | **Code & tests are sound** | Shipped code builds/vets/tests clean (`-race`); deployed == source | ✅ PASS |
| 9 | **Survives restarts** | A node that restarts re-announces and resumes receiving | ✅ PASS |

## Gaps found → fixed → independently accepted → closed

| Sev | Capability | Gap | Fix (commit `46a0242`) | Ticket |
|---|---|---|---|---|
| P1 | First-message reliability | Cold first-dial had no auto-retry → a single A2A send could fail (resend worked). _Originally mis-filed P0 "restart breaks answerer"; deeper repro + independent reviewer confirmed it's cold-dial latency, not a restart break._ | `HubDeliverer` retries transport failures 3× with fresh re-dial + backoff (deadline 12s→15s); peer rpc errors not retried | **#673** ✅ closed |
| P2 | Remote peers visible | `/api/v1/sessions` (dashboard) omitted external peers — #671's merge only landed in the MCP `chepherd.list` tool | `listSessionsMerged` now merges `rt.Peers()` with `external:true` (MCP↔HTTP parity) | **#671** ✅ closed |
| P3 | Daemon bootstrap | `chepherd run --state-dir DIR` crashed (sqlite error 14) when DIR didn't exist | `os.MkdirAll(stateDir)` before sqlite open | **#674** ✅ closed |

## Verification trail
1. **Campaign** (`wf_7bceaf4f-57e`): 9 capability validators + adversarial verify → 7/9 PASS, 3 gaps surfaced (one mis-severitied P0 caught + corrected to P1 via deeper repro).
2. **Independent 4-eyes review** (#672 comment): re-derived from primary evidence — upheld the #673 downgrade with its own repro, confirmed #671/#674 classifications, validated PASS claims against live prod.
3. **Fixes** committed `46a0242`; both parties redeployed on the fixed binary; `go build/vet/test -race` clean.
4. **Independent UAT acceptance**: all three fixes **ACCEPT** from fresh repro (cold + post-restart sends round-trip with zero dial-timeouts; `/api/v1/sessions` shows `external=true`; fresh state-dir starts clean).

## TURN relay on k8s (#675) — ✅ DONE + independently verified
- Hub redeployed `hostNetwork` with pion `RelayAddressGeneratorPortRange`: control UDP **3479** (3478 is iogrid's), bounded relay **50000-50063**, relay-IP 45.151.123.50.
- Verified twice (mine + independent 4-eyes): `healthz turn.enabled:true`; STUN binding to `:3479` → Success (firewall already open); pion `Allocate` → relay in range (`50063`, then independent `50009`); hub `total_allocations` incremented.
- git `openova-private@main` matches deployed (Flux-safe). #675 closed.
- _No remaining deferred items for the mesh._

---
_Generated by the autonomous UAT campaign + fix + independent-acceptance cycle._

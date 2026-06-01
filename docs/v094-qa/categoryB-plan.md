# v0.9.4 QA — Category B — Cross-org federation — TEST PLAN (PRE-EXEC, awaiting chepherd-lead confirmation)

**Drafted:** 2026-06-01 by chepherd-worker (QA test-engineer)
**Owner:** chepherd-worker
**Reviewer:** chepherd-lead
**Parent issue:** [#560 v0.9.4 QA campaign](https://github.com/chepherd/chepherd/issues/560)
**Spec citations:** [`docs/V0.9.2-ARCHITECTURE.md`](../V0.9.2-ARCHITECTURE.md) §10 Pattern 2 (lines 600–732), §15 Authentication (1178–1216), §22 Implementation notes (1404–1463), §23 Invariants (1466–1487)
**Sister evidence:** [`categoryA-evidence.md`](categoryA-evidence.md) (worker2, HALTED at A.1)

---

## 0. Premise + scope boundaries

The chepherd substrate under test is **`origin/main` @ `9df9f6d` (post Wave F8.1)**:

- `cmd/chepherd-hub/` — Wave F1 (#491) scaffold + F5 SDP relay (#495) + F6 TURN (#496) + F7 reverse-proxy tunnel (#497) + F8 cross-org JWT federation auth (#498)
- `internal/federation/cross_org_jwt.go` + `internal/runtimehttp/p0_557_federation_mint_*` — Wave F8.1 (#557) daemon `CrossOrgJWTMinter` mounted on daemon `/v1/federation/mint`
- `internal/webrtcrtc/` — Wave F2 (#492) per-session `PCStore` + DataChannel JSON-RPC adapter
- T3/T3.1 (#487/#527) — mTLS substrate + listener wiring + 2-daemon live-walk under `internal/runtime` + `cmd/run.go`
- F7.1 (#556) — runner-side reverse-proxy tunnel client (`internal/daemon/rc/signaling/client.go` family)

**Walk policy — pre-decided with chepherd-lead per role briefing:**

Category A halted at A2A method-name divergence (worker2 finding: `message/send` vs spec `SendMessage`). That divergence affects the **A2A application layer** (verbs + state enums + error codes). It does **NOT** affect the **federation transport layer** (mTLS handshake bytes, JWT ES256 signatures, SDP relay JSON, TURN REST creds, WS tunnel framing, ICE candidates). Category B's surface is the latter — therefore Category B can be walked on the current binary regardless of A-halt status. End-to-end A2A round-trips that traverse federation will still inherit A's method-name nonconformance, which I will note as **inherited-fail-from-A** in those specific cells without re-arguing the verdict.

**DoD bar (per CLAUDE.md §2 + worker2 precedent for A):** fresh provisioned env (clean `state-dir` for each daemon + ephemeral hub binary built from main at walk time) + captured wire bytes + every PASS/FAIL/PARTIAL row backed by a quotable spec sentence + binary artifact (response.json, pcap-equivalent, hub stderr line).

---

## 1. Topology (single bare-metal host, 3 processes + 2 runners)

```
host: 127.0.0.1  (only loopback used; cross-org isolation via distinct state-dirs,
                  distinct daemon listen ports, distinct ES256 keypairs)

  ┌─────────── orgX (alice.example) ──────────┐    ┌─── chepherd-hub ───┐
  │  daemon-X     127.0.0.1:18180  /jsonrpc   │    │  binary: chepherd- │
  │               /v1/federation/mint  (F8.1) │    │  hub @ HEAD        │
  │               mTLS listener  :18181 (T3.1)│◀──▶│  127.0.0.1:18190   │
  │  runner-XA    127.0.0.1:18185  /a2a/{sid} │    │  /v1/signaling/*   │
  │  runner-XB    127.0.0.1:18186  /a2a/{sid} │    │  /v1/turn/creds    │
  │  state-dir    /tmp/v094-qa-B-orgX-state   │    │  /v1/relay/*       │
  └────────────────────────────────────────────┘    │  /v1/federation/   │
                                                    │     auth           │
  ┌─────────── orgY (bob.example) ────────────┐    │  /v1/cards         │
  │  daemon-Y     127.0.0.1:18280  /jsonrpc   │    │  --turn-listen     │
  │               /v1/federation/mint  (F8.1) │◀──▶│     :18191 (UDP)   │
  │               mTLS listener  :18281 (T3.1)│    │  --stun-listen     │
  │  runner-YC    127.0.0.1:18285  /a2a/{sid} │    │     :18192 (UDP)   │
  │  state-dir    /tmp/v094-qa-B-orgY-state   │    │  --allowed-orgs    │
  └────────────────────────────────────────────┘    │   alice.example,   │
                                                    │   bob.example      │
                                                    └────────────────────┘
```

**Why this shape:**
- Two daemons + one hub = cheapest topology that exercises *every* cross-org seam (mTLS, JWT mint, SDP relay, TURN allocate, WS tunnel, F2 DataChannel) without requiring real public DNS or distinct hosts.
- Distinct ES256 keypairs per daemon are mandatory for F8 — the hub mints by relaying to the home daemon's signing key (§15.2). Same keypair would make verification trivially work and miss bugs.
- One runner per side is the minimum; second runner on org-X (XB) covers the §10 Pattern 2 Phase 5 case where both ends are behind private NAT and the hub is body-blind.

---

## 2. Sub-areas (9 cells)

Each sub-area below has: **spec quote → walk → captured artifact → verdict criterion**.

### B.1 — T3 mTLS substrate (cert generation + listener accepts presented client cert)

**Spec quote (§15.1):** *"For cross-org daemon-to-daemon (#27): **mTLS by default**. Both sides verify identity via certificates pre-exchanged out-of-band."*

**Walk:**
1. `chepherd run --enable-federation-mtls --federation-mtls-listen 127.0.0.1:18181 --federation-mtls-cert <X.crt> --federation-mtls-key <X.key> --federation-mtls-ca <X-ca.crt> ...` for daemon-X.
2. Symmetric for daemon-Y.
3. `curl --cacert X-ca.crt --cert Y-client.crt --key Y-client.key https://127.0.0.1:18181/healthz` — request from daemon-Y client cert against daemon-X listener.
4. Negative probe: `curl --cacert X-ca.crt https://127.0.0.1:18181/healthz` (no client cert) — must fail.
5. Negative probe: `curl --cacert X-ca.crt --cert SELF-SIGNED.crt --key SELF-SIGNED.key https://127.0.0.1:18181/healthz` — must fail with `bad certificate`.

**Captured:** `B1-mtls.handshake.success.curl-vvv.log`, `B1-mtls.no-clientcert.curl-vvv.log`, `B1-mtls.untrusted-cert.curl-vvv.log` + daemon-X stderr excerpts.

**Verdict criterion:** PASS iff success request returns 200 + both negative probes hit TLS layer rejection (NOT app-layer 401). FAIL if any negative probe succeeds. Pin the captured `client certificate verify` and `unknown CA` strings in the evidence.

---

### B.1.1 — T3.1 mTLS listener wiring into `cmd/run.go` (flag → live socket)

**Walk:** `chepherd run --help | grep federation-mtls` shows 4 expected flags. Boot daemon, `lsof -iTCP:18181` shows the listener. Kill daemon, listener closes. Boot WITHOUT flags, listener absent.

**Captured:** flag-help output, lsof snapshots, `B1.1-listener-lifecycle.log`.

**Verdict:** PASS iff flag → listener mapping is 1:1 + listener is gated on `--enable-federation-mtls` (default-off). FAIL if listener binds without the gate flag (silent default-on is a security regression).

---

### B.2 — F8 cross-org JWT mint via hub (alice→bob round-trip)

**Spec quote (§10 Pattern 2 Phase 2 lines 646–654):**
> *"A→RA: SendMessage to agent-C URL ... RA→CPX: request JWT to call agent-C ... CPX→BR: federation auth request for agent-C ... BR→CPY: relay to home daemon ... CPY: mint JWT signed by CPY key ... BR→CPX: forward JWT ... CPX→RA: bundled credentials"*

**Walk:**
1. POST hub `/v1/federation/auth` with body `{fromOrgId:alice.example, toAgentSID:<bob-runner-sid>, ...}` as alice-X.
2. Hub relays to daemon-Y `/v1/federation/mint` (F8.1).
3. Daemon-Y mints ES256 JWT with §15.2 claims (`iss=daemon-Y URL`, `sub=alice-caller-sid`, `aud=bob-runner-sid`, `exp=iat+60s`, `jti=<uuid>`, `chepherd_grant_id`, `chepherd_rate_window`).
4. Hub forwards back to alice-X.
5. Decode JWT header + payload, verify each claim against §15.2 row-by-row.
6. Verify signature against daemon-Y's published JWKS (`GET http://127.0.0.1:18280/.well-known/jwks.json`).
7. Negative: replay same `jti` within `exp` window — pin whether server rejects (spec implies replay protection via `jti`).
8. Negative: tamper one byte of JWT payload, verify signature rejection.

**Captured:** `B2-mint-request.json`, `B2-mint-response.json` (with full JWT), decoded `B2-mint-jwt-claims.json`, `B2-jwks.json`, `B2-replay.resp.json`, `B2-tampered.resp.json`.

**Verdict:** PASS iff all §15.2 claims present + ES256 signature verifies + tampered JWT rejected. PARTIAL if replay-protection missing (spec doesn't explicitly mandate enforcement). FAIL if any §15.2 claim missing or signature scheme not ES256.

---

### B.2.1 — F8.1 daemon `CrossOrgJWTMinter` mount onto `runtimehttp.Server`

**Walk:** `GET http://127.0.0.1:18280/v1/federation/mint` (auth probe) + `POST /v1/federation/mint` with valid hub-relay body — assert 200 + valid JWT in response. Negative: POST without hub-auth signature → reject.

**Captured:** route enumeration via `chepherd run --debug-routes` if available, else live HEAD probes against `/v1/federation/mint`; `B2.1-mount-probes.log`.

**Verdict:** PASS iff daemon-Y advertises `/v1/federation/mint` and only the hub can drive it. FAIL on direct-from-internet access.

---

### B.3 — F5 SDP signaling body-blind (cross-org offer/answer/ICE through hub)

**Spec quote (§10 Pattern 2 Phase 5 lines 670–675):**
> *"RA→BR: SDP offer plus ICE candidates plus DTLS fingerprint. BR→RC: forward signaling. RC→BR: SDP answer ... BR→RA: forward answer."*

**Spec quote (§23 invariant line 1479):** *"chepherd.org metadata-only summaries never include agent payload"*

**Walk:**
1. POST hub `/v1/signaling/offer` body `{fromOrgId:alice.example, toOrgId:bob.example, toRunnerSID:bob-XYZ, sdp:<opaque-blob>, ice:[...candidates], fingerprint:<sha256>}`.
2. GET hub `/v1/signaling/pending?orgId=bob.example&runnerSID=bob-XYZ` from bob's side — assert pending offer returned.
3. **Body-blind invariant probe:** push 1 KB of random bytes as the `sdp` field (NOT real SDP). Hub MUST relay byte-for-byte. Compute SHA-256 in-and-out, must match.
4. POST `/v1/signaling/answer` from bob → assert alice can fetch.
5. POST `/v1/signaling/ice` candidates in both directions.
6. Negative: spoof `fromOrgId:alice.example` from a non-allowed origin — must 403.
7. Negative: omit `fromOrgId` from `--allowed-orgs` allowlist on hub boot — must 403.

**Captured:** `B3-offer.req.json`, `B3-offer.resp.json`, `B3-pending.resp.json`, `B3-body-blind.in.sha256` + `B3-body-blind.out.sha256` (MUST match), `B3-spoof.resp.json`, `B3-allowlist-deny.resp.json`, hub stderr.

**Verdict:** PASS iff round-trip works + body-blind SHA-256 matches exactly + both negative probes deny. PARTIAL if hub logs the SDP body (violates §23 metadata-only invariant — log it as a security finding even if relay works). FAIL otherwise.

---

### B.4 — F6 TURN REST creds + Allocate (pion/turn/v5 against hub)

**Spec quote (§10 Pattern 2 Phase 4 lines 661–668):** *"RA→BR: TURN Allocate Request RFC 5766 ... BR→RA: relay candidate"*

**Walk:**
1. POST `/v1/turn/credentials` with `{orgId:alice.example, runnerSID:alice-XYZ, ttlSeconds:600}` — assert response carries `{username,password,ttl,uris:["turn:127.0.0.1:18191"]}` per RFC 5389 REST-API draft.
2. Verify `username` format = `<expiry>:<orgId>:<runnerSID>` (per REST API for TURN spec).
3. Verify `password = base64(HMAC-SHA1(sharedSecret, username))`.
4. Drive `pion/turn/v5/Client.Allocate(...)` against `udp://127.0.0.1:18191` with minted creds → expect a RELAYED-ADDRESS attribute back.
5. Negative: tamper password — pion should fail Allocate with 401.
6. Hub healthz before+after Allocate — `active_allocations` counter must increment + decrement on Close.
7. Hub stderr inspect — `OnAllocationCreated` / `OnAllocationDeleted` audit lines must contain only metadata (username, addrs, timestamps) and NEVER payload bytes per §23.

**Captured:** `B4-creds.req+resp.json`, `B4-pion-allocate.go.log`, `B4-tampered-allocate.log`, `B4-healthz-before/after.json`, hub stderr excerpt.

**Verdict:** PASS iff Allocate succeeds + 401 on tampered creds + healthz counter accurate + no payload bytes in stderr. FAIL if any.

---

### B.5 — F7 reverse-proxy tunnel bidirectional (body-blind)

**Spec quote (§10 Pattern 2 Phase 5 fallback lines 728–732):**
> *"RA→BR: A2A SendMessage encrypted with DTLS over TURN. BR→RC: forward ciphertext, broker cannot decrypt. RC→BR: response encrypted. BR→RA: forward ciphertext."*

**Walk:**
1. Bob's runner dials `wss://127.0.0.1:18190/v1/relay/connect?orgId=bob.example&runnerSID=bob-XYZ` (WS upgrade) — hub holds the inbound socket as bob's tunnel.
2. Alice's runner POSTs `/v1/relay/bob.example/bob-XYZ/jsonrpc` with body `{<opaque-ciphertext-equivalent>}`.
3. Hub forwards body bytes verbatim through bob's WS tunnel.
4. Bob's runner replies; hub forwards reply bytes verbatim to alice's POST response.
5. **Body-blind probe:** ship 1 KB random bytes as body, assert byte-identity in and out (SHA-256).
6. Bidirectional: alice→bob THEN bob→alice (server-initiated) via the same tunnel.
7. Negative: tunnel disconnect mid-call — alice receives 502 or 504, NOT a hang.

**Captured:** `B5-tunnel-handshake.log`, `B5-bidir.in/out.sha256`, `B5-disconnect.resp.json`, hub stderr excerpts.

**Verdict:** PASS iff round-trip works + body-blind SHA match + disconnect surfaces error promptly. FAIL otherwise.

---

### B.5.1 — F7.1 runner-side reverse-proxy tunnel client

**Walk:** boot runner with `--reverse-proxy-tunnel-url wss://...`, watch runner stderr for "connected to hub tunnel" + "received frame from hub" log lines on B.5 walk. Kill hub; runner must reconnect with backoff (assert at least 2 reconnect attempts within 30s).

**Captured:** `B5.1-runner-stderr.log`, `B5.1-reconnect-timing.log`.

**Verdict:** PASS iff connect+frame+reconnect all observed. FAIL on hang or crash.

---

### B.6 — F2 WebRTC DataChannel speedup vs HTTP baseline

**Spec quote (briefing — operator stated F2 claim: 1.58× speedup):** Restated from briefing — original §10 Pattern 2 Phase 7-8 establishes the P2P path; the perf claim itself appears in [`internal/webrtcrtc/peerconnection.go`](../../internal/webrtcrtc/peerconnection.go) and PR #492's body, to be cited verbatim in the evidence row.

**Walk:** (will likely move to Category H non-functional; cross-listed here because briefing names F2 under B)
1. Spin two runners with `PCStore` enabled.
2. Establish a WebRTC peer connection via the F5 signaling path.
3. Send N=1000 messages of M=256 bytes round-trip over the DataChannel; measure median + p99 latency.
4. Repeat N round-trips via raw HTTP between the same runner endpoints (baseline).
5. Compute speedup ratio.

**Captured:** `B6-rtt-datachannel.csv`, `B6-rtt-http.csv`, `B6-summary.json`.

**Verdict:** PASS iff median speedup ≥ 1.4× (giving 10% headroom vs the 1.58 claim — calibration on bare-metal loopback may differ from claim conditions); PARTIAL if 1.0–1.4×; FAIL if < 1.0× (DataChannel slower than HTTP on loopback would mean per-message overhead dominates). NOTE: if the 1.58× claim comes from non-loopback (cross-host) measurements, B.6 here is a SMOKE not a CERT — flag the methodology mismatch.

---

### B.7 — End-to-end Pattern 2 §10 cross-org round-trip (integration cell)

**Walk (single trace):** alice's agent → discover bob via `/v1/cards` → request JWT (B.2) → push SDP offer (B.3) → bob fetches offer → SDP answer back → ICE candidates exchange → DataChannel established → A2A `SendMessage` over DataChannel with JWT in header → bob processes → response back.

**Captured:** sequenced trace log with timestamps + every wire payload bundled into `B7-pattern2-e2e.har`.

**Verdict:** PASS iff every phase succeeds end-to-end (modulo **inherited-fail-from-A** on `SendMessage` vs `message/send` method name — flag that cell separately, do not block B.7 verdict on it). FAIL if any federation seam blocks.

---

## 3. Tooling

- `curl -vvv` for HTTP probes (with `--cacert/--cert/--key` for mTLS).
- `openssl s_client` for raw mTLS handshake byte inspection.
- `jq` for JSON wire decoding + claim extraction.
- `tcpdump -w pcap` for body-blind invariant proof (TURN/WS framing).
- `pion/turn/v5/Client` for B.4 Allocate.
- `pion/webrtc/v4` for B.6 DataChannel perf.
- `tshark -r pcap -Y 'http.request.uri matches "/v1/signaling"'` for SDP relay packet inspection.
- A bash walker script `scripts/a2a-conformance/walk-categoryB.sh` (sister to worker2's `walk-categoryA.sh`) drives B.1–B.5 deterministically; B.6 is a separate Go binary `scripts/a2a-conformance/walk-categoryB-F2.go` (Go because pion drivers are Go-only).
- `cosign` not required; key material is self-signed for ephemeral walk.

---

## 4. Halt criterion

Walk continues through all 9 cells regardless of individual-cell FAIL — Category A precedent (worker2 walked all 5 sub-areas even after A.1 halt). Each cell verdict stands independently. Mid-walk halt ONLY if:

- mTLS handshake itself crashes the daemon (process exit) — file P0 immediately.
- Hub leaks SDP/TURN/tunnel **payload bytes** in stderr or healthz (§23 metadata-only invariant) — file P0 immediately + halt remaining federation cells until invariant restored (security blast radius).
- Any test reveals a `panic:` in chepherd binaries — file P0 + halt.

Otherwise: complete the walk, ship `categoryB-evidence.md` with per-cell verdict, summarize gaps in P0/P1/P2 issue stubs.

---

## 5. Evidence shape (matches worker2's Category A format)

`docs/v094-qa/categoryB-evidence.md` with:
- header (walked-by, walked-on, binary version, fresh state-dir paths)
- VERDICT SUMMARY table (9 rows, one per cell, PASS/PARTIAL/FAIL/INHERITED-FAIL-FROM-A)
- per-cell section with: spec quote (URL-anchored to spec source), walk steps, captured bytes inline, diff vs spec, verdict + one-line reason
- consolidated P0/P1/P2 follow-up issue stubs to file
- index of evidence files under `/tmp/v094-qa-B-evidence/`

---

## 6. Open questions for chepherd-lead (pre-execute decision points)

1. **F2 perf cell:** the 1.58× claim — where in the codebase or PR body is the ORIGINAL measurement methodology (loopback? cross-host? message size? batch size?)? I'll grep for it but want to confirm I'm pinning the right baseline.
2. **Replay protection on JWT `jti`:** §15.2 lists `jti` as "Unique JWT ID (prevents replay)" but doesn't mandate server-side rejection of seen `jti`s. Should B.2 step 7 verdict be FAIL on missing enforcement, or just NOTE-AS-GAP-WITHOUT-FAILING (spec is descriptive, not prescriptive on enforcement timing)?
3. **mTLS cert generation:** OK to self-sign with `openssl` at walk time + pin the cert fingerprint into evidence? Or do you want me to use chepherd's own `cert generate` command if one exists?
4. **Inherited-fail-from-A treatment:** B.7 end-to-end will inherit worker2's A.1 method-name divergence. I propose marking those cells INHERITED-FAIL-FROM-A (deferred to A's remediation) rather than re-arguing the verdict. Confirm?
5. **TURN UDP port 18191 on bare-metal host:** if the host blocks unprivileged UDP bind, B.4 walks STUB-only (synthetic pion server). Confirm fallback acceptable, or do you want me to escalate as environmental gap?
6. **Body-blind invariant for F5 signaling:** spec doesn't explicitly forbid logging the SDP blob — §23 invariant is about "metadata-only **summaries**" sent to public agents. Logging SDP locally on hub for debugging may be acceptable. I'll PASS on round-trip + flag any SDP-in-log as a privacy NOTE rather than FAIL. Confirm?

---

## 7. Time budget estimate

- Topology bootstrap (3 binaries, certs, JWKS): ~25 min
- B.1 + B.1.1 + B.2 + B.2.1: ~30 min
- B.3 + B.4 + B.5 + B.5.1: ~50 min
- B.6 (Go perf walker): ~40 min
- B.7 e2e integration: ~25 min
- Evidence write-up: ~20 min
- **Total ~3h10m** within the ~3-4h budget for B+C+D+E+H combined; B alone fits the briefing's "heaviest" framing.

---

**Awaiting chepherd-lead confirmation before executing. Will not touch the surface until ack.**

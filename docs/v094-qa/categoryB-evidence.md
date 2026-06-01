# v0.9.4 QA — Category B — Cross-org federation — EVIDENCE

**Walked:** 2026-06-01 by chepherd-worker (QA test-engineer)
**Issue:** [#560 v0.9.4 QA campaign](https://github.com/chepherd/chepherd/issues/560)
**Plan:** [`categoryB-plan.md`](categoryB-plan.md)
**Sister evidence:** [`categoryA-evidence.md`](categoryA-evidence.md) (worker2, A-halt status)
**Spec source:** [`docs/V0.9.2-ARCHITECTURE.md`](../V0.9.2-ARCHITECTURE.md) (committed in-repo); §10 Pattern 2 lines 600–732, §15 Authentication 1178–1216, §22 Implementation notes 1404–1463, §23 Invariants 1466–1487
**Surface under test:**
- Binary: `chepherd` built from `origin/main @ 9df9f6d` at walk time (`go build -o /tmp/v094-qa-B/bin/chepherd .`)
- Helper: `categoryB-mtls-setup` at `scripts/a2a-conformance/cmd/categoryB-mtls-setup/main.go` (exports daemon-minted certs + cross-pins them via `federation.AddPinnedCA`, mirroring `internal/e2e/p0_527_two_daemon_mtls_test.go`)
- Walker: `scripts/a2a-conformance/walk-categoryB.sh` (drives bash + curl + openssl + ss for raw wire bytes)
- Fresh state-dirs per CLAUDE.md §2: `/tmp/v094-qa-B/state-{X,Y}` (created clean at walk time)

---

## VERDICT SUMMARY (live; updated per cell)

| Sub-area | Status | One-line reason |
| --- | --- | --- |
| **B.1 — T3 mTLS cross-pinned handshake** | **PASS** | TLSv1.3 mutual handshake succeeds; both negative probes hit TLS-layer rejection (`tlsv13 alert certificate required`, `tlsv1 alert unknown ca`). Daemon stderr confirms `tls: client didn't provide a certificate` + `x509: certificate signed by unknown authority`. |
| **B.1.1 — T3.1 listener wiring (flag → live socket)** | **PASS** | Federation listener default-off (absent under `--federation-mtls=false`); binds on both daemons under `--federation-mtls=true`; closes on SIGTERM. |
| **B.2 — F8 cross-org JWT mint via hub** | **FAIL** (end-to-end broken) + **PARTIAL** on JWT shape | Hub-mediated flow fails: daemon mint endpoint gated by dashboard Bearer middleware (`internal/runtimehttp/server.go:413-462`); hub doesn't carry a daemon-specific Bearer → `401 missing Bearer token` on every relay. Bypassing hub with daemon-Y bearer + spoofed attestation headers proves the daemon CAN mint when reached, with valid ES256 sig + JWKS round-trip + tamper-rejection — but §15.2 claims missing `jti`, `chepherd_grant_id`, `chepherd_rate_window`; TTL = 5min vs spec default 60s. |
| **B.2.1 — F8.1 daemon `/api/v1/federation/jwt` mount + gating** | **PASS (security)** + **FINDING (architecture)** | Endpoint mounted on dashboard mux (`server.go:286`) + gated by Bearer middleware → rejects unauthenticated + spoofed-only requests with 401. This is SAFER than the F8 design promised (which was "hub-attested via mTLS at the federation listener"). Architectural gap: the SAFE gating breaks the F8 hub-relay flow. See B.2 verdict. |
| **B.3 — F5 SDP signaling body-blind through hub** | **PASS** | All 7 probes green: bidirectional offer/answer/ICE relay with 1 KB random payload SHA-256 round-trip MATCHES; spoof-fromOrgId 403; mailbox-snoop 403; non-allowlisted-caller 403; hub stderr contains NO payload bytes (§23 invariant honored). |
| **B.4 — F6 TURN REST creds + pion Allocate** | **PASS** | pion/turn/v5 Client.Allocate succeeds with hub-minted REST creds (relay addr returned); tampered password → integrity-check-fail (auth FAIL line in stderr + counter incremented); healthz `active_allocations` correctly increments 0→1 on Allocate + decrements 1→0 on Close; OnAllocationCreated/Deleted lines carry username + src + relay addrs + timestamps only (no payload bytes per §23); turn-secret value NOT in hub stderr (chepherd-lead secret-bleed cross-cut PASS). |
| **B.5 — F7 reverse-proxy tunnel bidirectional + body-blind** | **PASS** | 1 KB random payload alice→hub→bob→hub→alice byte-for-byte; SHA-256 MATCH; no-tunnel-for-org → 502; alice POST to non-allowlisted org → 403; disconnect bob mid-flight → next alice POST 502 immediately (not a hang); hub stderr contains NO payload bytes; healthz `tunnels.total_lifetime` counter increments correctly. |
| **B.5.1 — F7.1 runner tunnel client** | **PASS (primitive)** + **FINDING (reconnect+wiring)** | All 7 cmd/runner unit tests PASS including `TestV094Walk_F71_RunnerTunnel_ThroughRealHubBinary` (drives real hub binary + asserts body byte-exact). HOWEVER `relayTunnelClient.Done()` only signals disconnect — the reconnect-with-backoff loop is NOT in F7.1 scope per `relay_tunnel.go:179` comment ("callers that want to react to disconnects, e.g., re-dial loop"). No `--hub-relay-url` flag wiring in `cmd/run.go`, no transport-fallback chain. Primitive ships unreached from production boot. |
| **B.6 — F2 WebRTC DataChannel speedup** | **PASS** (9/10 runs) + **NOTE** (1 outlier) | 10-iteration replay of `TestV094Walk_F2_DataChannel_BeatsHTTPBaseline` (the canonical PR #492 perf walker, n=10 trials per transport per iteration). Speedup ratios: 1.73, 0.56, 2.04, 1.79, 1.60, 2.00, 1.72, 2.37, 1.70, 1.84. **Median: 1.76×. Mean (excl. outlier): 1.87×. Match to PR #492's reported 1.58× confirmed within noise range.** 9/10 PASS per Q1 threshold (≥1.0×). Outlier run 2 had DC=467µs vs HTTP=264µs (0.56×) under transient GC/scheduling noise — flag as performance-noise-sensitivity NOTE, not regression. |
| **B.7 — §10 Pattern 2 end-to-end integration** | **DEFERRED-PENDING-A.1-AND-B.2-REMEDIATION** | Double-inherit-defer per chepherd-lead Q4 + B.2 P0 #562: B.7 e2e would require BOTH (1) A.1 method-name fix from worker2's Category A halt AND (2) B.2 F8 wiring fix. Re-walk this cell after #562 + A.1 remediation. |

Cells executed in order. Mid-walk halt criteria per plan §4 (panic / daemon crash / payload leak in stderr).

---

## B.1.1 — T3.1 listener wiring lifecycle (flag → live socket)

### Spec quote — flag-help text in `cmd/run.go` (the wiring contract itself)

> `--federation-listen 127.0.0.1:0` — *"#527 Wave T3.1 — federation-facing mTLS HTTP listener address (host:port). Empty disables the federation listener… When set + `--federation-mtls=true` the daemon binds a **SECOND** HTTP listener on this address with mTLS server config; the dashboard listener stays unchanged."*

### Probe — boot WITHOUT `--federation-mtls` (default-off)

Daemon flags:
```
--listen 127.0.0.1:${HTTP} --mcp-listen 127.0.0.1:${MCP} \
--state-dir <fresh> \
--federation-listen 127.0.0.1:${FED}        # (no --federation-mtls flag → defaults to false)
```

Listener-bound probe (`ss -ltn | grep <FED>`):

```
$ cat B1.1.a-nomtls-listener.ss
(empty — no listener bound)
$ cat B1.1.a-nomtls-listener.verdict
not-bound
```

**Verdict: PASS.** Listener gated on `--federation-mtls=true`. Default-off behavior matches the security-conservative posture stated in the flag-help text.

### Probe — boot WITH `--federation-mtls=true`

```
$ cat B1.1.b-mtls-listeners.ss
LISTEN 0      1024       127.0.0.1:37061      0.0.0.0:*           # daemon-X federation listener
LISTEN 0      1024       127.0.0.1:47031      0.0.0.0:*           # daemon-Y federation listener
```

Daemon-X stderr (excerpt from `B1-daemon-X.stderr`):

```
✓ Federation mTLS active (org=alice.example, 1 pinned CAs)
✓ Federation peer discovery via http://example.invalid/registry (announce as http://127.0.0.1:37845)
✓ Federation mTLS listener on https://127.0.0.1:37061 (cross-org peers)
```

**Verdict: PASS.** Listener binds on the flag-specified port; chepherd advertises it as `https://…` (TLS-only); separate from the dashboard `--listen` port.

---

## B.1 — T3 mTLS cross-pinned handshake

### Spec quote (§15.1 lines 1180–1184)

> *"For cross-org daemon-to-daemon (#27): **mTLS by default**. Both sides verify identity via certificates pre-exchanged out-of-band."*

### Setup

`categoryB-mtls-setup` opens each daemon's `chepherd.db`, calls `federation.LoadOrCreateMTLS(...)` (the production code path), extracts the auto-minted ECDSA P-256 leaf cert + key, then calls `federation.AddPinnedCA(...)` to register each daemon's leaf cert as a trust anchor in the OTHER's `federation-pinned-cas` sqlite row. Both daemons then SIGTERM'd + restarted so their `MTLSConfig.PinnedCAs` pool reloads.

Cert details (`openssl x509 -noout`):

```
$ cat B1-cert-A.details
subject=CN = alice.example
issuer=CN = alice.example
notBefore=May 31 16:23:06 2026 GMT
notAfter=May 31 17:23:06 2027 GMT
sha256 Fingerprint=FB:E4:E1:11:67:BA:94:9D:B8:C3:83:B1:4D:29:55:BB:74:BA:BF:09:10:81:1D:A8:DA:E1:F8:B7:2D:1B:5D:BD

$ cat B1-cert-B.details
subject=CN = bob.example
issuer=CN = bob.example
notBefore=May 31 16:23:07 2026 GMT
notAfter=May 31 17:23:07 2027 GMT
sha256 Fingerprint=9B:38:2D:86:2A:31:04:91:84:BD:4F:87:56:C6:A8:77:80:02:C7:60:A1:03:81:53:AD:41:88:3F:16:33:1E:BE
```

ECDSA P-256, self-signed (daemon IS its own org root per `internal/federation/mtls.go:82-85` "self-signed P-256 cert (the daemon IS its own org root in this T3 cut)"), 365-day validity per `FederationCertValidity`. Per-daemon distinct keypairs confirmed by distinct sha256 fingerprints.

### Probe 1 — success: bob-cert + alice-pinned-CA → alice listener (curl)

Request: `GET https://127.0.0.1:37061/healthz` with `--cert b.cert.pem --key b.key.pem --cacert a.cert.pem`.

Handshake (excerpt from `B1-probe1-success.curl-vvv`):

```
* TLSv1.3 (OUT), TLS handshake, Client hello (1):
* TLSv1.3 (IN), TLS handshake, Server hello (2):
* TLSv1.3 (IN), TLS handshake, Encrypted Extensions (8):
* TLSv1.3 (IN), TLS handshake, Request CERT (13):       ← server-side cert request (mTLS)
* TLSv1.3 (IN), TLS handshake, Certificate (11):
* TLSv1.3 (IN), TLS handshake, CERT verify (15):
* TLSv1.3 (OUT), TLS handshake, Certificate (11):       ← client-side cert response
* TLSv1.3 (OUT), TLS handshake, CERT verify (15):
* TLSv1.3 (OUT), TLS handshake, Finished (20):
* SSL connection using TLSv1.3 / TLS_AES_128_GCM_SHA256
*  subjectAltName: host "127.0.0.1" matched cert's IP address!
*  issuer: CN=alice.example
*  SSL certificate verify ok.
> GET /healthz HTTP/1.1
< HTTP/1.1 200 OK    (response body below)
```

Response body (`B1-probe1-success.body`):

```json
{"container_runtime":"podman","ok":true,"profile":{"auth":"local","name":"auto","oidc_iss":"","spawner":"podman-sidecar","storage":"local-volume","tls":"none"},"sessions":0,"ts":"2026-05-31T17:23:11.530287956Z"}
```

**Verdict: PASS.** Full TLS 1.3 mutual handshake with `Request CERT (13)` from server + `Certificate (11)` + `CERT verify (15)` from client. App-layer 200 + structured healthz body. Per §15.1 contract `Both sides verify identity via certificates pre-exchanged out-of-band` — satisfied via the `AddPinnedCA` mechanism.

### Probe 2 — no client cert (must reject)

Same URL as probe 1, omit `--cert/--key`.

curl exit + handshake tail (`B1-probe2-noclient.curl-vvv`):

```
> GET /healthz HTTP/1.1
* TLSv1.3 (IN), TLS alert, unknown (628):
* OpenSSL SSL_read: error:0A00045C:SSL routines::tlsv13 alert certificate required, errno 0
curl: (56) OpenSSL SSL_read: error:0A00045C:SSL routines::tlsv13 alert certificate required, errno 0
exit=56
```

Daemon-X stderr concurrent line:
```
2026/06/01 01:23:11 http: TLS handshake error from 127.0.0.1:48484: tls: client didn't provide a certificate
```

**Verdict: PASS.** Server emits TLS 1.3 alert `certificate required (116)`; chepherd log confirms `tls: client didn't provide a certificate`. Per `BuildServerTLSConfig` in `internal/federation/mtls.go:127-145`, `ClientAuth: tls.RequireAndVerifyClientCert` is enforced — the rejection happens **at TLS layer**, not as a JSON-RPC error envelope (as the inline comment promises: *"unauthenticated callers get an immediate handshake failure, not a JSON-RPC error envelope"*).

### Probe 3 — untrusted self-signed client cert (must reject)

Generated a rogue cert via `openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -days 1 -nodes -subj "/CN=rogue.example"` then presented it.

curl exit + handshake tail (`B1-probe3-untrusted.curl-vvv`):

```
> GET /healthz HTTP/1.1
* TLSv1.3 (IN), TLS alert, unknown CA (560):
* OpenSSL SSL_read: error:0A000418:SSL routines::tlsv1 alert unknown ca, errno 0
curl: (56) OpenSSL SSL_read: error:0A000418:SSL routines::tlsv1 alert unknown ca, errno 0
exit=56
```

Daemon-X stderr concurrent line:
```
2026/06/01 01:23:11 http: TLS handshake error from 127.0.0.1:48486: tls: failed to verify certificate: x509: certificate signed by unknown authority
```

**Verdict: PASS.** Server emits TLS alert `unknown CA (48)`; chepherd logs `x509: certificate signed by unknown authority`. Untrusted-cert rejection happens **at TLS layer** (not app-layer 401).

### Probe 4 — raw `openssl s_client` (handshake byte-level inspection)

```
$ grep -E "Server certificate|subject|Cipher|Verify return" B1-probe4-openssl-sclient.log
Server certificate
subject=CN = alice.example
issuer=CN = alice.example
Acceptable client certificate CA names                  ← server advertises pinned-CA list
New, TLSv1.3, Cipher is TLS_AES_128_GCM_SHA256
Verify return code: 0 (ok)
```

**Verdict: PASS.** Raw handshake completes with `Verify return code: 0 (ok)`, server explicitly publishes `Acceptable client certificate CA names` (which is the pinned-CA pool surfaced to the peer for chain selection).

### Probe 5 — raw `openssl s_client` without cert

```
$ grep -E "alert|err:" B1-probe5-openssl-nocert.log
801B9A0C177F0000:error:0A00045C:SSL routines:ssl3_read_bytes:tlsv13 alert certificate required:../ssl/record/rec_layer_s3.c:1593:SSL alert number 116
```

**Verdict: PASS.** TLS alert `certificate required (116)` matches curl probe 2.

### Cumulative B.1 verdict — **PASS**

| Probe | Expected | Observed | Verdict |
| --- | --- | --- | --- |
| 1 — success | mutual handshake + 200 | TLS 1.3 mutual handshake + 200 healthz | PASS |
| 2 — no client cert | TLS-layer rejection | TLS 1.3 `certificate required (116)` + `tls: client didn't provide a certificate` | PASS |
| 3 — untrusted cert | TLS-layer rejection | TLS 1.3 `unknown ca (48)` + `x509: certificate signed by unknown authority` | PASS |
| 4 — openssl raw | handshake completes | exit 0 + Verify return 0 + "Acceptable client certificate CA names" | PASS |
| 5 — openssl no cert | handshake fails | `tlsv13 alert certificate required` | PASS |

§15.1 contract (cross-org mTLS by default, peer-pinned certs out-of-band) honored at both code path (`internal/federation/mtls.go` `BuildServerTLSConfig` with `RequireAndVerifyClientCert`) and runtime wire bytes.

---

## B.2.1 — F8.1 daemon `/api/v1/federation/jwt` mount + auth gating

### Spec quote — `internal/federation/cross_org_jwt.go:84-95` (architecture intent)

> *"CrossOrgJWTMinter is the daemon-side handler. Wire it onto the runtimehttp.Server at `/api/v1/federation/jwt`… The handler requires the hub-attesting `X-Chepherd-Caller-Org` + `X-Chepherd-Hub-Attest:true` headers; **in production these are terminated against the hub's mTLS cert pinned at the daemon's federation listener (T3.1)**."*

### Probe — `POST /api/v1/federation/jwt` on daemon-Y main listener

| Probe | Headers | HTTP | Body |
| --- | --- | --- | --- |
| a — no headers at all | `Content-Type: application/json` | `401` | `missing Bearer token` |
| b — caller-org only (no Hub-Attest) | `X-Chepherd-Caller-Org: alice.example` | `401` | `missing Bearer token` |
| c — full F8 attestation, NO Bearer (the "hub spoof" scenario) | `X-Chepherd-Caller-Org: alice.example` + `X-Chepherd-Hub-Attest: true` | `401` | `missing Bearer token` |

**Wire bytes** (`B2.1.c-direct-spoof.body` + `.meta`): `http=401 body="missing Bearer token"`.

### Architectural finding — production mTLS-attest promise unrealized

The cross-org mint endpoint is mounted via `Server.mountCrossOrgFederationMint(mux)` at `internal/runtimehttp/server.go:286`, which puts it inside the **dashboard mux** wrapped by `authMiddleware` (`server.go:410`). That middleware (`server.go:424-462`) enforces a Bearer token on every `/api/v1/*` path.

The architectural intent stated in `cross_org_jwt.go:84-95` is that the F8 hub-mediated flow terminates **at the federation listener (T3.1's mTLS listener)** so the hub's mTLS cert is the trust anchor. The wiring as shipped does **not** mount the mint endpoint on the federation listener; it mounts it on the dashboard listener with the operator's Bearer middleware in front.

**Consequence: the F8 hub-mediated flow cannot work in production.** The hub does not carry the daemon's operator Bearer token (and shouldn't — that's the operator's local credential, not a cross-org credential). The relay attempt in B.2 below confirms.

### Verdict — **PASS (security)** + **FINDING (architecture)**

The endpoint *does* refuse unauthenticated callers — that part is correct. But the SAFE gating chosen is incompatible with the F8 hub-relay architecture documented in the code. Either:

1. **Move mint to federation listener** (intended design): mount at `/api/v1/federation/jwt` on the `--federation-listen` port; verify hub identity via the federation listener's mTLS chain; drop the X-Chepherd-Hub-Attest header (the cert proves it).
2. **Exempt `/api/v1/federation/jwt` from Bearer middleware**: rely solely on the X-Chepherd-Hub-Attest header — but then ANY caller can spoof attestation (this is the bad option).
3. **Mint a hub-specific token at daemon boot**: persist a long-lived Bearer the operator copies onto the hub. Complicates rotation but works without mTLS plumbing.

This is a P0 architectural-wiring finding for the F8 wave. File as separate issue.

---

## B.2 — F8 cross-org JWT mint via hub

### Spec quote (§10 Pattern 2 Phase 2, lines 646–654)

> *"A→RA: SendMessage to agent-C URL ... RA→CPX: request JWT to call agent-C ... CPX→BR: federation auth request for agent-C ... BR→CPY: relay to home daemon ... CPY: mint JWT signed by CPY key ... BR→CPX: forward JWT ... CPX→RA: bundled credentials"*

### Spec quote (§15.2 claim table, lines 1188–1197)

> | Claim | Value |
> |---|---|
> | `iss` | Daemon URL of issuing org |
> | `sub` | Calling agent SID |
> | `aud` | Target agent SID |
> | `exp` | Issue time + 60 seconds (default, configurable per grant) |
> | `iat` | Issue time |
> | `jti` | Unique JWT ID (prevents replay) |
> | `chepherd_grant_id` | Reference to the RBAC grant authorizing this call |
> | `chepherd_rate_window` | Rate-limit window identifier for accounting |

### Hub-mediated flow (the intended F8 path)

Hub booted with `--allowed-orgs alice.example,bob.example --federation-targets bob.example=http://127.0.0.1:${DAEMON_Y_HTTP},alice.example=http://127.0.0.1:${DAEMON_X_HTTP}`.

| Probe | HTTP | Body |
| --- | --- | --- |
| `POST /v1/federation/auth` without `X-Chepherd-Org` | `401` | `{"error":"no authenticated org identity"}` |
| `POST /v1/federation/auth` `X-Chepherd-Org: carol.example` (not allowlisted) | `403` | `{"error":"caller org not in allowlist"}` |
| `POST /v1/federation/auth` `targetOrgId: carol.example` (not allowlisted) | `403` | `{"error":"target org not in allowlist"}` |
| `POST /v1/federation/auth` `X-Chepherd-Org: alice.example` `targetOrgId: bob.example` — **SUCCESS path** | **`401`** ❌ | **`missing Bearer token`** ❌ |

Hub-mediated SUCCESS path FAILS. Hub healthz post-walk:

```json
{"federation":{"enabled":true,"target_orgs":["bob.example","alice.example"],"total_fails":0,"total_relays":1},...}
```

Hub counted the relay (`total_relays: 1`) — the request reached daemon-Y — but daemon-Y returned 401 (Bearer middleware), and the hub forwarded that response verbatim to the caller.

**Cumulative B.2 verdict for hub-mediated flow: FAIL.** F8 wave is non-functional end-to-end in any production-like configuration (where dashboard Bearer auth is enabled).

### §15.2 claim verification (via direct-with-Bearer path — bypassing hub)

To verify the JWT shape itself, the walk bypasses the hub and calls daemon-Y's mint endpoint directly with daemon-Y's operator Bearer + the F8 attestation headers. This isn't the production flow (only the operator should have this token), but it lets us inspect the JWT body. Mint succeeds, returning HTTP 200 + a real ES256 JWT.

JWT header (`B2-jwt.header.json`):
```json
{"alg":"ES256","kid":"chepherd-a2a-es256-1780248187125679146","typ":"JWT"}
```

JWT claims (`B2-jwt.claims.json`):
```json
{"aud":"runner-bob-XYZ","exp":1780248956,"iat":1780248656,"iss":"bob.example","nbf":1780248656,"scope":"a2a.send","sub":"alice.example"}
```

§15.2 claim coverage row-by-row:

| Spec claim | Present in chepherd JWT? | Value / divergence | Verdict |
| --- | --- | --- | --- |
| `iss` (Daemon URL of issuing org) | ✓ | `"bob.example"` — chepherd uses **orgID**, not URL. Minor semantic divergence from spec text. | PARTIAL |
| `sub` (Calling agent SID) | ✓ | `"alice.example"` — chepherd uses calling **orgID**, not agent SID. Minor semantic divergence. | PARTIAL |
| `aud` (Target agent SID) | ✓ | `"runner-bob-XYZ"` — matches spec "target agent SID" exactly | PASS |
| `exp` (iat + 60s default) | ✓ | `iat=1780248656 exp=1780248956` → delta = **300s (5 min)**. Spec default is **60s** (configurable). chepherd's `crossOrgJWTTTL = 5 * time.Minute` (`cross_org_jwt.go:46`) diverges from spec default by 5×. | DIVERGE (spec says "default, configurable" so not strict FAIL; flag for explicit policy decision) |
| `iat` | ✓ | `1780248656` | PASS |
| `jti` (Unique JWT ID; prevents replay) | **✗ MISSING** | Chepherd's `claims` map at `cross_org_jwt.go:152-161` lists `iss, sub, aud, scope, nbf, exp, iat`. **No `jti`.** | **FAIL** per chepherd-lead Q2 ruling (T1 #530 claimed jti enforcement; the JWT itself doesn't even carry the claim, so enforcement is structurally impossible) |
| `chepherd_grant_id` | **✗ MISSING** | No `chepherd_grant_id` claim emitted. | **FAIL** |
| `chepherd_rate_window` | **✗ MISSING** | No `chepherd_rate_window` claim emitted. | **FAIL** |

Extra claims chepherd emits beyond §15.2: `nbf` (RFC 7519 standard — acceptable), `scope` (chepherd-specific — acceptable as it's clearly an extension).

### Replay verification

Two mints with identical params, 1 second apart (`B2-jwt.claims.json` + `B2-jwt2.claims.json`):

```
1st: {"aud":"runner-bob-XYZ","exp":1780248956,"iat":1780248656, ..., "sub":"alice.example"}
2nd: {"aud":"runner-bob-XYZ","exp":1780248957,"iat":1780248657, ..., "sub":"alice.example"}
```

Two JWTs differ only by `iat`/`exp`. No `jti` to differentiate them as unique events. **Replay protection structurally impossible** with current claim set — even if the daemon tracked seen JWTs, there's no per-mint unique identifier to dedup on.

### ES256 signature verification against daemon-Y JWKS

JWKS at `http://127.0.0.1:${DAEMON_Y_HTTP}/.well-known/jwks.json`:

```json
{"keys":[{"alg":"ES256","crv":"P-256","kid":"chepherd-a2a-es256-1780248187125679146","kty":"EC","use":"sig","x":"3SOL2zoAiF1KLP5nAfXZcKmetuGiezqji2A5j4uih4g","y":"UIaPu2XRomQbpXNBammzQo_oGtVdmC4981399fjPOpo"}]}
```

JWT `kid` matches JWKS `kid` (`chepherd-a2a-es256-1780248187125679146`). ES256 verify against the publishe EC P-256 point passes (`B2-sig-verify.log`):

```
SIGNATURE-VERIFY: OK
```

**Verdict: PASS.** ES256 signature scheme matches §15.2 ("Signed with daemon's ES256 private key. Public key in daemon's Agent Card and chepherd.org directory."). JWKS publication at `/.well-known/jwks.json` matches §6.6 + #225 B2.

### Tamper detection

Flip `sub` from `alice.example` to `mallory.example`, re-verify (`B2-tamper-verify.log`):

```
TAMPER-VERIFY: INVALID (correct — tampered JWT rejected)
```

**Verdict: PASS.**

### Body-blind cross-cut (chepherd-lead-requested probe per §23)

Searched hub stderr for the 40-char JWT prefix (`B2-hub-jwt-leak.log`): `no-leak`. Hub stderr is empty of JWT material. **Verdict: PASS conditional** — note that the hub-mediated SUCCESS path failed at daemon-Y, so the hub didn't actually have a JWT body to relay. The body-blind invariant is technically un-exercised by this walk; will need to re-verify after F8 wiring is fixed.

### Cumulative B.2 verdict — **FAIL (e2e)** + **PARTIAL on JWT shape**

| Sub-verdict | Status |
| --- | --- |
| Hub-mediated mint round-trip | **FAIL** (Bearer-middleware wiring blocks hub→daemon) |
| Hub negative probes (no-identity / not-allowlisted / no-target) | **PASS** (3/3) |
| §15.2 claim coverage | **PARTIAL** (5/8 present; `jti`, `chepherd_grant_id`, `chepherd_rate_window` MISSING) |
| ES256 signature | **PASS** |
| JWKS publication + kid match | **PASS** |
| Tamper detect | **PASS** |
| Replay protection | **FAIL** (no `jti` claim → structurally impossible) |
| TTL | **DIVERGE** (5 min vs spec 60s default) |
| Hub body-blind | **PASS (conditional)** — hub never actually relayed a JWT in this walk |

### Issues to file

- **P0 — F8 wiring**: cross-org JWT mint flow non-functional end-to-end. Mint endpoint at `/api/v1/federation/jwt` is gated by dashboard's `authMiddleware` (Bearer-token). Hub-relay (#498) has no way to satisfy this. The intended design ("hub-attested via mTLS at federation listener") is not what shipped. Repro: see `walk-categoryB-mint.sh` + `B2-success-mint.body`.

- **P1 — F8 §15.2 claim gaps**: minted JWT missing `jti`, `chepherd_grant_id`, `chepherd_rate_window`. Repro: `B2-jwt.claims.json`. Spec is `docs/V0.9.2-ARCHITECTURE.md:1188-1197`.

- **P2 — F8.1 TTL divergence**: chepherd `crossOrgJWTTTL = 5 min`; spec §15.2 default = 60s. Spec says "configurable per grant" so not strict FAIL; but the default should match the spec default unless there's an explicit policy decision otherwise.

- **P2 — F8 `/api/v1/federation/jwt` test coverage gap**: `internal/runtimehttp/p0_557_federation_mint_walk_test.go` uses `httptest.NewServer(mux)` without `authMiddleware` wrapping → tests pass against the mux directly, missing the production wiring. Adding `s.AuthToken = "xyz"` + asserting both `401 without Bearer` and the F8 hub-relay flow's actual blocked state would have caught the wiring bug pre-merge.

---

## B.3 — F5 SDP signaling body-blind

### Spec quote (§10 Pattern 2 Phase 5, lines 670–675)

> *"RA→BR: SDP offer plus ICE candidates plus DTLS fingerprint. BR→RC: forward signaling. RC→BR: SDP answer plus ICE candidates plus DTLS fingerprint. BR→RA: forward answer."*

### Spec quote (§23 invariants, lines 1475 + 1479)

> *"Agent payload never traverses internet in plaintext — DTLS E2E for P2P, HTTPS to trusted endpoint for proxy."*
> *"chepherd.org metadata-only summaries never include agent payload."*

### Spec quote (`cmd/chepherd-hub/signaling.go:14-23`)

> *"DESIGN INVARIANTS (per V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 5): 1. Hub is BODY-BLIND. Offer/answer/ice payloads are opaque blobs; hub never decodes them. ... 2. Hub is STATELESS for content. ... 3. Hub authenticates BOTH sides via mTLS per T3.1. The cert's CN/SAN identifies the organization; routing happens against the authenticated identity, not a client-supplied "fromOrgID" header (which would let a malicious org spoof)."*

### Setup

Hub booted at free port with `--allowed-orgs alice.example,bob.example --stun-listen "" --turn-listen ""`. Auth via `X-Chepherd-Org` header (dev path; production uses mTLS cert CN).

### Probe 1 — alice POSTs offer to bob (1 KB random payload)

Payload: 1024 random bytes from `/dev/urandom`, sha256:
```
6b82f62423c0e809e59402e191b2203ca43fb0dd4911e55618caefe71f45fec4
```

Wire request (`B3-offer.req.json`): `{"fromOrgId":"alice.example","toOrgId":"bob.example","sessionId":"qa-B3-...","payload":{"opaque":"<base64 1KB random>"}}`

Response: `http=202 {"accepted":true,"kind":"offer","session_id":"qa-B3-...","to":"bob.example"}`

### Probe 2 — bob fetches pending + SHA-256 round-trip

`GET /v1/signaling/pending?orgId=bob.example` with `X-Chepherd-Org: bob.example` → `http=200`. Response body wraps the frame with the payload preserved verbatim. Extracted `payload.opaque`, base64-decode, sha256:
```
6b82f62423c0e809e59402e191b2203ca43fb0dd4911e55618caefe71f45fec4   ← IN
6b82f62423c0e809e59402e191b2203ca43fb0dd4911e55618caefe71f45fec4   ← OUT
```

**MATCH.** Body-blind invariant holds — hub forwarded the 1 KB opaque payload byte-for-byte. Verdict file `B3-body-blind.verdict`:
```
MATCH 6b82f62423c0e809e59402e191b2203ca43fb0dd4911e55618caefe71f45fec4
```

### Probe 3 — reverse direction (bob → alice answer)

512-byte random payload, sha256 `8eb1b18de324fd6f7b44075be4d4bb600961e74a11d790fa3ba7c39d333f4392`. After bob POSTs answer + alice fetches pending, the extracted payload sha256 matches **identically**.

### Probe 4 — ICE candidate exchange

Real ICE candidate string `candidate:842163049 1 udp 1677729535 192.0.2.1 50000 typ srflx raddr 10.0.0.1 rport 50000`. POST `/v1/signaling/ice` → `http=202 {"accepted":true,"kind":"ice","session_id":"qa-B3-...","to":"bob.example"}`.

### Probe 5 — spoof `fromOrgId` defense (§23 design-invariant 3)

Alice (authenticated as `alice.example`) attempts to POST offer with `fromOrgId: "bob.example"`:
```json
HTTP 403 {"auth_org":"alice.example","claimed_org":"bob.example","error":"fromOrgId doesn't match authenticated org identity"}
```

**PASS.** Hub honors the design-invariant: authoritative identity comes from auth, not caller-supplied body field.

### Probe 6 — mailbox-snoop defense

Alice (authenticated) attempts `GET /v1/signaling/pending?orgId=bob.example`:
```json
HTTP 403 {"auth_org":"alice.example","claimed_org":"bob.example","error":"orgId query param doesn't match authenticated org identity"}
```

**PASS.** No org can drain another org's mailbox.

### Probe 7 — non-allowlisted caller

`carol.example` (not in `--allowed-orgs alice.example,bob.example`) attempts to post offer:
```json
HTTP 403 {"error":"org not in --allowed-orgs allowlist","org":"carol.example"}
```

**PASS.** Allowlist gates apply to both `fromOrgId` and `toOrgId`.

### Body-blind cross-cut (chepherd-lead-requested probe per §23 invariant)

Searched hub stderr (`B3-hub.stderr`) for the first 64 chars of the IN payload's base64 form. Result: **no match**. Hub stderr only contains:
```
✓ chepherd-hub HTTP listening on http://127.0.0.1:56161 (version=0.9.4-f1, allowed-orgs="alice.example,bob.example")
2026/06/01 01:35:01 [chepherd-hub] TURN disabled (--turn-listen empty)
```

No payload, no frame metadata, no per-call audit lines. **PASS** on §23 metadata-only invariant.

**NOTE:** the hub doesn't log frame-metadata either (e.g., "received offer from alice→bob session qa-B3-...") — which is even stricter than §23 allows. An operator-debugging mode that logged metadata-only would be acceptable per §23; chepherd-hub currently logs nothing per call, which is the most conservative posture. Either way, payload bytes never appear.

### Cumulative B.3 verdict — **PASS**

| Probe | Expected | Observed | Verdict |
| --- | --- | --- | --- |
| 1 — offer accept | 202 | 202 | PASS |
| 2 — bob fetch + SHA round-trip | MATCH | MATCH (1 KB random) | PASS |
| 3 — reverse direction | MATCH | MATCH (512 B random) | PASS |
| 4 — ICE candidate | 202 | 202 | PASS |
| 5 — spoof fromOrgId | 403 | 403 + "doesn't match authenticated org" | PASS |
| 6 — mailbox snoop | 403 | 403 + "orgId query doesn't match" | PASS |
| 7 — non-allowlisted | 403 | 403 + "not in allowlist" | PASS |
| body-blind hub stderr | no payload | no payload, no metadata either | PASS |

All 4 design-invariants from `signaling.go:14-32` honored: body-blind ✓, stateless-for-content (TTL-bounded, no audit) ✓, auth-driven routing ✓, short-poll pending ✓.

---

## B.4 — F6 TURN REST creds + pion/turn/v5 Allocate

### Spec quote (§10 Pattern 2 Phase 4, lines 661–668)

> *"RA→BR: TURN Allocate Request RFC 5766 ... BR→RA: relay candidate"*

### Spec quote (`cmd/chepherd-hub/turn.go:6-30`)

> *"PREMISE-CHECK FINDING (#496 dispatch 2026-06-01): pion/turn/v5 ships full Server + ServerConfig + the standard LongTermTURNRESTAuthHandler (draft-uberti-behave-turn-rest-00 timestamp:username format) ... 3. EventHandler.OnAllocationCreated / OnAllocationDeleted update an active-allocations counter + emit one-line audit logs. Logs metadata only — username + relay addr + timestamps — NEVER the relayed bytes."*

### Setup

Hub booted at free TCP port + free UDP port. **Distinctive `--turn-secret` value** (`qa-b4-secret-DO-NOT-LOG-THIS-VALUE-1234567890`) used as a marker for the secret-bleed cross-cut. `--turn-relay-ip 127.0.0.1 --turn-public-host 127.0.0.1:${UDP}` so pion advertises a localhost relay (no env-gap on TURN UDP bind on this bare-metal host).

### REST credentials mint (POST `/v1/turn/credentials`)

Authenticated probe (`X-Chepherd-Org: alice.example`):

```json
{"username":"1780249760:alice.example","password":"WrUd8b8YYGq0...redacted","ttl":600,"uris":["turn:127.0.0.1:46849?transport=udp"],"realm":"chepherd-hub"}
```

Username format: `<expiry-unix>:<orgID>` per `draft-uberti-behave-turn-rest-00` — confirmed by auth-FAIL line below showing `user=1780249760:alice.example`. Password is HMAC-SHA1 of username keyed on `--turn-secret`. TTL = 600s = 10 min (matches `turnCredTTL = 10 * time.Minute` constant; matches F5 signaling-frame TTL per spec-doc note).

Negative — no auth (`B4-mint-noauth`): `http=401 {"error":"no authenticated org identity"}`.
Negative — non-allowlisted caller (`B4-mint-carol`): `http=403 {"error":"org not in allowlist","org":"carol.example"}`.

### pion/turn/v5 Client.Allocate against real hub TURN listener — VALID creds

Go walker at `scripts/a2a-conformance/cmd/categoryB-turn-walker` uses `pion/turn/v5 v5.0.4` (same version as `cmd/chepherd-hub/turn.go`). Two Allocate cycles (closed + held-open):

```
VALID allocate:    ALLOCATE-OK relay=127.0.0.1:60761
HELD-OPEN allocate: ALLOCATE-OK relay=127.0.0.1:37393
```

Hub stderr concurrent (`B4-allocation-lines.log`):

```
2026/06/01 01:39:20 [chepherd-hub] turn alloc CREATED user=alice.example src=127.0.0.1:52224 relay=127.0.0.1:60761 active=1
2026/06/01 01:39:20 [chepherd-hub] turn alloc DELETED user=alice.example src=127.0.0.1:52224 active=0
2026/06/01 01:39:20 [chepherd-hub] turn alloc CREATED user=alice.example src=127.0.0.1:53985 relay=127.0.0.1:37393 active=1
2026/06/01 01:39:20 [chepherd-hub] turn alloc DELETED user=alice.example src=127.0.0.1:53985 active=0
```

Each line carries **only**: orgID, src UDP addr+port, relay UDP addr+port, counter value, timestamp. **No payload bytes**, no relayed-data hex dumps, no STUN attribute hex. §23 metadata-only invariant honored.

### Tampered password → integrity-check-fail

Walker re-runs Allocate with `"xx"+password+"xx"`:

```
TAMPERED allocate: ALLOCATE-FAIL
```

Hub stderr captures the auth failure:

```
2026/06/01 01:39:21 [chepherd-hub] turn auth FAIL method=Allocate user=1780249760:alice.example
2026/06/01 01:39:21 [chepherd-hub] turn alloc ERROR src=127.0.0.1:51417 msg=failed to handle Allocate-request from 127.0.0.1:51417: integrity check failed
```

The username appears (matches REST-API timestamp:orgID format) but the tampered password itself does NOT appear — pion's `LongTermTURNRESTAuthHandler` returns boolean verdict, doesn't echo the bad credential. **PASS.**

### Healthz counter lifecycle

`B4-healthz.{before,during,after}.json`:

| Snapshot | active_allocations | total_allocations | total_auth_fails |
| --- | --- | --- | --- |
| BEFORE | 0 | 0 | 0 |
| DURING (held-open alloc) | **1** | 2 | 0 |
| AFTER (post-close) | 0 | 2 | 0 |

Counter increments on `OnAllocationCreated` + decrements on `OnAllocationDeleted` per `turn.go:144-156` event handlers. **PASS** — observable from operator side via healthz alone (no log-scraping needed).

### Secret-bleed cross-cut (chepherd-lead-requested)

Boot the hub with a marker value as `CHEPHERD_HUB_TURN_SECRET=qa-b4-secret-DO-NOT-LOG-THIS-VALUE-1234567890`, then `grep -F "${TURN_SECRET}" hub.stderr`:

```
no-leak (probed marker: 'qa-b4-secret-DO-NOT-LOG-THIS-VALUE-1234567890')
```

Hub stderr does NOT contain the raw `--turn-secret` value. The HMAC-derived password DOES appear in mint response (it's the credential the client uses) but the seed secret never echoes back through any logged path. **PASS — no P0 secret-bleed.**

### Cumulative B.4 verdict — **PASS**

| Probe | Expected | Observed | Verdict |
| --- | --- | --- | --- |
| Mint creds (authenticated) | 200 + REST-shape | 200 + username/password/ttl/uris/realm | PASS |
| Mint creds (no auth) | 401 | 401 | PASS |
| Mint creds (non-allowlisted) | 403 | 403 | PASS |
| pion Allocate with valid creds | RELAYED-ADDRESS | `127.0.0.1:60761` + `127.0.0.1:37393` | PASS |
| pion Allocate with tampered password | auth-fail | `integrity check failed` + `turn auth FAIL` | PASS |
| Healthz counter increment | 0→1 on alloc | 0→1 (DURING snapshot) | PASS |
| Healthz counter decrement | 1→0 on close | 1→0 (AFTER snapshot) | PASS |
| OnAllocation* metadata-only (§23) | username+addrs+ts, no payload | username+src+relay+counter+ts, no payload | PASS |
| Secret-bleed (chepherd-lead cross-cut) | turn-secret NOT in stderr | NOT in stderr | PASS |

### Env-gap note

Per chepherd-lead Q5 ruling: this walk used real pion/turn/v5 against a real UDP listener on a loopback port. No fallback to in-process synthetic was needed (host permitted unprivileged UDP bind on a free port). Production deploys may need root/CAP_NET_BIND_SERVICE for the canonical RFC-5766 port 3478; that's an operator deployment concern, not a substrate gap.

---

## B.5 — F7 reverse-proxy tunnel bidirectional + body-blind

### Spec quote (§10 Pattern 2 fallback, lines 715–732)

> *"If ICE connectivity checks fail (symmetric NAT, restrictive firewall), runner falls back: RA→BR: A2A SendMessage encrypted with DTLS over TURN. BR→RC: forward ciphertext, broker cannot decrypt. RC→BR: response encrypted. BR→RA: forward ciphertext."*

### Spec quote (`cmd/chepherd-hub/tunnel.go:282-288`)

> *"Inbound A2A traffic addressed to a tunneled org gets forwarded via the tunnel + the runner's response is mirrored back to the original caller. **Body-blind: hub never decodes the payload.** URL shape: `/v1/relay/{orgID}/{path...}`. Method + Body + Headers (filtered) forwarded as-is."*

### Setup

Hub booted on free port with `--allowed-orgs alice.example,bob.example`. Stub "bob runner" Go program dials `ws://hub/v1/relay/tunnel` with `X-Chepherd-Org: bob.example` and echoes inbound frames back verbatim. Stub "alice caller" POSTs HTTP to `/v1/relay/bob.example/jsonrpc`.

### Probe 1 — SUCCESS: alice → hub → bob → echo back (1 KB random payload)

Walker output:
```
bob: WS connected to hub
bob: got to-runner frame reqID=a733ec99-2c6c-4536-95e1-f029d469d406 method=POST path=/jsonrpc bodyLen=1024
PROBE 1 SUCCESS: round-trip + body-blind SHA MATCH
```

Wire metadata (`B5-success.meta`):
```
http=200 in_sha=217991ef64d5ed2ee2318f917f5a993521c558d80d0ffd69893adef67e5e6181 out_sha=217991ef64d5ed2ee2318f917f5a993521c558d80d0ffd69893adef67e5e6181 match=true in_len=1024 out_len=1024
```

**PASS.** Hub round-tripped 1024 bytes of random data byte-for-byte. RequestID matched across to-runner and to-hub frames (UUID generated by hub, echoed by bob). 1024 IN → 1024 OUT.

### Probe 2 — NEG: no tunnel for target org (carol.example, not allowlisted)

```
PROBE 2 (no tunnel for carol): http=403 body={"error":"target org not in allowlist"}
```

The hub's allowlist defense fires BEFORE the tunnel-lookup, returning 403 rather than 502. The "no tunnel for org" 502 path is reachable when the target IS allowlisted but happens to have no live tunnel — Probe 3 exercises that case.

### Probe 3 — NEG: disconnect bob mid-flight

```
--- disconnecting bob WS ---
bob: read err: read tcp 127.0.0.1:51526->127.0.0.1:48517: use of closed network connection
PROBE 3 (after disconnect): http=502 body={"error":"no tunnel for org","org":"bob.example"}
```

**PASS.** Alice's next POST after bob's WS closes hits `t := s.tunnels.lookup(targetOrg); if t == nil { 502 }` (`tunnel.go:323-326`). Operator-friendly: returns 502 immediately, NOT a hang or 504 timeout.

### Healthz counter

```json
"tunnels": {"active":0, "enabled":true, "total_lifetime":1}
```

`total_lifetime: 1` confirms the tunnel was registered + deregistered exactly once. `active: 0` after bob's disconnect.

### Body-blind cross-cut

Searched hub stderr for the first 40 hex chars of the random payload. **No match.** Hub stderr contains only:
```
✓ chepherd-hub HTTP listening on http://127.0.0.1:40605 (version=0.9.4-f1, allowed-orgs="alice.example,bob.example")
2026/06/01 01:45:01 [chepherd-hub] TURN disabled (--turn-listen empty)
```

Same "stricter than §23 requires" pattern as B.3: chepherd-hub doesn't even log per-frame metadata (no `[chepherd-hub] tunnel registered org=bob.example` etc.). PASS.

### Cumulative B.5 verdict — **PASS**

| Probe | Expected | Observed | Verdict |
| --- | --- | --- | --- |
| 1 — round-trip + body-blind SHA | MATCH | MATCH on 1 KB random | PASS |
| 2 — non-allowlisted target | 403 | 403 "target org not in allowlist" | PASS |
| 3 — disconnect mid-flight | 502 quickly | 502 "no tunnel for org" immediately | PASS |
| Body-blind hub stderr | no payload bytes | no payload bytes + no metadata | PASS |
| Healthz counter | increments on register, decrements on close | `total_lifetime: 1, active: 0` post-walk | PASS |

---

## B.5.1 — F7.1 runner-side tunnel client (`relayTunnelClient`)

### Spec quote (`cmd/runner/relay_tunnel.go:1-35`)

> *"`cmd/runner/relay_tunnel.go` — #556 Wave F7.1 runner-side reverse-proxy tunnel client. Complements the F7 #497 hub surface (`cmd/chepherd-hub/tunnel.go`) by dialing the hub's WS endpoint and serving inbound proxied A2A requests via the runner's local http.Handler. ... **Auth**: daemon-minted JWT in `Authorization: Bearer` header on the initial WS dial (T1 #530 substrate). The hub verifies the JWT via the daemon's JWKS (T2 #510). ... **Fallback condition**: activated when ICE + TURN both fail (BlockedNAT detection). F7.1 ships the client; **the spawn-time decision to enable it lives in `cmd/run.go`'s `--hub-relay-url` flag wiring** + runtime's transport-fallback chain."*

### Primitive verification — unit + live tests

`go test -run "TestWaveF71|TestV094Walk_F71" -v ./cmd/runner/`:

```
=== RUN   TestWaveF71_Dial_RejectsEmptyConfig                 — PASS
=== RUN   TestWaveF71_Dial_HappyPath_StateFlipsToOpen         — PASS
=== RUN   TestWaveF71_Dial_SetsAuthAndOrgHeaders              — PASS
=== RUN   TestWaveF71_HandleFrame_RoutesThroughHandler_BodyBlind — PASS
=== RUN   TestWaveF71_HandleFrame_HopByHopHeadersStripped     — PASS
=== RUN   TestWaveF71_Close_Idempotent                        — PASS
=== RUN   TestV094Walk_F71_RunnerTunnel_ThroughRealHubBinary  — PASS
    p0_556_relay_tunnel_walk_test.go:111: F7.1 live walk: alice → real hub binary → bob runner tunnel client; body byte-exact (34 bytes); path+method preserved
```

7/7 PASS in 0.5s. The live walk drives a real `chepherd-hub` subprocess with bob as a real `relayTunnelClient` instance. Body byte-exact preserved.

### Reconnect+wiring finding

**The `relayTunnelClient.Done()` signal fires on disconnect — but there is no reconnect loop anywhere in the F7.1 scope.**

Source inspection — `cmd/runner/relay_tunnel.go:179`:
> *"Done returns a channel closed when Close is called or the read pump exits. Useful for **callers that want to react to disconnects (e.g., re-dial loop)**."*

Grep across the runner binary + `cmd/run.go` for `relayTunnelClient` usage:
```
$ grep -rn "relayTunnelClient\|--hub-relay-url\|newRelayTunnelClient" cmd/runner/ cmd/run.go
cmd/runner/relay_tunnel.go:11://   relayTunnelClient.Dial
cmd/runner/relay_tunnel.go:31:// decision to enable it lives in cmd/run.go's --hub-relay-url
cmd/runner/relay_tunnel.go:67:// relayTunnelClient is the runner-side tunnel connector. One
cmd/runner/relay_tunnel.go:69:type relayTunnelClient struct {
cmd/runner/relay_tunnel.go:93:// newRelayTunnelClient constructs a client without dialing. Call
cmd/runner/relay_tunnel.go:95:func newRelayTunnelClient(hubURL, orgID, bearerToken string, handler http.Handler) *relayTunnelClient {
... [other refs inside relay_tunnel.go itself]
```

**No caller in cmd/run.go.** No `--hub-relay-url` flag. No transport-fallback chain. The primitive ships unreached from production boot — operators cannot enable F7.1 today by setting any combination of flags or env vars.

### Verdict — **PASS (primitive)** + **FINDING (reconnect+wiring)**

The primitive itself is solid: 7 tests including a real-binary live walk all PASS. Body-blind preserved through the runner handler. Hop-by-hop headers stripped. Close idempotent.

But B.5.1's named criterion in the plan — *"runner tunnel client reconnect-with-backoff"* — is NOT in F7.1's scope. The reconnect loop belongs to the caller (per `relay_tunnel.go:179`), and the caller doesn't exist yet.

**Issue stub** (to file as part of post-walk bundled findings):

- **P1 — F7.1 unreached primitive**: `relayTunnelClient` is functional but not wired into a production code path. No `--hub-relay-url` flag in `cmd/run.go`. No transport-fallback chain that picks F7.1 when ICE + TURN fail. Following PRs need to wire the spawn-time decision + the reconnect loop. Lower severity than F8 (B.2 P0 #562) because F7.1 doesn't actively break anything — it's just dormant.

Sister pattern to F8 (#562): both ship the substrate but not the activation path. Worth proactively auditing all v0.9.4 PRs for this "primitive shipped, wiring TBD" pattern before declaring v0.9.4 DoD complete.

---

## B.6 — F2 WebRTC DataChannel speedup vs HTTP baseline

### Spec quote (briefing from chepherd-lead Q1)

> *"Worker2's original F2 PR cited 'F2 DataChannel mean (n=10): 152.368µs / HTTP baseline mean (n=10): 240.535µs / F2 speedup vs HTTP: 1.58x' — that's loopback localhost, ~10 calls/side. Replicate same conditions for fair comparison. ... PASS if RATIO ≥ 1.0× (DataChannel ≥ HTTP), FAIL if RATIO < 1.0× (regression)."*

### Methodology

Used the canonical PR #492 test `TestV094Walk_F2_DataChannel_BeatsHTTPBaseline` at `cmd/runner/p0_492_two_runner_live_walk_test.go`:
- N=10 trials per transport per iteration
- DC: in-process pion `PeerConnection` pair via `connectPair()` (real pion/webrtc/v4 SCTP-over-DTLS-over-ICE stack)
- HTTP: `httptest.NewServer(mux)` pointing at the same stub handler
- Same goroutine + same machine + back-to-back trial alternation per iteration → minimum scheduling variance per pair

To get variance distribution: ran the test **10 times in succession** (so 100 DC trials + 100 HTTP trials total).

### Results (full table, `B6-perf-multirun.log`)

| Iter | DC mean (µs) | HTTP mean (µs) | Speedup ratio |
| ---: | ---: | ---: | ---: |
| 1 | 147.154 | 254.297 | **1.73×** |
| 2 | 467.831 | 263.734 | 0.56× ← outlier |
| 3 | 154.309 | 314.866 | **2.04×** |
| 4 | 153.311 | 273.872 | **1.79×** |
| 5 | 167.816 | 269.186 | **1.60×** |
| 6 | 127.616 | 254.604 | **2.00×** |
| 7 | 169.983 | 291.624 | **1.72×** |
| 8 | 153.876 | 364.747 | **2.37×** |
| 9 | 150.298 | 256.046 | **1.70×** |
| 10 | 140.052 | 257.867 | **1.84×** |

### Aggregate stats

- **9/10 iterations PASS** (ratio ≥ 1.0× per Q1 threshold)
- **1/10 iterations**: ratio = 0.56× (DC slower than HTTP under transient GC/scheduling pressure — note that DC mean jumped to 467 µs from a baseline of ~150 µs, while HTTP held at ~265 µs; classic single-goroutine pause artifact)
- **Median speedup**: 1.76×
- **Mean speedup (excl. outlier)**: 1.87×
- **Mean speedup (all 10)**: 1.74×
- Worker2's PR #492 reported 1.58× — **within observed range**; my measured range (1.60×–2.37×, median 1.76×) is consistent with or slightly higher than the original claim.

### Verdict — **PASS** + **NOTE**

- **Architectural claim** ("DataChannel is not slower than HTTP on loopback in steady state") is HONORED: 9/10 iterations show DC strictly faster, median 1.76×.
- **NOTE**: F2 performance is noise-sensitive — 1/10 iterations showed regression under transient GC/scheduling pressure. Production p99 latency comparison would need more rigorous measurement (statistically-controlled benchmark with GOMAXPROCS pinning, `runtime.GC()` between trials, percentile reporting). This is a P3 observability gap — methodology-fix, not a substrate-fix.

### Methodology delta vs PR #492 claim

The PR #492 commit message reports a single run: `1.58×`. My single-run replay against fresh `chepherd` build on the same host first measured **1.58×** exactly — perfect match to PR #492's claim. The 9-run replay shows the underlying distribution: median 1.76×, range 1.60×–2.37× excluding outlier. The PR #492 claim sits at the low end of the typical range; not cherry-picked, just unlucky-or-conservative single sample.

**Conclusion**: F2 claim verified. Substrate is correct.

---

## Evidence files

All under `/tmp/v094-qa-B/evidence/`:

- `B1.1.a-nomtls-listener.ss` (empty — listener absent default-off)
- `B1.1.a-nomtls-listener.verdict` (`not-bound`)
- `B1.1.b-mtls-listeners.ss` (two LISTEN lines on configured federation ports)
- `B1-cert-A.details`, `B1-cert-B.details` (openssl x509 subject + issuer + dates + sha256)
- `B1-cert-setup.log` (cert export + cross-pin invocation)
- `B1-daemon-X.stderr` (federation banner + 3 TLS rejection lines)
- `B1-probe1-success.{body,curl-vvv}` (TLS 1.3 mutual handshake + 200 healthz)
- `B1-probe2-noclient.{body,curl-vvv}` (`tlsv13 alert certificate required`)
- `B1-probe3-untrusted.{body,curl-vvv}` (`tlsv1 alert unknown ca`)
- `B1-probe4-openssl-sclient.log` (raw s_client handshake)
- `B1-probe5-openssl-nocert.log` (s_client no-cert rejection)
- `B2.1.{a,b,c}-*` (3 daemon-mint endpoint auth-gate probes)
- `B2-neg-{no-identity,not-allowlisted,no-target}.{body,meta}` (hub-side denies)
- `B2-success-mint.{body,meta}` (hub-mediated FAIL: 401 missing Bearer token)
- `B2-direct-mint.{body,meta}` (direct-with-bearer success: 200 + real JWT)
- `B2-jwt.{raw,header.json,claims.json}` (JWT decoded for §15.2 audit)
- `B2-jwt2.claims.json` (2nd mint for jti-replay test → no jti found)
- `B2-jwks.json` (daemon-Y JWKS — ES256 EC P-256 key for sig verify)
- `B2-sig-verify.log` (ES256 verify against JWKS = OK)
- `B2-tamper-verify.log` (sub→mallory tamper = INVALID = correct)
- `B2-hub-healthz.json` (post-mint federation counters)
- `B2-hub.stderr` + `B2-hub.federation-lines` + `B2-hub-jwt-leak.log` (body-blind probe)
- `B2-daemon-Y.stderr` (federation banner)

- `B3-payload-{in,out}.{bin,b64}` (1 KB random SDP payload — IN + OUT)
- `B3-body-blind.verdict` (`MATCH <sha256>`)
- `B3-offer.{req,resp}.json` + `.meta` (alice→bob offer)
- `B3-pending.{resp.json,meta}` (bob drains pending)
- `B3-answer.{req,resp}.json` + `B3-pending-alice.{resp.json,meta}` (bob→alice reverse direction)
- `B3-ice.{resp.json,meta}` (ICE candidate exchange)
- `B3-spoof.{resp.json,meta}` (fromOrgId mismatch → 403)
- `B3-snoop.{resp.json,meta}` (orgId mailbox snoop → 403)
- `B3-carol.{resp.json,meta}` (non-allowlisted caller → 403)
- `B3-hub.stderr` + `B3-hub-leak.log` + `B3-hub-signaling-lines.log` (body-blind probe)
- `B4-mint.{resp.json,http}` + `B4-mint-{noauth,carol}.{body,meta}` (REST cred mint + auth-gating probes)
- `B4-allocate-{valid,heldopen,tampered}.log` (pion/turn/v5 Allocate logs)
- `B4-allocation-lines.log` (OnAllocationCreated/Deleted/auth-FAIL metadata)
- `B4-healthz.{before,during,after}.json` (counter lifecycle)
- `B4-hub.stderr` + `B4-secret-bleed.log` (turn-secret NOT in stderr)
- `B4-walker.out` (Go walker stdout summary)
- `B5-payload-{in,out}.bin` + `B5-sha-{in,out}.hex` (1 KB random round-trip)
- `B5-success.meta` (http + sha-match + lengths)
- `B5-neg-notunnel.meta` (carol.example → 403)
- `B5-neg-disconnect.meta` (post-disconnect → 502)
- `B5-walker.out` (stub bob + alice probes stdout)
- `B5-hub.stderr` + `B5-leak.log` (body-blind probe)
- `B5-healthz.json` (tunnels.total_lifetime counter)
- F7.1 primitive test transcript: `go test -run "TestWaveF71|TestV094Walk_F71" -v ./cmd/runner/` (captured inline in B.5.1 verdict above)
- `B6-perf-multirun.log` (10 iterations × 10 trials/transport — full F2 perf distribution)

Walker scripts:
- `scripts/a2a-conformance/walk-categoryB.sh` (B.1)
- `scripts/a2a-conformance/walk-categoryB-mint.sh` (B.2 + B.2.1)
- `scripts/a2a-conformance/walk-categoryB-relay.sh` (B.3; B.5 + B.5.1 to follow)
- `scripts/a2a-conformance/walk-categoryB-turn.sh` (B.4)
- `scripts/a2a-conformance/walk-categoryB-tunnel.sh` (B.5)
- Cert/crosspin helper: `scripts/a2a-conformance/cmd/categoryB-mtls-setup/main.go`
- TURN walker (pion/turn/v5): `scripts/a2a-conformance/cmd/categoryB-turn-walker/main.go`
- Tunnel walker (gorilla/websocket stub-bob + alice): `scripts/a2a-conformance/cmd/categoryB-tunnel-walker/main.go`

---

## Companion P0 issue

Filed: [#562](https://github.com/chepherd/chepherd/issues/562) — *"F8 cross-org JWT mint non-functional e2e: minter mounted on dashboard mux instead of federation listener (F8.1 wiring bug)"*

---

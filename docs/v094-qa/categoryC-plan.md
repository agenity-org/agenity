# v0.9.4 QA — Category C — Runner lifecycle — TEST PLAN (PRE-EXEC, awaiting chepherd-lead confirmation)

**Drafted:** 2026-06-01 by chepherd-worker (QA test-engineer)
**Owner:** chepherd-worker
**Reviewer:** chepherd-lead
**Parent issue:** [#560 v0.9.4 QA campaign](https://github.com/chepherd/chepherd/issues/560)
**Spec citations:** `docs/V0.9.2-ARCHITECTURE.md` §5 #3 (chepherd-runner), §10 Pattern 1 Phases (PTY ownership), §22 (per-component implementation notes lines 1413–1421)
**Sister evidence:** [`categoryB-evidence.md`](categoryB-evidence.md) (federation infra — done) + [`categoryA-evidence.md`](categoryA-evidence.md) (A2A spec — halted)
**Companion PR (just merged):** [#564](https://github.com/chepherd/chepherd/pull/564) — Category B walker tree

---

## 0. Premise + scope boundaries

Wave R PRs delivered the runner-process-split (EPIC #453 CLOSED):
- **R1 #504** — `cmd/runner/` binary scaffold + register-WS to daemon
- **R2 #463** — per-session A2A JSON-RPC endpoint at `/a2a/{sid}/jsonrpc`
- **R3 #464** — per-session Agent Card at `/a2a/{sid}/.well-known/agent-card.json`
- **R4 #465** — PTY ownership moved into runner; Deliver drives PTY; silence-finalize
- **R5 #466** — daemon de-A2A cutover; legacy daemon `/jsonrpc` returns `410 Gone` + `Deprecation: true` + `Sunset` headers per RFC 8594/9745

Substrate confirmed at HEAD: `cmd/runner/main.go` (~430 LOC) + `internal/runtimehttp/daemon_a2a_cutover_410.go`.

**Walk policy** (per chepherd-lead's "pragmatic-halt-scope" ruling from Category B):
- C.1 (R1 boot/register/quiesce), C.2 (R4 PTY ownership), C.5 (R5 410-Gone), C.6 (operator UX) — INFRA, scope-orthogonal to A.1 method-name halt, walk now.
- C.3 (R2 per-session A2A endpoint) — **DEFERRED-PENDING-A.1-REMEDIATION** because the endpoint serves the same `message/send` / `tasks/get` etc. surface that worker2's A.1 found non-spec-conformant. Walking the endpoint structurally (it exists, mounted, accepts POST) is feasible; cell verdict will be `PASS (structural) + INHERITED-FAIL-FROM-A.1 (wire shape)`.
- C.4 (R3 per-session Agent Card) — **PARTIAL** since worker2's A.4 walked the daemon's central Agent Card and found it PARTIAL (5+ schema fields present; needs §4 full audit). The per-session card at `/a2a/{sid}/.well-known/agent-card.json` is structurally the same shape; will document any per-session-specific deltas.

**DoD bar** per CLAUDE.md §2 + Category B precedent: fresh state-dir + captured wire bytes + every PASS/FAIL row backed by a quotable spec sentence + binary artifact (PTY transcript, register-WS frame, healthz, screenshot).

---

## 1. Topology

```
host: 127.0.0.1
  ┌─────── chepherd-daemon (the control plane) ───────┐
  │  cmd/run.go bin: chepherd run --headless          │
  │  --listen 127.0.0.1:<DPORT_HTTP>  (dashboard)     │
  │  --mcp-listen 127.0.0.1:<DPORT_MCP>               │
  │  --state-dir /tmp/v094-qa-C/state-D               │
  │  (no federation — single-org local walk)          │
  │                                                    │
  │  Daemon /jsonrpc → 410 Gone (R5 cutover)          │
  │  Daemon /api/v1/agent-types ← runner registers    │
  └────────────────────────────────────────────────────┘
                  ▲
                  │ WS register
                  │
  ┌─────── chepherd-runner (the data plane) ──────────┐
  │  cmd/runner/main.go bin: chepherd-runner          │
  │  --daemon-url ws://127.0.0.1:<DPORT_HTTP>/...     │
  │  --sid sid-qa-C-1 (operator-supplied for test;    │
  │       production: daemon allocates at spawn)      │
  │  --a2a-listen 127.0.0.1:<RPORT_A2A>               │
  │     → mounts /a2a/sid-qa-C-1/{jsonrpc,            │
  │       .well-known/agent-card.json}                │
  │  --mcp-listen unix:/tmp/v094-qa-C/mcp.sock        │
  │  --agent-cmd "/bin/cat"  (stub agent for PTY      │
  │     ownership probe; real Claude has expensive    │
  │     boot)                                          │
  └────────────────────────────────────────────────────┘
```

For C.6 Playwright dashboard walk: same daemon + web frontend served from `web-dir`. Need to check whether chepherd's prebuilt web bundle exists or if I need `npm run build` first.

---

## 2. Sub-areas (6 cells)

### C.1 — R1 chepherd-runner boot, register-to-daemon, quiesce

**Spec quote (`cmd/runner/main.go:1-32`):**
> *"chepherd-runner is the data-plane process that runs as PID 1 inside each agent container. It owns: per-session A2A endpoint at `/a2a/{sid}/jsonrpc` (+ Agent Card at `/a2a/{sid}/.well-known/agent-card.json`), MCP HTTP server bound to a local Unix socket inside the container ... PTY master ownership for the agent ... outbound WS to chepherd-daemon for registration, command intake, audit egress."*

**Walk:**
1. Boot daemon at fresh state-dir + free ports.
2. Boot runner with `--daemon-url ws://daemon/api/v1/runners/register`, `--sid sid-qa-C-1`, `--a2a-listen 127.0.0.1:<port>`, `--mcp-listen unix:<sock>`, `--agent-cmd "/bin/cat"`.
3. Capture runner stderr → assert: "registered with daemon", "A2A endpoint listening", "MCP socket bound", "PTY child spawned (pid N)".
4. Probe daemon `/api/v1/peers` (or `/api/v1/agent-types` per `runtimehttp/server.go:294`) → assert runner SID listed.
5. SIGTERM runner → assert clean quiesce: deregister frame, A2A listener closes, MCP socket removed, child reaped.
6. Daemon should see "runner sid-qa-C-1 disconnected" in its stderr.

**Captured:** `C1-runner.stderr`, `C1-daemon.stderr`, `C1-register.ws-frame.json` (if loggable), `C1-peers-list.json`, `C1-quiesce-trace.log`.

**Verdict criterion:** PASS iff all 6 steps produce expected output. FAIL on hang / orphan socket / orphan child process / undocumented stderr lines suggesting incomplete lifecycle.

---

### C.2 — R4 PTY ownership inside runner

**Spec quote (§22 line 1420):** *"PTY: `github.com/creack/pty` (already in codebase)"*

**Spec quote (`cmd/runner/main.go:14-15`):** *"PTY master ownership for the agent (chepherd-runner is the agent process's parent; claude-code etc. is exec'd as its child with PTY allocation)"*

**§23 invariant (lines 1473-1475):** *"No PTY file descriptors cross pod boundaries ... Daemons hold zero PTY file descriptors. Only runners hold them."*

**Walk:**
1. After C.1 boot, snapshot `/proc/<runner-pid>/fd/` for PTY entries (search for `/dev/pts/*` and `ptmx`).
2. Snapshot `/proc/<daemon-pid>/fd/` for the same → assert daemon has ZERO PTY fds (invariant).
3. Driver an A2A `message/send` (using whatever wire shape works post-A.1) → assert PTY child receives the message + stdout flows back through runner's pump.
4. Trigger silence-finalize: send a message, wait for child to fall silent, assert task transitions to `completed` after the silence-finalize window.
5. **R4 specific** — verify the runner-side Deliver path actually drives the PTY (not a stub). Source inspection of `cmd/runner/agent_pump.go` + run grep for `pty.Open\|pty.Start\|os.OpenFile.*ptmx`.

**Captured:** `C2-runner-fd-snapshot.txt`, `C2-daemon-fd-snapshot.txt`, `C2-pty-roundtrip.log`, `C2-silence-finalize.log`.

**Verdict:** PASS iff (a) runner has PTY fds, (b) daemon has zero, (c) round-trip via PTY works, (d) silence-finalize triggers task completion.

---

### C.3 — R2 per-session A2A endpoint at `/a2a/{sid}/jsonrpc`

**Spec quote (`cmd/runner/main.go:368`):** *"per-session A2A endpoint TCP bind address (host:port). Empty disables. When set + --sid non-empty, mounts /a2a/<sid>/jsonrpc serving all 11 A2A methods. (#463 Wave R2)"*

**Walk:**
1. With runner from C.1 still running, probe `GET /a2a/sid-qa-C-1/jsonrpc` → expect 405 Method Not Allowed (only POST mounted).
2. `POST /a2a/sid-qa-C-1/jsonrpc` with valid JSON-RPC envelope → expect a JSON-RPC response (whatever shape, including the A.1 method-name divergence).
3. `POST /a2a/sid-doesnt-exist/jsonrpc` → expect 404.
4. Run worker2's `walk-categoryA.sh` against the runner's per-session endpoint (not the daemon's) → expect same 11 method bodies + state machine + Agent Card divergences as A.1 finds at daemon (cross-confirm runner serves the SAME shape as daemon retired).
5. Document: structural-existence PASS + wire-shape INHERITED-FAIL-FROM-A.1.

**Captured:** `C3-runner-a2a-405.body`, `C3-runner-a2a-404.body`, `C3-runner-a2a-method-probe.log`, `C3-A1-replay-runner.log` (worker2 walker re-run against runner endpoint).

**Verdict:** PASS (structural existence) + INHERITED-FAIL-FROM-A.1 (wire shape).

---

### C.4 — R3 per-session Agent Card at `/a2a/{sid}/.well-known/agent-card.json`

**Spec quote (commit msg of #515):** *"Wave R3: per-session Agent Card at /a2a/{sid}/.well-known/agent-card.json"*

**Walk:**
1. `GET /a2a/sid-qa-C-1/.well-known/agent-card.json` → expect 200 + JSON body.
2. Compare returned card against the central daemon card (worker2 walked `A4.agent-card.json` in Category A) for structural conformance.
3. Per-session-specific assertions:
   - `name` should reference the agent flavor (claude-code, etc.) and/or the SID
   - `url` should point to the per-session A2A endpoint, not the daemon's
   - `securitySchemes` + `protocolVersion` + capabilities should be present
4. `GET /a2a/sid-doesnt-exist/.well-known/agent-card.json` → expect 404.
5. Schema audit against A2A v1.0 §4 (cross-reference worker2's A.4 PARTIAL findings — same gaps likely apply).

**Captured:** `C4-card.json`, `C4-card-404.body`, `C4-card-diff-vs-A4.json` (diff vs worker2's daemon-side card), `C4-schema-audit.md`.

**Verdict:** PARTIAL (inherits the same schema gaps worker2 found at A.4) + PASS-on-existence-and-routing.

---

### C.5 — R5 daemon 410-Gone + Deprecation + Sunset headers

**Spec quote (`internal/runtimehttp/daemon_a2a_cutover_410.go:1-15`):**
> *"Wave R5 #466. Routes return HTTP 410 Gone with: a Deprecation: true response header (RFC 9745 Deprecation), a Sunset: <wave-R5-merge-date> response header (RFC 8594), and a Link: rel='successor-version' header pointing at the per-runner endpoint."*

**Spec quote (RFC 8594 §3):** *"The Sunset HTTP response header field indicates that a URI is likely to become unresponsive at a specified point in the future."*

**Walk:**
1. `POST http://daemon/jsonrpc -d '{"jsonrpc":"2.0",...}'` → assert HTTP 410.
2. Inspect response headers → assert `Deprecation: true`, `Sunset: Sun, 31 May 2026 00:00:00 GMT`, `Link: <url>; rel="successor-version"`.
3. `GET http://daemon/jsonrpc` → assert HTTP 410 with same headers (R5 410 applies to all methods).
4. `OPTIONS http://daemon/jsonrpc` → assert 410 (or 405; check spec — depends on whether OPTIONS is excluded).
5. Daemon stderr → assert `LEGACY A2A request <method> /jsonrpc from <addr>` audit line per `daemon_a2a_cutover_410.go:53`.

**Captured:** `C5-410-post.{body,headers}`, `C5-410-get.{body,headers}`, `C5-daemon-legacy-log.txt`.

**Verdict:** PASS iff 410 + 3 RFC-mandated headers + audit-log line.

---

### C.6 — Playwright dashboard spawn → SID materialization → operator UX walk

**Spec context:** chepherd-lead briefing — "For sid creation: Playwright dashboard walk + headless equivalent (operator UX coverage)".

**Walk:**
1. Build the chepherd web bundle (if not pre-built): `npm install && npm run build` in `web/` → produces static assets in `web/dist/`.
2. Boot chepherd with `--web-dir web/dist/` so the dashboard is reachable.
3. Playwright opens browser → navigates to dashboard → assert welcome / agent-list view loads.
4. Click "Spawn agent" → fill flavor (e.g., claude-code), team, role → submit.
5. Observe SID materialize in agent list within 5s.
6. Click the new agent → verify pane loads + per-session A2A endpoint URL is shown in the metadata panel.
7. Inspect browser DevTools network tab → assert POST to `/api/v1/agent-types/spawn` (or whatever the endpoint is) returns 200 + SID in body.
8. Screenshot evidence captured per CLAUDE.md §2 DoD.

**Captured:** `C6-screenshot-spawn-page.png`, `C6-screenshot-after-spawn.png`, `C6-network-trace.har`, `C6-dashboard-stderr.log`.

**Verdict:** PASS iff dashboard reachable + spawn flow works + screenshots captured + agent appears with valid per-session endpoint URL.

---

## 3. Tooling

- `curl -vvv` + `jq` for HTTP probes (C.1, C.3, C.4, C.5)
- `ss -ltn` + `lsof` for socket/fd snapshots (C.1, C.2)
- `awk` / `grep` on `/proc/<pid>/fd/` for PTY-ownership invariant (C.2)
- Playwright MCP for C.6 (`mcp__plugin_playwright_playwright__browser_*` tools loaded on demand)
- A bash walker `scripts/a2a-conformance/walk-categoryC.sh` (sister to B's walkers) drives C.1-C.5; C.6 is a separate Playwright-driven walk.

---

## 4. Halt criterion

Walk continues through all 6 cells regardless of individual-cell FAIL (Category A + B precedent). Mid-walk halt ONLY if:
- Runner crashes on boot — P0, file immediately
- Daemon 410-Gone leaks the legacy-method handler result (i.e., returns 200 with a real A2A body) — P0
- PTY fds leak into daemon (§23 invariant violation) — P0
- Playwright dashboard returns 500 on the spawn flow — P0

Otherwise: complete the walk, ship `categoryC-evidence.md`, summarize gaps in P0/P1/P2 issue stubs.

---

## 5. Evidence shape

`docs/v094-qa/categoryC-evidence.md`, format identical to B.

---

## 6. Open questions for chepherd-lead (pre-execute decision points)

1. **C.6 web bundle:** the chepherd repo's `web/` may or may not have a pre-built bundle. Confirm OK to `npm install && npm run build` (~3-5 min) before the Playwright walk? Or is there a faster way (e.g., dashboard dev-proxy mode without a build step)?

2. **C.3 INHERITED-FAIL-FROM-A.1 wire-shape coverage:** should I re-run worker2's full `walk-categoryA.sh` against the runner endpoint (vs cross-confirm by reading worker2's evidence) for completeness? Re-running adds ~5 min but produces fresh artifact matching the runner endpoint exactly. Default: re-run.

3. **C.4 schema audit depth:** worker2's A.4 was PARTIAL with `supportedInterfaces / signature / additionalInterfaces / documentationUrl / provider per §4 schema` listed as "needs audit". Should C.4 do the full §4 schema audit (and effectively close worker2's A.4 PARTIAL for the per-session card surface), or leave it at the same PARTIAL depth worker2 documented? My recommendation: full audit on per-session card; document as `PASS / PARTIAL / FAIL per §4 row-by-row` with explicit rows for every spec field.

4. **C.2 PTY ownership probe — agent-cmd choice:** I plan to use `--agent-cmd "/bin/cat"` as the stub agent (cheap, predictable). For silence-finalize verification, `/bin/cat` falls silent immediately after stdin closes, which should trigger the silence-finalize transition. Confirm OK, or do you want real claude-code (more expensive boot, requires API key + may flake)?

5. **C.5 OPTIONS method on retired /jsonrpc:** RFC 7231 says OPTIONS should advertise available methods; RFC 8594 doesn't speak to OPTIONS specifically. Should chepherd's 410-Gone path also respond to OPTIONS with 410, or with the standard 405-and-Allow-header pattern? Defer to your judgment; will pin chepherd's actual behavior either way.

6. **C.1 quiesce timing assertion:** what's the acceptable quiesce window (SIGTERM → all sockets/processes cleaned)? Plan default: 3s soft + 5s hard. Confirm or override.

---

## 7. Time budget estimate

- C.1 (R1 boot/register/quiesce): ~20 min
- C.2 (PTY ownership + silence-finalize): ~25 min
- C.3 (per-session A2A endpoint + worker2 walker re-run): ~25 min
- C.4 (per-session Agent Card + §4 audit): ~30 min
- C.5 (R5 410-Gone): ~15 min
- C.6 (Playwright dashboard): ~40 min (includes web build)
- Evidence write-up: ~20 min
- **Total ~3h** for Category C alone.

Note: this is ~30 min over the per-category budget (~21min/cell × 6 = ~126 min), but C.6 Playwright is the heaviest cell — UI walks need browser setup + screenshot capture which takes time.

If C.6 web build is fast (no install needed) it shaves ~10 min. If it requires full `npm install` it adds ~15 min.

---

**Awaiting chepherd-lead confirmation before executing. Will not boot daemon/runner until ack.**

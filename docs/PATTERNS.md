# Engineering patterns — distilled from the v0.9.x line

**Status:** Living.
**Audience:** chepherd daemon contributors (Go) + reviewers.

Each pattern names: the rule, the failure mode it prevents, the PR(s)
where chepherd learned it, the worker memory file where the
conversational form lives, and the CLAUDE.md principle it derives from.

> **Why this file exists.** The v0.9.4 development cycle shipped ~40 PRs
> in ~36h. The review + ship cycle generated patterns subsequent work
> should not re-discover. Whenever a pattern outlives the PR it was
> learned in, it lands here. New entries go at the bottom; the
> existing entries get edited in place if a follow-up PR refines
> them.

Cross-reference key:
- **PR** — the github.com/chepherd/chepherd PR number that exhibits
  the pattern (or fixed the violation that surfaced it).
- **Memory** — the worker-side `~/.claude/projects/.../memory/<slug>.md`
  filename. These are conversational notes; this doc is the
  ship-grade canonical form.
- **Principle** — section reference in `~/.claude/CLAUDE.md`.

---

## §1 — MCP HTTP transport gotchas

When implementing an MCP server that claude-code talks to, the
Streamable HTTP spec has three knobs that fail invisibly:

1. **`http+unix://` is not a valid URL scheme for claude-code's MCP
   client.** Use a local TCP listener (`127.0.0.1:<port>`) and let
   the runner discover the bound port (e.g. via stderr log line
   parsing). The Unix socket stays for audit / monitoring; agents
   need TCP.
2. **`Mcp-Session-Id` response header MUST be set on the response
   to `initialize`.** Subsequent requests echo it back as the
   `Mcp-Session-Id` request header. Missing this → claude-code
   reconnects on every call (looks like a slow MCP, not a broken
   one).
3. **`notifications/*` methods MUST return HTTP 202 with empty
   body** (not 200 + JSON). The Streamable HTTP spec is explicit;
   chepherd's prior `200 + {}` worked against the curl harness +
   broke claude-code in production.
4. **`GET /mcp/...` with `Accept: text/event-stream` MUST upgrade
   to SSE** for server→client notification delivery. Otherwise
   server-side tool calls (e.g. `chepherd.send_to_session` from
   one agent to another) get dropped silently.

**PR**: #525. **Memory**:
`feedback_marshal_config_files_never_sprintf.md` (sibling — the
.mcp.json case). **Principle**: §4 #2 IaC-first (declarative config
over hand-formatted strings).

---

## §2 — Single-host mTLS requires IP-SANs

When chepherd terminates mTLS for self-hosted federation /
in-cluster traffic, the leaf cert MUST list:

- `127.0.0.1` and `::1` in `IPAddresses`
- `localhost` in `DNSNames`

A cert with only Common Name (CN) fails modern Go's `crypto/tls`
with the `x509: certificate is not valid for any names, but wanted
to match localhost` family of errors — Go 1.21+ refuses CN-only
matching per RFC 6125 §6.4.4.

The trap: pure-localhost dev setups have no operator-visible domain
to put in DNS-SAN, so the cert author skips SANs entirely. Then
production federation works (real domains in SAN) but the local
smoke test fails opaquely.

**PR**: #526 + #529 (federation mTLS). **Memory**:
`feedback_mtls_single_host_requires_ip_sans.md`. **Principle**: §7
Modern Tech Stack — Security defense-in-depth.

---

## §3 — Worker validates the architect's premise before treating
       the dispatch as net-new

The architect's dispatch text presumes a substrate shape that may
be stale by the time the worker reads it. Pre-existing scaffolding
might already do half the work; an "add X" dispatch might really
be "wire X to the existing Y."

**Mandatory pre-coding check** when accepting any dispatch:

1. `grep` the dep tree for the named library / API / wire format.
   If it's already imported, find the call sites.
2. `grep` the chepherd codebase for the named symbol. If it's
   defined, the dispatch is probably "wire it into the new caller"
   not "implement it."
3. If the dispatch cites a PR or ADR as the canon, `git show
   <ref>` to read the actual diff — the architect's description
   compresses; the diff is authoritative.

Skipping this check leads to re-implementing existing helpers
(#525 case: substrate from `internal/mcpserver` was 80%
shipped already) or building atop a stale assumption (#528 → #495
sequence: the deployment expects the hub binary, not the
in-process substrate).

**PR(s)**: #525, #526, #528, #547. 12+
confirmations across the v0.9.4 cycle. **Memory**:
`feedback_worker_validates_architect_premise.md`. **Principle**:
§4 #1 (NEVER SPECULATE) + §4 #6 (trace end-to-end before fixing).

---

## §4 — Find what the dep already does + add only what it can't

For spec-compliance dispatches (A2A v1.0, RFC 8594, Streamable
HTTP, OAuth 2.1), the dep tree usually carries 80% of the spec.
The chepherd value-add is what the spec doesn't say + what the dep
can't do for you:

- **Out-of-band trust pins** that pre-empt the spec's discovery
  (e.g. the chepherd-hub well-known URL pinning + ICE candidate
  whitelisting on top of pion).
- **Org policy hooks** the spec leaves to "implementation defined"
  (e.g. the federation grant-check on top of standard mTLS).
- **Side-channel trust** (e.g. JWKS rotation overlap + JWT mint
  rate-limiting on top of stdlib JWT verify).

The anti-pattern is reimplementing what the dep already provides
just because the dispatch phrasing is "add X". For each X, ask:
"what does pion / coreos / coreutils / stdlib already do for this?"
The remainder IS the dispatch's actual scope.

**PR(s)**: #487, #492, #494, #495, #528, #547. **Memory**:
`feedback_find_what_dep_already_does_then_add_what_it_cant.md`.
**Principle**: §7 Modern
Tech Stack (community-adopted; reject bespoke that duplicates
off-the-shelf).

---

## §5 — Clock injection over timing widening

When a wall-clock-tight test flakes under CI parallel load:

- **First** recurrence: widening the window once is acceptable.
- **Second** recurrence: widening is now a workaround pattern.
- **Third** recurrence: the structural fix is mandatory.

The structural fix is **NOT** "use clockwork / jonboulle / etc."
across the codebase. It's a minimal-invasion seam:

1. Production code's `time.NewTimer(d).C` select branch gains a
   parallel `case <-explicitFireCh:` branch with identical exit
   semantics.
2. Production callers pass `nil` for the trigger → channel-blocks-
   forever → behavior unchanged from pre-seam.
3. Tests construct the trigger via a test-only constructor + use
   an observer hook to capture the spawned struct, then call the
   fire method at the precise moment the test wants.
4. Verify **50× consecutive PASS** with `go test -race -count=50`
   locally before opening the PR. This is the canonical proof
   threshold.

**PR**: #550 (#549). Closed a 3-recurrence chain: #524 → #542 →
#545. **Memory**: `feedback_clock_injection_over_widening.md`.
**Principle**: §3 anti-theater #3 (defensive patterns trigger
investigation, NOT approval — repeated "widen the window" was the
red flag).

### §5.1 — Sister rule: deterministic tests reveal latent drift

When clock-injection (or any jitter-removal) makes a test start
**failing** rather than passing, do not restore jitter. The
deterministic version is exposing **fixture-vs-production drift**
the wall-clock probabilistically buried.

The drift is usually:

- Test pushes setup state BEFORE production code is in a state
  to consume it (pre-buffered channels, pre-set globals, etc.).
  Production never has this backlog.
- Test relies on `select` randomness that wall-clock pauses
  consistently rigged one way.
- Production fixture from a minimal-repro is missing bytes a real
  production stream carries.

**Fix**: align the test's fixture timing / shape with production
reality. If the answer is "production never has this state," the
test scenario was wrong; drop or restructure the assertion. If the
answer is "production hits this state but rarely," the production
code has a race the test now exposes — fix the production code.

**PR**: #543 (rebase). **Memory**:
`feedback_deterministic_tests_reveal_latent_drift.md`.
**Principle**: §3 anti-theater #3 + §4 #1 NEVER SPECULATE.

---

## §6 — `json.Marshal` over sprintf for config files

JSON / YAML / TOML config files (`.mcp.json`, Agent Card,
deployment manifests) MUST be produced with the language's
canonical marshaler. `fmt.Sprintf`-into-template silently emits
invalid bytes when an embedded value contains `"`, `\`, control
characters, or non-ASCII.

The trap: a unit test that asserts on the substring `name":"x"`
passes because the bug is invisible to a substring grep — the
illegal `\` in the value doesn't break the test, only the
production consumer parsing the file.

**PR**: #525. **Memory**:
`feedback_marshal_config_files_never_sprintf.md`. **Principle**:
§4 #6 trace requirements end-to-end (test the bytes a real
consumer sees, not just the substring presence).

---

## §7 — Stacked-PR rebase recipe

When PR B depends on PR A (e.g. B imports a symbol A introduces),
B's branch is built on top of A's branch, not main. When A
squash-merges into main:

```sh
cd worktree-for-B
git fetch origin main
git rebase --onto origin/main <A-tip-sha>
# resolve any conflicts (usually trivial — A's diff already lives on main)
git push --force-with-lease
```

The `--onto` form replays only B's commits (skips A's old commits)
onto post-A main. Plain `git rebase origin/main` from a stacked
branch tries to replay both A's old commits + B's commits and
gets confused.

When B is opened, set the GitHub PR's base to A's branch (not
main). Once A merges, GitHub auto-retargets B's base to main.

**PR(s)**: every stacked-PR series in the v0.9.4 cycle. **Memory**:
`feedback_pr_conflict_resolution.md`. **Principle**: §6 GitHub
disciplines (conventional commit history, no force-push to main).

---

## §8 — Synth-fixtures hidden in main

Pattern detectors (e.g. agent prompt-cursor detection, A2A method
dispatch, JWT claim parsing) are sometimes tested only against
hand-written synthesized strings. The detector passes; production
fails because real bytes carry chrome (ANSI escapes, BOM, CR/LF,
trailing whitespace) the synth fixtures didn't.

**Mandatory audit before adopting a detector**: capture 5+
samples of the **real** input stream the detector will face in
production. Diff against the synth fixture. If the real bytes
contain anything the fixture doesn't, update the fixture + re-run
the detector. If the detector now fails, the production code is
incomplete.

**PR(s)**: #387 P0 (R4 cursor gate), #389 (response-end detection
revert). **Memory**: `feedback_real_fixtures_not_minimal_repro.md`.
**Principle**: §3 anti-theater #2 (validate against fresh state).

---

## §9 — Substrate-vs-production split discipline

When a feature's scope is too big to land in one reviewable PR (e.g.
the federation mTLS work: #526 + #529), split via:

1. **PR-N** ships the substrate: types, helpers, unit tests,
   smoke-mode wiring. NO production deployment changes.
2. **PR-N.1** wires substrate into the production cmd/run.go
   path. Live integration test. Documentation updates.

The two-PR form lets reviewers verify the substrate in isolation
+ catches integration-only failures in a small follow-up diff.

Anti-pattern: trying to land both in PR-N. The big diff becomes
unreviewable; integration bugs land alongside scaffolding bugs +
both look like the same regression.

**PR(s)**: #526 → #529 (federation mTLS), #492 → #495 (hub bridge).
**Memory**: `feedback_credential_pipeline_chain_discipline.md`.
**Principle**: §3 anti-theater #5 (too-big-to-review-carefully =
split before merging).

---

## §10 — Update fixtures when a bug recurs across N PRs

If the SAME bug fires across 3+ PRs that "fixed" it, the test
fixtures don't match the production input stream. Stop patching;
audit fixtures.

The recipe:

1. Capture a known-bad production sample (PR comment, operator
   bug report, CI failure log).
2. Add the captured bytes verbatim as a new fixture.
3. Re-run the original test against the new fixture. If it
   passes, the test is incomplete. If it fails, the production
   code is incomplete.
4. Don't merge the next fix until the new-fixture test is green.

**PR(s)**: #387 → #389 → #391 (response-end detection chain).
**Memory**: `feedback_real_fixtures_not_minimal_repro.md` +
`feedback_architect_prescriptions_need_live_premise_check.md`.
**Principle**: §3 anti-theater anti-pattern table (`Bulk-template
closure overriding live evidence`).

---

## Authority + maintenance

- **Canon source**: this file is shipped on `main`. Patterns
  here are sea-level for the v0.9.x line.
- **Drift policy**: when a PR violates a pattern, file the
  violation in PR review + update either the PR or this file
  (depending on which is wrong). If the pattern stops applying
  (architectural shift, dep change), the entry gets struck
  through with a SUPERSEDED reference, not deleted.
- **New entries**: only after the pattern has shipped in ≥2 PRs.
  Single-PR observations live in worker memory until they
  recur.
- **Removal**: deletion requires an ADR. Patterns here have
  cost behind them.


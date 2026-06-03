# UAT — chepherd dashboard — 2026-06-03

> **User-Acceptance-Test walk of the chepherd web dashboard.** Every row below is a **real click/type in the browser**, executed against the live dashboard with a real browser (Playwright-driven); the images under [`evidence/`](evidence) are genuine captures from this walk.
>
> **Golden rule — this document is 100% the end-user's experience.** No `curl`, no API call, no `kubectl`, no log grep, no source reading. A non-technical operator must be able to follow every row verbatim and reach the same verdict. Anything that needs a terminal is *Out of scope* and belongs to the dev team's automated suite.

---

## Metadata

| Field | Value |
|---|---|
| **Product / release** | chepherd daemon — v0.9.2 released / **v0.9.4 in development** (federation mesh) |
| **Build under test** | `main` dashboard web bundle served by the daemon container |
| **Environment** | [`http://localhost:8083/v0.9.4/`](http://localhost:8083/v0.9.4/) — operator's daemon, reached over an SSH `-L 8083` tunnel. Daemon hub-connected to [`https://signal.openova.io`](https://signal.openova.io). |
| **Surface** | Responsive **web** (desktop browser) |
| **Tester** | `@p0-474-lonely` (UAT executor) — driven with Playwright |
| **Walk date** | 2026-06-03 |
| **Overall verdict** | 🟢 **PASS (after fixes)** — all four walked journeys pass on screen; TC-02 and TC-04 each failed on the first walk, were fixed ([#676](https://github.com/chepherd/chepherd/issues/676)–[#679](https://github.com/chepherd/chepherd/issues/679), [#682](https://github.com/chepherd/chepherd/issues/682)) and **re-walked** before acceptance. See roll-up. |

---

## How to read this document

**Result legend** (exactly one per step):

| Symbol | Meaning | Rule |
|---|---|---|
| ✅ | **PASS** | Saw the expected result on screen. **Requires a committed screenshot** in the Evidence cell. |
| ❌ | **FAIL** | Did the action; the screen was wrong or errored. Defect filed; issue left **open**. |
| ⛔ | **BLOCKED** | Could not attempt the step (a prior step failed). Not a pass, not a fail. |
| ⏭️ | **N/A** | Step doesn't apply to this build/surface. |
| ☐ | **NOT WALKED** | Untouched. |

Evidence is a committed screenshot under [`evidence/`](evidence), linked `[📷 …](evidence/….png)`. **No ✅ without a resolving screenshot.** The executor is read-only on the product and **never closes issues** — a fix only flips a row back to ☐; acceptance is the *next* walk.

---

## Test journeys

### TC-01 — Operator signs in and reaches the workspace

- **Persona:** Operator opening the chepherd dashboard for the first time this session.
- **Goal (user's words):** *"As an operator, I want to log in with my bootstrap token so that I reach my workspace."*
- **Surface:** Responsive web, desktop browser.
- **Preconditions:** The daemon is running and serving the dashboard; the operator has the bootstrap token chepherd printed at startup.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [`/v0.9.4/`](http://localhost:8083/v0.9.4/) | Open the page | A **"🔑 chepherd login"** dialog: *"Paste the bootstrap token chepherd printed at startup."* | ✅ | [📷 tc01-0-login-dialog](evidence/tc01-0-login-dialog.png) |
| 2 | chepherd login | Paste the token, click **Sign in** | Dialog closes; the **workspace** loads — header shows *"N agent · M teams · K memberships"* and the pane grid (sessions · terminal · team transcript · federation · multi-host · agent-details · scorecard) | ✅ | [📷 tc01-1-workspace](evidence/tc01-1-workspace.png) |

- **Journey verdict:** 🟢 **PASS** — token sign-in reaches the workspace.

---

### TC-02 — See another party's node discovered via the central mesh

- **Persona:** Operator checking that an independent peer (`openova-bastion`) shows up in their dashboard.
- **Goal (user's words):** *"As an operator, I want to see other chepherd parties that joined the mesh, on screen, without configuring anything by hand."*
- **Surface:** Responsive web.
- **Preconditions:** Signed in (TC-01); another party (`openova-bastion`) is registered on the hub; this daemon is hub-connected.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Workspace → **federation** pane | Look at the pane | The peer **`openova-bastion`** listed under **"Federation (1)"** with *"synced Ns ago"* | ✅ | [📷 tc02-2-federation-peer](evidence/tc02-2-federation-peer.png) |
| 2 | Header | Look at the top bar | A peer switcher **`openova-bastion ▾`** is present | ✅ | [📷 tc02-2-federation-peer](evidence/tc02-2-federation-peer.png) |
| 3 | **multi-host** pane | Look at the peer's row | Peer shown reachable (*"Reachable via hub mesh"*), not a red error | ✅ | [📷 tc02-2-federation-peer](evidence/tc02-2-federation-peer.png) |

- **Journey verdict:** 🟢 **PASS (after fixes)** — On the **first** walk this journey **FAILED**: the federation pane showed *"Federation (0) — configure --federation-registry-url"* ([📷 before](evidence/tc02-1-federation-empty-before.png)) because the launcher never connected the daemon to the hub ([#676](https://github.com/chepherd/chepherd/issues/676)); the multi-host row showed *"Failed to fetch"* ([#679](https://github.com/chepherd/chepherd/issues/679)); the empty-state pointed at the wrong flag ([#678](https://github.com/chepherd/chepherd/issues/678)). After those fixes the re-walk passes as recorded above.

---

### TC-03 — Send a cross-host message to a peer from the UI

- **Persona:** Operator messaging the federated peer.
- **Goal (user's words):** *"As an operator, I want to type a message to another party in the transcript and have it delivered over the mesh."*
- **Surface:** Responsive web.
- **Preconditions:** Signed in; `openova-bastion` is a discovered team member (TC-02).

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | **team transcript** pane | Read the *"members:"* line | The list includes **`@openova-bastion`** | ✅ | [📷 tc03-1-message-sent](evidence/tc03-1-message-sent.png) |
| 2 | team transcript compose box | Type `@openova-bastion <message>` and press **Enter** | The compose box clears (sent) and the message appears in the transcript addressed **operator → openova-bastion** (routed over the mesh) | ✅ | [📷 tc03-1-message-sent](evidence/tc03-1-message-sent.png) |

- **Journey verdict:** 🟢 **PASS** — a message composed in the UI routed to the federated peer.

---

### TC-04 — Spawn a new local agent from the dashboard (Built-in sandbox)

- **Persona:** Operator starting a worker agent from scratch, no external git account.
- **Goal (user's words):** *"As an operator, I want to click + new and spawn a Solo agent on the built-in sandbox that shows up in my sessions list with a live terminal."*
- **Surface:** Responsive web.
- **Preconditions:** Signed in; a saved Claude account is available (it auto-selects in the wizard).

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Workspace header | Click **+ new** | The **"+ Spawn workspace"** wizard opens on step 1 *Shape* (Solo / Pair / Trio / Scrum Team / Squad / Custom) | ✅ | [📷 tc04-1-wizard-shape](evidence/tc04-1-wizard-shape.png) |
| 2 | Step 1 *Shape* | Keep **Solo** selected, click **Next →** | Step 2 *Repo* — *"Where's the code?"* with providers (Built-in / GitHub / GitLab / Bitbucket / Gitea / On-prem) | ✅ | [📷 tc04-1-wizard-shape](evidence/tc04-1-wizard-shape.png) |
| 3 | Step 2 *Repo* | Click **Built-in (Embedded sandbox)**, type a repo name (e.g. `verify-682`), click **Next →** | Typing a valid name auto-commits — **Next** enables with no Enter needed; step 3 *Skills* — *"Who's bringing what?"* skill matrix | ✅ | re-walked after [#682](https://github.com/chepherd/chepherd/issues/682) fix — see journey verdict |
| 4 | Step 3 *Skills* | Accept defaults, click **Next →** | Step 4 *Agents* — *"Which agents + which models?"* (claude-code · claude-opus-4-7) | ✅ | [📷 tc04-2-wizard-skills](evidence/tc04-2-wizard-skills.png) |
| 5 | Step 4 *Agents* | Accept defaults, click **Next →** | Step 5 *Accounts* — a saved Claude account auto-selects; *"✓ All 1 agents have an account selected"* | ✅ | [📷 tc04-3-wizard-accounts](evidence/tc04-3-wizard-accounts.png) |
| 6 | Step 5 *Accounts* | Click **Next →** | Step 6 *Launch* — review table + pre-flight; the embedded sandbox is reported honestly as **"⏳ Embedded sandbox — provisioned on launch"** (amber, not a green it can't verify), alongside *"✓ All accounts valid · ✓ 1/1 agent slots ready"* | ✅ | [📷 tc04-4-wizard-preflight](evidence/tc04-4-wizard-preflight.png) *(pre-fix frame; post-fix copy verified on the re-walk)* |
| 7 | Step 6 *Launch* | Click **⚡ Launch 1 agents** | The wizard closes; the header agent count increments (1 → **2 agents · 1 team · 1 membership**) and the new **`generalist`** agent appears live in the **sessions** pane | ✅ | [📷 tc04-682-fixed-agent-live](evidence/tc04-682-fixed-agent-live.png) |
| 8 | Workspace → **sessions** pane | Click the new **generalist** | **agent-details** shows the spawned agent: agent `claude-code`, role `worker`, team `solo`, repo **`chepherd-admin/verify-682`** (the embedded sandbox repo, cloned) | ✅ | [📷 tc04-682-generalist-details](evidence/tc04-682-generalist-details.png) |

- **Journey verdict:** 🟢 **PASS (after fixes)** — On the **first** walk this journey **FAILED**: Launch errored **`⚠ 1 of 1 agents failed` → `provider "embedded" not registered`** ([📷 before](evidence/tc04-5-spawn-failed.png)) right after a **false-green** pre-flight (*"✓ Embedded Gitea ready"*), and the repo step's *"valid name auto-commits"* hint was untrue (commit only fired on Enter). All three were filed as **[#682](https://github.com/chepherd/chepherd/issues/682) (P1)**; the fix ([PR #683](https://github.com/chepherd/chepherd/pull/683)) also caught a latent second failure the dead-end had been hiding (the embedded Gitea sidecar's bind-mounts used the wrong path namespace). After the fixes were deployed, the **full journey was re-walked live**: name auto-commits on input, the pre-flight is honest amber, and Launch produced a real running agent with the embedded repo cloned — as recorded above.

---

## Roll-up

| TC | Surface | Journey | Steps | Walked | ✅ | ❌ | ⛔ | Verdict |
|---|---|---|---|---|---|---|---|---|
| TC-01 | web | Sign in → workspace | 2 | 2 | 2 | 0 | 0 | 🟢 PASS |
| TC-02 | web | Discover federated peer | 3 | 3 | 3 | 0 | 0 | 🟢 PASS *(after fixes)* |
| TC-03 | web | Send cross-host message | 2 | 2 | 2 | 0 | 0 | 🟢 PASS |
| TC-04 | web | Spawn local agent (built-in) | 8 | 8 | 8 | 0 | 0 | 🟢 PASS *(after fixes)* |
| | | **Total** | **15** | **15** | **15** | **0** | **0** | |

**Overall verdict:** 🟢 **PASS (after fixes).** Every walked journey passes on screen: sign-in, discovering an independent party through the central hub, messaging it cross-host, and spawning a local agent from scratch on the built-in sandbox. Two journeys initially **FAILED** and were fixed + **re-walked** before acceptance: TC-02 ([#676](https://github.com/chepherd/chepherd/issues/676)/[#678](https://github.com/chepherd/chepherd/issues/678)/[#679](https://github.com/chepherd/chepherd/issues/679)) and TC-04 ([#682](https://github.com/chepherd/chepherd/issues/682)). One caveat: spawning onto an **external** git provider (GitHub/GitLab/…) was **not walked** (needs a connected account) — its verdict is unknown, not assumed.

---

## Defects found during this walk

> All found by *looking at the dashboard* — none were visible to a prior API/curl checklist.

| Defect | Step | What the user saw | Severity | Ticket |
|---|---|---|---|---|
| Built-in-sandbox spawn fails | TC-04.7 | *"⚠ 1 of 1 agents failed — provider 'embedded' not registered"* | P1 | [#682](https://github.com/chepherd/chepherd/issues/682) ✅ fixed + re-walked |
| Launch pre-flight false green | TC-04.6 | *"✓ Embedded Gitea ready"* immediately before the spawn fails on the embedded provider | P2 | [#682](https://github.com/chepherd/chepherd/issues/682) ✅ fixed (now honest amber) |
| Repo "auto-commit" copy is wrong | TC-04.3 | *"valid name auto-commits"* but a valid name doesn't commit; **Next** stays disabled until you press **Enter** | P3 | [#682](https://github.com/chepherd/chepherd/issues/682) ✅ fixed (commits on input) |
| Federation pane empty (launcher didn't pass `--hub-url`) | TC-02.1 | *"Federation (0) — configure --federation-registry-url"* despite a live mesh | P1 | [#676](https://github.com/chepherd/chepherd/issues/676) ✅ fixed |
| Terminal retried a dead session's WebSocket → 404 loop | dashboard load | Repeating `…/attach` 404 errors every ~5s | P2 | [#677](https://github.com/chepherd/chepherd/issues/677) ✅ fixed |
| Empty-state copy pointed at the wrong flag | TC-02.1 | *"configure --federation-registry-url"* (older mechanism, not the hub mesh) | P3 | [#678](https://github.com/chepherd/chepherd/issues/678) ✅ fixed |
| Multi-host showed a red error for hub peers | TC-02.3 | *"Failed to fetch"* under `openova-bastion` | P2 | [#679](https://github.com/chepherd/chepherd/issues/679) ✅ fixed |

---

## Out of scope (handled by the dev team, NOT walked here)

Unit / integration / contract tests, CI pipelines, `curl`/`kubectl`/API/log verification, source-code reading, the daemon's mesh wire-protocol internals. Capabilities with no on-screen surface have no UAT row — the protocol-level mesh proof lives in the dev team's suite, separately.

---

_Every row above is one click a real operator could repeat. Issues are left open for the owner to close after re-walk; the UAT executor never self-closes._

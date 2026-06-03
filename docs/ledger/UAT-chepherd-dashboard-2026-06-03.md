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
| **Overall verdict** | ⚠️ **CONDITIONAL** — sign-in, federated-peer discovery, and cross-host messaging **PASS**; the local **Built-in-sandbox spawn** journey **FAILS** ([#682](https://github.com/chepherd/chepherd/issues/682), P1 go-live blocker for local spawn). See roll-up. |

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
| 3 | Step 2 *Repo* | Click **Built-in (Embedded sandbox)**, type a repo name `uat-walk-demo`, press **Enter**, click **Next →** | Repo commits and **Next** enables; step 3 *Skills* — *"Who's bringing what?"* skill matrix | ❌ | see note — auto-commit copy defect |
| 4 | Step 3 *Skills* | Accept defaults, click **Next →** | Step 4 *Agents* — *"Which agents + which models?"* (claude-code · claude-opus-4-7) | ✅ | [📷 tc04-2-wizard-skills](evidence/tc04-2-wizard-skills.png) |
| 5 | Step 4 *Agents* | Accept defaults, click **Next →** | Step 5 *Accounts* — a saved Claude account auto-selects; *"✓ All 1 agents have an account selected"* | ✅ | [📷 tc04-3-wizard-accounts](evidence/tc04-3-wizard-accounts.png) |
| 6 | Step 5 *Accounts* | Click **Next →** | Step 6 *Launch* — review table + pre-flight; expanding **▸ Pre-flight ✓ checks passed** shows **"✓ All accounts valid · ✓ Embedded Gitea ready · ✓ 1/1 agent slots ready"** | ✅ | [📷 tc04-4-wizard-preflight](evidence/tc04-4-wizard-preflight.png) |
| 7 | Step 6 *Launch* | Click **⚡ Launch 1 agents** | A new agent appears in the **sessions** pane with a live **terminal** tab | ❌ | [📷 tc04-5-spawn-failed](evidence/tc04-5-spawn-failed.png) |

- **Journey verdict:** 🔴 **FAIL** — Launch errored on screen: **`⚠ 1 of 1 agents failed` → `provider "embedded" not registered`**, despite the pre-flight (step 6) having shown a **false green** *"✓ Embedded Gitea ready"*. The built-in-sandbox spawn — the simplest, default path — never produces a running agent. Filed **[#682](https://github.com/chepherd/chepherd/issues/682) (P1)**, which also covers the false-green pre-flight (theater) and a P3 copy defect at step 3: *"valid name auto-commits"* is wrong — a valid name does **not** auto-commit; the repo only commits when you press **Enter** (no repo-create request fires otherwise, and **Next** stays disabled). Issue left **open**; the executor does not close it.

---

## Roll-up

| TC | Surface | Journey | Steps | Walked | ✅ | ❌ | ⛔ | Verdict |
|---|---|---|---|---|---|---|---|---|
| TC-01 | web | Sign in → workspace | 2 | 2 | 2 | 0 | 0 | 🟢 PASS |
| TC-02 | web | Discover federated peer | 3 | 3 | 3 | 0 | 0 | 🟢 PASS *(after fixes)* |
| TC-03 | web | Send cross-host message | 2 | 2 | 2 | 0 | 0 | 🟢 PASS |
| TC-04 | web | Spawn local agent (built-in) | 7 | 7 | 5 | 2 | 0 | 🔴 FAIL |
| | | **Total** | **14** | **14** | **12** | **2** | **0** | |

**Overall verdict:** ⚠️ **CONDITIONAL.** The federation-mesh journeys an operator goes live for — sign-in, discovering an independent party through the central hub, and messaging it cross-host — all **PASS** on screen. The local **Built-in-sandbox spawn** journey **FAILS** ([#682](https://github.com/chepherd/chepherd/issues/682), P1): the default Solo + Built-in spawn cannot create a running agent, and the Launch pre-flight gives a false green before it. That is a go-live blocker for the "spawn a local agent from scratch" capability and must be fixed + re-walked before this journey is accepted. Spawning onto an **external** git provider (GitHub/GitLab/…) was **not walked** (needs a connected account) — its verdict is unknown, not assumed.

---

## Defects found during this walk

> All found by *looking at the dashboard* — none were visible to a prior API/curl checklist.

| Defect | Step | What the user saw | Severity | Ticket |
|---|---|---|---|---|
| Built-in-sandbox spawn fails | TC-04.7 | *"⚠ 1 of 1 agents failed — provider 'embedded' not registered"* | P1 | [#682](https://github.com/chepherd/chepherd/issues/682) |
| Launch pre-flight false green | TC-04.6 | *"✓ Embedded Gitea ready"* immediately before the spawn fails on the embedded provider | P2 | [#682](https://github.com/chepherd/chepherd/issues/682) |
| Repo "auto-commit" copy is wrong | TC-04.3 | *"valid name auto-commits"* but a valid name doesn't commit; **Next** stays disabled until you press **Enter** | P3 | [#682](https://github.com/chepherd/chepherd/issues/682) |
| Federation pane empty (launcher didn't pass `--hub-url`) | TC-02.1 | *"Federation (0) — configure --federation-registry-url"* despite a live mesh | P1 | [#676](https://github.com/chepherd/chepherd/issues/676) ✅ fixed |
| Terminal retried a dead session's WebSocket → 404 loop | dashboard load | Repeating `…/attach` 404 errors every ~5s | P2 | [#677](https://github.com/chepherd/chepherd/issues/677) ✅ fixed |
| Empty-state copy pointed at the wrong flag | TC-02.1 | *"configure --federation-registry-url"* (older mechanism, not the hub mesh) | P3 | [#678](https://github.com/chepherd/chepherd/issues/678) ✅ fixed |
| Multi-host showed a red error for hub peers | TC-02.3 | *"Failed to fetch"* under `openova-bastion` | P2 | [#679](https://github.com/chepherd/chepherd/issues/679) ✅ fixed |

---

## Out of scope (handled by the dev team, NOT walked here)

Unit / integration / contract tests, CI pipelines, `curl`/`kubectl`/API/log verification, source-code reading, the daemon's mesh wire-protocol internals. Capabilities with no on-screen surface have no UAT row — the protocol-level mesh proof lives in the dev team's suite, separately.

---

_Every row above is one click a real operator could repeat. Issues are left open for the owner to close after re-walk; the UAT executor never self-closes._

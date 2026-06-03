# UAT — chepherd dashboard (federation mesh) — 2026-06-03

> **Standard User-Acceptance-Test walk.** A **filled, real** walk of [`UAT-TEMPLATE.md`](UAT-TEMPLATE.md) for the chepherd **web dashboard**. This is NOT illustrative — it was executed against the live dashboard with a real browser; the evidence images under [`evidence/`](evidence) are genuine captures from the walk. The walk surfaced 4 on-screen defects ([#676](https://github.com/chepherd/chepherd/issues/676)–[#679](https://github.com/chepherd/chepherd/issues/679)), all since fixed.
>
> **Golden rule — 100% the end-user's experience.** Every row is a real click/type in the browser. No curl, no API, no logs, no source reading (those live in *Out of scope*).

---

## Metadata

| Field | Value |
|---|---|
| **Product / release** | chepherd daemon — v0.9.2 released / **v0.9.4 in development** (federation mesh) |
| **Build under test** | `main` @ `3be76fe` (dashboard web bundle served by the daemon container) |
| **Environment** | [`http://localhost:8083/v0.9.4/`](http://localhost:8083/v0.9.4/) (operator's daemon; reached over an SSH `-L 8083` tunnel). Daemon hub-connected to [`https://signal.openova.io`](https://signal.openova.io). |
| **Surface(s)** | Responsive **web** (desktop browser) |
| **Tester** | `@p0-474-lonely` (UAT executor) — driven with Playwright |
| **Walk date** | 2026-06-03 |
| **Overall verdict** | 🟢 PASS *(walked journeys; 4 defects found during the walk, all fixed + re-verified; 1 journey not walked — see roll-up)* |

---

## How to read & fill this document

**Result legend:** ✅ PASS *(screenshot required)* · ❌ FAIL *(defect filed, issue left open)* · ⛔ BLOCKED · ⏭️ N/A · ☐ NOT WALKED.
Evidence is a committed screenshot under [`evidence/`](evidence), linked `[📷 …](evidence/….png)`. The executor is read-only on the product and never closes issues.

---

## Test journeys

### TC-01 — Operator signs in and reaches the workspace *(web)*

- **Persona:** Operator opening the chepherd dashboard for the first time this session.
- **Goal (user's words):** *"As an operator, I want to log in with my bootstrap token so that I reach my workspace."*
- **Surface:** Responsive web, desktop browser.
- **Preconditions:** The daemon is running and serving the dashboard; the operator has the bootstrap token chepherd printed at startup (`$STATE_DIR/auth.printed`).

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | [`/v0.9.4/`](http://localhost:8083/v0.9.4/) | Open the page | A **"🔑 chepherd login"** dialog: *"Paste the bootstrap token chepherd printed at startup."* | ✅ | (login dialog) |
| 2 | chepherd login | Paste the token into the field, click **Sign in** | Dialog closes; the **workspace** loads — header shows *"N agent · M teams · K memberships"* and the pane grid (sessions · terminal · team transcript · federation · multi-host · agent-details · scorecard) | ✅ | [📷 tc01-1-workspace](evidence/tc01-1-workspace.png) |

- **Journey verdict:** ☑ **PASS** — token sign-in reaches the workspace.

---

### TC-02 — See another party's node discovered via the central mesh *(web)*

- **Persona:** Operator checking that an independent peer (`openova-bastion`) shows up in their dashboard.
- **Goal (user's words):** *"As an operator, I want to see other chepherd parties that joined the mesh, on screen, without configuring anything by hand."*
- **Surface:** Responsive web.
- **Preconditions:** Signed in (TC-01); another party (`openova-bastion`) is registered on the hub; this daemon is hub-connected.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Workspace → **federation** pane | Look at the pane | The peer **`openova-bastion`** listed under **"Federation (1)"** with *"synced Ns ago"* | ✅ | [📷 tc02-2-federation-peer](evidence/tc02-2-federation-peer.png) |
| 2 | Header | Look at the top bar | A peer switcher **`openova-bastion ▾`** is present | ✅ | [📷 tc02-2-federation-peer](evidence/tc02-2-federation-peer.png) |
| 3 | **multi-host** pane | Look at the peer's row | Peer shown as reachable (*"Reachable via hub mesh"*), not a red error | ✅ | [📷 tc02-2-federation-peer](evidence/tc02-2-federation-peer.png) |

- **Journey verdict:** ☑ **PASS (after fixes)** — On the **first** walk this journey **FAILED**: the federation pane showed *"Federation (0) — configure --federation-registry-url"* ([📷 before](evidence/tc02-1-federation-empty-before.png)) because the launcher never connected the daemon to the hub ([#676](https://github.com/chepherd/chepherd/issues/676)), the multi-host row showed *"Failed to fetch"* ([#679](https://github.com/chepherd/chepherd/issues/679)), and the empty-state pointed at the wrong flag ([#678](https://github.com/chepherd/chepherd/issues/678)). After those fixes the re-walk passes as recorded above.

---

### TC-03 — Send a cross-host message to a peer from the UI *(web)*

- **Persona:** Operator messaging the federated peer.
- **Goal (user's words):** *"As an operator, I want to type a message to another party in the transcript and have it delivered over the mesh."*
- **Surface:** Responsive web.
- **Preconditions:** Signed in; `openova-bastion` is a discovered team member (TC-02).

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | **team transcript** pane | Read the *"members:"* line | The list includes **`@openova-bastion`** | ✅ | [📷 tc03-1-message-sent](evidence/tc03-1-message-sent.png) |
| 2 | team transcript compose box | Type `@openova-bastion <message>` and press **Enter** | The compose box clears (sent) and the message appears in the transcript addressed **operator → openova-bastion** (routed over the mesh) | ✅ | [📷 tc03-1-message-sent](evidence/tc03-1-message-sent.png) |

- **Journey verdict:** ☑ **PASS** — message composed in the UI routed to the federated peer.

---

### TC-04 — Spawn a new agent from the dashboard *(web)*

- **Persona:** Operator starting a worker agent.
- **Goal (user's words):** *"As an operator, I want to click + new and spawn an agent that shows up in my sessions list with a live terminal."*
- **Surface:** Responsive web.
- **Preconditions:** Signed in; Claude credentials available on the host.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Workspace header | Click **+ new** | The spawn wizard opens | ☐ | |
| 2 | Spawn wizard | Complete the wizard and launch | A new agent appears in the **sessions** pane with a live **terminal** tab | ☐ | |

- **Journey verdict:** ☐ **NOT WALKED** — requires Claude credentials on the host; not exercised this session. Left ☐ honestly rather than marked PASS.

---

## Roll-up

| TC | Surface | Journey | Steps | Walked | ✅ | ❌ | ⛔ | Verdict |
|---|---|---|---|---|---|---|---|---|
| TC-01 | web | Sign in → workspace | 2 | 2 | 2 | 0 | 0 | 🟢 PASS |
| TC-02 | web | Discover federated peer | 3 | 3 | 3 | 0 | 0 | 🟢 PASS *(after fixes)* |
| TC-03 | web | Send cross-host message | 2 | 2 | 2 | 0 | 0 | 🟢 PASS |
| TC-04 | web | Spawn an agent | 2 | 0 | 0 | 0 | 0 | ☐ NOT WALKED |
| | | **Total** | **9** | **7** | **7** | **0** | **0** | |

**Overall verdict:** 🟢 **PASS** for the walked journeys — after the 4 defects this walk surfaced were fixed and re-verified on screen. The spawn journey (TC-04) is not walked (needs Claude creds) and must not be counted as accepted.

---

## Defects found during this walk

> All four were found by *looking at the dashboard* — none were visible to the prior API/curl checklist.

| Defect | Step | What the user saw | Severity | Ticket |
|---|---|---|---|---|
| Federation pane empty — daemon never joined the mesh (launcher didn't pass `--hub-url`) | TC-02.1 | *"Federation (0) — configure --federation-registry-url"* despite a live mesh | P1 | [#676](https://github.com/chepherd/chepherd/issues/676) ✅ fixed |
| Terminal pane retried a dead session's WebSocket → 404 loop, console flooded | dashboard load | Repeating `…/attach` 404 errors every ~5s | P2 | [#677](https://github.com/chepherd/chepherd/issues/677) ✅ fixed |
| Empty-state copy pointed at the wrong flag | TC-02.1 | *"configure --federation-registry-url"* (the older mechanism, not the hub mesh) | P3 | [#678](https://github.com/chepherd/chepherd/issues/678) ✅ fixed |
| Multi-host showed a red error for hub peers | TC-02.3 | *"Failed to fetch"* under `openova-bastion` | P2 | [#679](https://github.com/chepherd/chepherd/issues/679) ✅ fixed |

---

## Out of scope (handled by the dev team, NOT walked here)

Unit/integration/contract tests, CI pipelines, `curl`/`kubectl`/API/log verification, source-code reading, the daemon's mesh wire protocol internals. Capabilities with no on-screen surface have no UAT row — the protocol-level mesh proof lives in the dev team's suite, separately.

---

_Filled from [`UAT-TEMPLATE.md`](UAT-TEMPLATE.md) v1; sibling native sample: [`UAT-SAMPLE-mobile.md`](UAT-SAMPLE-mobile.md). Every row is one click a real operator could repeat._

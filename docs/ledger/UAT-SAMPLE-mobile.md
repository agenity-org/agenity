# UAT — chepherd-rc iOS app — <not yet walked>

> **Standard User-Acceptance-Test walk** — a filled-shape sample of [`UAT-TEMPLATE.md`](UAT-TEMPLATE.md) for the **native iOS** chepherd-rc client.
>
> **⚠️ DEMONSTRATION / NOT YET WALKED.** No live build was available to walk: per [`mobile/ios/README.md`](../../mobile/ios/README.md) the app is `chepherd-rc v0.2.0-rc3` with App Store submission **pending** and no TestFlight link identified. Per the UAT standard, when there is no live target you **mark the walk NOT STARTED rather than invent one** — so every Result below is ☐ and there are **no ✅ and no evidence** (a ✅ would require a real device screenshot). The screens, labels, and gestures are real (taken from `mobile/ios/Sources/ChepherdApp/Views/*`); the rows are the exact script a tester runs once a build exists.
>
> **Golden rule — 100% the end-user's experience.** Every row is a real tap/type/scan on the phone. No terminal, no API, no code reading.

---

## Metadata

| Field | Value |
|---|---|
| **Product / release** | chepherd-rc iOS client `v0.2.0-rc3` (a *separate* version line from the chepherd daemon at v0.9.2) |
| **Build under test** | **none identified** — App Store submission pending; no TestFlight build link in the repo |
| **Environment** | Would pair to the operator's bastion daemon over WebRTC DataChannel; relay handles only the signaling handshake |
| **Surface(s)** | Native **iOS app** (SwiftUI) — target device TBD (record exact device + iOS at walk time) |
| **Tester** | — (not yet assigned) |
| **Walk date** | — |
| **Overall verdict** | ⬜ **NOT STARTED** *(no build to walk)* |

---

## How to read & fill this document

**Result legend:** ✅ PASS *(device screenshot required)* · ❌ FAIL · ⛔ BLOCKED · ⏭️ N/A · ☐ NOT WALKED (every cell here).
Mobile rules: the walk **starts at install/launch**; name screens as a user would (Swift view name in parens for engineers); OS permission dialogs, the OAuth web-auth sheet, and connection state are each their own step; a ✅ needs a device screenshot committed under `evidence/`. The executor is read-only on the app and never closes issues. **Until a real device walk happens, every row stays ☐.**

---

## Test journeys *(script — to be walked on a real build)*

### TC-10 — First launch and sign in *(iOS app)*

- **Persona:** Operator installing the chepherd-rc app on their personal iPhone to watch their sessions from anywhere.
- **Goal (user's words):** *"As an operator, I want to open the app and sign in, so that I see my agent sessions."*
- **Surface:** iOS app — device + iOS to be recorded at walk time.
- **Preconditions:** App installed (TestFlight/store — link once it exists); signed out; the operator has a chepherd account/daemon to pair with.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Home screen (springboard) | Tap the **chepherd** icon | Splash → app opens on the **Sign in** screen (`SignInView`) showing *"chepherd"* and *"Sign in to view your sessions from anywhere."* | ☐ | _(capture on walk)_ |
| 2 | Sign in (`SignInView`) | Tap **Sign in** | The system **web-auth sheet** opens (`ASWebAuthenticationSession`) — iOS prompts *"chepherd wants to sign in using …"* | ☐ | _(capture on walk)_ |
| 3 | Web-auth sheet | Complete the OAuth login | Sheet dismisses; app lands on the **Dashboard** (`DashboardView`, navigation title *"chepherd"*) | ☐ | _(capture on walk)_ |

- **Journey verdict:** ☐ NOT WALKED — no build available.

---

### TC-11 — See my sessions and the live connection state *(iOS app)*

- **Persona:** Signed-in operator checking their agents.
- **Goal (user's words):** *"As an operator, I want to see my sessions and whether the phone is actually connected to my daemon."*
- **Surface:** iOS app, signed in from TC-10.
- **Preconditions:** Signed in; at least one session exists on the paired daemon.

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | Dashboard (`DashboardView`) | Look at the top status line | A transport indicator showing kind/state (e.g. *webrtc/connected*) — confirms the peer-to-peer DataChannel is up, not just relay | ☐ | _(capture on walk)_ |
| 2 | Dashboard → **SESSIONS** list | Read a session row (`SessionRow`) | Each row shows the agent name + its scorecard **G V F E** values | ☐ | _(capture on walk)_ |
| 3 | Dashboard | Pull-to-refresh | List refreshes without error | ☐ | _(capture on walk)_ |

- **Journey verdict:** ☐ NOT WALKED.

---

### TC-12 — Open a session's detail *(iOS app)*

- **Persona:** Operator inspecting one agent.
- **Goal (user's words):** *"As an operator, I want to tap a session and see its scorecard trend and recent log."*
- **Surface:** iOS app, signed in.
- **Preconditions:** Signed in; a session visible in the list (TC-11).

| # | Screen you're on | What you do | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| 1 | SESSIONS list | Tap a session row | The **session detail** screen (`SessionDetailView`) opens | ☐ | _(capture on walk)_ |
| 2 | Session detail | Look at the **trend** + **log** sections | A scorecard trend (sparkline) and a recent **log** with timestamped lines | ☐ | _(capture on walk)_ |
| 3 | Session detail | Background the app, then reopen | App returns to the same screen and reconnects (`SequenceCounter` resume) without a crash | ☐ | _(capture on walk)_ |

- **Journey verdict:** ☐ NOT WALKED.

---

## Roll-up

| TC | Surface | Journey | Steps | Walked | ✅ | ❌ | ⛔ | Verdict |
|---|---|---|---|---|---|---|---|---|
| TC-10 | iOS app | First launch + sign in | 3 | 0 | 0 | 0 | 0 | ☐ NOT WALKED |
| TC-11 | iOS app | Sessions + connection state | 3 | 0 | 0 | 0 | 0 | ☐ NOT WALKED |
| TC-12 | iOS app | Session detail | 3 | 0 | 0 | 0 | 0 | ☐ NOT WALKED |
| | | **Total** | **9** | **0** | **0** | **0** | **0** | |

**Overall verdict:** ⬜ **NOT STARTED** — no shippable build to walk. When a TestFlight/store build exists, assign a tester + device, walk top-to-bottom, and capture a device screenshot per ✅.

---

## Defects found during this walk

_None — the app was not walked._

---

## Out of scope (handled by the dev team, NOT walked here)

The Swift unit tests under `mobile/ios/Tests/`, the WebRTC/WSTransport/signaling internals, PKCE/token-store crypto, contract tests, CI. Capabilities with no on-screen surface have no UAT row.

---

_Filled from [`UAT-TEMPLATE.md`](UAT-TEMPLATE.md) v1; sibling web sample (a real walk): [`UAT-SAMPLE-dashboard-web.md`](UAT-SAMPLE-dashboard-web.md). Screens/labels are real from `mobile/ios/Sources/ChepherdApp/Views/`; every row is one tap a person could repeat once a build ships._

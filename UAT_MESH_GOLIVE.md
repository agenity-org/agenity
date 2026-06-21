# Chepherd Federation Mesh — User Acceptance Test (UI walkthrough)

> **Status:** Canon — operator UAT script (driven entirely through the dashboard UI, not the API)
> **Audience:** An operator accepting the federation mesh through the actual product
> **How to read:** each case is *open/click/look* in the dashboard. Do the **Action**, observe the **Expected on screen**, record **Result**.

**Overall result (last walked 2026-06-03, via the live dashboard with Playwright):** ⚠️ **CONDITIONAL** — the core flows pass *once the dashboard daemon is connected to the hub*, but real UI testing surfaced launcher + widget defects (see "Defects found"). This replaces the earlier API/curl-based checklist, which never exercised the product UI and therefore missed these.

## Preconditions
| # | Requirement | Note |
|---|---|---|
| P1 | Dashboard reachable at `http://localhost:8083/v0.9.4/` (e.g. via `ssh -L 8083:127.0.0.1:8083 …`) | served by the operator's chepherd daemon |
| P2 | The daemon is **hub-connected**: started with `CHEPHERD_HUB_URL=https://signal.openova.io` + `CHEPHERD_ORG_ID=<org>` | **REQUIRED for the mesh to appear in the UI** — see Defect #676 (the launcher didn't pass these until fixed) |
| P3 | At least one other party is registered on the hub (e.g. `openova-bastion`) | so there is a peer to see |
| P4 | The operator bootstrap token (printed at daemon startup / `$STATE_DIR/auth.printed`) | pasted at login |

## Test cases (UI)

### UAT-01 — Reach the dashboard & see the login  ✅ PASS
| Step | Action (operator) | Expected on screen | Result |
|---|---|---|---|
| 1 | Open `http://localhost:8083/v0.9.4/` | A **"🔑 chepherd login"** dialog: *"Paste the bootstrap token chepherd printed at startup."* | ✅ |

### UAT-02 — Log in  ✅ PASS
| Step | Action | Expected | Result |
|---|---|---|---|
| 1 | Paste the bootstrap token into the field, click **Sign in** | Dialog closes; the **workspace loads** with a header reading *"N agent · M teams · K memberships"* and the pane grid (sessions / terminal / team transcript / federation / multi-host / agent-details / scorecard) | ✅ |

### UAT-03 — Federated peer is visible in the Federation pane  ✅ PASS *(after Defect #676 fix)*
| Step | Action | Expected | Result |
|---|---|---|---|
| 1 | Look at the **federation** pane | Heading **"Federation (1)"** with the peer **`openova-bastion`** listed and *"synced Ns ago"* (NOT the empty *"Federation (0) — configure --federation-registry-url"*) | ✅ |

### UAT-04 — Peer switcher appears in the header  ✅ PASS
| Step | Action | Expected | Result |
|---|---|---|---|
| 1 | Look at the top header bar | A peer/host switcher button **`openova-bastion ▾`** is present | ✅ |

### UAT-05 — Multi-host workspace lists the peer  ⚠️ PASS with defect
| Step | Action | Expected | Result |
|---|---|---|---|
| 1 | Open the **multi-host** pane | **"Multi-host workspace (… · 2 hosts)"**; `local` and **`openova-bastion`** (`external-a2a`) both listed | ✅ |
| 2 | Look at the peer's session list | _Expected:_ peer sessions or a "reachable via hub" state | ❌ shows **"Failed to fetch"** — Defect **#679** |

### UAT-06 — Peer is a team member  ✅ PASS
| Step | Action | Expected | Result |
|---|---|---|---|
| 1 | In the **team transcript** pane header, read "members:" | The list includes **`@openova-bastion`** | ✅ |

### UAT-07 — Send a cross-host message from the UI  ✅ PASS
| Step | Action | Expected | Result |
|---|---|---|---|
| 1 | In the team-transcript compose box type `@openova-bastion <message>` and press **Enter** | The compose box clears (sent) | ✅ |
| 2 | Confirm it routed | The message appears in the transcript addressed `operator → openova-bastion` (routed to the peer over the hub-relayed mesh) | ✅ (verified: message landed addressed to `openova-bastion`) |

### UAT-08 — Spawn an agent from the UI  ☐ NOT YET WALKED
| Step | Action | Expected | Result |
|---|---|---|---|
| 1 | Click **"+ new"** and complete the spawn wizard | A new agent appears in the **sessions** pane and a live **terminal** tab | ☐ pending (requires Claude credentials on the host; not exercised this round — do not mark PASS until walked) |

## Defects found by this UI walkthrough (none of which the prior API checklist caught)
| # | Severity | What the user hits | Ticket |
|---|---|---|---|
| Launcher | P1 | `scripts/start.sh` didn't pass `--hub-url`/`--org-id`, so the dashboard daemon never joined the mesh → Federation pane permanently "0 peers". **FIXED** (env passthrough). | [#676](https://github.com/agenity-org/agenity/issues/676) |
| Dead-session WS | P2 | Terminal pane retries a gone session's WebSocket attach → 404 loop, console floods with errors. | [#677](https://github.com/agenity-org/agenity/issues/677) |
| Stale empty-state copy | P3 | Federation/Multi-host empty state tells the user to set `--federation-registry-url` (the older flag), not `--hub-url`. | [#678](https://github.com/agenity-org/agenity/issues/678) |
| Multi-host fetch | P2 | Multi-host shows **"Failed to fetch"** for hub-only peers (tries to HTTP a `hub://` URL directly). | [#679](https://github.com/agenity-org/agenity/issues/679) |

## Honest note on methodology
The previous version of this document asserted "remote peers visible in dashboard = PASS" based on `curl`/`go test` evidence — it was never validated in the product UI, and when actually walked, the dashboard showed **zero** mesh peers until the launcher was fixed (#676). A UAT must be driven through the UI a real user touches; this version is. Evidence screenshots captured during the walk: `uat-dashboard-federation-empty.png` (before fix), `uat-dashboard-mesh-peer-visible.png` (after fix), `uat-dashboard-msg-sent.png`.

**Operator sign-off:** ______________________  **Date:** __________

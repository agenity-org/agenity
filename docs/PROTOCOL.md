# chepherd-rc Protocol — v1

**Status:** Canon. This is the wire protocol for the chepherd-rc client
line (web, iOS, Android, relay), shipping under chepherd-rc `v0.2.0-rc3`.
It is versioned independently of the chepherd daemon (Go binary, currently
v0.9.2 canon / v0.9.4 in development) — the `v1` here is the protocol
envelope version, NOT a client or daemon release number.
**Authority:** authoritative for every chepherd-rc client + the daemon's
rc bridge. Each implementation conforms to this doc; the doc does not
track the implementations.
**Governance:** changes go through an ADR and bump the protocol version
label per semver (see §9). Backwards-incompatible envelope changes are a
new major (`v2`); new optional fields are not breaking.

**Repo:** `github.com/agenity-org/agenity`
**Schema:** `https://chepherd.org/schema/v1.json` (JSON Schema, served as
static asset from the website repo once DNS lands)

---

## Why a protocol document

Three independent client implementations (web, iOS, Android) plus the daemon
will speak this wire format. Drift between any two of them would be a hard
debug. This doc is the single source of truth — every implementation
references it by file path + commit SHA.

The protocol must also support TWO transports without changing the message
shape, so business-logic code in all clients is transport-agnostic:

| Transport | Default? | Server sees data? | Use case |
|---|---|---|---|
| **WebRTC DataChannel** | yes | NO (DTLS-encrypted P2P) | privacy-conscious; most users |
| **WebSocket via relay** | opt-in fallback | yes (over TLS to relay) | symmetric NAT + no TURN + opted-in |

The signaling channel (offer/answer/ICE exchange) only exists in WebRTC mode
and is itself a small REST surface on the relay — not part of the main
protocol envelope.

---

## Layered overview

```
┌────────────────────────────────────────────────────────────────────┐
│  Layer 4 — Business logic (verdicts, scorecard, commands)          │
│  (identical across both transports)                                │
├────────────────────────────────────────────────────────────────────┤
│  Layer 3 — Message envelope (this document, §3)                    │
│  {type, ts, seq, payload}                                          │
├────────────────────────────────────────────────────────────────────┤
│  Layer 2 — Transport                                               │
│   · WebRTC DataChannel (ordered, reliable, JSON frames)            │
│   · WebSocket (text frames, TLS)                                   │
├────────────────────────────────────────────────────────────────────┤
│  Layer 1 — Auth (§6)                                               │
│   · OAuth2 PKCE flow against identity-svc                          │
│   · Bearer token in subprotocol negotiation                        │
└────────────────────────────────────────────────────────────────────┘
```

---

## §1. Connection lifecycle

### WebRTC mode (default) — trickled ICE

Two REST endpoint groups exist on the relay; modern clients use the
trickled-ICE form (`/offer` + `/candidate` + `/candidates`). The legacy
bundled-ICE form (`/initiate` + `/poll` + `/answer`) remains for older
daemons.

```
client                       relay (signaling only)              daemon
  │                                  │                              │
  │── POST /v1/signaling/offer ─────►│                              │
  │   {bastion_id, offer:{...}}      │                              │
  │                                  │── queues offer ────────────►│
  │                                  │   GET /v1/signaling/poll     │
  │                                  │◄─────────────────────────────│
  │                                  │                              │ SetRemoteDesc + CreateAnswer
  │                                  │                              │ GatheringCompletePromise
  │                                  │◄─ POST /v1/signaling/answer ─│
  │                                  │   {sdp_answer + bundled ICE} │
  │                                  │                              │
  │◄── 200 {answer, client_id} ──────│                              │
  │                                  │                              │
  │── POST /v1/signaling/candidate ─►│                              │
  │   {bastion_id, candidate}        │  enqueue per-peer            │
  │                                  │◄── GET /v1/signaling/        │
  │                                  │     candidates?bastion_id ───│
  │                                  │  drain queue                 │
  │                                  │                              │ AddICECandidate
  │                                                                  │
  │═══ DataChannel established (DTLS, peer-to-peer) ════════════════│
  │     (signaling channel CAN be closed at this point)             │
  │                                                                  │
  │── {type:"register", ...} ──────────────────────────────────────►│
  │                                                                  │
  │◄── {type:"state", ...} ──────────────────────────────────────────│
```

### WS-relay mode (opt-in)

```
client                                              relay (data + signaling)              daemon
  │── WSS upgrade ─────────────────────────────────►│                                       │
  │   Sec-WebSocket-Protocol:                       │                                       │
  │     chepherd-rc-v1.<bastion>.<token>            │                                       │
  │   (browsers can't send Authorization;           │                                       │
  │    daemons + Go clients may use Bearer)         │                                       │
  │◄── 101 Switching Protocols ─────────────────────│                                       │
  │   relay echoes back the EXACT subprotocol       │                                       │
  │                                                  │── WSS /v1/signaling/ws ─────────────►│
  │                                                  │   ?role=daemon&bastion_id=<id>       │
  │                                                  │   Authorization: Bearer <daemon-tok> │
  │                                                  │◄── 101 ──────────────────────────────│
  │                                                  │                                       │
  │                                                  │── {type:"register"} ──────────────────│
  │                                                  │                                       │
  │◄── {type:"state", ...} ──── (broadcast to ALL clients in room) ──── {type:"state", ...} ◄│
  │                                                  │                                       │
  │── {type:"command", action:"pause"} ─────────────►│── {type:"command", action:"pause"} ──►│
  │                                                  │                                       │
  │◄── {type:"ack", ok:true} ───────────────────────◄── {type:"ack", ok:true} ───────────────│
```

The relay's `/v1/signaling/ws` hub is a per-bastion room: at most one
daemon, many clients (web + iOS + Android can all watch the same
daemon simultaneously). Client frames fan into the daemon; daemon
frames broadcast to every client.

**Auth on the WS path**:
- **Browsers** (no Authorization header support): embed JWT in the
  subprotocol — `chepherd-rc-v1.<bastion_id>.<jwt>`. The relay parses
  the subprotocol on accept; relay negotiates back the EXACT string so
  the browser completes the upgrade.
- **Daemons + Go clients**: standard `Authorization: Bearer <jwt>` +
  query string `?role=...&bastion_id=...`. The relay accepts both
  forms on `/v1/signaling/ws`.

In WS-relay mode, the relay is a stateful multiplexer; in WebRTC mode it
only matchmakes and then steps out.

---

## §2. Subprotocol negotiation

WebSocket clients MUST request the subprotocol via the
`Sec-WebSocket-Protocol` header:

| Subprotocol | Meaning |
|---|---|
| `chepherd.v1.ws` | WebSocket relay mode, JSON envelopes |
| `chepherd.v1.signaling` | WebSocket signaling for WebRTC (legacy — REST endpoints preferred) |

Servers MUST echo the highest-version subprotocol they support.

WebRTC DataChannel: opened with `label: "chepherd.v1.p2p"`.

---

## §3. Message envelope

Every message — both directions, both transports — is one JSON object per
frame:

```typescript
{
  "type":    "register" | "state" | "log" | "verdict" | "command" | "ack" | "ping" | "pong" | "error",
  "ts":      "2026-05-23T21:30:14.123Z",    // RFC3339Nano UTC; sender clock
  "seq":     12345,                          // monotonic per-direction, per-connection
  "payload": { ... type-specific ... }
}
```

Constraints:

- `ts` is the SENDER's UTC clock at frame creation. Receivers do not require
  clock sync but MAY warn if drift > 60 seconds.
- `seq` resets on each new connection. Used for reconnection resume (§5).
- Frames MUST be ≤ 256 KiB. Larger payloads (e.g. mass log dump) MUST chunk.
- Unknown `type` MUST be ignored by receivers (forward-compatibility) — they
  SHOULD log a warning at debug level.

---

## §4. Message types

### `register` — daemon → server/peer

Sent ONCE at connection establishment by the daemon.

```json
{
  "type": "register",
  "ts": "...",
  "seq": 1,
  "payload": {
    "bastion_id": "emrah-bastion-01",     // operator-chosen, stable
    "user_id": "alice@example.com",        // from auth token claim
    "chepherd_version": "0.2.0",
    "capabilities": ["pause", "unpause", "inject", "refresh", "tmux_attach"],
    "session_count": 7,
    "hostname": "bastion-fra1"            // best-effort, may be empty
  }
}
```

### `state` — daemon → peer

Snapshot of all watched sessions. Sent on connection + on any session
state change (judge tick lands, sentinel file written, etc.).

Throttled: ≥ 1 per minute, ≤ 1 per 200ms.

```json
{
  "type": "state",
  "ts": "...",
  "seq": 42,
  "payload": {
    "sessions": [
      {
        "uuid": "5c468708-346e-4400-...",
        "tmux_name": "openova-27",
        "repo": "openova",
        "trust_band": "concerned",
        "last_verdict": "intervene",
        "last_scorecard": {"G": 3, "V": 1, "F": 1, "E": 0},
        "next_tick_at": "2026-05-23T21:32:00Z",
        "live_signals": {
          "refreshed_at": "...",
          "in_progress_count": 31,
          "unclaimed_backlog_count": 26,
          "backlog_count": 78,
          "commits_last_hour_count": 9,
          "git_last_commit_age_min": 2.1,
          "tracker_mtime_age_min": 4.3
        },
        "intervention_count": 35,
        "paused": false
      },
      ...
    ]
  }
}
```

### `log` — daemon → peer

One log line. Backpressure-aware (§7).

```json
{
  "type": "log",
  "ts": "2026-05-23T21:30:14.890Z",       // log line's own timestamp
  "seq": 1024,
  "payload": {
    "session": "openova-27",
    "level": "verdict" | "info" | "warn" | "error",
    "text": "openova-27: verdict=intervene ref=P9,P14 G/V/F/E=3/1/1/0 reason=..."
  }
}
```

### `verdict` — daemon → peer

Emitted after each judge tick, in addition to (and BEFORE) the
corresponding `state` update. Useful for "alert on new intervene" UX
without diffing two `state` snapshots.

```json
{
  "type": "verdict",
  "ts": "...",
  "seq": 43,
  "payload": {
    "session_uuid": "5c468708-...",
    "session": "openova-27",
    "verdict": "intervene",
    "principle_ref": "P9, P14",
    "scorecard": {"G": 3, "V": 1, "F": 1, "E": 0},
    "scorecard_note": "Third consecutive theater...",
    "message": "[SUPERVISOR — P9, P14 | G/V/F/E=...] ...",
    "cost_usd": 0.1127,
    "injected": true                      // did the daemon paste-buffer it?
  }
}
```

### `command` — peer → daemon

Operator-initiated action.

```json
{
  "type": "command",
  "ts": "...",
  "seq": 1,
  "payload": {
    "session_uuid": "5c468708-...",
    "action": "pause" | "unpause" | "refresh" | "inject" | "tmux_attach_hint",
    "args": {                                // action-specific
      "message": "..."                       // only for "inject"
    }
  }
}
```

`tmux_attach_hint` is informational — the peer is signaling "I'm about to
ask the user to attach to this session"; the daemon may log it but takes
no other action.

### `ack` — daemon → peer

Response to a `command`. Always sent within 2 seconds; if longer, daemon
SHOULD send a `pending` ack first.

```json
{
  "type": "ack",
  "ts": "...",
  "seq": 44,
  "payload": {
    "in_reply_to": 1,                      // peer's command seq
    "ok": true,
    "result": "session paused",            // human-readable
    "error": null                          // populated when ok=false
  }
}
```

### `ping` / `pong` — bidirectional

Every 30 seconds either side. Used to detect dead connections.

```json
{ "type": "ping", "ts": "...", "seq": N, "payload": {} }
{ "type": "pong", "ts": "...", "seq": M, "payload": {"in_reply_to": N} }
```

3 missed pongs → connection considered dead, reconnect.

### `error` — bidirectional

Out-of-band error. Receiver should log; doesn't break the connection
unless `code` is in {AUTH_REVOKED, PROTOCOL_VIOLATION, VERSION_MISMATCH}.

```json
{
  "type": "error",
  "ts": "...",
  "seq": 45,
  "payload": {
    "code": "AUTH_REVOKED" | "RATE_LIMIT" | "PROTOCOL_VIOLATION" | ...,
    "in_reply_to": N,                      // optional; the seq that triggered this
    "message": "Token expired; re-authenticate."
  }
}
```

---

## §5. Reconnection + resume

Connections WILL drop (mobile networks, laptop sleep, tunnel restarts).
Reconnect logic is identical for both transports.

1. Client/daemon notices ping timeout OR DataChannel close OR WSS close.
2. Backoff: 1s, 2s, 5s, 10s, 30s (capped). Reset after a successful 60s.
3. On reconnect, daemon includes `last_seen_seq` in `register`:
   ```json
   {"type":"register","payload":{...,"last_seen_seq":1024}}
   ```
4. Server (or peer) MAY replay events with `seq > last_seen_seq` from a
   ring-buffer (default 5000 events ≈ 5 minutes of activity).
5. If the ring-buffer doesn't cover the gap, server sends an `error` with
   `code: "RESUME_GAP"` and the client refetches a full `state` snapshot.

ICE restart (WebRTC) is preferred over full DataChannel teardown when the
network changes mid-session.

---

## §6. Authentication

OAuth2 PKCE flow against `https://rc.openova.io/v1/auth/*` (proxied to
identity-svc / Keycloak). The auth flow is BROKER-style:

1. Client requests authorization → identity-svc → user signs in
2. Authorization code returned to client → exchanged for tokens
3. Bearer token used on:
   - WSS connection handshake (`Authorization: Bearer …`)
   - Signaling endpoint REST calls (`Authorization: Bearer …`)
4. Token contains claims:
   - `sub` (user identity)
   - `chepherd:user_id` (mapped to bastion ownership)
   - `chepherd:permitted_bastions` (list of bastion IDs the user can drive)
   - `exp` (1 hour default)
   - `iss` (`https://identity.openova.io`)

Refresh tokens (30-day default) handle silent re-auth. On `AUTH_REVOKED`,
client MUST re-prompt the user.

Daemon-side auth: separate from client auth. Each bastion has a long-lived
registration token (generated on `chepherd rc enable`, stored encrypted on
disk). The relay maps tokens → bastion_ids → user_ids.

---

## §7. Backpressure + rate limiting

The log stream is the only high-volume channel. Daemons MUST:

- Apply local rate-limit: max 100 log lines/sec emitted.
- If the channel's send-buffer is > 1MB outstanding, drop oldest log lines
  with type=info first, then warn (never drop error/verdict).
- Emit a `warning` log line `[chepherd] dropped N log lines (backpressure)`
  when this fires, so the operator notices.

Clients MUST:

- Apply local filtering BEFORE pushing to the UI (so the wire stays cheap).
- Use a circular buffer of ≤ 10000 log lines in memory.

Relay MUST:

- Enforce per-client rate limit: max 10 commands/sec.
- Enforce per-bastion fan-out limit: at most 16 concurrent clients per
  bastion (excess get `error: RATE_LIMIT`).

---

## §8. Privacy guarantees

These are CONTRACTUAL and verified by audit:

1. In WebRTC mode, the relay sees ONLY:
   - identity (bearer token → user_id)
   - signaling SDP/ICE (encrypted-channel keys exchange; relay can't
     decrypt the resulting DataChannel)
   - push-notification metadata (event type, session name — NOT contents)
2. In WebRTC mode, the relay does NOT see:
   - any `state`, `log`, `verdict`, `command`, `ack` payloads
3. In WS-relay mode, the relay sees ALL frames in plaintext (over TLS to
   the relay). This is OPT-IN and disclosed at `chepherd rc enable`.
4. Audit: every quarter, a 3rd-party penetration test confirms the relay
   has no method (operational or technical) to read DataChannel contents.

---

## §9. Versioning

This is `v1`. Future versions bump the subprotocol label:
- WebRTC label: `chepherd.v1.p2p` → `chepherd.v2.p2p`
- WebSocket subprotocol: `chepherd.v1.ws` → `chepherd.v2.ws`

The relay supports the union of all live versions; clients can interoperate
with daemons one major version behind (graceful upgrade).

Backwards-incompatible changes (envelope shape, mandatory fields) get a
new major. New optional fields are NOT a breaking change.

---

## §10. Reference implementations

- Daemon (Go): `internal/daemon/rc/` + `internal/daemon/rc/webrtc/` + `internal/daemon/rc/ws/`
- Web client (TypeScript): https://github.com/agenity-org/agenity-rc-web/tree/main/src/protocol
- iOS client (Swift): https://github.com/agenity-org/agenity-rc-ios/tree/main/Sources/Protocol
- Android client (Kotlin): https://github.com/agenity-org/agenity-rc-android/tree/main/protocol/src

Each implementation references this doc's commit SHA in its package header.

---

## §11. Test conformance

A relay/daemon/client claims conformance to chepherd v1 if it passes the
upstream conformance suite:

```bash
go test ./internal/daemon/rc/conformance -count=1
```

Conformance covers:
- envelope round-trip (encode → decode → equal)
- subprotocol negotiation
- reconnection + resume (seq replay)
- backpressure (drop oldest info before error)
- auth handshake (PKCE, token refresh)
- ping/pong cadence
- WebRTC offer/answer/ICE exchange

The conformance suite is the gating CI test for chepherd-rc releases.

# chepherd-relay

Status: Living
Authority: subproject README (relay implementation). The wire contract is canon in [docs/PROTOCOL.md](../docs/PROTOCOL.md) — protocol changes go via ADR + semver; this implementation conforms to it, not vice-versa.
Audience: operators self-hosting the chepherd-rc relay.

Signaling + auth + push proxy for the chepherd-rc remote-control client line (web, iOS, Android; currently v0.2.0-rc3, versioned independently of the chepherd daemon).

Hosted in the OpenOva Sovereign instance (`openova-private`) as a Helm-deployed Blueprint `bp-chepherd-relay`. Every OpenOva Sovereign that installs the Blueprint becomes its own chepherd-rc relay endpoint.

## Privacy contract

This service **never sees the data plane** of any chepherd session.

| The relay sees | The relay never sees |
|---|---|
| OAuth bearer tokens (identity) | session state snapshots |
| WebRTC SDP offers + answers | log lines |
| ICE candidates (NAT discovery) | verdicts + reasoning |
| Push notification metadata | command payloads |
| Bastion registration ledger | DataChannel contents |

When two peers establish a WebRTC DataChannel via this relay's signaling endpoints, the resulting channel is DTLS-encrypted end-to-end. The relay cannot decrypt it — the DTLS keys are derived inside each peer's stack from the SDP fingerprint exchange.

Audited quarterly by a third-party pentest (see [audits/](audits/)).

## Endpoints

| Endpoint | Method | Purpose |
|---|---|---|
| `/v1/auth/*` | various | OAuth2 PKCE proxy to identity-svc (Keycloak) |
| `/v1/signaling/initiate` | POST | Client sends offer + ICE; gets bastion's answer |
| `/v1/signaling/poll` | GET | Bastion long-polls for incoming offers |
| `/v1/signaling/answer` | POST | Bastion sends SDP answer back to client |
| `/v1/ws` | WS | Fallback WebSocket relay (opt-in; relay DOES see data in this mode) |
| `/v1/push/register` | POST | Mobile device registers APNs/FCM token |
| `/v1/push/notify` | internal | Daemon emits a `notify` event → relay forwards via APNs/FCM |
| `/v1/health` | GET | Liveness probe |
| `/v1/stats` | GET | Per-tenant operational metrics |

## Protocol

This relay implements the server side of the chepherd-rc protocol; the canonical spec is [docs/PROTOCOL.md](../docs/PROTOCOL.md). That doc is authoritative — protocol changes land there via ADR + semver, and this implementation conforms to it.

## Deploy

The canonical deployment is via the `bp-chepherd-relay` Blueprint in `openova-io/openova/products/chepherd-relay/chart/`. To self-host:

```bash
git clone https://github.com/agenity-org/agenity-relay
cd chepherd-relay
helm install chepherd-relay ./chart \
  --namespace chepherd-relay --create-namespace \
  --set ingress.host=rc.your-domain.example.com \
  --set identitySvc.url=https://identity.your-domain.example.com
```

Required secrets:
- `chepherd-relay-postgres` — connection URI (CNPG cluster recommended)
- `chepherd-relay-apns` — APNs auth key (.p8 + key ID + team ID)
- `chepherd-relay-fcm` — FCM service account JSON

## Build

```bash
go build -o chepherd-relay ./cmd/chepherd-relay
./chepherd-relay --config /etc/chepherd-relay/config.toml
```

## License

MIT.

## Related

- chepherd (main repo + daemon + TUI): https://github.com/agenity-org/agenity
- chepherd-rc-web (browser client): https://github.com/agenity-org/agenity-rc-web (TBD)
- chepherd-rc-ios: https://github.com/agenity-org/agenity-rc-ios (TBD)
- chepherd-rc-android: https://github.com/agenity-org/agenity-rc-android (TBD)

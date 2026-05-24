# chepherd.io edge infrastructure

Hosted services that local chepherd installs reach for mobile-device pairing. **Powered by OpenOva** — runs as workloads in a public OpenOva Sovereign at `chepherd.io`.

## What runs

| Service | Purpose | Cost profile |
|---|---|---|
| **coturn** (STUN+TURN) | NAT traversal so a phone's WebRTC client can find a laptop's chepherd | STUN: trivial (anonymous reflect, <1KB/pairing). TURN: real bandwidth — 15% of mobile sessions fall back to relayed |
| **discovery** | Account-based device pairing: phone → "find my Mac" via short code | Tiny: handful of bytes per device per day |
| **(optional) analytics** | Anonymous install pulse, opt-in | Tiny |

All three deploy as a single bp-chepherd-edge Blueprint into a public OpenOva Sovereign.

## DNS

| Hostname | Purpose |
|---|---|
| `chepherd.io` | Marketing landing page (static, Astro) |
| `app.chepherd.io` | Hosted web client for travelers |
| `download.chepherd.io` | CDN-fronted installer downloads |
| `stun.chepherd.io` | STUN/TURN endpoint (port 3478 UDP + 5349 TLS) |
| `discovery.chepherd.io` | Discovery service API (HTTPS) |

Sovereign DNS pattern: `stun.chepherd.io → CNAME stun.<sov-fqdn>` via PowerDNS lua-records GSLB (per openova-io/openova#2316 bonus item 1). Single-region for v0.7; geographic spread later as TURN traffic warrants.

## Install

```bash
helm install bp-chepherd-edge ./edge/chart \
  --namespace chepherd-edge \
  --create-namespace
```

## Sub-charts

- `chart/templates/coturn-*.yaml` — coturn deployment + Service for STUN+TURN
- `chart/templates/discovery-*.yaml` — small Go service for device pairing
- `chart/configs/turnserver.conf` — coturn config (chepherd-tuned)

## Branding

Every chepherd-mobile pairing UI shows "powered by OpenOva" with the OpenOva mark next to chepherd's. Curious clicks → openova-io/openova catalog page.

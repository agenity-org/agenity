# bp-chepherd — chepherd as an OpenOva Blueprint

Status: Living
Authority: subproject README (deployment packaging). The chepherd daemon contract is canon in [docs/V0.9.2-ARCHITECTURE.md](../docs/V0.9.2-ARCHITECTURE.md).
Audience: operators deploying the chepherd daemon into an OpenOva Sovereign.

This directory packages the chepherd daemon (Go runtime; latest released tag v0.9.2, v0.9.4 in development) as a Helm chart installable into any OpenOva Sovereign.

## What you get

- **chepherd runtime** (pty-host + MCP server + @target relay) running as a StatefulSet
- **Adam + Chepherd** spawned as the default 2-agent team (or just Adam with `--set runtime.monitored=false`)
- **3 PVCs per session** (`/repo`, `/.claude-memory`, `/.cache`) — preserves the openova Sandbox "close laptop, open phone later" semantics
- **openova-MCP sidecar pattern** — bundles the openova-sandbox-mcp binary inside the chepherd image so the MCP transport works (no separate Pod)
- **Cilium Gateway HTTPRoute** at `/apps/bp-chepherd/dashboard`
- **Auth handshake** via catalyst-api session cookie → `whoami` header injection
- **Console sidebar entry** via Blueprint CR `consoleUI.sidebarEntry`

## Install

```bash
helm repo add chepherd https://chepherd.io/charts
helm install bp-chepherd chepherd/bp-chepherd \
  --namespace bp-chepherd \
  --create-namespace
```

Or directly from this directory while developing:

```bash
helm install bp-chepherd ./blueprint/chart \
  --namespace bp-chepherd \
  --create-namespace
```

## Configuration

See `chart/values.yaml`. Common overrides:

```bash
# Solo mode (no Chepherd shepherd — just Adam)
helm install ... --set runtime.monitored=false

# Use qwen-code as the default agent
helm install ... --set runtime.defaultAgent=qwen-code

# Bigger ring buffer per session
helm install ... --set runtime.ringBufferBytes=4194304

# Smaller PVCs for dev clusters
helm install ... --set persistence.repo.size=2Gi --set persistence.cache.size=1Gi
```

## Contract with OpenOva platform

Per the joint EPIC openova-io/openova#2316:

- bp-chepherd preserves the StatefulSet + 3-PVC + Service + HTTPRoute shape that openova's original Sandbox chart used
- chepherd MCP namespace is `chepherd.*`; openova-MCP keeps `gitea.*`, `sandbox.db.*`, `k8s.*`, `marketplace.*`, `sandbox.deploy.*`, `sandbox.stripe.*`
- Auth chain: catalyst-api cookie → Cilium Gateway `whoami` → chepherd WS reads `X-Catalyst-User` header
- JWT signing key sourced from `newapi-bp-newapi-token-signing-key` Secret via emberstack/reflector
- openova-MCP binary bundled at `/usr/local/bin/openova-sandbox-mcp` inside the chepherd image (sidecar bundle, no separate Deployment)

## Source attribution

The pty-host portion of chepherd is lifted from `openova-io/openova` at tag `pty-server-handoff-1.0` (commit `c65dbdca`). See `internal/ptyhost/LICENSE-NOTICE` in the chepherd repo.

## Version history

The daemon and its remote-control clients version independently:

- **chepherd daemon** (this chart): latest released tag v0.9.2; v0.9.4 in active development (QA complete, not yet tagged). Provides the runtime, MCP HTTP server, @target relay, multi-agent control room, and A2A federation mesh (STUN P2P + TURN relay via signal.openova.io). The original v0.5.0 lift from the OpenOva pty-host substrate is recorded in the project [CHANGELOG.md](../CHANGELOG.md).
- **chepherd-rc clients** (web, iOS, Android, relay): a separate component line versioned independently, currently at v0.2.0-rc3 (pre-release).

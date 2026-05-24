# bp-chepherd — chepherd as an OpenOva Blueprint

This directory packages chepherd v0.5+ as a Helm chart installable into any OpenOva Sovereign.

## What you get

- **chepherd runtime** (pty-host + MCP server + @target relay) running as a StatefulSet
- **Adam + Chepherd** spawned as the default 2-agent team (or just Adam with `--set runtime.monitored=false`)
- **3 PVCs per session** (`/repo`, `/.claude-memory`, `/.cache`) — preserves the openova Sandbox "close laptop, open phone later" semantics
- **openova-MCP sidecar pattern** — bundles the openova-sandbox-mcp binary inside the chepherd image so MCP-via-stdio works (no separate Pod, per openova Wave 0.3.4 EOF lesson)
- **Cilium Gateway HTTPRoute** at `/apps/bp-chepherd/dashboard`
- **Auth handshake** via catalyst-api session cookie → `whoami` header injection
- **Console sidebar entry** via Blueprint CR `consoleUI.sidebarEntry` (gated on openova Wave 5.69)

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

## Status

- v0.5.0: lift + runtime + MCP server + @target relay + Adam/Chepherd bootstrap + minimal TUI
- v0.6.0 (next): web client, provider abstraction, first-run wizard, OS keychain
- v0.7.0: bp-chepherd Blueprint catalog submission, iOS + Android clients, native installers, chepherd.io edge infra

#!/usr/bin/env bash
# Start chepherd in a rootless podman container.
#
# Architecture (v0.8/v0.9): ONE chepherd container + N sibling agent
# containers, all on the SAME host podman. Each agent appears in the
# operator's `podman ps` like any other container.
#
# Chepherd reaches the host podman via the bind-mounted user socket:
#   /run/user/${UID}/podman/podman.sock → /run/host-podman/podman.sock
#   CONTAINER_HOST=unix:///run/host-podman/podman.sock
#
# No nested-podman, no skopeo sync, no separate agent-storage bind.

set -euo pipefail

IMAGE="${CHEPHERD_IMAGE:-chepherd:latest}"
AGENT_IMAGE="${CHEPHERD_AGENT_IMAGE:-chepherd-agent:latest}"
PORT="${CHEPHERD_PORT:-8083}"

# Agent-image dependency check — the chepherd daemon spawns sibling
# containers from ${AGENT_IMAGE}. Without it every spawn dies with
# the misleading "fork/exec /usr/bin/claude: no such file or
# directory" surfacing in the dashboard. Auto-rebuild if missing so
# `podman image prune` (disk cleanup) doesn't silently break spawn.
# Set CHEPHERD_SKIP_AGENT_IMAGE_CHECK=1 to bypass.
if [ -z "${CHEPHERD_SKIP_AGENT_IMAGE_CHECK:-}" ]; then
  if ! podman image exists "${AGENT_IMAGE}" 2>/dev/null; then
    echo "→ ${AGENT_IMAGE} missing — building (was wiped by image prune, or never built)..." >&2
    if [ -f "$(dirname "$0")/../Dockerfile.agent" ]; then
      ( cd "$(dirname "$0")/.." && podman build -f Dockerfile.agent -t "${AGENT_IMAGE}" . ) >&2 || {
        echo "FATAL: ${AGENT_IMAGE} build failed; spawns will fail with fork/exec errors. See above." >&2
        exit 1
      }
      echo "→ ${AGENT_IMAGE} built." >&2
    else
      echo "FATAL: Dockerfile.agent not found and ${AGENT_IMAGE} missing — cannot proceed." >&2
      exit 1
    fi
  fi
fi
# State dir is version-agnostic — contains operator data (vault, git
# providers, canon, agent home dirs, embedded gitea, etc.) that
# persists across chepherd releases.
STATE_DIR="${HOME}/.local/state/chepherd"
REPOS_DIR="${HOME}/repos"
CLAUDE_DIR="${HOME}/.claude"

# Host Claude account auto-detect (~/.claude/.credentials.json +
# ~/.claude.json) is normally bind-mounted into chepherd so agents
# can spawn claude-code immediately using the operator's existing
# account. Set CHEPHERD_NO_HOST_CLAUDE=1 to disable — chepherd will
# then start with NO Claude credentials, forcing the operator to
# paste a token via the dashboard's vault before any spawn works.
# Use this for "fresh-from-scratch" testing or when running chepherd
# on a host that should not auto-leak its operator's PAT.
NO_HOST_CLAUDE="${CHEPHERD_NO_HOST_CLAUDE:-}"
CLAUDE_MOUNTS=()
if [ -z "${NO_HOST_CLAUDE}" ]; then
  if [ -d "${CLAUDE_DIR}" ]; then
    CLAUDE_MOUNTS+=(-v "${CLAUDE_DIR}:/home/chepherd/.claude:ro")
  fi
  if [ -f "${HOME}/.claude.json" ]; then
    CLAUDE_MOUNTS+=(-v "${HOME}/.claude.json:/home/chepherd/.claude.json:ro")
  fi
fi

# Path to the operator's rootless podman socket. systemd-managed:
# `systemctl --user enable --now podman.socket` if missing.
HOST_PODMAN_SOCK="/run/user/$(id -u)/podman/podman.sock"
if [ ! -S "${HOST_PODMAN_SOCK}" ]; then
  echo "→ Enabling rootless podman socket (systemctl --user enable --now podman.socket)" >&2
  systemctl --user enable --now podman.socket >/dev/null 2>&1 || {
    echo "WARN: rootless podman socket not active at ${HOST_PODMAN_SOCK}" >&2
    echo "      Run: systemctl --user enable --now podman.socket" >&2
  }
fi

mkdir -p "${STATE_DIR}"

# Stop any existing instance
podman rm -f chepherd 2>/dev/null || true

# #398 P0 v2 — chepherd-net podman network. Option A (bind 0.0.0.0:9090)
# fixed the host side of the port-mapping but slirp4netns isolates
# rootless containers from host loopback at the kernel level; agents
# still got "Network is unreachable" connecting to 10.0.2.2:9090.
# Option B per architect: create a shared user-defined podman network
# both chepherd container and every agent container attach to. Agents
# reach the MCP server via container-name DNS (ws://chepherd:9090/mcp/ws)
# without any host-loopback gymnastics. Sets up cleanly for v0.9.4
# Option C (full sibling-pod architecture).
#
# #403 P0 — chepherd-net depends on a working podman network backend.
# Podman 4.x+ ships netavark by default and needs no extra packages.
# Podman 3.x defaults to CNI which requires /opt/cni/bin/* plugins
# from the containernetworking-plugins package. On Ubuntu 22.04
# default install the CNI plugins are missing → chepherd container
# fails to start with "failed to mount netns directory for rootless
# cni: no such file or directory" (architect's #403 repro). Detect
# the backend up-front + fall back to slirp4netns + propagate the
# choice to the runtime so agents get the matching network mode.
NETWORK_BACKEND="$(podman info --format '{{.Host.NetworkBackend}}' 2>/dev/null || echo unknown)"
# Podman 3.x older builds (e.g. Ubuntu 22.04's 3.4.4) don't expose the
# Host.NetworkBackend field — the template eval errors + empty string is
# returned. Treat empty as "cni" since pre-netavark Podman defaults to
# CNI; the cni branch then probes plugin paths and either succeeds or
# falls through to slirp4netns. Without this, Ubuntu 22.04 hosts with
# CNI plugins installed at /usr/lib/cni would never reach the
# cni-detection branch + would always slirp4netns-fallback (the #414
# operator pain).
if [ -z "${NETWORK_BACKEND}" ] || [ "${NETWORK_BACKEND}" = "unknown" ]; then
  NETWORK_BACKEND="cni"
fi
USE_CHEPHERD_NET=0
case "${NETWORK_BACKEND}" in
  netavark)
    USE_CHEPHERD_NET=1
    ;;
  cni)
    # CNI requires bridge + firewall plugins. Probe canonical locations
    # before trusting CNI; missing plugins are the most common cause
    # of "failed to mount netns directory" (#403 P0).
    #
    # Locations checked:
    #   /opt/cni/bin/            — upstream containernetworking-plugins default
    #   /usr/lib/cni/            — Debian/Ubuntu apt package location
    #   /usr/libexec/cni/        — Fedora/RHEL location
    # First location with BOTH bridge + firewall wins. #406 originally
    # only checked /opt/cni/bin — false-negative on stock Debian/Ubuntu
    # hosts where the apt package installs to /usr/lib/cni/, forcing
    # unnecessary slirp4netns fallback + MCP-transport caveat that
    # surfaces as operator-visible "-32000" on cross-container MCP
    # calls (#414).
    for cni_dir in /opt/cni/bin /usr/lib/cni /usr/libexec/cni; do
      if [ -x "$cni_dir/bridge" ] && [ -x "$cni_dir/firewall" ]; then
        USE_CHEPHERD_NET=1
        echo "→ CNI plugins detected at $cni_dir — using chepherd-net" >&2
        break
      fi
    done
    ;;
esac
# #414 architectural fix — agent containers now SHARE chepherd's network
# namespace via --network=container:chepherd. Agent reaches MCP at
# 127.0.0.1:9090 via loopback in the same netns. No CNI, no netavark,
# no slirp4netns gateway hops, no port forwards. Works on every podman
# version with shared-netns support (essentially all of them).
#
# The chepherd container itself still uses chepherd-net (when CNI/netavark
# work) OR slirp4netns (fallback) — both publish 9090 to host so operator
# can curl from host. Agents bypass this entirely via shared netns.
#
# AGENT_NETWORK_ENV propagates the runtime's matching defaults: when an
# operator wants to OVERRIDE the shared-netns architecture (e.g., agents
# on a separate K8s node, multi-host federation), they set
# CHEPHERD_CONTAINER_NETWORK explicitly + the runtime honors it.
AGENT_NETWORK_ENV=(-e "CHEPHERD_CONTAINER_NETWORK=container:chepherd" -e "CHEPHERD_MCP_URL=ws://127.0.0.1:9090/mcp/ws")
if [ "${USE_CHEPHERD_NET}" -eq 1 ]; then
  podman network create chepherd-net 2>/dev/null || true
  NETWORK_ARGS=(--network chepherd-net)
  HOST_PORT_BIND="127.0.0.1"
  echo "→ chepherd container: chepherd-net (backend=${NETWORK_BACKEND})" >&2
  echo "→ agent containers: container:chepherd (shared netns — bypasses CNI plumbing)" >&2
else
  NETWORK_ARGS=()
  HOST_PORT_BIND="0.0.0.0"
  echo "→ chepherd container: slirp4netns (CNI/netavark unavailable, host loopback fallback)" >&2
  echo "→ agent containers: container:chepherd (shared netns — bypasses CNI entirely; #414 architectural fix)" >&2
  echo "   (Multi-agent MCP works because agents share chepherd's netns; no host loopback hop needed.)" >&2
  echo "" >&2
fi

if [ -z "${NO_HOST_CLAUDE}" ] && [ ${#CLAUDE_MOUNTS[@]} -gt 0 ]; then
  echo "→ Starting chepherd (sibling-container architecture, host ~/.claude auto-detected)..."
elif [ -n "${NO_HOST_CLAUDE}" ]; then
  echo "→ Starting chepherd (sibling-container architecture, CHEPHERD_NO_HOST_CLAUDE — no host creds)..."
else
  echo "→ Starting chepherd (sibling-container architecture, no host ~/.claude found)..."
fi
# Path translation: bind-mount paths chepherd constructs (using its
# /home/chepherd/... view) must be rewritten to HOST paths before they
# reach the host podman daemon. We pass the host paths as env vars so
# the runtime can substitute the prefix.
exec podman run \
  --name chepherd \
  --rm \
  --detach \
  "${NETWORK_ARGS[@]}" \
  `# ↑ #398 P0 v2 + #403 fallback: NETWORK_ARGS expands to "--network chepherd-net" when CNI/` \
  `# netavark plugins are present, OR to nothing (default slirp4netns) when the backend isn't` \
  `# functional. The runtime gets the matching CHEPHERD_CONTAINER_NETWORK + CHEPHERD_MCP_URL` \
  `# via AGENT_NETWORK_ENV below so agentNetworkMode() and the MCP URL default both pick the` \
  `# fallback path consistently.` \
  -e HOME=/home/chepherd \
  -p "127.0.0.1:${PORT}:8080" \
  -p "${HOST_PORT_BIND}:9090:9090" \
  `# ↑ #398 v2: 127.0.0.1 binding when chepherd-net is active (agents use container DNS).` \
  `# 0.0.0.0 binding when falling back to slirp4netns (agents need to reach the host loopback` \
  `# via 10.0.2.2; #398 v1 caveat about kernel-level isolation still applies but this is the` \
  `# best we can do without CNI). v0.9.4 Option C drops this mapping entirely.` \
  "${AGENT_NETWORK_ENV[@]}" \
  `# ↑ #403 P0: propagate the chepherd-net-vs-fallback choice to the runtime so agentNetworkMode()` \
  `# and the MCP URL default match what scripts/start.sh actually achieved. Without this, the` \
  `# runtime might pick chepherd-net for agents while chepherd itself fell back to slirp4netns,` \
  `# and agent spawns would error out trying to attach to a non-existent network.` \
  -v "${STATE_DIR}:/home/chepherd/.local/state/chepherd:rw" \
  "${CLAUDE_MOUNTS[@]}" \
  -v "${REPOS_DIR}:/home/chepherd/repos:rw" \
  -v "$(pwd)/web/dist:/app/web/dist:ro" \
  -v "${HOST_PODMAN_SOCK}:/run/host-podman/podman.sock:rw" \
  -e CONTAINER_HOST="unix:///run/host-podman/podman.sock" \
  -e CHEPHERD_HOST_STATE_DIR="${STATE_DIR}" \
  -e CHEPHERD_HOST_REPOS_DIR="${REPOS_DIR}" \
  -e CHEPHERD_HOST_CLAUDE_DIR="${CLAUDE_DIR}" \
  -e CHEPHERD_MCP_LISTEN=0.0.0.0:9090 \
  -e CHEPHERD_AGENT_MCP_URL=${CHEPHERD_AGENT_MCP_URL:-http://127.0.0.1:9090/mcp} \
  `# ↑ make agents use the canonical Streamable-HTTP MCP transport (#478) instead` \
  `# of the deprecated stdio bridge. copilot's strict JSON-RPC parser chokes on the` \
  `# bridge framing ("Unexpected end of JSON input"); HTTP gives it clean responses.` \
  `# Verified: POST /mcp initialize+tools/list return valid JSON from inside agents.` \
  -e CHEPHERD_CLEANUP_ORPHANS_ON_START=${CHEPHERD_CLEANUP_ORPHANS_ON_START:-true} \
  ${CHEPHERD_FEDERATION_REGISTRY_URL:+-e CHEPHERD_FEDERATION_REGISTRY_URL="${CHEPHERD_FEDERATION_REGISTRY_URL}"} \
  `# #676 — join the hub-relayed federation mesh so the dashboard's Federation/Multi-host` \
  `# panes show hub-discovered peers. Set CHEPHERD_HUB_URL + CHEPHERD_ORG_ID to enable.` \
  ${CHEPHERD_HUB_URL:+-e CHEPHERD_HUB_URL="${CHEPHERD_HUB_URL}"} \
  ${CHEPHERD_ORG_ID:+-e CHEPHERD_ORG_ID="${CHEPHERD_ORG_ID}"} \
  "${IMAGE}" \
  run \
    --headless \
    --listen 0.0.0.0:8080 \
    --web-dir /app/web/dist \
    --state-dir /home/chepherd/.local/state/chepherd \
    --cwd /home/chepherd/repos

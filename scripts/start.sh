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
PORT="${CHEPHERD_PORT:-8083}"
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
USE_CHEPHERD_NET=0
case "${NETWORK_BACKEND}" in
  netavark)
    USE_CHEPHERD_NET=1
    ;;
  cni)
    # CNI requires /opt/cni/bin/bridge etc. Probe one canonical plugin
    # before trusting CNI; missing plugins are the most common cause
    # of "failed to mount netns directory" (#403 P0).
    if [ -x /opt/cni/bin/bridge ] && [ -x /opt/cni/bin/firewall ]; then
      USE_CHEPHERD_NET=1
    fi
    ;;
esac
if [ "${USE_CHEPHERD_NET}" -eq 1 ]; then
  # `podman network create` is idempotent via `|| true` — re-runs
  # across operator bounces don't error if the network already exists.
  podman network create chepherd-net 2>/dev/null || true
  NETWORK_ARGS=(--network chepherd-net)
  AGENT_NETWORK_ENV=(-e "CHEPHERD_CONTAINER_NETWORK=chepherd-net" -e "CHEPHERD_MCP_URL=ws://chepherd:9090/mcp/ws")
  HOST_PORT_BIND="127.0.0.1"
  echo "→ Network: chepherd-net (backend=${NETWORK_BACKEND}, agents reach MCP via container DNS)" >&2
else
  # Fallback: slirp4netns. Multi-agent MCP via chepherd-net DNS isn't
  # available; chepherd-net fix #398v2/#395/#396 ship as advisory
  # capability rather than guaranteed. Operators install
  # containernetworking-plugins or upgrade to netavark for full
  # multi-agent operation.
  NETWORK_ARGS=()
  AGENT_NETWORK_ENV=(-e "CHEPHERD_CONTAINER_NETWORK=slirp4netns:port_handler=slirp4netns" -e "CHEPHERD_MCP_URL=ws://host.containers.internal:9090/mcp/ws")
  HOST_PORT_BIND="0.0.0.0"  # so agents can at least reach via 10.0.2.2 (kernel-isolation caveat per #398 v1 still applies, but this is the best we can do)
  echo "" >&2
  echo "⚠  WARNING: podman network backend '${NETWORK_BACKEND}' lacks required plugins (#403 P0)." >&2
  echo "   Falling back to slirp4netns. Multi-agent MCP toolkit may be degraded:" >&2
  echo "   - Agent → chepherd MCP reachable via host.containers.internal (10.0.2.2)" >&2
  echo "   - Kernel-level rootless-loopback isolation may still block back-connects" >&2
  echo "   For full multi-agent capability, install containernetworking-plugins (apt:" >&2
  echo "   containernetworking-plugins or dnf: containernetworking-plugins) OR upgrade" >&2
  echo "   to Podman 4.x+ (netavark backend). Bounce chepherd after install." >&2
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
  "${IMAGE}" \
  run \
    --headless \
    --listen 0.0.0.0:8080 \
    --web-dir /app/web/dist \
    --state-dir /home/chepherd/.local/state/chepherd \
    --cwd /home/chepherd/repos

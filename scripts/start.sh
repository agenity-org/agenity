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
# `podman network create` is idempotent via `|| true` — re-runs across
# operator bounces don't error out if the network already exists.
podman network create chepherd-net 2>/dev/null || true

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
  --network chepherd-net \
  `# ↑ #398 P0 v2: chepherd attaches to chepherd-net so agent sibling containers (also attached)` \
  `# can reach the MCP server via container-name DNS: ws://chepherd:9090/mcp/ws. Replaces the` \
  `# Option A 0.0.0.0:9090 host-port binding which slirp4netns kernel-isolation blocked agents` \
  `# from reaching despite the host-side bind succeeding. 9090 host-port mapping kept only for` \
  `# operator-side curl debugging; agents use the in-network address.` \
  -e HOME=/home/chepherd \
  -p "127.0.0.1:${PORT}:8080" \
  -p "127.0.0.1:9090:9090" \
  `# ↑ #398 v2: back to 127.0.0.1-only for the host-port mapping. Agents reach chepherd via` \
  `# chepherd-net (container-name DNS); this mapping is only for operator-side curl/debug from` \
  `# the host. v0.9.4 Option C will drop this mapping entirely + put MCP in a sibling container.` \
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

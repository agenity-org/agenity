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

echo "→ Starting chepherd (sibling-container architecture)..."
# Path translation: bind-mount paths chepherd constructs (using its
# /home/chepherd/... view) must be rewritten to HOST paths before they
# reach the host podman daemon. We pass the host paths as env vars so
# the runtime can substitute the prefix.
exec podman run \
  --name chepherd \
  --rm \
  --detach \
  -e HOME=/home/chepherd \
  -p "127.0.0.1:${PORT}:8080" \
  -p "127.0.0.1:9090:9090" \
  -v "${STATE_DIR}:/home/chepherd/.local/state/chepherd:rw" \
  -v "${CLAUDE_DIR}:/home/chepherd/.claude:ro" \
  -v "${HOME}/.claude.json:/home/chepherd/.claude.json:ro" \
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

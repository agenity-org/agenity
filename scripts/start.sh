#!/usr/bin/env bash
# Start chepherd in a rootless podman container.
#
# Architecture: chepherd runs containerized. Its OWN internal podman spawns
# agent containers — no host daemon dependency.
#
# The chepherd container runs --privileged so that UID 0 inside the container
# maps to the real host user, and so the inner podman can create mount
# namespaces for agent containers without hitting user-namespace limits.
#
# The agent image is loaded once into the persistent agent storage using
# skopeo on the host (no nested podman required). The inner root podman reads
# from that storage when spawning agents.

set -euo pipefail

IMAGE="${CHEPHERD_IMAGE:-chepherd:latest}"
AGENT_IMAGE="${CHEPHERD_AGENT_IMAGE:-chepherd-agent:latest}"
PORT="${CHEPHERD_PORT:-8083}"
STATE_DIR="${HOME}/.local/state/chepherd-v08"
REPOS_DIR="${HOME}/repos"
CLAUDE_DIR="${HOME}/.claude"

# Persistent storage for the container's internal podman.
# Mounted at /var/lib/chepherd-agents inside the container.
# Inner podman uses --root /var/lib/chepherd-agents/storage when spawning agents.
AGENT_STORAGE="${STATE_DIR}/agent-storage"
GRAPH_ROOT="${AGENT_STORAGE}/storage"
RUN_ROOT="${AGENT_STORAGE}/run"

mkdir -p "${STATE_DIR}" "${GRAPH_ROOT}" "${RUN_ROOT}"

# Stop any existing instance
podman rm -f chepherd 2>/dev/null || true

# ── Sync agent image into the persistent agent storage ──────────────────────
# skopeo copies directly to the containers-storage format used by inner podman.
# No nested podman / newuidmap required — skopeo just writes file blobs.
echo "→ Syncing ${AGENT_IMAGE} into agent storage..."
skopeo copy \
  "containers-storage:localhost/chepherd-agent:latest" \
  "containers-storage:[overlay@${GRAPH_ROOT}+${RUN_ROOT}]localhost/chepherd-agent:latest"

echo "→ Starting chepherd..."
exec podman run \
  --name chepherd \
  --rm \
  --detach \
  --privileged \
  -e HOME=/home/chepherd \
  -p "127.0.0.1:${PORT}:8080" \
  -p "127.0.0.1:9090:9090" \
  -v "${STATE_DIR}:/home/chepherd/.local/state/chepherd:rw" \
  -v "${AGENT_STORAGE}:/var/lib/chepherd-agents:rw" \
  -v "${CLAUDE_DIR}:/home/chepherd/.claude:ro" \
  -v "${HOME}/.claude.json:/home/chepherd/.claude.json:ro" \
  -v "${REPOS_DIR}:/home/chepherd/repos:rw" \
  -v "$(pwd)/web/dist:/app/web/dist:ro" \
  --device /dev/fuse \
  --security-opt label=disable \
  -e CHEPHERD_MCP_LISTEN=0.0.0.0:9090 \
  "${IMAGE}" \
  run \
    --headless \
    --listen 0.0.0.0:8080 \
    --web-dir /app/web/dist \
    --state-dir /home/chepherd/.local/state/chepherd \
    --cwd /home/chepherd/repos

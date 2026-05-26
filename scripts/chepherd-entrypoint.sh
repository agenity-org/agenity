#!/bin/bash
# chepherd container entrypoint.
#
# Two responsibilities the bare ENTRYPOINT couldn't do:
#
# 1. Load chepherd-agent:latest into the in-pod podman storage (#121).
#    The image is bind-mounted in as a tar at /agent-image.tar (the pod
#    YAML / compose.yaml ships it that way), or pulled from a registry
#    if AGENT_IMAGE_REF is set. Without this, the in-pod podman can't
#    spawn agents.
#
# 2. Permission-fix /home/chepherd/.local/state/chepherd when a fresh
#    named volume is mounted — podman play kube creates it as
#    root-owned; we chown to the chepherd user before the daemon writes.

set -euo pipefail

# chepherd MUST run as root inside the container — the outer --privileged
# makes UID 0 safe (it maps to the host user's UID via rootless podman's
# user-namespace), and nested podman needs root inside to use /run/libpod
# for its lock files + manage agent containers. Dropping to a non-root
# user (which an earlier rev of this script did) breaks nested podman
# with "error opening /run/libpod/alive.lck: permission denied".

# Optional: load agent image from a tarball mount if one is present.
# scripts/start.sh ships the image via skopeo into the bind-mounted
# /var/lib/chepherd-agents/storage instead — this branch is only for
# K8s deployments where bind-mounting the storage dir isn't an option.
if [ -f /agent-image.tar ] && ! podman --root /var/lib/chepherd-agents/storage \
     --runroot /var/lib/chepherd-agents/run image exists chepherd-agent:latest 2>/dev/null; then
  echo "chepherd-entrypoint: loading /agent-image.tar into agent storage…"
  podman --root /var/lib/chepherd-agents/storage --runroot /var/lib/chepherd-agents/run \
    load -i /agent-image.tar 2>&1 | tail -3 || echo "warn: failed to load /agent-image.tar"
fi

exec /usr/local/bin/chepherd run \
  --headless \
  --listen "0.0.0.0:8080" \
  --web-dir "/app/web/dist" \
  --state-dir "/home/chepherd/.local/state/chepherd" \
  "$@"

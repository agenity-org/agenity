#!/usr/bin/env bash
# Stop the running chepherd container.
set -euo pipefail
podman rm -f chepherd 2>/dev/null && echo "chepherd stopped" || echo "chepherd was not running"

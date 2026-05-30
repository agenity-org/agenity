#!/usr/bin/env bash
# scripts/test-agent-entrypoint-exec-bit.sh — #367 P0 regression test.
# Asserts scripts/agent-entrypoint.sh has the exec bit set so:
#   1. COPY --chmod=755 has the right source mode (defensive)
#   2. Anyone running the script directly during dev doesn't hit permission denied
#
# CI invokes this; local pre-commit hook can too.
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ ! -x scripts/agent-entrypoint.sh ]]; then
    echo "✗ scripts/agent-entrypoint.sh missing exec bit (#367 P0 regression)"
    ls -la scripts/agent-entrypoint.sh
    echo
    echo "Fix: chmod +x scripts/agent-entrypoint.sh"
    exit 1
fi
echo "✓ scripts/agent-entrypoint.sh has exec bit"

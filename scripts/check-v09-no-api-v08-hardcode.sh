#!/usr/bin/env bash
# scripts/check-v09-no-api-v08-hardcode.sh — regression guard for #591.
#
# v0.9 dashboard components must NOT hardcode /api-v0X/ legacy prefixes.
# Each release ships its own URL namespace per #297/#567 URL-versioning
# rule; v0.9 calls must use the canonical /api/v1/* prefix that proxies
# via the unversioned CHEPHERD_PORT.
#
# Run pre-commit OR in CI. Exits non-zero if any /api-v0X/ string is
# found in web/src/components/v09/.

set -euo pipefail

ROOT="${ROOT:-$(git rev-parse --show-toplevel)}"
TARGET="${ROOT}/web/src/components/v09"

if [ ! -d "$TARGET" ]; then
  echo "regression guard: target dir not found: $TARGET" >&2
  exit 2
fi

HITS=$(grep -rn "/api-v0" "$TARGET" 2>/dev/null || true)
if [ -n "$HITS" ]; then
  echo "FAIL #591 regression: v09/ components must not hardcode /api-v0X/ prefixes" >&2
  echo "$HITS" >&2
  echo "" >&2
  echo "Fix: replace '/api-v08/v1' (and similar) with '/api/v1' so the canonical CHEPHERD_PORT proxy handles routing." >&2
  exit 1
fi

echo "OK #591 regression guard — zero /api-v0X/ hardcodes in $TARGET"

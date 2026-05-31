#!/usr/bin/env bash
# scripts/check-v09-no-home-chepherd-hardcode.sh — regression guard for #597.
#
# v0.9 dashboard components must NOT hardcode /home/chepherd/* paths
# (those assume chepherd runs inside its own container as user
# 'chepherd' with home /home/chepherd; breaks every host-direct deploy
# where the operator's $HOME is different).
#
# Run pre-commit OR in CI. Exits non-zero if any /home/chepherd hit
# is found in v09/* outside the explicit allowlist.
#
# ALLOWLIST — pending fixes elsewhere; entries here have a tracking
# issue + an expected merge ordering. Remove the entry once the sister
# PR merges. (#597 acceptance criterion #1 will be met fully once the
# allowlist is empty.)

set -euo pipefail

ROOT="${ROOT:-$(git rev-parse --show-toplevel)}"
TARGET="${ROOT}/web/src/components/v09"

if [ ! -d "$TARGET" ]; then
  echo "regression guard: target dir not found: $TARGET" >&2
  exit 2
fi

# Files allowed to retain /home/chepherd hardcodes pending sister fix.
# Each row: <relative-path>:<tracking-issue>
ALLOWLIST=(
  "Stage5Launch.svelte:#594/#602"  # Stage5Launch.svelte cwd is fixed by #602 (chepherd-lead's PR)
)

HITS=$(grep -rn "/home/chepherd" "$TARGET" 2>/dev/null || true)
if [ -z "$HITS" ]; then
  echo "OK #597 regression guard — zero /home/chepherd hardcodes in $TARGET"
  exit 0
fi

# Filter allowlist: drop any hit whose file appears in ALLOWLIST.
FILTERED=""
while IFS= read -r line; do
  [ -z "$line" ] && continue
  allowed=0
  for entry in "${ALLOWLIST[@]}"; do
    fname="${entry%%:*}"
    if echo "$line" | grep -q "/${fname}:"; then
      allowed=1
      break
    fi
  done
  if [ "$allowed" -eq 0 ]; then
    FILTERED="${FILTERED}${line}
"
  fi
done <<< "$HITS"

if [ -n "$FILTERED" ]; then
  echo "FAIL #597 regression: v09/ components must not hardcode /home/chepherd paths" >&2
  printf "%s" "$FILTERED" >&2
  echo "" >&2
  echo "Fix: replace hardcoded path with config/env-driven OR translation-aware code." >&2
  echo "See AuthGate.svelte help-text edit as the reference pattern for env-aware copy." >&2
  exit 1
fi

echo "OK #597 regression guard — zero unallowlisted /home/chepherd hits in $TARGET"
echo "  (allowlist active: ${ALLOWLIST[*]})"
echo "  Tighten the allowlist when each sister PR merges."

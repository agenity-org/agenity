#!/usr/bin/env bash
#
# check-stage4-model-ids.sh — pins the canonical model IDs in
# Stage4AgentTypes.svelte's MODELS_BY_TYPE map (#244 regression gate).
#
# Operator-blocked when claude-opus-4 (CLI alias, not API canonical)
# slipped into the wizard's hardcoded list in #237/PR #243. Architect
# called the apology + spelled out the correct set; this guard locks
# them in so a future rev that drops one of these IDs surfaces at CI
# rather than at operator-clicks-Launch time.
#
# What we assert:
#   - claude-opus-4-7   present
#   - claude-sonnet-4-6 present
#   - claude-haiku-4-5  present
#   - claude-opus-4 / claude-sonnet-4 / claude-haiku-4 NOT present
#     (the un-versioned forms are CLI aliases the API rejects)
#
# Usage:
#   ./scripts/check-stage4-model-ids.sh    # passes when canonical set intact
#   exit 0 if clean; exit 1 on any drift.
#
# Refs #237 #244.

set -euo pipefail

cd "$(dirname "$0")/.."

FILE="web/src/components/v09/Stage4AgentTypes.svelte"
if [[ ! -f "$FILE" ]]; then
    echo "check-stage4-model-ids.sh: $FILE missing — has the file been moved?" >&2
    exit 2
fi

REQUIRED=(
    "claude-opus-4-7"
    "claude-sonnet-4-6"
    "claude-haiku-4-5"
)

# The un-versioned aliases that surface in claude --help's docstring
# but get rejected by Anthropic's API. Operator-blocked when these
# slipped in during #237. Guard against re-introduction.
FORBIDDEN=(
    "'claude-opus-4'"
    "'claude-sonnet-4'"
    "'claude-haiku-4'"
)

violations=()

for id in "${REQUIRED[@]}"; do
    if ! grep -qF "'$id'" "$FILE"; then
        violations+=("MISSING required canonical ID: $id")
    fi
done

for bad in "${FORBIDDEN[@]}"; do
    if grep -qF "$bad" "$FILE"; then
        violations+=("FORBIDDEN alias (API rejects) re-introduced: $bad")
    fi
done

if [[ ${#violations[@]} -gt 0 ]]; then
    echo "STAGE 4 MODEL-ID GUARD FAILED (#244):" >&2
    for v in "${violations[@]}"; do
        echo "  - $v" >&2
    done
    echo >&2
    echo "Canonical IDs per architect call 2026-05-30 + claude binary string-grep." >&2
    echo "Un-versioned aliases (claude-opus-4 etc.) are CLI shortcuts the API rejects." >&2
    echo "See $FILE MODELS_BY_TYPE + issue #244." >&2
    exit 1
fi

echo "✓ Stage 4 MODELS_BY_TYPE canonical IDs intact (#244)."

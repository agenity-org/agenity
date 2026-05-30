#!/usr/bin/env bash
#
# banned-vocab-shepherd.sh — #225 row F5 CI guard.
#
# Rejects NEW operator-facing 'shepherd' / 'Shepherd' references in
# the two surfaces that ship verbatim text to operators:
#
#   - internal/prompts/*.md    (system prompts baked into the binary)
#   - **/*.astro               (marketing site pages)
#
# Out of scope (separate transition sweeps; not enforced by this guard):
#
#   - Go identifiers and code (RoleShepherd, runFlagNoShepherd, etc.)
#   - internal/mcpserver/server.go MCP tool descriptions documenting the
#     'shepherd' role enum string (back-compat per F4)
#   - web/src/components/Dashboard.svelte UI strings (separate Wave)
#   - Historical markdown (rebrand docs, archived PR notes)
#   - vendor/, node_modules/, generated files
#
# Lines with back-compat exemption markers are skipped:
#   - "back-compat" / "alias" / "legacy" anywhere on the line
#   - "--scrummaster-name" or "#225 row F" reference
#
# Usage:
#   ./scripts/banned-vocab-shepherd.sh        # scans the repo
#   exit 0 if clean; exit 1 if any new operator-facing 'shepherd' ref found.

set -euo pipefail

cd "$(dirname "$0")/.."

LINE_EXEMPT_REGEX='(back-compat|alias|legacy|#225 row F|--scrummaster-name|--no-shepherd|@shepherd|@scrummaster|your-session-name)'

hits=$(git grep -nE '[Ss]hepherd' -- \
    'internal/prompts/*.md' \
    '*.astro' \
    || true)

violations=""
while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    rest="${line#*:}"
    body="${rest#*:}"
    if echo "$body" | grep -qE "$LINE_EXEMPT_REGEX"; then
        continue
    fi
    violations+="$line"$'\n'
done <<< "$hits"

if [[ -n "${violations//[$'\n\t ']/}" ]]; then
    echo "✗ banned-vocab violation (#225 F5): operator-facing 'shepherd' reference."
    echo "$violations"
    echo
    echo "Rename to 'Scrum Master' (display text) or 'scrummaster' (file/var name)."
    echo "If the line is a back-compat alias, add a comment containing 'back-compat',"
    echo "'alias', or 'legacy' — the guard will then skip it."
    exit 1
fi

echo "✓ banned-vocab clean (#225 F5): no new operator-facing 'shepherd' refs."

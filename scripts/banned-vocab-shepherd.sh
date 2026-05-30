#!/usr/bin/env bash
#
# banned-vocab-shepherd.sh — #225 row F5 + #292 CI guard.
#
# Rejects NEW operator-facing 'shepherd' / 'Shepherd' references in
# surfaces that ship verbatim text or rendered UI to operators:
#
#   - internal/prompts/*.md           (system prompts baked into binary)
#   - **/*.astro                      (marketing site pages)
#   - web/src/components/Dashboard.svelte (post-#292 main dashboard)
#   - web/src/components/v06/**/*.svelte   (post-#292 v06 components)
#
# Out of scope (separate transition sweeps; not enforced by this guard):
#
#   - Go identifiers and code (RoleShepherd, runFlagNoShepherd, etc.)
#   - internal/mcpserver/server.go MCP tool descriptions documenting the
#     'shepherd' role enum string (back-compat per F4)
#   - v07/v08 dashboard components (chepherd-mobile shape — separate sweep)
#   - Historical markdown (rebrand docs, archived PR notes)
#   - vendor/, node_modules/, generated files
#
# Lines with back-compat exemption markers are skipped:
#   - "back-compat" / "alias" / "legacy" anywhere on the line
#   - "--scrummaster-name" or "#225 row F" or "#292" reference
#   - field names ending in shepherding (wire API field, back-compat)
#   - widget identifier "shepherd-assessment-card" (workspace registry key)
#
# Usage:
#   ./scripts/banned-vocab-shepherd.sh        # scans the repo
#   exit 0 if clean; exit 1 if any new operator-facing 'shepherd' ref found.

set -euo pipefail

cd "$(dirname "$0")/.."

LINE_EXEMPT_REGEX='(back-compat|alias|legacy|#225 row F|#292|--scrummaster-name|--no-shepherd|@shepherd|@scrummaster|your-session-name|shepherding|shepherd-assessment-card|legacy wire value|SCRUM_MASTER_ROLES|'\''shepherd'\'', *'\''scrummaster'\''|value="shepherd")'

hits=$(git grep -nE '[Ss]hepherd' -- \
    'internal/prompts/*.md' \
    '*.astro' \
    'web/src/components/Dashboard.svelte' \
    'web/src/components/v06/TemplatePicker.svelte' \
    'web/src/components/v06/Workspace.svelte' \
    'web/src/components/v06/TeamSettings.svelte' \
    'web/src/components/v06/AgentSettings.svelte' \
    'web/src/components/v06/widgets/WidgetSessionBoard.svelte' \
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

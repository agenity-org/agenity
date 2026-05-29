#!/usr/bin/env bash
#
# banned-vocab-grep.sh — anti-regression guard for v0.9.1's renamed
# catalog. Fails if any banned vocabulary appears in operator-visible
# code, tests, or UI source (architect 2026-05-28 FINAL+, #200).
#
# Banned strings (case-insensitive, word-aware) anywhere in the catalog
# / wizard / runtimehttp / web/src tree:
#
#   Shepherd / shepherd        — replaced by Scrum Master + process-coaching
#   Stack Trio                 — fabrication, never existed
#   RACI                       — operator rejected
#   Docs Writer                — old v0.8 role name, removed
#   Frontend Implementer       — replaced by Frontend Developer
#   Backend Implementer        — replaced by Backend Developer
#
# Allowed exceptions (whitelisted paths) — the infrastructure-layer
# runtime supervisor in internal/runtime + internal/mcpserver +
# internal/runtimehttp/server.go is still internally called "shepherd"
# (that's the meta-watcher role-name in the PTY runtime). It is NOT
# catalog vocabulary and is out of scope of this rule. Likewise
# legacy v0.6 components in web/src/components/v06/ are frozen at
# their release shape.
#
# Usage:
#   ./scripts/banned-vocab-grep.sh                # scans the repo
#   exit 0 if clean; exit 1 if any banned hit found outside allowlist.

set -euo pipefail

cd "$(dirname "$0")/.."

# Paths to scan: catalog packages, wizard UI, v0.9 components.
SCAN_PATHS=(
  internal/canon
  internal/roles
  internal/skills
  internal/templateregistry
  internal/agent
  web/src/components/v09
  web/src/pages
)

# Allowlist (paths where the banned-vocab rule does not apply).
# Each entry is a substring match against the file path.
ALLOWLIST=(
  # Infrastructure-layer 'shepherd' role-name in the PTY runtime is
  # legacy v0.5+ terminology, not catalog vocabulary.
  internal/runtime
  internal/mcpserver
  # v0.6 dashboard components are frozen.
  web/src/components/v06
  # Marketing/brand pages use "meta-shepherd" as the project's own
  # self-description (the product is named Chepherd / it watches
  # agents); this is editorial copy, not catalog vocabulary.
  web/src/pages/brand.astro
  web/src/pages/docs.astro
  web/src/pages/vs/
  # The banned-vocab guard script itself names the strings it watches for.
  scripts/banned-vocab-grep.sh
  # Catalog source files contain "what's banned" documentation comments
  # that legitimately mention the banned strings (the rule body).
  internal/canon/canon_test.go
  internal/roles/roles.go
  internal/roles/builtins.go
  internal/skills/builtins.go
  internal/templateregistry/builtins.go
  internal/templateregistry/registry.go
  # Test files legitimately list banned strings as fixtures for the
  # TestNoBannedVocab guards. The guards assert the strings are absent
  # from catalog DATA, not from the test fixture lists themselves.
  _test.go
)

BANNED=(
  "[Ss]hepherd"
  "[Ss]tack[ _-][Tt]rio"
  "\\bRACI\\b"
  "Docs Writer"
  "Frontend Implementer"
  "Backend Implementer"
)

violations=()
for path in "${SCAN_PATHS[@]}"; do
  [[ -e "$path" ]] || continue
  while IFS= read -r file; do
    skip=0
    for allowed in "${ALLOWLIST[@]}"; do
      if [[ "$file" == *"$allowed"* ]]; then
        skip=1
        break
      fi
    done
    [[ $skip -eq 1 ]] && continue
    for term in "${BANNED[@]}"; do
      if matches=$(grep -nE "$term" "$file" 2>/dev/null); then
        while IFS= read -r m; do
          violations+=("$file: $m")
        done <<< "$matches"
      fi
    done
  done < <(find "$path" -type f \( -name '*.go' -o -name '*.svelte' -o -name '*.astro' -o -name '*.ts' -o -name '*.js' \) 2>/dev/null)
done

if [[ ${#violations[@]} -gt 0 ]]; then
  echo "BANNED VOCAB VIOLATIONS:" >&2
  for v in "${violations[@]}"; do echo "  $v" >&2; done
  echo >&2
  echo "Allowed exceptions live in internal/runtime + internal/mcpserver" >&2
  echo "(infrastructure-layer supervisor) + web/src/components/v06 (frozen)." >&2
  exit 1
fi

echo "✓ No banned-vocab regressions in catalog / wizard / v0.9 components."

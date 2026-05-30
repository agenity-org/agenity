#!/usr/bin/env bash
#
# check-grid-class-collision.sh — defensive guard from #228.
#
# `web/src/styles/global.css:25` defines an UNSCOPED `.grid` rule:
#   .grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 2rem; }
#
# Any `<table class="grid">` (or `<form>`, `<ul>`, etc.) in any Svelte
# component would silently inherit that rule and have its `display`
# overridden — the exact bug shape that surfaced in #226 (Stage 3
# Skills tab rendered thead+tbody as side-by-side grid columns).
#
# This script enforces:
#   1. The marketing site (`web/src/pages/*.astro`) MAY use
#      `class="grid"` — the global rule is the intended layout.
#   2. NO Svelte component (`.svelte`) may use `class="grid"` —
#      always scope (e.g. `.skills-grid`, `.role-grid`,
#      `.template-grid`).
#   3. Specifically: `<table class="grid">` is a HARD ERROR
#      anywhere in the repo.
#
# Exits 0 on clean. Exits 1 on any violation, with file:line cites.
#
# Refs #226 #228.

set -euo pipefail

cd "$(dirname "$0")/.."

violations=0

# ─── 1. No <table class="grid"> anywhere ──────────────────────────
# Matches `<table … class="… grid …">` where `grid` is a STANDALONE
# token (not a suffix like `skills-grid` or `role-grid` — those scoped
# names are exactly what the #228 sweep promotes). Token boundaries
# are space, start-of-attr, or close-quote.
if hits=$(grep -rn -E '<table[^>]*class="([^"]*[[:space:]])?grid([[:space:]][^"]*)?"' web/src/ \
        --include='*.svelte' --include='*.astro' --include='*.html' \
        2>/dev/null || true); [[ -n "$hits" ]]; then
    echo "FAIL — <table class=\"grid\"> found (#226 bug shape):"
    echo "$hits" | sed 's/^/    /'
    echo
    violations=$((violations + 1))
fi

# ─── 2. No `class="grid"` in any Svelte component ─────────────────
# Marketing pages (`.astro`) are exempt — the global rule is the
# intended layout there.
if hits=$(grep -rn 'class="grid"' web/src/components/ --include='*.svelte' 2>/dev/null || true); [[ -n "$hits" ]]; then
    echo "FAIL — unscoped class=\"grid\" in Svelte component (#228 sweep):"
    echo "$hits" | sed 's/^/    /'
    echo "    Rename to a scope-specific class (e.g. .template-grid,"
    echo "    .role-grid, .skills-grid) and define the layout in the"
    echo "    component's <style> block."
    echo
    violations=$((violations + 1))
fi

if (( violations > 0 )); then
    echo "GRID-CLASS-COLLISION GUARD FAILED ($violations violation(s))." >&2
    echo "See $0 + issue #228 for the rename pattern." >&2
    exit 1
fi

echo "✓ No \`class=\"grid\"\` collisions in Svelte components (#228 sweep clean)."

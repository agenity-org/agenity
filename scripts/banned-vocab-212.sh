#!/usr/bin/env bash
#
# banned-vocab-212.sh — anti-regression guard for the architect-spec'd
# #212 sweep. Fails the build if any banned token appears in non-docs,
# non-Markdown files anywhere in the repo. Tokens live in
# scripts/.banned-vocab-tokens-212 so this script body doesn't need to
# name them (preserves the "0 hits in scope files" invariant).
#
# Scope (exclusions match the architect's pathspec):
#   - docs/    is exempt (rule body lives there)
#   - *.md     is exempt (PR descriptions, READMEs, CHANGELOGs)
#
# Usage:
#   ./scripts/banned-vocab-212.sh        # scans the repo
#   exit 0 if clean; exit 1 if any banned token found in scope.

set -euo pipefail

cd "$(dirname "$0")/.."

TOKENS_FILE="$(dirname "$0")/.banned-vocab-tokens-212"
if [[ ! -f "$TOKENS_FILE" ]]; then
    echo "banned-vocab-212.sh: missing $TOKENS_FILE" >&2
    exit 2
fi

# Build the alternation pattern from the token list (one regex per line,
# blank + comment lines skipped). git-grep -E accepts the | alternation.
PATTERN=""
while IFS= read -r line; do
    [[ -z "$line" || "$line" =~ ^# ]] && continue
    if [[ -z "$PATTERN" ]]; then
        PATTERN="$line"
    else
        PATTERN="$PATTERN|$line"
    fi
done < "$TOKENS_FILE"

if [[ -z "$PATTERN" ]]; then
    echo "banned-vocab-212.sh: token list empty — refusing to run" >&2
    exit 2
fi

# Run the grep. `-I` skips binary files, `-nE` numbers + extended regex.
# Pathspec exclusions:
#   :!docs/                                  — rule body lives there
#   :!*.md                                   — PR bodies, CHANGELOGs, READMEs
#   :!scripts/.banned-vocab-tokens-212       — THIS list (self-recursion guard)
#   :!scripts/.banned-vocab-tokens           — sibling v0.9.1 list, same shape
#   :!scripts/banned-vocab-212.sh            — defense-in-depth vs inline-token regression
#   :!scripts/banned-vocab-grep.sh           — same, sibling enforcement script
if hits=$(git grep -nIE "$PATTERN" -- \
        ':!docs/' ':!*.md' \
        ':!scripts/.banned-vocab-tokens-212' \
        ':!scripts/.banned-vocab-tokens' \
        ':!scripts/banned-vocab-212.sh' \
        ':!scripts/banned-vocab-grep.sh' \
        2>/dev/null); then
    echo "BANNED VOCAB DETECTED (#212):" >&2
    echo "$hits" >&2
    echo >&2
    echo "Tokens scanned via scripts/.banned-vocab-tokens-212; see issue #212 for the rationale." >&2
    exit 1
fi

echo "✓ No banned-vocab regressions in non-docs files (#212)."

#!/usr/bin/env bash
# scripts/test-agent-entrypoint.sh — regression test for #254.
#
# Verifies that agent-entrypoint.sh ALWAYS overwrites ~/.claude/.credentials.json
# and ~/.claude.json from /run/secrets/, even when those files already
# exist in $HOME. The prior `[ ! -e ]` skip-if-exists guard pinned a
# first-spawn snapshot across subsequent spawns; chepherd's
# materializeAgentSecrets pre-refreshes on every spawn, so the
# entrypoint must let that fresh content land.
#
# Test substrate: tmpfs $HOME, faked /run/secrets/, real entrypoint
# script (sourced from this repo). Asserts:
#   1. First boot writes content "FRESH-A" → both files end up with "FRESH-A".
#   2. Second boot with /run/secrets/ now carrying "FRESH-B" → both files
#      end up with "FRESH-B" (NOT pinned to "FRESH-A" by the existence guard).
#
# Run: ./scripts/test-agent-entrypoint.sh
# Exits 0 on PASS, 1 on FAIL.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ENTRYPOINT="$SCRIPT_DIR/agent-entrypoint.sh"

if [[ ! -f "$ENTRYPOINT" ]]; then
    echo "test-agent-entrypoint.sh: $ENTRYPOINT missing — has it been moved?" >&2
    exit 2
fi

# Sandbox $HOME and /run/secrets/. We can't bind-mount inside the test,
# so we shim the entrypoint by overriding the absolute /run/secrets/
# path via a sed-rewrite into a tmp copy, and running with a tmpfs HOME.
TMP_ROOT="$(mktemp -d -t chepherd-entrypoint-test-XXXXXX)"
trap 'rm -rf "$TMP_ROOT"' EXIT

FAKE_HOME="$TMP_ROOT/home"
FAKE_SECRETS="$TMP_ROOT/secrets"
mkdir -p "$FAKE_HOME" "$FAKE_SECRETS"

# Rewrite /run/secrets/ → $FAKE_SECRETS inside a copy of the entrypoint.
SHIM_ENTRYPOINT="$TMP_ROOT/agent-entrypoint.shim.sh"
sed "s|/run/secrets/|$FAKE_SECRETS/|g" "$ENTRYPOINT" > "$SHIM_ENTRYPOINT"
chmod +x "$SHIM_ENTRYPOINT"

# Replace the `exec "$@"` tail with a no-op so the entrypoint returns
# after the copy step.
sed -i 's|^exec "\$@"$|true|' "$SHIM_ENTRYPOINT"

run_entrypoint() {
    HOME="$FAKE_HOME" "$SHIM_ENTRYPOINT"
}

assert_file_content() {
    local path="$1"; local expected="$2"; local label="$3"
    if [[ ! -f "$path" ]]; then
        echo "FAIL — $label: file $path does not exist" >&2
        return 1
    fi
    local actual
    actual="$(cat "$path")"
    if [[ "$actual" != "$expected" ]]; then
        echo "FAIL — $label: expected '$expected' in $path, got '$actual'" >&2
        return 1
    fi
}

# ─── Spawn 1 ─────────────────────────────────────────────────────────
echo "FRESH-A-credentials" > "$FAKE_SECRETS/claude-credentials"
echo "FRESH-A-onboarding"  > "$FAKE_SECRETS/claude-onboarding"
run_entrypoint
assert_file_content "$FAKE_HOME/.claude/.credentials.json" "FRESH-A-credentials" "spawn-1 credentials"
assert_file_content "$FAKE_HOME/.claude.json"              "FRESH-A-onboarding"  "spawn-1 onboarding"
echo "✓ spawn-1: both files landed FRESH-A as expected."

# ─── Spawn 2 ─ same HOME, NEW /run/secrets/ content. The pre-#254 bug
# was that the existence guard skipped the copy and the agent kept
# yesterday's content. Assert FRESH-B lands. ───────────────────────────
echo "FRESH-B-credentials" > "$FAKE_SECRETS/claude-credentials"
echo "FRESH-B-onboarding"  > "$FAKE_SECRETS/claude-onboarding"
run_entrypoint
assert_file_content "$FAKE_HOME/.claude/.credentials.json" "FRESH-B-credentials" "spawn-2 credentials (regression — #254)"
assert_file_content "$FAKE_HOME/.claude.json"              "FRESH-B-onboarding"  "spawn-2 onboarding  (regression — #254)"
echo "✓ spawn-2: both files refreshed to FRESH-B — regression closed."

# ─── No secrets case ─────────────────────────────────────────────────
# Ensure the entrypoint is silent + doesn't error when the secret
# files are missing (operator forgot to wire vault → claude-code falls
# into OAuth login UI, which is the intended fallback).
rm -f "$FAKE_SECRETS/claude-credentials" "$FAKE_SECRETS/claude-onboarding"
rm -rf "$FAKE_HOME/.claude" "$FAKE_HOME/.claude.json"
mkdir -p "$FAKE_HOME/.claude"
run_entrypoint
if [[ -e "$FAKE_HOME/.claude/.credentials.json" || -e "$FAKE_HOME/.claude.json" ]]; then
    echo "FAIL — empty-secrets: unexpected files materialised in HOME" >&2
    exit 1
fi
echo "✓ empty-secrets: no spurious files when /run/secrets/ is empty."

echo
echo "✓ agent-entrypoint.sh refresh-on-every-spawn behaviour intact (#254)."

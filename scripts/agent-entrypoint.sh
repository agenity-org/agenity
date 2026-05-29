#!/bin/bash
# chepherd agent container entrypoint.
#
# claude-code v2.1.150 needs TWO files to consider itself logged in:
#   1) ~/.claude/.credentials.json   — the OAuth tokens (accessToken etc.)
#   2) ~/.claude.json                — the per-installation state file,
#      MUST contain hasCompletedOnboarding: true + userID + oauthAccount
#      or claude-code re-runs the onboarding/login flow even with valid
#      credentials.
#
# chepherd's materializeAgentSecrets writes both to /run/secrets/. This
# script copies them into the locations claude-code reads at startup.
# The vault entry the operator chose is the source of truth.
set -e

mkdir -p "$HOME/.claude/projects"
# #254 — ALWAYS overwrite from /run/secrets/ on every container boot,
# regardless of whether the target file exists in $HOME. The per-agent
# HOME directory persists across spawns (rootless podman bind-mount of
# agents/<name>/home/), so the first spawn's `.credentials.json`
# survives subsequent spawns. The prior `[ ! -e ... ]` guard caused
# claude-code to wake with a 23h-stale accessToken — past the
# (~minutes-to-hours) Anthropic OAuth expiry — which triggered
# claude-code's login UI on container boot (operator-perceived as a
# Stripe-ish redirect on the fullstack agent in #254).
#
# Safe to overwrite: chepherd's runtime.materializeAgentSecrets calls
# refreshClaudeOAuthIfNeeded BEFORE writing /run/secrets/, so the
# materialized file is always at least as fresh as any in-process
# refresh claude-code may have done previously.
if [ -f /run/secrets/claude-credentials ]; then
    cp /run/secrets/claude-credentials "$HOME/.claude/.credentials.json"
    chmod 600 "$HOME/.claude/.credentials.json"
fi
if [ -f /run/secrets/claude-onboarding ]; then
    cp /run/secrets/claude-onboarding "$HOME/.claude.json"
    chmod 600 "$HOME/.claude.json"
fi

exec "$@"

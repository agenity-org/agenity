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
if [ -f /run/secrets/claude-credentials ] && [ ! -e "$HOME/.claude/.credentials.json" ]; then
    cp /run/secrets/claude-credentials "$HOME/.claude/.credentials.json"
    chmod 600 "$HOME/.claude/.credentials.json"
fi
if [ -f /run/secrets/claude-onboarding ] && [ ! -e "$HOME/.claude.json" ]; then
    cp /run/secrets/claude-onboarding "$HOME/.claude.json"
    chmod 600 "$HOME/.claude.json"
fi

exec "$@"

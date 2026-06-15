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
# chepherd's materializeAgentSecrets writes per-flavor OAuth blobs to
# /run/secrets/. This script copies each one into the location its CLI
# reads at startup. The vault entry the operator chose is the source of
# truth.
set -e

mkdir -p "$HOME/.claude/projects"

# #254 — ALWAYS overwrite from /run/secrets/ on every container boot,
# regardless of whether the target file exists in $HOME. The per-agent
# HOME directory persists across spawns (rootless podman bind-mount of
# agents/<name>/home/), so the first spawn's credential files survive
# subsequent spawns. The prior `[ ! -e ... ]` guard caused claude-code to
# wake with a 23h-stale accessToken — past the (~minutes-to-hours)
# Anthropic OAuth expiry — which triggered claude-code's login UI on
# container boot (operator-perceived as a Stripe-ish redirect on the
# fullstack agent in #254).
#
# Safe to overwrite: chepherd's runtime.materializeAgentSecrets calls
# refreshClaudeOAuthIfNeeded BEFORE writing /run/secrets/, so the
# materialized file is always at least as fresh as any in-process refresh
# claude-code may have done previously.
#
# #741 — generalize the historically claude-only copy into a per-flavor
# table of "<secret-file> <dest-path>" rows. Each row: if
# /run/secrets/<secret-file> exists, mkdir -p its destination dir, copy,
# and chmod 600. The two claude rows behave EXACTLY as before (same source
# files, same dests, same 600 perms). Adding a flavor = append a row here
# AND a matching agentOAuthCredsTable row in internal/runtime/runtime.go.
#
# NOTE: a leading "~/" in the dest is expanded to "$HOME/" below; we keep
# the table values readable rather than pre-resolving in Go.
copy_secret() {
    # $1 = secret filename under /run/secrets ; $2 = ~-relative dest path
    local src="/run/secrets/$1"
    local dest="${2/#\~\//$HOME/}"
    if [ -f "$src" ]; then
        mkdir -p "$(dirname "$dest")"
        cp "$src" "$dest"
        chmod 600 "$dest"
    fi
}

# secret-file → dest-path table. Claude rows MUST stay first + unchanged.
copy_secret claude-credentials "~/.claude/.credentials.json"
copy_secret claude-onboarding  "~/.claude.json"
copy_secret gemini-creds       "~/.gemini/oauth_creds.json"
copy_secret qwen-creds         "~/.qwen/oauth_creds.json"
copy_secret copilot-creds      "~/.config/gh/hosts.yml"

# #741 TASK C — opencode provider/model selection.
#
# opencode (sst/opencode) reads ~/.config/opencode/opencode.json. The
# "model" field uses "provider/model" form and opencode auto-loads any
# well-known provider whose API key is present in the environment
# (GROQ_API_KEY, CEREBRAS_API_KEY, OPENAI_API_KEY, ...). chepherd's
# agentAuthEnvTable injects whichever of those keys the operator stored in
# the vault. Here we point opencode at the matching FREE provider's model:
#   GROQ_API_KEY     → groq/llama-3.3-70b-versatile
#   CEREBRAS_API_KEY → cerebras/llama-3.3-70b
# An explicit OPENCODE_MODEL env wins over both (operator override).
#
# We only write the config when a groq/cerebras key (or OPENCODE_MODEL) is
# present — that combination only happens for opencode agents, and writing
# the file is a no-op for any other flavor (nothing else reads it).
opencode_model=""
if [ -n "${OPENCODE_MODEL:-}" ]; then
    opencode_model="$OPENCODE_MODEL"
elif [ -n "${GROQ_API_KEY:-}" ]; then
    opencode_model="groq/llama-3.3-70b-versatile"
elif [ -n "${CEREBRAS_API_KEY:-}" ]; then
    opencode_model="cerebras/llama-3.3-70b"
fi
if [ -n "$opencode_model" ]; then
    mkdir -p "$HOME/.config/opencode"
    printf '{\n  "$schema": "https://opencode.ai/config.json",\n  "model": "%s"\n}\n' \
        "$opencode_model" > "$HOME/.config/opencode/opencode.json"
fi

exec "$@"

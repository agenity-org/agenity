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

# #741 — opencode.json (model + MCP) is written by the DAEMON
# (writeFlavorMCPConfig) to the bind-mounted home BEFORE this entrypoint runs.
# We must NOT write it here: a boot-time write runs AFTER the mount and would
# clobber the daemon's `mcp` block. Model selection is preserved in the daemon
# (opencodeModelFromEnv: OPENCODE_MODEL > GROQ_API_KEY > CEREBRAS_API_KEY); if a
# provider key is only present at container runtime, opencode auto-resolves the
# model from it. Nothing to do here.

exec "$@"

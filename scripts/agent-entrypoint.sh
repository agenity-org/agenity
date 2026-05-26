#!/bin/bash
# chepherd agent container entrypoint.
#
# Links /run/secrets/claude-credentials (provided by chepherd via bind-mount
# from the per-agent secrets dir, populated from the token vault) into
# ~/.claude/.credentials.json so claude-code can find it at its hardcoded
# path. The vault is the source of truth — claude-code may refresh the
# token in-place but chepherd captures fresh tokens via PTY OAuth-URL
# detection (R5).
set -e

mkdir -p "$HOME/.claude/projects"
if [ -f /run/secrets/claude-credentials ] && [ ! -e "$HOME/.claude/.credentials.json" ]; then
    cp /run/secrets/claude-credentials "$HOME/.claude/.credentials.json"
    chmod 600 "$HOME/.claude/.credentials.json"
fi

exec "$@"

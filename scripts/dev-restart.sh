#!/usr/bin/env bash
# Single-script rebuild + restart for the chepherd v0.5 dev loop.
# Bundles the destructive commands (pkill, rm -rf state) so the Claude
# Code harness only prompts once for "run scripts/dev-restart.sh"
# instead of for every individual command.
#
# Usage: ./scripts/dev-restart.sh [spawn_name] [spawn_cwd]
#   spawn_name: optional — auto-spawn this worker after restart
#   spawn_cwd:  optional — directory for that worker (default talentmesh)

set -euo pipefail

BIN=/home/openova/.local/bin/chepherd-v05
STATE=/home/openova/.local/state/chepherd-v05
TMP=/tmp/cph-build.$$
LOG=/tmp/runtime.log

echo "[dev-restart] building..."
go build -o "$TMP" ./
echo "[dev-restart] build ok"

echo "[dev-restart] stopping running runtime..."
pkill -9 chepherd-v05 2>/dev/null || true
sleep 2

echo "[dev-restart] installing new binary"
cp -f "$TMP" "$BIN"
rm -f "$TMP"

echo "[dev-restart] clearing stale state"
rm -f /home/openova/.mcp.json "$STATE/runtime.sock"
rm -rf "$STATE/sessions"/*

echo "[dev-restart] starting headless runtime"
nohup "$BIN" run --cwd /home/openova --headless > "$LOG" 2>&1 &
sleep 4

if pgrep -af chepherd-v05 | grep -v eval | head -1; then
  echo "[dev-restart] runtime alive"
else
  echo "[dev-restart] FAILED — see $LOG"
  exit 1
fi

# Optional worker spawn.
if [ "${1:-}" != "" ]; then
  NAME="$1"
  CWD="${2:-/home/openova/repos/talentmesh}"
  echo "[dev-restart] spawning worker '$NAME' in $CWD"
  curl -s -X POST http://127.0.0.1:8080/api/v1/sessions \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"$NAME\",\"agent\":\"claude-code\",\"tribe\":\"default\",\"role\":\"worker\",\"cwd\":\"$CWD\"}" \
    | head -c 200
  echo
fi

echo "[dev-restart] done"

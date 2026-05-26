#!/usr/bin/env bash
# Dev cycle for v0.6 — installs to chepherd-v07 with its own port + state dir
# so v0.5 keeps running undisturbed. v0.5 is the supervised runtime during
# this development.
#
# Usage: ./scripts/dev-restart-v07.sh [spawn_name] [spawn_cwd]

set -euo pipefail

BIN=/home/openova/.local/bin/chepherd-v07
STATE=/home/openova/.local/state/chepherd-v07
TMP=/tmp/cph-v07-build.$$
LOG=/tmp/v07-runtime.log

echo "[v07] building..."
go build -o "$TMP" ./
echo "[v07] build ok"

echo "[v07] stopping previous v0.6 runtime (if any)"
pkill -9 chepherd-v07 2>/dev/null || true
sleep 1

echo "[v07] installing"
cp -f "$TMP" "$BIN"
rm -f "$TMP"

echo "[v07] clearing v07 state"
rm -f "$STATE/runtime.sock"
rm -rf "$STATE/sessions"/* 2>/dev/null || true
mkdir -p "$STATE"

echo "[v07] starting on :8082"
nohup "$BIN" run --cwd /home/openova --headless --listen 127.0.0.1:8082 --state-dir "$STATE" > "$LOG" 2>&1 &
sleep 4

if pgrep -af chepherd-v07 | grep -v eval | head -1; then
  echo "[v07] runtime alive on http://127.0.0.1:8082"
else
  echo "[v07] FAILED — see $LOG"
  exit 1
fi

if [ "${1:-}" != "" ]; then
  NAME="$1"
  CWD="${2:-/home/openova/repos/chepherd}"
  echo "[v07] spawning '$NAME' in $CWD"
  curl -s -X POST http://127.0.0.1:8082/api/v1/sessions \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"$NAME\",\"agent\":\"claude-code\",\"team\":\"default\",\"role\":\"worker\",\"cwd\":\"$CWD\"}" \
    | head -c 200
  echo
fi

echo "[v07] done"

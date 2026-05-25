#!/usr/bin/env bash
# Dev cycle for v0.6 — installs to chepherd-v06 with its own port + state dir
# so v0.5 keeps running undisturbed. v0.5 is the supervised runtime during
# this development.
#
# Usage: ./scripts/dev-restart-v06.sh [spawn_name] [spawn_cwd]

set -euo pipefail

BIN=/home/openova/.local/bin/chepherd-v06
STATE=/home/openova/.local/state/chepherd-v06
TMP=/tmp/cph-v06-build.$$
LOG=/tmp/v06-runtime.log

echo "[v06] building..."
go build -o "$TMP" ./
echo "[v06] build ok"

echo "[v06] stopping previous v0.6 runtime (if any)"
pkill -9 chepherd-v06 2>/dev/null || true
sleep 1

echo "[v06] installing"
cp -f "$TMP" "$BIN"
rm -f "$TMP"

echo "[v06] clearing v06 state"
rm -f "$STATE/runtime.sock"
rm -rf "$STATE/sessions"/* 2>/dev/null || true
mkdir -p "$STATE"

echo "[v06] starting on :8081"
nohup "$BIN" run --cwd /home/openova --headless --listen 127.0.0.1:8081 --state-dir "$STATE" > "$LOG" 2>&1 &
sleep 4

if pgrep -af chepherd-v06 | grep -v eval | head -1; then
  echo "[v06] runtime alive on http://127.0.0.1:8081"
else
  echo "[v06] FAILED — see $LOG"
  exit 1
fi

if [ "${1:-}" != "" ]; then
  NAME="$1"
  CWD="${2:-/home/openova/repos/chepherd}"
  echo "[v06] spawning '$NAME' in $CWD"
  curl -s -X POST http://127.0.0.1:8081/api/v1/sessions \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"$NAME\",\"agent\":\"claude-code\",\"team\":\"default\",\"role\":\"worker\",\"cwd\":\"$CWD\"}" \
    | head -c 200
  echo
fi

echo "[v06] done"

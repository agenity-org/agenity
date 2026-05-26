#!/usr/bin/env bash
# Dev cycle for v0.6 — installs to chepherd-v08 with its own port + state dir
# so v0.5 keeps running undisturbed. v0.5 is the supervised runtime during
# this development.
#
# Usage: ./scripts/dev-restart-v08.sh [spawn_name] [spawn_cwd]

set -euo pipefail

BIN=/home/openova/.local/bin/chepherd-v08
STATE=/home/openova/.local/state/chepherd-v08
TMP=/tmp/cph-v08-build.$$
LOG=/tmp/v08-runtime.log

echo "[v08] building..."
go build -o "$TMP" ./
echo "[v08] build ok"

echo "[v08] stopping previous v0.6 runtime (if any)"
pkill -9 chepherd-v08 2>/dev/null || true
sleep 1

echo "[v08] installing"
cp -f "$TMP" "$BIN"
rm -f "$TMP"

echo "[v08] clearing v08 state"
rm -f "$STATE/runtime.sock"
rm -rf "$STATE/sessions"/* 2>/dev/null || true
mkdir -p "$STATE"

echo "[v08] starting on :8083"
nohup "$BIN" run --cwd /home/openova --headless --listen 127.0.0.1:8083 --state-dir "$STATE" > "$LOG" 2>&1 &
sleep 4

if pgrep -af chepherd-v08 | grep -v eval | head -1; then
  echo "[v08] runtime alive on http://127.0.0.1:8083"
else
  echo "[v08] FAILED — see $LOG"
  exit 1
fi

if [ "${1:-}" != "" ]; then
  NAME="$1"
  CWD="${2:-/home/openova/repos/chepherd}"
  echo "[v08] spawning '$NAME' in $CWD"
  curl -s -X POST http://127.0.0.1:8083/api/v1/sessions \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"$NAME\",\"agent\":\"claude-code\",\"team\":\"default\",\"role\":\"worker\",\"cwd\":\"$CWD\"}" \
    | head -c 200
  echo
fi

echo "[v08] done"

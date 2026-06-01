#!/usr/bin/env bash
# scripts/a2a-conformance/walk-categoryC.sh — v0.9.4 QA Category C
# cells C.1 (R1 boot/register/quiesce) + C.2 (R4 PTY ownership) +
# C.3 (R2 per-session A2A endpoint) + C.4 (R3 per-session Agent Card)
# + C.5 (R5 daemon 410-Gone).
#
# C.6 Playwright dashboard walk is separate (needs npm build +
# browser context).
#
# Spec: docs/V0.9.2-ARCHITECTURE.md §5 #3 + §22 + §23 invariants
# (PTY ownership) + RFC 8594 (Sunset) + RFC 9745 (Deprecation).

set -uo pipefail

ROOT="${ROOT:-/tmp/v094-qa-C}"
BIN_DIR="${ROOT}/bin"
EVIDENCE_DIR="${ROOT}/evidence"
LOG_DIR="${ROOT}/logs"
STATE_D="${ROOT}/state-D"

CHEPHERD="${BIN_DIR}/chepherd"
RUNNER="${BIN_DIR}/chepherd-runner"

mkdir -p "${EVIDENCE_DIR}" "${LOG_DIR}" "${STATE_D}"

free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
wait_for_http() { local url="$1" tries=50; while ((tries-- > 0)); do curl -fsS -o /dev/null --max-time 1 "$url" 2>/dev/null && return 0; sleep 0.2; done; return 1; }
stop_pid() { local nm="$1" p="${LOG_DIR}/${nm}.pid"; [ -f "$p" ] && { kill -TERM "$(cat $p)" 2>/dev/null||true; sleep 0.5; kill -KILL "$(cat $p)" 2>/dev/null||true; rm -f "$p"; }; }
do_cleanup() { stop_pid runner; stop_pid daemon; }
trap do_cleanup EXIT

PORT_D_HTTP=$(free_port)
PORT_D_MCP=$(free_port)
PORT_R_A2A=$(free_port)
PORT_R_MCP=$(free_port)
SID="sid-qa-C-1"
SOCK="${ROOT}/mcp-runner.sock"
RUNNER_STATE="${ROOT}/runner-state"
mkdir -p "${RUNNER_STATE}"
rm -f "${SOCK}"

echo "=== Category C walk ==="
echo "  daemon http: 127.0.0.1:${PORT_D_HTTP}"
echo "  runner A2A:  127.0.0.1:${PORT_R_A2A}"
echo "  runner MCP:  127.0.0.1:${PORT_R_MCP}  (sock: ${SOCK})"
echo "  sid:         ${SID}"

# ─── Boot daemon ────────────────────────────────────────────────
DAEMON_LOG="${LOG_DIR}/daemon.log"
(
  "${CHEPHERD}" run \
    --headless --no-shepherd=true \
    --listen "127.0.0.1:${PORT_D_HTTP}" \
    --mcp-listen "127.0.0.1:${PORT_D_MCP}" \
    --state-dir "${STATE_D}" \
    > "${DAEMON_LOG}" 2>&1 &
  echo $! > "${LOG_DIR}/daemon.pid"
)
wait_for_http "http://127.0.0.1:${PORT_D_HTTP}/healthz" || { tail -50 "$DAEMON_LOG"; exit 1; }
DAEMON_PID=$(cat "${LOG_DIR}/daemon.pid")
echo "  daemon up (pid ${DAEMON_PID})"

# Fetch daemon bearer
DAEMON_BEARER=$(tr -d '\n\r ' < "${STATE_D}/auth.printed")
echo "  daemon bearer: ${DAEMON_BEARER:0:30}..."

# ─── C.5: Pre-boot R5 410-Gone probe (daemon-only, no runner needed) ──
echo
echo "=== C.5 — R5 daemon 410-Gone ==="
# POST /jsonrpc → 410 with RFC 8594/9745 headers
curl -sS -o "${EVIDENCE_DIR}/C5-410-post.body" \
  -D "${EVIDENCE_DIR}/C5-410-post.headers" \
  -w "http=%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"message/send","params":{}}' \
  "http://127.0.0.1:${PORT_D_HTTP}/jsonrpc" \
  > "${EVIDENCE_DIR}/C5-410-post.meta"
echo "POST  /jsonrpc: $(cat ${EVIDENCE_DIR}/C5-410-post.meta)"

# GET /jsonrpc → 410 too
curl -sS -o "${EVIDENCE_DIR}/C5-410-get.body" \
  -D "${EVIDENCE_DIR}/C5-410-get.headers" \
  -w "http=%{http_code}\n" \
  -X GET \
  "http://127.0.0.1:${PORT_D_HTTP}/jsonrpc" \
  > "${EVIDENCE_DIR}/C5-410-get.meta"
echo "GET   /jsonrpc: $(cat ${EVIDENCE_DIR}/C5-410-get.meta)"

# OPTIONS /jsonrpc → pin actual behavior
curl -sS -o "${EVIDENCE_DIR}/C5-410-options.body" \
  -D "${EVIDENCE_DIR}/C5-410-options.headers" \
  -w "http=%{http_code}\n" \
  -X OPTIONS \
  "http://127.0.0.1:${PORT_D_HTTP}/jsonrpc" \
  > "${EVIDENCE_DIR}/C5-410-options.meta"
echo "OPTIONS /jsonrpc: $(cat ${EVIDENCE_DIR}/C5-410-options.meta)"

# Daemon stderr should have the LEGACY A2A audit line
echo "--- daemon stderr LEGACY-A2A lines ---"
grep -i "legacy a2a\|R5\b\|410" "${DAEMON_LOG}" > "${EVIDENCE_DIR}/C5-daemon-legacy.log" || true
cat "${EVIDENCE_DIR}/C5-daemon-legacy.log"

# ─── C.1: Boot runner — register with daemon ──────────────────────
echo
echo "=== C.1 — R1 chepherd-runner boot + register-WS ==="
RUNNER_LOG="${LOG_DIR}/runner.log"
(
  "${RUNNER}" \
    -daemon-url "ws://127.0.0.1:${PORT_D_HTTP}" \
    -auth-token "${DAEMON_BEARER}" \
    -sid "${SID}" \
    -agent "sovereign-shell" \
    -a2a-listen "127.0.0.1:${PORT_R_A2A}" \
    -a2a-base-url "http://127.0.0.1:${PORT_R_A2A}" \
    -mcp-socket "${SOCK}" \
    -mcp-tcp-listen "127.0.0.1:${PORT_R_MCP}" \
    -state-dir "${RUNNER_STATE}" \
    -name "qa-C-runner" \
    > "${RUNNER_LOG}" 2>&1 &
  echo $! > "${LOG_DIR}/runner.pid"
)
sleep 2
RUNNER_PID=$(cat "${LOG_DIR}/runner.pid" 2>/dev/null)
if [ -z "${RUNNER_PID}" ] || ! kill -0 "${RUNNER_PID}" 2>/dev/null; then
  echo "FAIL: runner exited"
  cat "${RUNNER_LOG}"
  exit 1
fi
echo "  runner pid: ${RUNNER_PID}"
echo "--- runner stderr (first 30 lines) ---"
head -30 "${RUNNER_LOG}"
cp "${RUNNER_LOG}" "${EVIDENCE_DIR}/C1-runner.stderr"

# Daemon should show "runner registered" line
echo "--- daemon stderr runner-registered line ---"
grep -i "runner registered\|sid=${SID}" "${DAEMON_LOG}" > "${EVIDENCE_DIR}/C1-daemon-registered.log" || true
cat "${EVIDENCE_DIR}/C1-daemon-registered.log"

# Probe daemon /api/v1/runners — should list our SID
echo "--- daemon /api/v1/runners ---"
curl -sS -o "${EVIDENCE_DIR}/C1-runners-list.json" -w "http=%{http_code}\n" \
  -H "Authorization: Bearer ${DAEMON_BEARER}" \
  "http://127.0.0.1:${PORT_D_HTTP}/api/v1/runners" \
  > "${EVIDENCE_DIR}/C1-runners-list.meta"
echo "  $(cat ${EVIDENCE_DIR}/C1-runners-list.meta)"
echo "  body: $(cat ${EVIDENCE_DIR}/C1-runners-list.json | head -c 400)"

# Probe runner A2A listener up
echo "--- runner A2A listener check ---"
(ss -ltn 2>/dev/null) | grep -E "127.0.0.1:${PORT_R_A2A}\b" > "${EVIDENCE_DIR}/C1-runner-a2a-listener.ss" || true
cat "${EVIDENCE_DIR}/C1-runner-a2a-listener.ss"

# Probe runner MCP TCP listener up
echo "--- runner MCP TCP listener check ---"
(ss -ltn 2>/dev/null) | grep -E "127.0.0.1:${PORT_R_MCP}\b" > "${EVIDENCE_DIR}/C1-runner-mcp-listener.ss" || true
cat "${EVIDENCE_DIR}/C1-runner-mcp-listener.ss"

# Probe runner MCP Unix socket exists
ls -la "${SOCK}" > "${EVIDENCE_DIR}/C1-runner-mcp-sock.ls" 2>&1
echo "  sock: $(cat ${EVIDENCE_DIR}/C1-runner-mcp-sock.ls)"

# ─── C.2: PTY ownership invariant probe (§23) ─────────────────────
echo
echo "=== C.2 — R4 PTY ownership inside runner ==="

# Snapshot runner /proc/<pid>/fd
ls -la "/proc/${RUNNER_PID}/fd/" 2>&1 > "${EVIDENCE_DIR}/C2-runner-fd.ls"
echo "--- runner /proc/${RUNNER_PID}/fd summary ---"
grep -E "pts|ptmx" "${EVIDENCE_DIR}/C2-runner-fd.ls" || echo "  (no pty fd entries found in runner)"
# Also: lsof to surface human-readable
lsof -p "${RUNNER_PID}" 2>/dev/null | grep -E "pts|PTY|TTY|ptmx|/dev/" | head -20 > "${EVIDENCE_DIR}/C2-runner-lsof-pty.log" || true
cat "${EVIDENCE_DIR}/C2-runner-lsof-pty.log"

# Snapshot daemon /proc/<pid>/fd — assert ZERO pty
ls -la "/proc/${DAEMON_PID}/fd/" 2>&1 > "${EVIDENCE_DIR}/C2-daemon-fd.ls"
echo "--- daemon /proc/${DAEMON_PID}/fd PTY summary ---"
DAEMON_PTY_COUNT=$(grep -cE "pts|ptmx" "${EVIDENCE_DIR}/C2-daemon-fd.ls" 2>/dev/null || echo "0")
echo "  daemon PTY fd count: ${DAEMON_PTY_COUNT}"
echo "${DAEMON_PTY_COUNT}" > "${EVIDENCE_DIR}/C2-daemon-pty-count"

lsof -p "${DAEMON_PID}" 2>/dev/null | grep -E "pts|PTY|TTY|ptmx" > "${EVIDENCE_DIR}/C2-daemon-lsof-pty.log" || true
echo "--- daemon lsof PTY entries: $(wc -l < ${EVIDENCE_DIR}/C2-daemon-lsof-pty.log) ---"

# Check process tree — runner should have a child PID for /bin/sh
echo "--- runner child processes (sovereign-shell agent) ---"
pgrep -P "${RUNNER_PID}" 2>/dev/null | while read CPID; do
  CMDLINE=$(tr '\0' ' ' < "/proc/${CPID}/cmdline" 2>/dev/null)
  echo "  child pid=${CPID} cmdline='${CMDLINE}'"
done > "${EVIDENCE_DIR}/C2-runner-children.log"
cat "${EVIDENCE_DIR}/C2-runner-children.log"

# ─── C.3: Per-session A2A endpoint surface probes ────────────────
echo
echo "=== C.3 — R2 per-session A2A endpoint ==="
RUNNER_A2A_URL="http://127.0.0.1:${PORT_R_A2A}"

# Probe a: GET → 405 (POST only)
curl -sS -o "${EVIDENCE_DIR}/C3-get.body" -w "http=%{http_code}\n" \
  "${RUNNER_A2A_URL}/a2a/${SID}/jsonrpc" \
  > "${EVIDENCE_DIR}/C3-get.meta"
echo "  GET /a2a/${SID}/jsonrpc: $(cat ${EVIDENCE_DIR}/C3-get.meta) body=$(cat ${EVIDENCE_DIR}/C3-get.body)"

# Probe b: POST with valid JSON-RPC envelope (use the slash-camelCase form chepherd ships)
curl -sS -o "${EVIDENCE_DIR}/C3-post-tasks-list.body" -w "http=%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":"c3-1","method":"tasks/list","params":{}}' \
  "${RUNNER_A2A_URL}/a2a/${SID}/jsonrpc" \
  > "${EVIDENCE_DIR}/C3-post-tasks-list.meta"
echo "  POST tasks/list: $(cat ${EVIDENCE_DIR}/C3-post-tasks-list.meta)"
echo "    body (first 200): $(head -c 200 ${EVIDENCE_DIR}/C3-post-tasks-list.body)..."

# Probe c: POST to /a2a/sid-doesnt-exist/jsonrpc → 404
curl -sS -o "${EVIDENCE_DIR}/C3-post-bad-sid.body" -w "http=%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":"c3-2","method":"tasks/list","params":{}}' \
  "${RUNNER_A2A_URL}/a2a/sid-doesnt-exist/jsonrpc" \
  > "${EVIDENCE_DIR}/C3-post-bad-sid.meta"
echo "  POST bad-sid: $(cat ${EVIDENCE_DIR}/C3-post-bad-sid.meta) body=$(cat ${EVIDENCE_DIR}/C3-post-bad-sid.body)"

# Re-run worker2's walker against the runner endpoint (RUNNER, not daemon)
echo "--- Re-running walk-categoryA.sh against RUNNER endpoint ---"
mkdir -p "${ROOT}/A1-replay-evidence"
bash scripts/a2a-conformance/walk-categoryA.sh \
  "${RUNNER_A2A_URL}/a2a/${SID}" \
  "" \
  "${ROOT}/A1-replay-evidence" \
  > "${EVIDENCE_DIR}/C3-A1-replay.log" 2>&1 || true
# Note: walk-categoryA.sh expects URL+"/jsonrpc"; the runner endpoint URL is "/a2a/{sid}/jsonrpc"
# So passing "${RUNNER_A2A_URL}/a2a/${SID}" + then it appends "/jsonrpc" → "/a2a/{sid}/jsonrpc"
tail -10 "${EVIDENCE_DIR}/C3-A1-replay.log"
ls "${ROOT}/A1-replay-evidence/" | head -5 > "${EVIDENCE_DIR}/C3-A1-replay-files.txt" 2>&1

# ─── C.4: Per-session Agent Card ──────────────────────────────────
echo
echo "=== C.4 — R3 per-session Agent Card ==="
curl -sS -o "${EVIDENCE_DIR}/C4-card.json" -w "http=%{http_code}\n" \
  "${RUNNER_A2A_URL}/a2a/${SID}/.well-known/agent-card.json" \
  > "${EVIDENCE_DIR}/C4-card.meta"
echo "  GET well-known card: $(cat ${EVIDENCE_DIR}/C4-card.meta)"
echo "  body (first 400): $(head -c 400 ${EVIDENCE_DIR}/C4-card.json)..."

# Bad sid
curl -sS -o "${EVIDENCE_DIR}/C4-card-404.body" -w "http=%{http_code}\n" \
  "${RUNNER_A2A_URL}/a2a/sid-doesnt-exist/.well-known/agent-card.json" \
  > "${EVIDENCE_DIR}/C4-card-404.meta"
echo "  GET bad-sid card: $(cat ${EVIDENCE_DIR}/C4-card-404.meta) body=$(cat ${EVIDENCE_DIR}/C4-card-404.body)"

# JWKS at the per-session endpoint?
curl -sS -o "${EVIDENCE_DIR}/C4-jwks.body" -w "http=%{http_code}\n" \
  "${RUNNER_A2A_URL}/a2a/${SID}/.well-known/jwks.json" \
  > "${EVIDENCE_DIR}/C4-jwks.meta"
echo "  GET jwks (per-session): $(cat ${EVIDENCE_DIR}/C4-jwks.meta)"

# ─── C.1 quiesce probe ────────────────────────────────────────────
echo
echo "=== C.1 (cont) — SIGTERM quiesce probe ==="
START=$(date +%s%N)
kill -TERM "${RUNNER_PID}"
echo "  sent SIGTERM at $(date -u +%FT%T.%NZ)"
# Wait up to 3s soft, 5s hard
for i in 1 2 3 4 5 6 7 8 9 10; do
  if ! kill -0 "${RUNNER_PID}" 2>/dev/null; then break; fi
  sleep 0.5
done
END=$(date +%s%N)
ELAPSED_MS=$(( (END - START) / 1000000 ))
if kill -0 "${RUNNER_PID}" 2>/dev/null; then
  echo "  runner DID NOT quiesce within 5s — sending SIGKILL"
  kill -KILL "${RUNNER_PID}" 2>/dev/null || true
  echo "FAIL ${ELAPSED_MS}ms" > "${EVIDENCE_DIR}/C1-quiesce.verdict"
else
  echo "  runner quiesced in ${ELAPSED_MS}ms"
  echo "PASS ${ELAPSED_MS}ms" > "${EVIDENCE_DIR}/C1-quiesce.verdict"
fi
rm -f "${LOG_DIR}/runner.pid"

# After quiesce: confirm A2A listener closed, MCP socket cleaned
echo "--- post-quiesce: A2A listener ---"
(ss -ltn 2>/dev/null) | grep -E "127.0.0.1:${PORT_R_A2A}\b" > "${EVIDENCE_DIR}/C1-postquiesce-a2a.ss" || true
[ -s "${EVIDENCE_DIR}/C1-postquiesce-a2a.ss" ] && echo "  ✗ A2A listener still bound" || echo "  ✓ A2A listener closed"

echo "--- post-quiesce: MCP socket ---"
ls -la "${SOCK}" 2>&1 > "${EVIDENCE_DIR}/C1-postquiesce-sock.ls"
[ -e "${SOCK}" ] && echo "  ✗ MCP socket NOT cleaned" || echo "  ✓ MCP socket cleaned"

# Daemon should have "runner disconnected" line
echo "--- daemon stderr post-quiesce ---"
grep -iE "disconnect|runner.*${SID}" "${DAEMON_LOG}" > "${EVIDENCE_DIR}/C1-daemon-quiesce.log" || true
cat "${EVIDENCE_DIR}/C1-daemon-quiesce.log"

# Final daemon stderr capture
cp "${DAEMON_LOG}" "${EVIDENCE_DIR}/C-daemon.stderr"

echo
echo "=== Category C walk (C.1-C.5) complete ==="
ls -la "${EVIDENCE_DIR}/C"* 2>&1 | wc -l
echo "evidence files in ${EVIDENCE_DIR}"

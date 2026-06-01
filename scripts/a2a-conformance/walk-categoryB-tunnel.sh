#!/usr/bin/env bash
# scripts/a2a-conformance/walk-categoryB-tunnel.sh — v0.9.4 QA
# Category B cell B.5 — F7 reverse-proxy tunnel walk.
#
# Boots a clean chepherd-hub binary, drives the Go tunnel walker
# (stub bob runner + alice caller + body-blind SHA round-trip +
# disconnect probe), captures all artifacts.
#
# Spec: docs/V0.9.2-ARCHITECTURE.md §10 Pattern 2 fallback (lines
# 715-732) + §23 invariants (body-blind).

set -uo pipefail

ROOT="${ROOT:-/tmp/v094-qa-B}"
BIN_DIR="${ROOT}/bin"
EVIDENCE_DIR="${ROOT}/evidence"
LOG_DIR="${ROOT}/logs"
HUB="${BIN_DIR}/chepherd-hub"
WALKER="${BIN_DIR}/categoryB-tunnel-walker"

mkdir -p "${EVIDENCE_DIR}" "${LOG_DIR}"

free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
wait_for_http() { local url="$1" tries=50; while ((tries-- > 0)); do curl -fsS -o /dev/null --max-time 1 "$url" 2>/dev/null && return 0; sleep 0.2; done; return 1; }
stop_pid() { local n="$1" p="${LOG_DIR}/${n}.pid"; [ -f "$p" ] && { kill -TERM "$(cat $p)" 2>/dev/null||true; sleep 0.5; kill -KILL "$(cat $p)" 2>/dev/null||true; rm -f "$p"; }; }
do_cleanup() { stop_pid hub-tunnel; }
trap do_cleanup EXIT

PORT_HUB=$(free_port)
echo "=== B.5 — F7 reverse-proxy tunnel walk ==="
echo "  hub: 127.0.0.1:${PORT_HUB}"

HUB_LOG="${LOG_DIR}/hub-tunnel.log"
(
  "${HUB}" \
    -listen "127.0.0.1:${PORT_HUB}" \
    -stun-listen "" -turn-listen "" \
    -allowed-orgs "alice.example,bob.example" \
    > "${HUB_LOG}" 2>&1 &
  echo $! > "${LOG_DIR}/hub-tunnel.pid"
)
wait_for_http "http://127.0.0.1:${PORT_HUB}/healthz" || { tail -80 "$HUB_LOG"; exit 1; }
echo "  hub up (pid $(cat ${LOG_DIR}/hub-tunnel.pid))"

# Drive the Go walker
"${WALKER}" \
  --hub-url "http://127.0.0.1:${PORT_HUB}" \
  --ws-url  "ws://127.0.0.1:${PORT_HUB}" \
  --out-dir "${EVIDENCE_DIR}" 2>&1 | tee "${EVIDENCE_DIR}/B5-walker.out"

# Cross-cut: hub stderr inspection for payload leak
echo
echo "=== Body-blind cross-cut: hub stderr ==="
cp "${HUB_LOG}" "${EVIDENCE_DIR}/B5-hub.stderr"

# Searching for the random payload in stderr
PAYLOAD_HEX=$(xxd -ps -c 1000 "${EVIDENCE_DIR}/B5-payload-in.bin" 2>/dev/null | head -c 40)
echo "  payload hex prefix (40 chars): ${PAYLOAD_HEX}"
# Hub stderr is text-based; the payload is binary → unlikely to match by accident
if grep -q "${PAYLOAD_HEX}" "${HUB_LOG}" 2>/dev/null; then
  echo "  LEAK: payload prefix in hub stderr"
  echo "leak detected: ${PAYLOAD_HEX}" > "${EVIDENCE_DIR}/B5-leak.log"
else
  echo "  PASS: payload bytes NOT in hub stderr"
  echo "no-leak (probed hex prefix ${PAYLOAD_HEX})" > "${EVIDENCE_DIR}/B5-leak.log"
fi

echo
echo "--- hub stderr (relay-related lines) ---"
grep -iE "relay|tunnel|register|deregister" "${HUB_LOG}" | head -30

echo
echo "=== B.5 walk complete. Evidence: ${EVIDENCE_DIR} ==="
ls -la "${EVIDENCE_DIR}/B5"* 2>&1 | head -20

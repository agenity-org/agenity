#!/usr/bin/env bash
# scripts/a2a-conformance/walk-categoryB-turn.sh — v0.9.4 QA Category B,
# cell B.4 — F6 TURN REST creds + pion/turn/v5 Client.Allocate against
# the real chepherd-hub TURN listener.
#
# Spec source: docs/V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 4 +
# §23 invariants (metadata-only audit) + chepherd-lead cross-cut
# (turn-secret must NEVER appear in hub stderr).

set -uo pipefail

ROOT="${ROOT:-/tmp/v094-qa-B}"
BIN_DIR="${ROOT}/bin"
EVIDENCE_DIR="${ROOT}/evidence"
LOG_DIR="${ROOT}/logs"

HUB="${BIN_DIR}/chepherd-hub"
TURN_WALKER="${BIN_DIR}/categoryB-turn-walker"

mkdir -p "${EVIDENCE_DIR}" "${LOG_DIR}"

free_port_tcp() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
free_port_udp() { python3 -c 'import socket;s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
wait_for_http() { local url="$1" tries=50; while ((tries-- > 0)); do curl -fsS -o /dev/null --max-time 1 "$url" 2>/dev/null && return 0; sleep 0.2; done; return 1; }
stop_pid() { local lbl="$1" p="${LOG_DIR}/${lbl}.pid"; [ -f "$p" ] && { kill -TERM "$(cat $p)" 2>/dev/null||true; sleep 0.5; kill -KILL "$(cat $p)" 2>/dev/null||true; rm -f "$p"; }; }
cleanup() { stop_pid hub-turn; }
trap cleanup EXIT

PORT_HTTP=$(free_port_tcp)
PORT_TURN_UDP=$(free_port_udp)
# Use a deterministic, distinctive secret so we can grep stderr for it.
TURN_SECRET="qa-b4-secret-DO-NOT-LOG-THIS-VALUE-1234567890"
echo "=== B.4 — F6 TURN walk ==="
echo "  hub http: 127.0.0.1:${PORT_HTTP}"
echo "  hub turn UDP: 127.0.0.1:${PORT_TURN_UDP}"
echo "  turn-secret: <DISTINCT-MARKER>"

HUB_LOG="${LOG_DIR}/hub-turn.log"
(
  CHEPHERD_HUB_TURN_SECRET="${TURN_SECRET}" "${HUB}" \
    -listen "127.0.0.1:${PORT_HTTP}" \
    -stun-listen "" \
    -turn-listen "127.0.0.1:${PORT_TURN_UDP}" \
    -turn-realm "chepherd-hub" \
    -turn-relay-ip "127.0.0.1" \
    -turn-public-host "127.0.0.1:${PORT_TURN_UDP}" \
    -allowed-orgs "alice.example,bob.example" \
    > "${HUB_LOG}" 2>&1 &
  echo $! > "${LOG_DIR}/hub-turn.pid"
)
wait_for_http "http://127.0.0.1:${PORT_HTTP}/healthz" || { tail -80 "$HUB_LOG"; exit 1; }
echo "  hub up (pid $(cat ${LOG_DIR}/hub-turn.pid))"

# Drive the Go walker
echo "--- Running pion/turn/v5 walker ---"
"${TURN_WALKER}" \
  --hub-url "http://127.0.0.1:${PORT_HTTP}" \
  --turn-udp "127.0.0.1:${PORT_TURN_UDP}" \
  --as-org "alice.example" \
  --out-dir "${EVIDENCE_DIR}" 2>&1 | tee "${EVIDENCE_DIR}/B4-walker.out"

# Cross-cut probes
echo
echo "--- Cross-cut: NEG mint creds (no auth) ---"
curl -sS -o "${EVIDENCE_DIR}/B4-mint-noauth.body" -w "http=%{http_code}\n" \
  -X POST "http://127.0.0.1:${PORT_HTTP}/v1/turn/credentials" \
  > "${EVIDENCE_DIR}/B4-mint-noauth.meta"
echo "  $(cat ${EVIDENCE_DIR}/B4-mint-noauth.meta) body=$(cat ${EVIDENCE_DIR}/B4-mint-noauth.body)"

echo "--- Cross-cut: NEG mint creds (non-allowlisted org) ---"
curl -sS -o "${EVIDENCE_DIR}/B4-mint-carol.body" -w "http=%{http_code}\n" \
  -X POST -H "X-Chepherd-Org: carol.example" \
  "http://127.0.0.1:${PORT_HTTP}/v1/turn/credentials" \
  > "${EVIDENCE_DIR}/B4-mint-carol.meta"
echo "  $(cat ${EVIDENCE_DIR}/B4-mint-carol.meta) body=$(cat ${EVIDENCE_DIR}/B4-mint-carol.body)"

# ─── Secret-bleed probe (chepherd-lead cross-cut) ───────────────────
echo
echo "=== SECRET-BLEED PROBE: turn-secret must NOT appear in hub stderr ==="
cp "${HUB_LOG}" "${EVIDENCE_DIR}/B4-hub.stderr"
if grep -F -q "${TURN_SECRET}" "${HUB_LOG}"; then
  echo "  P0 SECRET LEAK: --turn-secret value appears in hub stderr!"
  grep -F "${TURN_SECRET}" "${HUB_LOG}" > "${EVIDENCE_DIR}/B4-secret-bleed.log"
else
  echo "  PASS: turn-secret NOT in hub stderr"
  echo "no-leak (probed marker: '${TURN_SECRET}')" > "${EVIDENCE_DIR}/B4-secret-bleed.log"
fi

# ─── OnAllocation* metadata invariant (§23) ────────────────────────
echo "--- OnAllocation* lines from hub stderr ---"
grep -E "turn alloc|turn auth|turn channel" "${HUB_LOG}" > "${EVIDENCE_DIR}/B4-allocation-lines.log" || true
cat "${EVIDENCE_DIR}/B4-allocation-lines.log"

echo
echo "--- healthz before / during / after ---"
echo "BEFORE (active_allocations):"
jq -c '.turn // {}' "${EVIDENCE_DIR}/B4-healthz.before.json"
echo "DURING:"
jq -c '.turn // {}' "${EVIDENCE_DIR}/B4-healthz.during.json"
echo "AFTER:"
jq -c '.turn // {}' "${EVIDENCE_DIR}/B4-healthz.after.json"

echo
echo "=== B.4 walk complete. Evidence: ${EVIDENCE_DIR} ==="
ls -la "${EVIDENCE_DIR}/B4"* 2>&1 | head -25

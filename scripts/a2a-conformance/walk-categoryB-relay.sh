#!/usr/bin/env bash
# scripts/a2a-conformance/walk-categoryB-relay.sh — v0.9.4 QA Category B,
# cell B.3 (F5 SDP signaling body-blind) + B.5 (F7 reverse-proxy tunnel).
#
# Topology: 1 chepherd-hub binary (no daemons needed for these cells —
# they're hub-only routes). Boots fresh hub, drives the relay flows,
# captures body-blind evidence + spoof-deny + allowlist-deny probes.
#
# Spec source: docs/V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 5
# + §23 Invariants (metadata-only summaries).

set -uo pipefail

ROOT="${ROOT:-/tmp/v094-qa-B}"
BIN_DIR="${ROOT}/bin"
EVIDENCE_DIR="${ROOT}/evidence"
LOG_DIR="${ROOT}/logs"

HUB="${BIN_DIR}/chepherd-hub"

mkdir -p "${EVIDENCE_DIR}" "${LOG_DIR}"

free_port() {
  python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'
}

wait_for_http() {
  local url="$1" tries=50
  while ((tries-- > 0)); do
    if curl -fsS -o /dev/null --max-time 1 "$url" 2>/dev/null; then return 0; fi
    sleep 0.2
  done
  return 1
}

stop_pid() {
  local label="$1"
  local pidfile="${LOG_DIR}/${label}.pid"
  if [ -f "$pidfile" ]; then
    local pid; pid=$(cat "$pidfile")
    kill -TERM "$pid" 2>/dev/null || true
    for _ in 1 2 3 4 5; do kill -0 "$pid" 2>/dev/null || { rm -f "$pidfile"; return 0; }; sleep 0.2; done
    kill -KILL "$pid" 2>/dev/null || true; rm -f "$pidfile"
  fi
}

cleanup_all() { stop_pid hub-relay; }
trap cleanup_all EXIT

PORT_HUB=$(free_port)

echo "=== Boot chepherd-hub for B.3 (signaling) + B.5 (tunnel) ==="
HUB_LOG="${LOG_DIR}/hub-relay.log"
(
  "${HUB}" \
    -listen "127.0.0.1:${PORT_HUB}" \
    -stun-listen "" -turn-listen "" \
    -allowed-orgs "alice.example,bob.example" \
    > "${HUB_LOG}" 2>&1 &
  echo $! > "${LOG_DIR}/hub-relay.pid"
)
wait_for_http "http://127.0.0.1:${PORT_HUB}/healthz" || { tail -50 "$HUB_LOG"; exit 1; }
echo "  hub up: 127.0.0.1:${PORT_HUB}"

HUB_URL="http://127.0.0.1:${PORT_HUB}"

# ─── B.3 — F5 SDP signaling body-blind ──────────────────────────────
echo
echo "=== B.3 — F5 SDP signaling body-blind ==="

# Generate 1 KB random "SDP" payload (NOT real SDP; we want to test that
# the hub forwards bytes verbatim regardless of content).
PAYLOAD_IN="${EVIDENCE_DIR}/B3-payload-in.bin"
PAYLOAD_IN_B64="${EVIDENCE_DIR}/B3-payload-in.b64"
head -c 1024 /dev/urandom > "${PAYLOAD_IN}"
base64 -w 0 "${PAYLOAD_IN}" > "${PAYLOAD_IN_B64}"
SHA_IN=$(sha256sum "${PAYLOAD_IN}" | cut -d' ' -f1)
echo "  payload IN sha256: ${SHA_IN}"

SESSION_ID="qa-B3-$(date +%s%N)"
echo "  sessionId: ${SESSION_ID}"

# PROBE 1 — alice posts offer to bob
echo "--- B.3 PROBE 1: alice POSTs offer to bob (1 KB random payload) ---"
OFFER_REQ=$(jq -nc --arg sid "${SESSION_ID}" --arg payload "$(cat ${PAYLOAD_IN_B64})" \
  '{fromOrgId:"alice.example",toOrgId:"bob.example",sessionId:$sid,payload:{opaque:$payload}}')
echo "${OFFER_REQ}" > "${EVIDENCE_DIR}/B3-offer.req.json"
curl -sS -X POST -o "${EVIDENCE_DIR}/B3-offer.resp.json" -w "http=%{http_code}\n" \
  -H "Content-Type: application/json" \
  -H "X-Chepherd-Org: alice.example" \
  -d @"${EVIDENCE_DIR}/B3-offer.req.json" \
  "${HUB_URL}/v1/signaling/offer" \
  > "${EVIDENCE_DIR}/B3-offer.meta"
echo "  $(cat ${EVIDENCE_DIR}/B3-offer.meta) body=$(cat ${EVIDENCE_DIR}/B3-offer.resp.json)"

# PROBE 2 — bob fetches pending frames
echo "--- B.3 PROBE 2: bob fetches pending ---"
curl -sS -X GET -o "${EVIDENCE_DIR}/B3-pending.resp.json" -w "http=%{http_code}\n" \
  -H "X-Chepherd-Org: bob.example" \
  "${HUB_URL}/v1/signaling/pending?orgId=bob.example" \
  > "${EVIDENCE_DIR}/B3-pending.meta"
echo "  $(cat ${EVIDENCE_DIR}/B3-pending.meta)"
echo "  pending body (first 200 chars): $(head -c 200 ${EVIDENCE_DIR}/B3-pending.resp.json)..."

# Extract the payload.opaque field + sha256 compare
PAYLOAD_OUT_B64=$(jq -r '.frames[0].payload.opaque // empty' "${EVIDENCE_DIR}/B3-pending.resp.json")
echo "${PAYLOAD_OUT_B64}" | base64 -d > "${EVIDENCE_DIR}/B3-payload-out.bin" 2>/dev/null
SHA_OUT=$(sha256sum "${EVIDENCE_DIR}/B3-payload-out.bin" | cut -d' ' -f1)
echo "  payload OUT sha256: ${SHA_OUT}"

if [ "$SHA_IN" = "$SHA_OUT" ]; then
  echo "  body-blind SHA-256 MATCHES → bytes round-tripped verbatim"
  echo "MATCH ${SHA_IN}" > "${EVIDENCE_DIR}/B3-body-blind.verdict"
else
  echo "  body-blind SHA-256 MISMATCH — hub mutated the payload!"
  echo "MISMATCH in=${SHA_IN} out=${SHA_OUT}" > "${EVIDENCE_DIR}/B3-body-blind.verdict"
fi

# PROBE 3 — Bob answers back to alice (different payload)
echo "--- B.3 PROBE 3: bob POSTs answer to alice (reverse direction) ---"
ANS_PAYLOAD=$(head -c 512 /dev/urandom | base64 -w 0)
ANS_SHA=$(echo -n "${ANS_PAYLOAD}" | base64 -d | sha256sum | cut -d' ' -f1)
ANS_REQ=$(jq -nc --arg sid "${SESSION_ID}" --arg payload "${ANS_PAYLOAD}" \
  '{fromOrgId:"bob.example",toOrgId:"alice.example",sessionId:$sid,payload:{opaque:$payload}}')
echo "${ANS_REQ}" > "${EVIDENCE_DIR}/B3-answer.req.json"
curl -sS -X POST -o "${EVIDENCE_DIR}/B3-answer.resp.json" -w "http=%{http_code}\n" \
  -H "Content-Type: application/json" \
  -H "X-Chepherd-Org: bob.example" \
  -d @"${EVIDENCE_DIR}/B3-answer.req.json" \
  "${HUB_URL}/v1/signaling/answer" \
  > "${EVIDENCE_DIR}/B3-answer.meta"
echo "  $(cat ${EVIDENCE_DIR}/B3-answer.meta)"

# Alice fetches answer
curl -sS -X GET -o "${EVIDENCE_DIR}/B3-pending-alice.resp.json" -w "http=%{http_code}\n" \
  -H "X-Chepherd-Org: alice.example" \
  "${HUB_URL}/v1/signaling/pending?orgId=alice.example" \
  > "${EVIDENCE_DIR}/B3-pending-alice.meta"
ANS_OUT_PAYLOAD=$(jq -r '.frames[0].payload.opaque // empty' "${EVIDENCE_DIR}/B3-pending-alice.resp.json")
ANS_OUT_SHA=$(echo -n "${ANS_OUT_PAYLOAD}" | base64 -d 2>/dev/null | sha256sum | cut -d' ' -f1)
echo "  answer body-blind: in=${ANS_SHA} out=${ANS_OUT_SHA}"
[ "$ANS_SHA" = "$ANS_OUT_SHA" ] && echo "  PASS reverse-direction body-blind"

# PROBE 4 — ICE candidate
echo "--- B.3 PROBE 4: ICE candidate exchange ---"
ICE_REQ=$(jq -nc --arg sid "${SESSION_ID}" '{fromOrgId:"alice.example",toOrgId:"bob.example",sessionId:$sid,payload:{candidate:"candidate:842163049 1 udp 1677729535 192.0.2.1 50000 typ srflx raddr 10.0.0.1 rport 50000"}}')
curl -sS -X POST -o "${EVIDENCE_DIR}/B3-ice.resp.json" -w "http=%{http_code}\n" \
  -H "Content-Type: application/json" \
  -H "X-Chepherd-Org: alice.example" \
  -d "${ICE_REQ}" \
  "${HUB_URL}/v1/signaling/ice" \
  > "${EVIDENCE_DIR}/B3-ice.meta"
echo "  $(cat ${EVIDENCE_DIR}/B3-ice.meta) body=$(cat ${EVIDENCE_DIR}/B3-ice.resp.json)"

# PROBE 5 — spoof fromOrgId
echo "--- B.3 PROBE 5: spoof fromOrgId (alice claims to be bob) ---"
SPOOF_REQ=$(jq -nc '{fromOrgId:"bob.example",toOrgId:"alice.example",sessionId:"spoof",payload:{opaque:"x"}}')
curl -sS -X POST -o "${EVIDENCE_DIR}/B3-spoof.resp.json" -w "http=%{http_code}\n" \
  -H "Content-Type: application/json" \
  -H "X-Chepherd-Org: alice.example" \
  -d "${SPOOF_REQ}" \
  "${HUB_URL}/v1/signaling/offer" \
  > "${EVIDENCE_DIR}/B3-spoof.meta"
echo "  $(cat ${EVIDENCE_DIR}/B3-spoof.meta) body=$(cat ${EVIDENCE_DIR}/B3-spoof.resp.json)"

# PROBE 6 — orgId-mailbox-snoop (alice tries to read bob's mailbox)
echo "--- B.3 PROBE 6: orgId-mismatch on pending (snoop attempt) ---"
curl -sS -X GET -o "${EVIDENCE_DIR}/B3-snoop.resp.json" -w "http=%{http_code}\n" \
  -H "X-Chepherd-Org: alice.example" \
  "${HUB_URL}/v1/signaling/pending?orgId=bob.example" \
  > "${EVIDENCE_DIR}/B3-snoop.meta"
echo "  $(cat ${EVIDENCE_DIR}/B3-snoop.meta) body=$(cat ${EVIDENCE_DIR}/B3-snoop.resp.json)"

# PROBE 7 — non-allowlisted caller
echo "--- B.3 PROBE 7: non-allowlisted caller (carol.example) ---"
CAROL_REQ=$(jq -nc '{fromOrgId:"carol.example",toOrgId:"alice.example",sessionId:"x",payload:{opaque:"x"}}')
curl -sS -X POST -o "${EVIDENCE_DIR}/B3-carol.resp.json" -w "http=%{http_code}\n" \
  -H "Content-Type: application/json" \
  -H "X-Chepherd-Org: carol.example" \
  -d "${CAROL_REQ}" \
  "${HUB_URL}/v1/signaling/offer" \
  > "${EVIDENCE_DIR}/B3-carol.meta"
echo "  $(cat ${EVIDENCE_DIR}/B3-carol.meta) body=$(cat ${EVIDENCE_DIR}/B3-carol.resp.json)"

# BODY-BLIND CROSS-CUT: hub stderr must NOT contain the payload bytes.
echo
echo "=== Body-blind invariant probe: hub stderr inspection ==="
cp "${HUB_LOG}" "${EVIDENCE_DIR}/B3-hub.stderr"
# Search for our random 32-char ascii prefix (taking a slice of the base64
# payload so the search string is greppable but distinctive)
PAYLOAD_PREFIX=$(head -c 64 "${PAYLOAD_IN_B64}")
if grep -F -q "${PAYLOAD_PREFIX}" "${HUB_LOG}"; then
  echo "  LEAK DETECTED: payload prefix found in hub stderr"
  grep -F "${PAYLOAD_PREFIX}" "${HUB_LOG}" > "${EVIDENCE_DIR}/B3-hub-leak.log"
else
  echo "  PASS body-blind: hub stderr does NOT contain payload bytes"
  echo "no-leak (searched prefix: '${PAYLOAD_PREFIX:0:40}…')" > "${EVIDENCE_DIR}/B3-hub-leak.log"
fi

echo "--- hub stderr (all signaling-related lines) ---"
grep -iE "signaling|frame|offer|answer|ice|enqueue|TURN|relay|federation" "${HUB_LOG}" > "${EVIDENCE_DIR}/B3-hub-signaling-lines.log" || true
head -20 "${EVIDENCE_DIR}/B3-hub-signaling-lines.log"

echo
echo "=== B.3 walk complete. Evidence: ${EVIDENCE_DIR} ==="
ls -la "${EVIDENCE_DIR}/B3"* 2>&1 | head -20

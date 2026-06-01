#!/usr/bin/env bash
# scripts/a2a-conformance/walk-categoryB.sh — v0.9.4 QA Category B walker.
#
# Drives the cross-org federation surface against TWO real chepherd
# daemons + (later cells) ONE chepherd-hub binary on loopback.
#
#   B.1   — T3 mTLS handshake success + 2 negative probes
#   B.1.1 — T3.1 listener wiring lifecycle (flag-gated)
#   B.2…  — JWT mint / SDP relay / TURN / tunnel / WebRTC (later phases)
#
# Spec source: docs/V0.9.2-ARCHITECTURE.md §10 Pattern 2 + §15 + §22 + §23
# Plan:        docs/v094-qa/categoryB-plan.md
#
# Evidence lands under ${EVIDENCE_DIR} (default /tmp/v094-qa-B/evidence).
# Daemon logs under ${LOG_DIR}            (default /tmp/v094-qa-B/logs).

set -uo pipefail

ROOT="${ROOT:-/tmp/v094-qa-B}"
BIN_DIR="${ROOT}/bin"
EVIDENCE_DIR="${ROOT}/evidence"
LOG_DIR="${ROOT}/logs"
CERT_DIR="${ROOT}/certs"
STATE_X="${ROOT}/state-X"
STATE_Y="${ROOT}/state-Y"

CHEPHERD="${BIN_DIR}/chepherd"
SETUP="${BIN_DIR}/categoryB-mtls-setup"

mkdir -p "${EVIDENCE_DIR}" "${LOG_DIR}" "${CERT_DIR}" "${STATE_X}" "${STATE_Y}"

# ── helpers ────────────────────────────────────────────────────────
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

start_daemon() {
  local label="$1" state="$2" org="$3" http_addr="$4" mcp_addr="$5" fed_addr="$6"
  local log="${LOG_DIR}/${label}.log"
  (
    "${CHEPHERD}" run \
      --headless \
      --no-shepherd=true \
      --listen "${http_addr}" \
      --mcp-listen "${mcp_addr}" \
      --state-dir "${state}" \
      --federation-mtls=true \
      --federation-org-id "${org}" \
      --federation-listen "${fed_addr}" \
      --federation-registry-url "http://example.invalid/registry" \
      > "${log}" 2>&1 &
    echo $! > "${LOG_DIR}/${label}.pid"
  )
  if ! wait_for_http "http://${http_addr}/healthz"; then
    echo "FAIL: daemon ${label} healthz never came up"
    tail -50 "${log}"
    return 1
  fi
  echo "  daemon ${label} up: http=${http_addr} mcp=${mcp_addr} fed=${fed_addr} (pid $(cat ${LOG_DIR}/${label}.pid))"
}

stop_daemon() {
  local label="$1"
  local pidfile="${LOG_DIR}/${label}.pid"
  if [ -f "$pidfile" ]; then
    local pid; pid=$(cat "$pidfile")
    kill -TERM "$pid" 2>/dev/null || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      kill -0 "$pid" 2>/dev/null || { rm -f "$pidfile"; return 0; }
      sleep 0.2
    done
    kill -KILL "$pid" 2>/dev/null || true
    rm -f "$pidfile"
  fi
}

cleanup_all() {
  stop_daemon daemon-X
  stop_daemon daemon-Y
}
trap cleanup_all EXIT

# ── B.1.1: listener wiring lifecycle (boot + lsof + kill) ──────────
echo "=== B.1.1 — T3.1 listener wiring lifecycle ==="
PORT_X_HTTP=$(free_port); PORT_X_MCP=$(free_port); PORT_X_FED=$(free_port)
PORT_Y_HTTP=$(free_port); PORT_Y_MCP=$(free_port); PORT_Y_FED=$(free_port)

# Probe a — boot WITHOUT --federation-mtls — fed listener MUST be absent.
echo "--- B.1.1.a — boot WITHOUT --federation-mtls (listener absent) ---"
NOMTLS_PORT_HTTP=$(free_port); NOMTLS_PORT_MCP=$(free_port); NOMTLS_PORT_FED=$(free_port)
NOMTLS_STATE="${ROOT}/state-nomtls"; mkdir -p "${NOMTLS_STATE}"
(
  "${CHEPHERD}" run --headless --no-shepherd=true \
    --listen "127.0.0.1:${NOMTLS_PORT_HTTP}" \
    --mcp-listen "127.0.0.1:${NOMTLS_PORT_MCP}" \
    --state-dir "${NOMTLS_STATE}" \
    --federation-listen "127.0.0.1:${NOMTLS_PORT_FED}" \
    > "${LOG_DIR}/nomtls.log" 2>&1 &
  echo $! > "${LOG_DIR}/nomtls.pid"
)
sleep 1
wait_for_http "http://127.0.0.1:${NOMTLS_PORT_HTTP}/healthz" || true
# When --federation-mtls=false (default) the federation listener does not bind.
if (ss -ltn 2>/dev/null || netstat -ltn 2>/dev/null) | grep -E "127.0.0.1:${NOMTLS_PORT_FED}\b" > "${EVIDENCE_DIR}/B1.1.a-nomtls-listener.ss"; then
  echo "  PROBE-A UNEXPECTED: federation listener bound without --federation-mtls"
  ss_RESULT="UNEXPECTED-BOUND"
else
  echo "  PROBE-A as-expected: federation listener NOT bound (default-off gating)"
  ss_RESULT="not-bound"
fi
echo "$ss_RESULT" > "${EVIDENCE_DIR}/B1.1.a-nomtls-listener.verdict"
kill -TERM "$(cat ${LOG_DIR}/nomtls.pid)" 2>/dev/null || true; sleep 1; rm -f "${LOG_DIR}/nomtls.pid"

# Probe b — boot WITH --federation-mtls — fed listener MUST be present.
echo "--- B.1.1.b — boot WITH --federation-mtls=true (listener present) ---"
start_daemon daemon-X "${STATE_X}" "alice.example" "127.0.0.1:${PORT_X_HTTP}" "127.0.0.1:${PORT_X_MCP}" "127.0.0.1:${PORT_X_FED}" || true
start_daemon daemon-Y "${STATE_Y}" "bob.example"   "127.0.0.1:${PORT_Y_HTTP}" "127.0.0.1:${PORT_Y_MCP}" "127.0.0.1:${PORT_Y_FED}" || true

sleep 1
(ss -ltn 2>/dev/null || netstat -ltn 2>/dev/null) | grep -E "127.0.0.1:(${PORT_X_FED}|${PORT_Y_FED})\b" > "${EVIDENCE_DIR}/B1.1.b-mtls-listeners.ss" || true
echo "  fed-listener snapshot:"; cat "${EVIDENCE_DIR}/B1.1.b-mtls-listeners.ss"

# ── B.1: T3 cross-pinned mTLS handshake ─────────────────────────────
echo "=== B.1 — T3 cross-pinned mTLS handshake ==="
echo "--- Cross-pin via categoryB-mtls-setup helper ---"
"${SETUP}" \
  --a-state-dir "${STATE_X}" --a-org-id "alice.example" \
  --b-state-dir "${STATE_Y}" --b-org-id "bob.example"   \
  --out-dir "${CERT_DIR}" > "${EVIDENCE_DIR}/B1-cert-setup.log" 2>&1
echo "  cert artifacts:"; ls -la "${CERT_DIR}"

# Restart daemons so the pinned-CA pool reloads into MTLSConfig.
echo "--- Restart daemons to pick up pinned-CAs ---"
stop_daemon daemon-X
stop_daemon daemon-Y
sleep 1
start_daemon daemon-X "${STATE_X}" "alice.example" "127.0.0.1:${PORT_X_HTTP}" "127.0.0.1:${PORT_X_MCP}" "127.0.0.1:${PORT_X_FED}"
start_daemon daemon-Y "${STATE_Y}" "bob.example"   "127.0.0.1:${PORT_Y_HTTP}" "127.0.0.1:${PORT_Y_MCP}" "127.0.0.1:${PORT_Y_FED}"
sleep 1

# Print cert details for evidence (no secrets).
echo "--- cert details ---"
openssl x509 -in "${CERT_DIR}/a.cert.pem" -noout -subject -issuer -dates -fingerprint -sha256 > "${EVIDENCE_DIR}/B1-cert-A.details" 2>&1
openssl x509 -in "${CERT_DIR}/b.cert.pem" -noout -subject -issuer -dates -fingerprint -sha256 > "${EVIDENCE_DIR}/B1-cert-B.details" 2>&1
cat "${EVIDENCE_DIR}/B1-cert-A.details"
cat "${EVIDENCE_DIR}/B1-cert-B.details"

# Probe 1 — SUCCESS: B-client cert + A-pinned-CA → A-listener (presents A cert, verified by B-pinned-CA on A-listener too)
echo "--- B.1 PROBE 1: success (Y-cert + X-pinned-CA → X listener) ---"
curl -v --max-time 5 \
  --cacert "${CERT_DIR}/a.cert.pem" \
  --cert   "${CERT_DIR}/b.cert.pem" \
  --key    "${CERT_DIR}/b.key.pem" \
  "https://127.0.0.1:${PORT_X_FED}/healthz" \
  > "${EVIDENCE_DIR}/B1-probe1-success.body" \
  2> "${EVIDENCE_DIR}/B1-probe1-success.curl-vvv"
echo "exit=$?" >> "${EVIDENCE_DIR}/B1-probe1-success.curl-vvv"

# Probe 2 — NEGATIVE: no client cert → TLS rejection (RequireAndVerifyClientCert)
echo "--- B.1 PROBE 2: no-client-cert → TLS rejection ---"
curl -v --max-time 5 \
  --cacert "${CERT_DIR}/a.cert.pem" \
  "https://127.0.0.1:${PORT_X_FED}/healthz" \
  > "${EVIDENCE_DIR}/B1-probe2-noclient.body" \
  2> "${EVIDENCE_DIR}/B1-probe2-noclient.curl-vvv"
echo "exit=$?" >> "${EVIDENCE_DIR}/B1-probe2-noclient.curl-vvv"

# Probe 3 — NEGATIVE: untrusted self-signed cert → TLS rejection
echo "--- B.1 PROBE 3: self-signed (untrusted) client cert → TLS rejection ---"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
  -keyout "${CERT_DIR}/rogue.key.pem" \
  -out    "${CERT_DIR}/rogue.cert.pem" \
  -days 1 -nodes \
  -subj "/CN=rogue.example" 2>&1 | tail -5
curl -v --max-time 5 \
  --cacert "${CERT_DIR}/a.cert.pem" \
  --cert   "${CERT_DIR}/rogue.cert.pem" \
  --key    "${CERT_DIR}/rogue.key.pem" \
  "https://127.0.0.1:${PORT_X_FED}/healthz" \
  > "${EVIDENCE_DIR}/B1-probe3-untrusted.body" \
  2> "${EVIDENCE_DIR}/B1-probe3-untrusted.curl-vvv"
echo "exit=$?" >> "${EVIDENCE_DIR}/B1-probe3-untrusted.curl-vvv"

# Probe 4 (raw openssl s_client for wire-level handshake bytes)
echo "--- B.1 PROBE 4: openssl s_client raw handshake (success path) ---"
echo "Q" | timeout 5 openssl s_client \
  -connect "127.0.0.1:${PORT_X_FED}" \
  -CAfile  "${CERT_DIR}/a.cert.pem" \
  -cert    "${CERT_DIR}/b.cert.pem" \
  -key     "${CERT_DIR}/b.key.pem" \
  -servername 127.0.0.1 \
  > "${EVIDENCE_DIR}/B1-probe4-openssl-sclient.log" 2>&1
echo "  s_client exit=$?"

# Probe 5 — openssl s_client WITHOUT cert (should fail at handshake)
echo "--- B.1 PROBE 5: openssl s_client WITHOUT cert (handshake fail) ---"
echo "Q" | timeout 5 openssl s_client \
  -connect "127.0.0.1:${PORT_X_FED}" \
  -CAfile  "${CERT_DIR}/a.cert.pem" \
  -servername 127.0.0.1 \
  > "${EVIDENCE_DIR}/B1-probe5-openssl-nocert.log" 2>&1
echo "  s_client exit=$?"

# Capture daemon-X stderr (the TLS-rejection lines)
cp "${LOG_DIR}/daemon-X.log" "${EVIDENCE_DIR}/B1-daemon-X.stderr"

echo
echo "=== Walk B.1 + B.1.1 complete. Evidence: ${EVIDENCE_DIR} ==="
ls -la "${EVIDENCE_DIR}" | head -40

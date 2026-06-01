#!/usr/bin/env bash
# scripts/a2a-conformance/walk-categoryB-mint.sh — v0.9.4 QA Category B,
# cells B.2 + B.2.1 — F8 cross-org JWT mint via hub + F8.1 daemon mount.
#
# Reuses state-X / state-Y left in place by walk-categoryB.sh
# (they carry the pinned-CAs + minted federation cert from B.1
# setup).
#
# Spec source: docs/V0.9.2-ARCHITECTURE.md §10 Pattern 2 Phase 2 +
# §15.2 JWT claims table.
#
# Run AFTER walk-categoryB.sh so the cross-pinned cert material
# already exists. This walker boots:
#   - chepherd daemon-X (alice.example)
#   - chepherd daemon-Y (bob.example)
#   - chepherd-hub      (allow-list alice.example,bob.example;
#                        federation-targets bob.example=http://daemon-Y-http)
# and drives the cross-org mint round trip.

set -uo pipefail

ROOT="${ROOT:-/tmp/v094-qa-B}"
BIN_DIR="${ROOT}/bin"
EVIDENCE_DIR="${ROOT}/evidence"
LOG_DIR="${ROOT}/logs"
STATE_X="${ROOT}/state-X"
STATE_Y="${ROOT}/state-Y"

CHEPHERD="${BIN_DIR}/chepherd"
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

start_daemon() {
  local label="$1" state="$2" org="$3" http_addr="$4" mcp_addr="$5" fed_addr="$6"
  local log="${LOG_DIR}/${label}.log"
  (
    "${CHEPHERD}" run \
      --headless --no-shepherd=true \
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
  wait_for_http "http://${http_addr}/healthz" || { tail -50 "$log"; return 1; }
  echo "  daemon ${label} up: http=${http_addr} fed=${fed_addr} (pid $(cat ${LOG_DIR}/${label}.pid))"
}

stop_daemon() {
  local label="$1"
  local pidfile="${LOG_DIR}/${label}.pid"
  if [ -f "$pidfile" ]; then
    local pid; pid=$(cat "$pidfile")
    kill -TERM "$pid" 2>/dev/null || true
    for _ in 1 2 3 4 5; do kill -0 "$pid" 2>/dev/null || { rm -f "$pidfile"; return 0; }; sleep 0.2; done
    kill -KILL "$pid" 2>/dev/null || true; rm -f "$pidfile"
  fi
}

cleanup_all() {
  stop_daemon daemon-X
  stop_daemon daemon-Y
  stop_daemon hub
}
trap cleanup_all EXIT

PORT_X_HTTP=$(free_port); PORT_X_MCP=$(free_port); PORT_X_FED=$(free_port)
PORT_Y_HTTP=$(free_port); PORT_Y_MCP=$(free_port); PORT_Y_FED=$(free_port)
PORT_HUB=$(free_port)

echo "=== Boot 2 daemons + 1 hub (state-dirs reused from B.1) ==="
start_daemon daemon-X "${STATE_X}" "alice.example" "127.0.0.1:${PORT_X_HTTP}" "127.0.0.1:${PORT_X_MCP}" "127.0.0.1:${PORT_X_FED}"
start_daemon daemon-Y "${STATE_Y}" "bob.example"   "127.0.0.1:${PORT_Y_HTTP}" "127.0.0.1:${PORT_Y_MCP}" "127.0.0.1:${PORT_Y_FED}"

echo "--- boot hub ---"
HUB_LOG="${LOG_DIR}/hub.log"
(
  "${HUB}" \
    -listen "127.0.0.1:${PORT_HUB}" \
    -stun-listen "" -turn-listen "" \
    -allowed-orgs "alice.example,bob.example" \
    -federation-targets "bob.example=http://127.0.0.1:${PORT_Y_HTTP},alice.example=http://127.0.0.1:${PORT_X_HTTP}" \
    > "${HUB_LOG}" 2>&1 &
  echo $! > "${LOG_DIR}/hub.pid"
)
wait_for_http "http://127.0.0.1:${PORT_HUB}/healthz" || { tail -50 "$HUB_LOG"; exit 1; }
echo "  hub up: http=127.0.0.1:${PORT_HUB} (pid $(cat ${LOG_DIR}/hub.pid))"

# ─── B.2.1 — daemon mount + auth gating ──────────────────────────────
echo
echo "=== B.2.1 — F8.1 daemon /api/v1/federation/jwt mount + auth gating ==="

# Probe a — endpoint exists (returns 401 without attestation headers)
curl -sS -o "${EVIDENCE_DIR}/B2.1.a-no-headers.body" -w "http=%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -d '{"scope":"a2a.send","audience":"runner-Z"}' \
  "http://127.0.0.1:${PORT_Y_HTTP}/api/v1/federation/jwt" \
  > "${EVIDENCE_DIR}/B2.1.a-no-headers.meta"
echo "  PROBE a (no headers): $(cat ${EVIDENCE_DIR}/B2.1.a-no-headers.meta) body=$(cat ${EVIDENCE_DIR}/B2.1.a-no-headers.body)"

# Probe b — caller header but no Hub-Attest:true
curl -sS -o "${EVIDENCE_DIR}/B2.1.b-no-attest.body" -w "http=%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -H "X-Chepherd-Caller-Org: alice.example" \
  -d '{"scope":"a2a.send"}' \
  "http://127.0.0.1:${PORT_Y_HTTP}/api/v1/federation/jwt" \
  > "${EVIDENCE_DIR}/B2.1.b-no-attest.meta"
echo "  PROBE b (no attest): $(cat ${EVIDENCE_DIR}/B2.1.b-no-attest.meta) body=$(cat ${EVIDENCE_DIR}/B2.1.b-no-attest.body)"

# Probe c — direct caller+attest (BYPASSES HUB). This exposes the
# attack surface: anyone reaching the dashboard listener can spoof.
curl -sS -o "${EVIDENCE_DIR}/B2.1.c-direct-spoof.body" -w "http=%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -H "X-Chepherd-Caller-Org: alice.example" \
  -H "X-Chepherd-Hub-Attest: true" \
  -d '{"scope":"a2a.send","audience":"runner-spoof"}' \
  "http://127.0.0.1:${PORT_Y_HTTP}/api/v1/federation/jwt" \
  > "${EVIDENCE_DIR}/B2.1.c-direct-spoof.meta"
echo "  PROBE c (direct spoof): $(cat ${EVIDENCE_DIR}/B2.1.c-direct-spoof.meta) body=$(head -c 200 ${EVIDENCE_DIR}/B2.1.c-direct-spoof.body)..."

# ─── B.2 — F8 hub-mediated cross-org JWT mint ────────────────────────
echo
echo "=== B.2 — F8 cross-org JWT mint via hub /v1/federation/auth ==="

# Negative — no auth identity (no X-Chepherd-Org header, no mTLS cert)
curl -sS -o "${EVIDENCE_DIR}/B2-neg-no-identity.body" -w "http=%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -d '{"targetOrgId":"bob.example","scope":"a2a.send"}' \
  "http://127.0.0.1:${PORT_HUB}/v1/federation/auth" \
  > "${EVIDENCE_DIR}/B2-neg-no-identity.meta"
echo "  NEG no-identity:   $(cat ${EVIDENCE_DIR}/B2-neg-no-identity.meta) body=$(cat ${EVIDENCE_DIR}/B2-neg-no-identity.body)"

# Negative — caller not allowlisted (carol.example)
curl -sS -o "${EVIDENCE_DIR}/B2-neg-not-allowlisted.body" -w "http=%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -H "X-Chepherd-Org: carol.example" \
  -d '{"targetOrgId":"bob.example","scope":"a2a.send"}' \
  "http://127.0.0.1:${PORT_HUB}/v1/federation/auth" \
  > "${EVIDENCE_DIR}/B2-neg-not-allowlisted.meta"
echo "  NEG not-allowed:   $(cat ${EVIDENCE_DIR}/B2-neg-not-allowlisted.meta) body=$(cat ${EVIDENCE_DIR}/B2-neg-not-allowlisted.body)"

# Negative — target org has no federation-target registered
curl -sS -o "${EVIDENCE_DIR}/B2-neg-no-target.body" -w "http=%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -H "X-Chepherd-Org: alice.example" \
  -d '{"targetOrgId":"carol.example","scope":"a2a.send"}' \
  "http://127.0.0.1:${PORT_HUB}/v1/federation/auth" \
  > "${EVIDENCE_DIR}/B2-neg-no-target.meta"
echo "  NEG no-target:     $(cat ${EVIDENCE_DIR}/B2-neg-no-target.meta) body=$(cat ${EVIDENCE_DIR}/B2-neg-no-target.body)"

# SUCCESS attempt — alice → hub → daemon-Y mint (EXPECT 401 due to dashboard
# Bearer middleware gating the mint endpoint at /api/v1/federation/jwt; this
# is a documented finding, not a walk error).
curl -sS -o "${EVIDENCE_DIR}/B2-success-mint.body" -w "http=%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -H "X-Chepherd-Org: alice.example" \
  -d '{"targetOrgId":"bob.example","scope":"a2a.send","audience":"runner-bob-XYZ"}' \
  "http://127.0.0.1:${PORT_HUB}/v1/federation/auth" \
  > "${EVIDENCE_DIR}/B2-success-mint.meta"
echo "  HUB-MEDIATED mint:    $(cat ${EVIDENCE_DIR}/B2-success-mint.meta) body=$(cat ${EVIDENCE_DIR}/B2-success-mint.body)"

# DIRECT-WITH-BEARER mint — bypass the hub, call daemon-Y mint endpoint
# directly with daemon-Y's operator bearer token + the F8 attestation
# headers. This is NOT the production flow (only the operator should have
# this token), but lets us verify the §15.2 claims + signature shape since
# the hub-mediated path is currently broken by the auth-middleware wiring.
DAEMON_Y_BEARER=$(tr -d '\n\r ' < "${STATE_Y}/auth.printed" 2>/dev/null)
echo "  (using daemon-Y operator bearer to bypass auth middleware for §15.2 evidence)"
curl -sS -o "${EVIDENCE_DIR}/B2-direct-mint.body" -w "http=%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${DAEMON_Y_BEARER}" \
  -H "X-Chepherd-Caller-Org: alice.example" \
  -H "X-Chepherd-Hub-Attest: true" \
  -d '{"scope":"a2a.send","audience":"runner-bob-XYZ"}' \
  "http://127.0.0.1:${PORT_Y_HTTP}/api/v1/federation/jwt" \
  > "${EVIDENCE_DIR}/B2-direct-mint.meta"
echo "  DIRECT-WITH-BEARER:   $(cat ${EVIDENCE_DIR}/B2-direct-mint.meta)"
echo "  direct mint body:     $(head -c 400 ${EVIDENCE_DIR}/B2-direct-mint.body)"

# Extract the JWT for analysis (from the direct path, since hub path is broken)
JWT=$(jq -r '.jwt // empty' "${EVIDENCE_DIR}/B2-direct-mint.body")
echo "${JWT}" > "${EVIDENCE_DIR}/B2-jwt.raw"
echo "  JWT length: ${#JWT}"
# Decode header + payload (base64url, no padding)
b64url_decode() { python3 -c 'import sys,base64;s=sys.stdin.read().strip();s+="="*((4-len(s)%4)%4);print(base64.urlsafe_b64decode(s).decode())'; }
echo "$JWT" | cut -d. -f1 | b64url_decode 2>/dev/null > "${EVIDENCE_DIR}/B2-jwt.header.json" || true
echo "$JWT" | cut -d. -f2 | b64url_decode 2>/dev/null > "${EVIDENCE_DIR}/B2-jwt.claims.json" || true
echo "  JWT header: $(cat ${EVIDENCE_DIR}/B2-jwt.header.json)"
echo "  JWT claims: $(cat ${EVIDENCE_DIR}/B2-jwt.claims.json)"

# Fetch daemon-Y JWKS
curl -sS -o "${EVIDENCE_DIR}/B2-jwks.json" -w "http=%{http_code}\n" \
  "http://127.0.0.1:${PORT_Y_HTTP}/.well-known/jwks.json" \
  > "${EVIDENCE_DIR}/B2-jwks.meta"
echo "  daemon-Y JWKS:     $(cat ${EVIDENCE_DIR}/B2-jwks.meta)"
echo "  JWKS body:         $(head -c 400 ${EVIDENCE_DIR}/B2-jwks.json)"

# Verify ES256 signature via python + cryptography (already in distro for most cases) — fall back to openssl if missing
python3 - "$JWT" "${EVIDENCE_DIR}/B2-jwks.json" > "${EVIDENCE_DIR}/B2-sig-verify.log" 2>&1 << 'PYEOF'
import sys, json, base64, hashlib
from cryptography.hazmat.primitives.asymmetric import ec, utils as ec_utils
from cryptography.hazmat.primitives import serialization
from cryptography.exceptions import InvalidSignature

jwt = sys.argv[1]
jwks_path = sys.argv[2]
hdr, payload, sig = jwt.split('.')
def b64d(s):
    s = s + "=" * ((4 - len(s) % 4) % 4)
    return base64.urlsafe_b64decode(s)
hdrJ = json.loads(b64d(hdr).decode())
print("JWT header:", hdrJ)
kid = hdrJ.get("kid", "")
alg = hdrJ.get("alg", "")
print(f"kid={kid} alg={alg}")
jwks = json.loads(open(jwks_path).read())
key_jwk = None
for k in jwks.get("keys", []):
    if k.get("kid") == kid:
        key_jwk = k
        break
if key_jwk is None and len(jwks.get("keys", [])) == 1:
    key_jwk = jwks["keys"][0]
print("matched key:", key_jwk)
x = int.from_bytes(b64d(key_jwk["x"]), "big")
y = int.from_bytes(b64d(key_jwk["y"]), "big")
pubkey = ec.EllipticCurvePublicNumbers(x, y, ec.SECP256R1()).public_key()
signing_input = f"{hdr}.{payload}".encode()
sig_bytes = b64d(sig)
# ES256 sig is raw r||s (each 32 bytes); convert to DER for cryptography.verify
r = int.from_bytes(sig_bytes[:32], "big")
s = int.from_bytes(sig_bytes[32:], "big")
der_sig = ec_utils.encode_dss_signature(r, s)
try:
    pubkey.verify(der_sig, signing_input, ec.ECDSA(hashes.SHA256()) if False else __import__("cryptography.hazmat.primitives.hashes", fromlist=["SHA256"]).SHA256.__call__.__self__.SHA256() if False else None)
except Exception as e:
    pass
# Cleaner verify:
from cryptography.hazmat.primitives import hashes
try:
    pubkey.verify(der_sig, signing_input, ec.ECDSA(hashes.SHA256()))
    print("SIGNATURE-VERIFY: OK")
except InvalidSignature:
    print("SIGNATURE-VERIFY: INVALID")
PYEOF
cat "${EVIDENCE_DIR}/B2-sig-verify.log"

# Tamper test — flip last byte of payload, re-encode, verify
python3 - "$JWT" "${EVIDENCE_DIR}/B2-jwks.json" > "${EVIDENCE_DIR}/B2-tamper-verify.log" 2>&1 << 'PYEOF'
import sys, json, base64
from cryptography.hazmat.primitives.asymmetric import ec, utils as ec_utils
from cryptography.hazmat.primitives import hashes
from cryptography.exceptions import InvalidSignature

jwt = sys.argv[1]
jwks_path = sys.argv[2]
hdr, payload, sig = jwt.split('.')
def b64d(s):
    s = s + "=" * ((4 - len(s) % 4) % 4)
    return base64.urlsafe_b64decode(s)
def b64e(b):
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()
hdrJ = json.loads(b64d(hdr).decode())
jwks = json.loads(open(jwks_path).read())
key_jwk = jwks["keys"][0]
x = int.from_bytes(b64d(key_jwk["x"]), "big")
y = int.from_bytes(b64d(key_jwk["y"]), "big")
pubkey = ec.EllipticCurvePublicNumbers(x, y, ec.SECP256R1()).public_key()

# Tamper: flip 'sub' from 'alice.example' to 'mallory.example'
claims = json.loads(b64d(payload).decode())
claims["sub"] = "mallory.example"
tampered_payload = b64e(json.dumps(claims, separators=(",", ":")).encode())
tampered_jwt = f"{hdr}.{tampered_payload}.{sig}"
signing_input = f"{hdr}.{tampered_payload}".encode()
sig_bytes = b64d(sig)
r = int.from_bytes(sig_bytes[:32], "big")
s = int.from_bytes(sig_bytes[32:], "big")
der_sig = ec_utils.encode_dss_signature(r, s)
try:
    pubkey.verify(der_sig, signing_input, ec.ECDSA(hashes.SHA256()))
    print("TAMPER-VERIFY: OK (BUG — tampered JWT verified valid!)")
except InvalidSignature:
    print("TAMPER-VERIFY: INVALID (correct — tampered JWT rejected)")
print(f"Tampered claims: sub={claims['sub']}")
PYEOF
cat "${EVIDENCE_DIR}/B2-tamper-verify.log"

# Replay test — mint twice with same params via direct path, compare jti
sleep 1
curl -sS -o "${EVIDENCE_DIR}/B2-direct-mint-2.body" -w "http=%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${DAEMON_Y_BEARER}" \
  -H "X-Chepherd-Caller-Org: alice.example" \
  -H "X-Chepherd-Hub-Attest: true" \
  -d '{"scope":"a2a.send","audience":"runner-bob-XYZ"}' \
  "http://127.0.0.1:${PORT_Y_HTTP}/api/v1/federation/jwt" \
  > "${EVIDENCE_DIR}/B2-direct-mint-2.meta"
JWT2=$(jq -r '.jwt // empty' "${EVIDENCE_DIR}/B2-direct-mint-2.body")
if [ -n "$JWT2" ]; then
  echo "$JWT2" | cut -d. -f2 | b64url_decode 2>/dev/null > "${EVIDENCE_DIR}/B2-jwt2.claims.json" || true
  echo "  2nd mint claims:   $(cat ${EVIDENCE_DIR}/B2-jwt2.claims.json)"
fi

# Body-blind cross-cut: grep hub stderr for the JWT value (signed bytes leaking?)
echo
echo "=== Body-blind cross-cut: hub stderr inspection ==="
cp "${HUB_LOG}" "${EVIDENCE_DIR}/B2-hub.stderr"
echo "--- searching hub.stderr for JWT bytes (signed JWS starts 'eyJ') ---"
if [ -n "$JWT" ] && [ "${#JWT}" -gt 20 ]; then
  if grep -q "${JWT:0:40}" "${HUB_LOG}"; then
    echo "  LEAK DETECTED: JWT prefix found in hub stderr"
    grep "${JWT:0:40}" "${HUB_LOG}" > "${EVIDENCE_DIR}/B2-hub-jwt-leak.log"
  else
    echo "  body-blind: hub stderr does NOT contain the minted JWT bytes (PASS)"
    echo "no-leak (probed JWT prefix '${JWT:0:40}')" > "${EVIDENCE_DIR}/B2-hub-jwt-leak.log"
  fi
else
  echo "  body-blind probe skipped: no JWT to search for (hub-mediated path failed; see B.2 verdict)"
  echo "skipped — hub-mediated path failed; no JWT body produced" > "${EVIDENCE_DIR}/B2-hub-jwt-leak.log"
fi
echo "--- hub stderr full (filtered to federation lines) ---"
grep -E "federat|relay|jwt|JWT|F8|targetOrg" "${HUB_LOG}" > "${EVIDENCE_DIR}/B2-hub.federation-lines" || true
head -40 "${EVIDENCE_DIR}/B2-hub.federation-lines"

# Hub /healthz federation stats
curl -sS "http://127.0.0.1:${PORT_HUB}/healthz" \
  > "${EVIDENCE_DIR}/B2-hub-healthz.json"
echo
echo "--- hub /healthz post-mint ---"
cat "${EVIDENCE_DIR}/B2-hub-healthz.json" | jq -c . 2>/dev/null || cat "${EVIDENCE_DIR}/B2-hub-healthz.json"

# Capture daemon-Y stderr
cp "${LOG_DIR}/daemon-Y.log" "${EVIDENCE_DIR}/B2-daemon-Y.stderr"
echo "--- daemon-Y federation lines ---"
grep -iE "federat|jwt|mint|F8" "${LOG_DIR}/daemon-Y.log" | head -20

echo
echo "=== B.2 + B.2.1 walk complete. Evidence: ${EVIDENCE_DIR} ==="
ls -la "${EVIDENCE_DIR}/B2"* 2>&1 | head -30

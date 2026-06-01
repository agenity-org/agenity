#!/usr/bin/env bash
# scripts/a2a-conformance/walk-categoryA.sh — v0.9.4 QA Category A walker.
#
# Drives the 11 A2A v1.0 method bodies + state machine + error codes + Agent
# Card against a running chepherd-run instance and captures full request/
# response bytes into ${EVIDENCE_DIR} for the evidence markdown.
#
# Spec source: https://a2a-protocol.org/latest/specification/
#              (backed by a2aproject/A2A@main)
#
# Usage:
#   walk-categoryA.sh <chepherd-url> <bootstrap-token> <evidence-dir>

set -uo pipefail

CHEPHERD_URL="${1:-http://127.0.0.1:18080}"
TOKEN="${2:-}"
EVIDENCE_DIR="${3:-/tmp/v094-qa-A-evidence}"

mkdir -p "$EVIDENCE_DIR"

call() {
  local id="$1" method="$2" params="$3" file="$4"
  local body
  body=$(printf '{"jsonrpc":"2.0","id":"%s","method":"%s","params":%s}' "$id" "$method" "$params")
  printf "%s\n" "$body" > "${EVIDENCE_DIR}/${file}.req.json"
  local http
  http=$(curl -sS -o "${EVIDENCE_DIR}/${file}.resp.json" \
    -w "%{http_code}" \
    -X POST -H "Content-Type: application/json" \
    -H "Authorization: Bearer ${TOKEN}" \
    -d "$body" \
    "${CHEPHERD_URL}/jsonrpc")
  printf "%s\n" "$http" > "${EVIDENCE_DIR}/${file}.http"
  printf "%-65s  HTTP %s\n" "$method  ($file)" "$http"
}

call_sse() {
  local id="$1" method="$2" params="$3" file="$4"
  local body
  body=$(printf '{"jsonrpc":"2.0","id":"%s","method":"%s","params":%s}' "$id" "$method" "$params")
  printf "%s\n" "$body" > "${EVIDENCE_DIR}/${file}.req.json"
  # SSE: include response headers + body. Max 3s wait.
  timeout 3 curl -sS -i -N -X POST -H "Content-Type: application/json" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Accept: text/event-stream" \
    -d "$body" \
    "${CHEPHERD_URL}/jsonrpc" > "${EVIDENCE_DIR}/${file}.resp.raw" 2>&1 || true
  printf "%-65s  (SSE captured to %s.resp.raw)\n" "$method  ($file)" "$file"
}

# ─── Agent Card discovery (A.4) ─────────────────────────────────
echo "=== A.4 Agent Card discovery ==="
curl -sS -i "${CHEPHERD_URL}/.well-known/agent-card.json" \
  > "${EVIDENCE_DIR}/A4.agent-card.resp.raw" 2>&1
curl -sS -o "${EVIDENCE_DIR}/A4.agent-card.json" -w "%{http_code}\n" \
  "${CHEPHERD_URL}/.well-known/agent-card.json" \
  > "${EVIDENCE_DIR}/A4.agent-card.http"
echo "  /.well-known/agent-card.json HTTP $(cat ${EVIDENCE_DIR}/A4.agent-card.http)"
curl -sS -i "${CHEPHERD_URL}/.well-known/jwks.json" \
  > "${EVIDENCE_DIR}/A4.jwks.resp.raw" 2>&1
curl -sS -o "${EVIDENCE_DIR}/A4.jwks.json" -w "%{http_code}\n" \
  "${CHEPHERD_URL}/.well-known/jwks.json" \
  > "${EVIDENCE_DIR}/A4.jwks.http"
echo "  /.well-known/jwks.json       HTTP $(cat ${EVIDENCE_DIR}/A4.jwks.http)"

# ─── A.1 11 method bodies ───────────────────────────────────────
echo
echo "=== A.1 11 method bodies ==="

# message/send with non-existent contextId → expect error-style response
call "s1" "message/send" \
  '{"message":{"role":"user","kind":"message","contextId":"no-such-session","parts":[{"kind":"text","text":"probe"}]}}' \
  "A1-01.message_send.nosession"

# message/send with valid params but session won't exist — capture for shape
# tasks/list to find the auto-created task ID from above
call "l1" "tasks/list" '{}' "A1-04.tasks_list"
# Capture task ID for downstream calls
TASK_ID=$(jq -r '.result.tasks[0].id // empty' "${EVIDENCE_DIR}/A1-04.tasks_list.resp.json")
echo "  captured TASK_ID=${TASK_ID}"

# tasks/get with valid id (the auto-created one)
if [ -n "$TASK_ID" ]; then
  call "g1" "tasks/get" "{\"taskId\":\"${TASK_ID}\"}" "A1-03.tasks_get.valid"
fi
call "g2" "tasks/get" '{"taskId":"definitely-does-not-exist"}' "A1-03.tasks_get.missing"

# tasks/cancel — illegal (task already in failed state) + missing id
if [ -n "$TASK_ID" ]; then
  call "c1" "tasks/cancel" "{\"taskId\":\"${TASK_ID}\"}" "A1-05.tasks_cancel.illegal_state"
fi
call "c2" "tasks/cancel" '{"taskId":"definitely-does-not-exist"}' "A1-05.tasks_cancel.missing"

# tasks/resubscribe — SSE
if [ -n "$TASK_ID" ]; then
  call_sse "r1" "tasks/resubscribe" "{\"taskId\":\"${TASK_ID}\"}" "A1-06.tasks_resubscribe"
fi

# message/stream — SSE
call_sse "ms1" "message/stream" \
  '{"message":{"role":"user","kind":"message","contextId":"no-such-session","parts":[{"kind":"text","text":"stream"}]}}' \
  "A1-02.message_stream"

# pushNotificationConfig/set — chepherd-flat shape
call "p1" "tasks/pushNotificationConfig/set" \
  '{"taskId":"task-x","url":"https://example.org/webhook","filters":["state.completed"]}' \
  "A1-07.push_set.flat"
# pushNotificationConfig/set — A2A spec-shape (nested)
call "p2" "tasks/pushNotificationConfig/set" \
  '{"taskId":"task-x","pushNotificationConfig":{"url":"https://example.org/webhook2"}}' \
  "A1-07.push_set.spec_nested"

# Capture configID for get/delete
CFG_ID=$(jq -r '.result.config.id // empty' "${EVIDENCE_DIR}/A1-07.push_set.flat.resp.json")
echo "  captured CFG_ID=${CFG_ID}"

# pushNotificationConfig/list
call "pl1" "tasks/pushNotificationConfig/list" '{"taskId":"task-x"}' "A1-09.push_list"

# pushNotificationConfig/get
if [ -n "$CFG_ID" ]; then
  call "pg1" "tasks/pushNotificationConfig/get" "{\"id\":\"${CFG_ID}\"}" "A1-08.push_get"
fi
call "pg2" "tasks/pushNotificationConfig/get" '{"id":"no-such-cfg"}' "A1-08.push_get.missing"

# pushNotificationConfig/delete
if [ -n "$CFG_ID" ]; then
  call "pd1" "tasks/pushNotificationConfig/delete" "{\"id\":\"${CFG_ID}\"}" "A1-10.push_delete"
fi

# agent/getAuthenticatedExtendedCard
call "e1" "agent/getAuthenticatedExtendedCard" '{}' "A1-11.agent_getExtendedCard"

# ─── A.3 JSON-RPC 2.0 error codes ───────────────────────────────
echo
echo "=== A.3 JSON-RPC 2.0 error codes ==="

# -32700 ParseError — invalid JSON
curl -sS -o "${EVIDENCE_DIR}/A3-32700.parse_error.resp.json" -w "%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${TOKEN}" \
  -d '{not valid json' \
  "${CHEPHERD_URL}/jsonrpc" > "${EVIDENCE_DIR}/A3-32700.parse_error.http"
echo "  ParseError (-32700)         HTTP $(cat ${EVIDENCE_DIR}/A3-32700.parse_error.http)"

# -32600 InvalidRequest — missing jsonrpc field
curl -sS -o "${EVIDENCE_DIR}/A3-32600.invalid_request.resp.json" -w "%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${TOKEN}" \
  -d '{"method":"tasks/list","params":{}}' \
  "${CHEPHERD_URL}/jsonrpc" > "${EVIDENCE_DIR}/A3-32600.invalid_request.http"
echo "  InvalidRequest (-32600)     HTTP $(cat ${EVIDENCE_DIR}/A3-32600.invalid_request.http)"

# -32601 MethodNotFound — unknown method
call "mnf1" "tasks/nonexistentMethod" '{}' "A3-32601.method_not_found"

# -32602 InvalidParams — wrong-typed params
call "ip1" "tasks/get" '"not-an-object"' "A3-32602.invalid_params"

# Auth-required (-32001 — chepherd-specific or A2A AUTH_REQUIRED?)
curl -sS -o "${EVIDENCE_DIR}/A3-auth-required.resp.json" -w "%{http_code}\n" \
  -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":"a1","method":"tasks/list","params":{}}' \
  "${CHEPHERD_URL}/jsonrpc" > "${EVIDENCE_DIR}/A3-auth-required.http"
echo "  AuthRequired                HTTP $(cat ${EVIDENCE_DIR}/A3-auth-required.http)"

echo
echo "=== Walk complete. Evidence in: ${EVIDENCE_DIR} ==="
ls -la "${EVIDENCE_DIR}" | head -50

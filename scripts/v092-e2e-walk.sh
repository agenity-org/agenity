#!/usr/bin/env bash
# scripts/v092-e2e-walk.sh — operator-facing v0.9.2 end-to-end walk.
#
# The 9-step ship gate that closes epic #208 per architect's spec:
#   1. chepherd run starts on a fresh $stateDir; binds MCP-HTTP/WS +
#      A2A endpoint + shepherd tick loop
#   2. Operator spawns a session via the v0.9.2 CLI surface (or
#      wizard via Playwright; not automated here)
#   3. curl /.well-known/agent-card.json → HTTP 200
#   4. curl A2A SendMessage JSON-RPC → 200 with
#      SendMessageResult{task:{state:working, contextId}}
#   5. Wait ≥ shepherd.TickInterval (60s default) so tick fires
#   6. Verify SessionRepository carries last_tick_at + next_tick_at
#   7. Operator takes Playwright dashboard screenshot
#   8. Post evidence as comment on epic #208
#   9. 4-eyes via dispatched sub-agent reviewer
#
# This script automates steps 3, 4, 6 (curl + repository query). Steps
# 1, 2, 5, 7 are operator-driven; steps 8, 9 are tech-lead workflow.
#
# Usage:
#   scripts/v092-e2e-walk.sh <chepherd-run-url> <session-id>
#
# Example:
#   scripts/v092-e2e-walk.sh http://127.0.0.1:8083 my-session
#
# Refs #208.

set -euo pipefail

CHEPHERD_URL="${1:-http://127.0.0.1:8083}"
SESSION_ID="${2:-}"
# Matches cmd/run.go's default state dir. Operator overrides via
# CHEPHERD_STATE_DIR env or by re-running with the same --state-dir
# the chepherd binary was launched with.
STATE_DIR="${CHEPHERD_STATE_DIR:-$HOME/.local/state/chepherd-v05}"

if [ -z "$SESSION_ID" ]; then
    echo "ERROR: SESSION_ID is required as second arg." >&2
    echo "Usage: $0 <chepherd-run-url> <session-id>" >&2
    exit 2
fi

step() {
    printf "\n\033[1;34m▶ %s\033[0m\n" "$*"
}

ok() {
    printf "\033[1;32m✓\033[0m %s\n" "$*"
}

fail() {
    printf "\033[1;31m✗\033[0m %s\n" "$*"
    exit 1
}

# ─── Step 3: Agent Card serves at /.well-known/agent-card.json ──
step "Step 3 — GET ${CHEPHERD_URL}/.well-known/agent-card.json"
CARD_RESPONSE=$(curl -fsS -o /tmp/v092-walk-card.json -w "%{http_code}" "${CHEPHERD_URL}/.well-known/agent-card.json") || \
    fail "Agent Card fetch failed (HTTP ${CARD_RESPONSE:-???}). Is chepherd run bound at ${CHEPHERD_URL}?"
[ "$CARD_RESPONSE" = "200" ] || fail "Agent Card status ${CARD_RESPONSE}, want 200"
ok "Agent Card HTTP 200 (saved to /tmp/v092-walk-card.json)"

# Verify x-chepherd-p2p extension present
if command -v jq >/dev/null 2>&1; then
    P2P_PRESENT=$(jq -r '."x-chepherd-p2p" | type' /tmp/v092-walk-card.json)
    [ "$P2P_PRESENT" = "object" ] || fail "x-chepherd-p2p extension missing or wrong type ($P2P_PRESENT)"
    ok "x-chepherd-p2p extension present"
else
    printf "  (jq not installed — skipping x-chepherd-p2p assertion)\n"
fi

# ─── Step 4: A2A SendMessage JSON-RPC ──────────────────────────
step "Step 4 — POST A2A SendMessage to ${CHEPHERD_URL}/jsonrpc"
SEND_BODY=$(cat <<JSON
{
  "jsonrpc": "2.0",
  "id": "e2e-1",
  "method": "SendMessage",
  "params": {
    "message": {
      "role": "user",
      "kind": "message",
      "contextId": "${SESSION_ID}",
      "parts": [{"kind": "text", "text": "hello from e2e walk"}]
    }
  }
}
JSON
)

SEND_HTTP=$(curl -fsS -o /tmp/v092-walk-send.json -w "%{http_code}" \
    -X POST -H "Content-Type: application/json" \
    -d "$SEND_BODY" \
    "${CHEPHERD_URL}/jsonrpc") || \
    fail "SendMessage POST failed (HTTP ${SEND_HTTP:-???})"
[ "$SEND_HTTP" = "200" ] || fail "SendMessage status ${SEND_HTTP}, want 200"
ok "SendMessage HTTP 200 (saved to /tmp/v092-walk-send.json)"

if command -v jq >/dev/null 2>&1; then
    TASK_STATE=$(jq -r '.result.task.status.state // empty' /tmp/v092-walk-send.json)
    TASK_CONTEXT=$(jq -r '.result.task.contextId // empty' /tmp/v092-walk-send.json)
    TASK_ID=$(jq -r '.result.task.id // empty' /tmp/v092-walk-send.json)
    [ "$TASK_STATE" = "working" ] || fail "Task.state = '${TASK_STATE}', want 'working'"
    [ "$TASK_CONTEXT" = "$SESSION_ID" ] || fail "Task.contextId = '${TASK_CONTEXT}', want '${SESSION_ID}'"
    [ -n "$TASK_ID" ] || fail "Task.id is empty (server should auto-generate UUIDv7)"
    ok "Task.state=working, Task.contextId=${SESSION_ID}, Task.id=${TASK_ID}"
else
    printf "  (jq not installed — skipping Task shape assertion)\n"
fi

# ─── Step 6: SessionRepository carries shepherd tick stamp ─────
# Step 5 (wait ≥60s for shepherd tick) is operator-driven; this step
# only verifies the post-wait state. Operator should sleep first.
step "Step 6 — verify SessionRepository tick stamp in ${STATE_DIR}/chepherd.db"
if [ ! -f "${STATE_DIR}/chepherd.db" ]; then
    fail "Persistence store not found at ${STATE_DIR}/chepherd.db (chepherd run uses this path)"
fi

if command -v sqlite3 >/dev/null 2>&1; then
    SESSION_STATE=$(sqlite3 "${STATE_DIR}/chepherd.db" \
        "SELECT state_json FROM sessions WHERE session_id = '${SESSION_ID}';")
    if [ -z "$SESSION_STATE" ]; then
        fail "Session '${SESSION_ID}' not found in SessionRepository"
    fi
    if command -v jq >/dev/null 2>&1; then
        NEXT_TICK=$(printf "%s" "$SESSION_STATE" | jq -r '.next_tick_at // empty')
        LAST_TICK=$(printf "%s" "$SESSION_STATE" | jq -r '.last_tick_at // empty')
        [ -n "$NEXT_TICK" ] || fail "state.next_tick_at not stamped — shepherd tick didn't reach this session"
        [ -n "$LAST_TICK" ] || fail "state.last_tick_at not stamped"
        ok "shepherd tick evidence: last_tick_at=${LAST_TICK}, next_tick_at=${NEXT_TICK}"
    else
        printf "  state JSON: %s\n" "$SESSION_STATE"
    fi
else
    printf "  (sqlite3 not installed — skipping SessionRepository assertion)\n"
fi

step "DONE — evidence collected"
printf "\nEvidence files for epic #208 comment:\n"
printf "  /tmp/v092-walk-card.json   (Agent Card response body)\n"
printf "  /tmp/v092-walk-send.json   (SendMessage response body)\n"
printf "\nNext: take Playwright dashboard screenshot + post all evidence as a\n"
printf "comment on epic #208. Then dispatch sub-agent reviewer for 4-eyes.\n"

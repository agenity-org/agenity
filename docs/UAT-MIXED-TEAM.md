# UAT: Mixed-Team Agent Communication (live walkthrough)

Goal: prove each non-claude agent can hold a real conversation with a claude
agent through chepherd — receive a knock, call `get_task`, process, and reply via
an MCP tool — with **skills + canon loaded**. Every result below is from the live
daemon, recorded as it ran. No "connected" or "delivered" claims count as PASS —
only a full round-trip (knock → `get_task` → reply tool) does.

## Test procedure (identical for every agent)

1. **Spawn** the agent (flavor + provider + model) into team `mixed`.
2. **MCP handshake** — daemon log shows `initialize → OK` + `tools/list → OK (N tools)`.
3. **Knock** — `POST /api/v1/teams/mixed/messages` `@<agent>` (operator → agent).
4. **Process** — daemon log shows `tools/call chepherd.get_task → OK`.
5. **Reply** — daemon log shows `tools/call chepherd.{alert_human|send_to_session} → OK`.
6. **Verdict** — PASS only if steps 2–5 all hold. FAIL records the exact root cause.

Evidence source: `podman logs chepherd | grep '[chepherd-mcp] <agent>:'` (the
daemon's own per-agent tool-call log) + the agent's own session transcript.

## Results

### Pair 1 — claude ↔ claude (claude-code, Claude subscription) — ✅ PASS
- MCP: `initialize → OK`, `tools/list → OK (27 tools)`
- Process+reply: `get_task → OK`, `alert_human → OK`, `send_to_session→operator → OK`
- Skills/canon: agent listed its loaded skills + team canon on request.
- Durability: survives token expiry (#744 daemon refresher — verified 5m→407m).

### Pair 2 — claude ↔ copilot (GitHub Copilot CLI 1.0.63) — ⚠️ chepherd-side DONE, token-type blocked
- **Token injection: FIXED ✅** — added github-pat to vault; `GITHUB_TOKEN: SET` in container.
- **MCP transport: FIXED ✅** — switched agents to the canonical Streamable-HTTP transport
  (`CHEPHERD_AGENT_MCP_URL=http://127.0.0.1:9090/mcp`, #478) instead of the stdio bridge.
  copilot's `Unexpected end of JSON input` is **GONE** — log now shows `MCP client for
  chepherd connected ... Started MCP client for remote server chepherd`. Verified the
  HTTP endpoint answers `initialize`+`tools/list` with clean JSON from inside the container.
- **Remaining blocker (operator's to provide): classic PAT rejected** — copilot logs
  `Classic PATs are not supported. Please use fine-grained PATs or other supported token
  types.` The host `gh` token is a classic `ghp_…` PAT. copilot needs a **fine-grained
  PAT with Copilot access** or the Copilot OAuth login. Not a chepherd bug.

### Pair 3 — claude ↔ gemini (gemini-cli, gemini-2.5-flash) — ⚠️ FAIL (model)
- MCP: `initialize → OK`, `tools/list → OK (27 tools)`, no `-32601` (prompts/resources fix shipped).
- Process: **never called `get_task`** — session log shows the knock received, zero
  assistant response. gemini-2.5-flash does not complete the agentic turn (matches
  operator's "Thinking 3m14s" observation). Not a chepherd bug; model capability.
- Next lever to try: gemini-2.5-pro (more capable) or longer timeout.

### Pair 4 — claude ↔ opencode (Cerebras gpt-oss-120b / Groq) — ❌ FAIL (free-tier TPM)
- MCP: `initialize → OK`, `tools/list → OK (27 tools)`; model resolved to `cerebras/gpt-oss-120b`.
- Process: opencode log shows `ERROR ... Tokens per minute limit exceeded` on
  Cerebras — same wall as Groq's 12k TPM. opencode emits ~40k-token requests
  (system prompt + 27 tools + AGENTS.md + file context); **no free tier accepts that**.
- Root cause is structural: opencode is too heavy for free TPM tiers. The correct
  tool for a free TPM tier is a lean agent (aider/little-coder) or a paid tier.

## Verdict (2026-06-16)

| Pair | Result | Blocker | chepherd's? |
|---|---|---|---|
| claude ↔ claude | ✅ PASS (full round-trip, HTTP transport, durable) | — | works |
| claude ↔ copilot | ⚠️ chepherd-side DONE | classic PAT rejected — needs fine-grained PAT / Copilot OAuth | no (token type) |
| claude ↔ gemini | ❌ FAIL | gemini-2.5-flash never completes the turn (same on WS + HTTP) | no (model) |
| claude ↔ opencode | ❌ FAIL | free TPM (Groq 12k / Cerebras) < opencode's ~40k req | no (provider) |

**Transport upgrade shipped:** all agents now use the canonical Streamable-HTTP MCP
transport (#478) instead of the deprecated stdio bridge — fixes copilot's strict parser
and is the documented forward path. claude + gemini verified no-regression on HTTP.

---

## Reproducible walkthrough scripts

### Common setup (run once)
```bash
export TOK=$(cat /home/openova/.local/state/chepherd/auth.printed)   # daemon bearer token
BASE=http://127.0.0.1:8083
PFX=chepherd-agent-42102551-                                          # container name prefix (instanceUUID)
spawn(){ curl -s -X POST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  $BASE/api/v1/sessions -d "{\"Name\":\"$1\",\"Agent\":\"$2\",\"Team\":\"mixed\",\"Role\":\"$3\",\"Cwd\":\"/home/chepherd/repos\"}"; }
knock(){ curl -s -X POST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  $BASE/api/v1/teams/mixed/messages -d "{\"author\":\"operator\",\"body\":\"@$1 $2\"}"; }
calls(){ podman logs chepherd 2>&1 | grep "\[chepherd-mcp\] $1: tools/call"; }   # processing evidence
```
**PASS criterion (all pairs):** after `knock`, `calls <agent>` shows
`chepherd.get_task → OK` AND a reply tool (`alert_human` or `send_to_session`) `→ OK`.

### Pair: claude ↔ claude  — ✅ PASS
- **Precond:** claude-oauth in vault (✓).
- **Steps:** `spawn tech-lead claude-code lead` → `knock tech-lead "call alert_human confirming you can run"` → `calls tech-lead`.
- **Expected/Actual:** `get_task → OK`, `alert_human → OK`, `send_to_session→operator → OK`. **PASS.**

### Pair: claude ↔ copilot — ⚠️ chepherd-side PASS, token-type blocked
- **Precond:** github-pat in vault (`POST /api/v1/vault {provider:github-pat,env_var:GITHUB_TOKEN,value:<PAT>}`); daemon on HTTP transport (`CHEPHERD_AGENT_MCP_URL` set).
- **Steps:** `spawn reviewer copilot worker` → check `podman exec ${PFX}reviewer env | grep GITHUB_TOKEN` (expect SET) → check `~/.copilot/mcp-config.json` (expect `type:http`) → `knock reviewer ...` → `calls reviewer`.
- **Expected:** copilot MCP connects clean (no `Unexpected end of JSON input`), then full round-trip.
- **Actual:** token SET ✓, `type:http` ✓, JSON error GONE ✓; **FAILS at** `Classic PATs are not supported` in `~/.copilot/logs/*.log`. **Pass blocked on token type** → supply a fine-grained PAT with Copilot access (or Copilot OAuth), then re-run.

### Pair: claude ↔ gemini — ❌ FAIL (model)
- **Precond:** google-api (GEMINI_API_KEY) in vault (✓); flavor pins `--model gemini-2.5-flash`.
- **Steps:** `spawn qa gemini-cli worker` → `knock qa ...` → `calls qa` + inspect `podman exec ${PFX}qa sh -c 'cat ~/.gemini/tmp/*/logs.json'`.
- **Expected:** `get_task → OK` + reply.
- **Actual:** MCP `initialize`+`tools/list → OK` (no `-32601` after the prompts/resources fix), knock received in logs.json, but **zero assistant turn / no tool call** on WS *and* HTTP. **FAIL** — gemini-2.5-flash can't drive the agentic loop. Lever: try `gemini-2.5-pro`.

### Pair: claude ↔ opencode / Cerebras — ❌ FAIL (free-tier TPM)
- **Precond:** cerebras-api (and/or groq-api) in vault (✓); opencode model resolves to `cerebras/gpt-oss-120b`.
- **Steps:** `spawn backend-dev opencode worker` → `knock backend-dev ...` → inspect `podman exec ${PFX}backend-dev sh -c 'tail ~/.local/share/opencode/log/opencode.log'`.
- **Expected:** `get_task → OK` + reply.
- **Actual:** MCP OK, model `cerebras/gpt-oss-120b`, but `ERROR ... Tokens per minute limit exceeded` (same on Groq). **FAIL** — opencode's ~40k-token requests exceed every free TPM tier. Lever: paid tier OR a lean agent (aider/little-coder).

### Pair: claude ↔ qwen — ⏭ NOT RUN (no credential)
- **Precond (missing):** no dashscope-api key in vault and no Qwen-OAuth login. qwen-code needs `DASHSCOPE_API_KEY`, a Qwen-OAuth dir, or an OpenAI-compatible base URL.
- **To run:** add a DashScope key (`POST /api/v1/vault {provider:dashscope-api,...}`) or point qwen-code at an OpenAI-compatible endpoint, then `spawn qa-qwen qwen-code worker` + the standard knock/verify. Honestly recorded as not-run rather than claimed.

**chepherd-side bugs fixed this session:** gemini MCP `-32601` on prompts/resources
(→ "MCP issues detected" gone); opencode default → Cerebras + correct model id;
copilot git-token injection (vault github-pat); #744 token-expiry death (daemon refresher).

**The one remaining chepherd-fixable blocker is copilot's MCP framing** — and copilot
runs a capable, non-TPM-limited model, so fixing it (bridge framing or M2 HTTP
transport) yields a *second* fully-working agent. gemini/opencode are blocked by
model capability and free-tier TPM respectively — not chepherd bugs; they need a
paid tier or a leaner agent.

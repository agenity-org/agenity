#!/usr/bin/env python3
"""lean-coder — a minimal but REAL chepherd mesh agent for free-tier LLMs.

Off-the-shelf agents fail as free mesh nodes: opencode is too heavy for free
TPM, gemini-cli/qwen-code don't call the chepherd MCP tools, aider has no MCP.
lean-coder speaks chepherd's MCP over HTTP directly and keeps each request small
enough for a free tier — while still being a usable agent:

  * STATEFUL — keeps a bounded rolling conversation so follow-ups have context
    (no "amnesia"). History is capped so it stays within free TPM.
  * INTERACTIVE — plain lines typed into its pane are handled as live chat and
    answered in the pane; knock markers are handled as A2A tasks over MCP.

Contract (provided by the chepherd daemon at spawn):
  CHEPHERD_MCP_URL / CHEPHERD_AGENT_MCP_URL   MCP endpoint (ws normalized to http)
  CHEPHERD_TOKEN        bearer token for the MCP server
  CHEPHERD_AGENT_NAME   this agent's @-handle
  Provider via args:  --base-url URL  --model NAME  --key-env ENVVAR   (env fallbacks)

Knock protocol: the daemon writes one line to stdin per inbound task:
  [chepherd-knock taskID=<uuid> from=<name>]
We get_task(uuid) -> ask the LLM (with history) -> reply via send_to_session
(peer) or alert_human (operator). Anything else typed in is live chat.
"""
import collections
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request

# The daemon exposes MCP as Streamable-HTTP at /mcp and a WS bridge at /mcp/ws.
# We speak HTTP, so prefer the HTTP URL and normalize a ws:// CHEPHERD_MCP_URL.
_mcp = (os.environ.get("CHEPHERD_AGENT_MCP_URL")
        or os.environ.get("CHEPHERD_MCP_URL", "http://127.0.0.1:9090/mcp"))
_mcp = _mcp.replace("wss://", "https://").replace("ws://", "http://")
if _mcp.endswith("/mcp/ws"):
    _mcp = _mcp[:-len("/ws")]
MCP_URL = _mcp
TOKEN = os.environ.get("CHEPHERD_TOKEN", "")
NAME = os.environ.get("CHEPHERD_AGENT_NAME", "lean-coder")

# Provider is selectable per-spawn via agent_args, with env fallbacks.
import argparse as _argparse
_ap = _argparse.ArgumentParser(add_help=False)
_ap.add_argument("--base-url")
_ap.add_argument("--model")
_ap.add_argument("--key-env")
_a, _ = _ap.parse_known_args()
LLM_BASE = (_a.base_url or os.environ.get("LLM_BASE_URL")
            or "https://api.cerebras.ai/v1").rstrip("/")
LLM_MODEL = _a.model or os.environ.get("LLM_MODEL") or "gpt-oss-120b"
_keyenv = _a.key_env or "LLM_API_KEY"
LLM_KEY = (os.environ.get(_keyenv) or os.environ.get("LLM_API_KEY")
           or os.environ.get("CEREBRAS_API_KEY") or os.environ.get("OPENAI_API_KEY") or "")

KNOCK_RE = re.compile(r"\[chepherd-knock taskID=([0-9a-fA-F-]+) from=([^\]]+)\]")

# Bounded conversation memory — last HISTORY_MAX turns shared across knocks +
# interactive chat, so follow-ups keep context. Capped to stay within free TPM
# (Cerebras 30k/min, Groq 6k/min): ~16 turns of short messages ≈ a few k tokens.
HISTORY_MAX = 16
HISTORY = collections.deque(maxlen=HISTORY_MAX)
_rpc_id = 0


def _post(url, payload, headers, timeout=30):
    data = json.dumps(payload).encode()
    headers = dict(headers)
    # Cerebras's edge 403s the default Python-urllib User-Agent — set our own.
    headers.setdefault("User-Agent", "lean-coder/1.0")
    last = None
    for attempt in range(4):
        req = urllib.request.Request(url, data=data, method="POST", headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                body = r.read().decode()
        except urllib.error.HTTPError as e:
            last = e
            # Free tiers throw transient 429/5xx (Gemini 503 overload etc.) — back off + retry.
            if e.code in (429, 500, 502, 503, 504) and attempt < 3:
                time.sleep(2 * (attempt + 1))
                continue
            raise
        if body.lstrip().startswith("data:"):
            for line in reversed(body.splitlines()):
                line = line.strip()
                if line.startswith("data:"):
                    return json.loads(line[5:].strip())
        return json.loads(body)
    raise last


def mcp_call(tool, arguments):
    global _rpc_id
    _rpc_id += 1
    payload = {"jsonrpc": "2.0", "id": _rpc_id, "method": "tools/call",
               "params": {"name": "chepherd." + tool, "arguments": arguments}}
    headers = {"Content-Type": "application/json",
               "Accept": "application/json, text/event-stream",
               "Authorization": "Bearer " + TOKEN,
               # The HTTP MCP server scopes get_task etc. by this header.
               "X-Chepherd-Agent": NAME}
    resp = _post(MCP_URL, payload, headers)
    if "error" in resp:
        raise RuntimeError("mcp %s: %s" % (tool, resp["error"]))
    return resp.get("result", {})


def task_text(get_task_result):
    blob = get_task_result
    if isinstance(blob, dict) and isinstance(blob.get("content"), list):
        for part in blob["content"]:
            if part.get("type") == "text":
                try:
                    blob = json.loads(part["text"]); break
                except Exception:
                    return part["text"]
    inp = (blob or {}).get("input", blob)
    msg = inp.get("message", inp) if isinstance(inp, dict) else {}
    parts = (msg or {}).get("parts") or (inp or {}).get("parts") or []
    texts = [p.get("text", "") for p in parts if isinstance(p, dict)]
    return " ".join(t for t in texts if t).strip() or json.dumps(blob)[:500]


SYSTEM = ("You are %s, a teammate on a chepherd agent mesh backed by %s. "
          "You DO have memory of this conversation (it is provided below). "
          "Be helpful and specific; give concrete, measurable detail when asked." % (NAME, LLM_MODEL))


def ask_llm(prompt):
    """One OpenAI-compatible chat request, WITH the rolling conversation history."""
    messages = [{"role": "system", "content": SYSTEM}]
    messages.extend(HISTORY)
    messages.append({"role": "user", "content": prompt[:6000]})
    payload = {"model": LLM_MODEL, "messages": messages,
               "max_tokens": 800, "temperature": 0.3}
    headers = {"Content-Type": "application/json", "Authorization": "Bearer " + LLM_KEY}
    resp = _post(LLM_BASE + "/chat/completions", payload, headers, timeout=60)
    if "error" in resp:
        raise RuntimeError("llm: %s" % resp["error"])
    msg = resp["choices"][0]["message"]
    text = msg.get("content") or msg.get("reasoning") or "(no content)"
    text = re.sub(r"(?s)<think>.*?</think>", "", text).strip() or "(no content)"
    # Remember this turn so the next message has context (no amnesia).
    HISTORY.append({"role": "user", "content": prompt[:6000]})
    HISTORY.append({"role": "assistant", "content": text})
    return text


def handle_knock(task_id, sender):
    print("[lean-coder] knock taskID=%s from=%s" % (task_id, sender), flush=True)
    env = mcp_call("get_task", {"taskID": task_id})
    prompt = task_text(env)
    print("[lean-coder] task: %s" % prompt[:160], flush=True)
    reply = ask_llm(prompt)
    print("[lean-coder] reply: %s" % reply[:200], flush=True)
    sender = sender.strip()
    if sender in ("operator", "human", "shepherd"):
        mcp_call("alert_human", {"body": "[%s] %s" % (NAME, reply), "kind": "accomplishment"})
    else:
        mcp_call("send_to_session", {"name": sender, "body": reply})
    print("[lean-coder] delivered to %s" % sender, flush=True)


def handle_interactive(line):
    """A plain line typed into our pane — answer it live, in the pane."""
    reply = ask_llm(line)
    print("\n%s> %s\n" % (NAME, reply), flush=True)


def main():
    print("[lean-coder] %s online — model=%s via %s, MCP=%s | stateful + interactive "
          "(type a message, or wait for knocks)" % (NAME, LLM_MODEL, LLM_BASE, MCP_URL), flush=True)
    for raw in sys.stdin:
        line = raw.rstrip("\r\n")
        if not line.strip():
            continue
        m = KNOCK_RE.search(line)
        try:
            if m:
                handle_knock(m.group(1), m.group(2))
            else:
                handle_interactive(line)
        except Exception as e:  # never die on one bad turn
            print("[lean-coder] ERROR: %s" % e, flush=True)


if __name__ == "__main__":
    main()

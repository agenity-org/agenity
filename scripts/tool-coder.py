#!/usr/bin/env python3
"""tool-coder — a lean chepherd mesh agent that takes REAL ACTIONS via the
model's NATIVE function-calling, on FREE-tier LLMs.

This is the tool-calling sibling of scripts/lean-coder.py. lean-coder is
chat-only BY DESIGN: it does get_task -> ask-LLM-for-text -> reply and never
uses the model's tool-calling. tool-coder closes that gap. On a knock it runs a
real native-function-calling loop:

    model emits a tool_call -> we EXECUTE it locally -> feed the result back ->
    repeat until the model returns a final text answer -> reply via chepherd MCP.

The premise (PROVEN live 2026-06-21): Cerebras gpt-oss-120b, Groq
llama-3.3-70b-versatile and Gemini 2.5-flash all emit OpenAI-style `tool_calls`
on /chat/completions. The free constraint is QUANTITY (RPD/TPM), not capability.
So tool-coder keeps context TIGHT (one system line + the task + tool results,
truncated) so a multi-step tool loop still fits free TPM.

Toolset exposed to the model (executed locally inside the agent container):
  * read_file(path)          — read a file, capped to keep TPM low
  * write_file(path,content) — write a file
  * run_bash(cmd)            — run a shell command, capped output

Reply path reuses lean-coder's exact MCP-over-HTTP pattern (send_to_session for
peers, alert_human for the operator).

Contract (provided by the chepherd daemon at spawn):
  CHEPHERD_MCP_URL / CHEPHERD_AGENT_MCP_URL   MCP endpoint (ws normalized to http)
  CHEPHERD_TOKEN        bearer token for the MCP server
  CHEPHERD_AGENT_NAME   this agent's @-handle
  Provider via args:  --base-url URL  --model NAME  --key-env ENVVAR  (env fallbacks)
                      or --model provider/model (e.g. groq/llama-3.3-70b-versatile)

Knock protocol: the daemon writes one line to stdin per inbound task:
  [chepherd-knock taskID=<uuid> from=<name>]
We get_task(uuid) -> run the tool loop -> reply via send_to_session/alert_human.
A plain line typed in is handled the same way (interactive), printed in-pane.
"""
import argparse
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request

# ─── MCP endpoint (same normalization lean-coder uses) ───────────────────────
_mcp = (os.environ.get("CHEPHERD_AGENT_MCP_URL")
        or os.environ.get("CHEPHERD_MCP_URL", "http://127.0.0.1:9090/mcp"))
_mcp = _mcp.replace("wss://", "https://").replace("ws://", "http://")
if _mcp.endswith("/mcp/ws"):
    _mcp = _mcp[:-len("/ws")]
MCP_URL = _mcp
TOKEN = os.environ.get("CHEPHERD_TOKEN", "")
NAME = os.environ.get("CHEPHERD_AGENT_NAME", "tool-coder")

# ─── provider selection (identical scheme to lean-coder) ─────────────────────
_ap = argparse.ArgumentParser(add_help=False)
_ap.add_argument("--base-url")
_ap.add_argument("--model")
_ap.add_argument("--key-env")
_ap.add_argument("--max-steps", type=int, default=8)
_a, _ = _ap.parse_known_args()
LLM_BASE = (_a.base_url or os.environ.get("LLM_BASE_URL")
            or "https://api.cerebras.ai/v1").rstrip("/")
LLM_MODEL = _a.model or os.environ.get("LLM_MODEL") or "gpt-oss-120b"
_keyenv = _a.key_env or "LLM_API_KEY"
_PROVIDERS = {
    "cerebras": ("https://api.cerebras.ai/v1", "CEREBRAS_API_KEY"),
    "groq": ("https://api.groq.com/openai/v1", "GROQ_API_KEY"),
    "gemini": ("https://generativelanguage.googleapis.com/v1beta/openai", "GEMINI_API_KEY"),
}
if "/" in LLM_MODEL:
    _prov, _rest = LLM_MODEL.split("/", 1)
    if _prov in _PROVIDERS:
        if not _a.base_url:
            LLM_BASE = _PROVIDERS[_prov][0]
        if not _a.key_env:
            _keyenv = _PROVIDERS[_prov][1]
        LLM_MODEL = _rest
LLM_KEY = (os.environ.get(_keyenv) or os.environ.get("LLM_API_KEY")
           or os.environ.get("CEREBRAS_API_KEY") or os.environ.get("OPENAI_API_KEY") or "")
MAX_STEPS = _a.max_steps

KNOCK_RE = re.compile(r"\[chepherd-knock taskID=([0-9a-fA-F-]+) from=([^\]]+)\]")

# Output caps keep each round-trip within free TPM. Tool results are the main
# token sink in a tool loop, so cap them hard.
TOOL_OUT_CAP = 4000     # chars of any single tool result fed back to the model
FILE_READ_CAP = 8000    # chars read_file returns
BASH_TIMEOUT = 30       # seconds per run_bash

_rpc_id = 0


# ─── HTTP (shared with lean-coder's approach: urllib, UA set, SSE-aware, retry) ─
def _post(url, payload, headers, timeout=60):
    data = json.dumps(payload).encode()
    headers = dict(headers)
    headers.setdefault("User-Agent", "tool-coder/1.0")
    last = None
    for attempt in range(4):
        req = urllib.request.Request(url, data=data, method="POST", headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                body = r.read().decode()
        except urllib.error.HTTPError as e:
            last = e
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


# ─── the local toolset (executed inside the agent) ───────────────────────────
TOOLS_SPEC = [
    {"type": "function", "function": {
        "name": "read_file",
        "description": "Read a text file from the agent's filesystem and return its contents.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string", "description": "Absolute or relative file path."}},
            "required": ["path"]}}},
    {"type": "function", "function": {
        "name": "write_file",
        "description": "Write (create or overwrite) a text file on the agent's filesystem.",
        "parameters": {"type": "object", "properties": {
            "path": {"type": "string", "description": "File path to write."},
            "content": {"type": "string", "description": "Full file content."}},
            "required": ["path", "content"]}}},
    {"type": "function", "function": {
        "name": "run_bash",
        "description": "Run a shell command in the agent's container and return its stdout/stderr. "
                       "Use this for ls, date, git, grep, running tests, etc.",
        "parameters": {"type": "object", "properties": {
            "cmd": {"type": "string", "description": "The shell command line to run."}},
            "required": ["cmd"]}}},
]


def _cap(s, n=TOOL_OUT_CAP):
    s = s if isinstance(s, str) else str(s)
    return s if len(s) <= n else s[:n] + "\n…[truncated %d chars]" % (len(s) - n)


def tool_read_file(path):
    try:
        with open(path, "r", errors="replace") as f:
            return _cap(f.read(FILE_READ_CAP), FILE_READ_CAP)
    except Exception as e:
        return "ERROR reading %s: %s" % (path, e)


def tool_write_file(path, content):
    try:
        d = os.path.dirname(path)
        if d:
            os.makedirs(d, exist_ok=True)
        with open(path, "w") as f:
            f.write(content or "")
        return "wrote %d bytes to %s" % (len(content or ""), path)
    except Exception as e:
        return "ERROR writing %s: %s" % (path, e)


def tool_run_bash(cmd):
    try:
        p = subprocess.run(["/bin/sh", "-c", cmd], capture_output=True,
                           text=True, timeout=BASH_TIMEOUT)
        out = (p.stdout or "") + (("\n[stderr] " + p.stderr) if p.stderr else "")
        if p.returncode != 0:
            out += "\n[exit %d]" % p.returncode
        return _cap(out.strip() or "(no output)")
    except subprocess.TimeoutExpired:
        return "ERROR: command timed out after %ds" % BASH_TIMEOUT
    except Exception as e:
        return "ERROR running cmd: %s" % e


DISPATCH = {
    "read_file": lambda a: tool_read_file(a.get("path", "")),
    "write_file": lambda a: tool_write_file(a.get("path", ""), a.get("content", "")),
    "run_bash": lambda a: tool_run_bash(a.get("cmd", "")),
}


SYSTEM = ("You are %s, a teammate on a chepherd agent mesh, backed by %s. "
          "You can take real actions on this machine using the provided tools "
          "(read_file, write_file, run_bash). When a question needs real data "
          "from the system (files, dates, command output), CALL A TOOL — do not "
          "guess or fabricate. After you have what you need, give a concise, "
          "concrete final answer that quotes the actual tool output. Keep tool "
          "use minimal to stay within rate limits." % (NAME, LLM_MODEL))


def _chat(messages):
    """One OpenAI-compatible chat-completions request WITH the toolset."""
    payload = {"model": LLM_MODEL, "messages": messages,
               "tools": TOOLS_SPEC, "tool_choice": "auto",
               "max_tokens": 1024, "temperature": 0.2}
    headers = {"Content-Type": "application/json", "Authorization": "Bearer " + LLM_KEY}
    resp = _post(LLM_BASE + "/chat/completions", payload, headers, timeout=90)
    if "error" in resp:
        raise RuntimeError("llm: %s" % resp["error"])
    return resp["choices"][0]


def run_tool_loop(task):
    """The native-function-calling loop.

    Returns (text, steps, ok) where ok is False when the turn could not produce
    a real answer (model returned no content, or the step budget was exhausted).
    Callers use ok to tag the operator reply kind correctly — see handle_knock.

    Context stays tight: system + task + (assistant tool_calls / tool results),
    each tool result capped. This is what keeps a multi-step loop inside free TPM.
    """
    messages = [{"role": "system", "content": SYSTEM},
                {"role": "user", "content": task[:6000]}]
    steps = []
    for step in range(MAX_STEPS):
        choice = _chat(messages)
        msg = choice["message"]
        tool_calls = msg.get("tool_calls") or []
        if not tool_calls:
            # Final answer. Strip qwen-style <think> + reasoning fallback.
            raw = msg.get("content") or msg.get("reasoning")
            text = re.sub(r"(?s)<think>.*?</think>", "", raw or "").strip()
            return (text or "(no content)", steps, bool(text))
        # The assistant turn that requested tools MUST be echoed back verbatim
        # (with its tool_calls) before the tool results, per the OpenAI schema.
        messages.append({"role": "assistant",
                         "content": msg.get("content") or "",
                         "tool_calls": tool_calls})
        for tc in tool_calls:
            fn = tc.get("function", {})
            tname = fn.get("name", "")
            try:
                targs = json.loads(fn.get("arguments") or "{}")
            except Exception:
                targs = {}
            handler = DISPATCH.get(tname)
            result = handler(targs) if handler else "ERROR: unknown tool %s" % tname
            steps.append((tname, targs, result))
            print("[tool-coder] step %d: %s(%s) -> %s"
                  % (step + 1, tname, json.dumps(targs)[:120], _cap(result, 200)),
                  flush=True)
            messages.append({"role": "tool", "tool_call_id": tc.get("id", ""),
                             "name": tname, "content": _cap(result)})
    # Ran out of steps — make one last call WITHOUT tools to force a text answer.
    payload = {"model": LLM_MODEL, "messages": messages,
               "max_tokens": 1024, "temperature": 0.2}
    headers = {"Content-Type": "application/json", "Authorization": "Bearer " + LLM_KEY}
    resp = _post(LLM_BASE + "/chat/completions", payload, headers, timeout=90)
    m = resp["choices"][0]["message"]
    raw = m.get("content") or m.get("reasoning")
    text = re.sub(r"(?s)<think>.*?</think>", "", raw or "").strip()
    # Falling through the step budget without a clean final answer is a failed
    # turn — ok=False so the operator reply is tagged failure, not accomplishment.
    return (text or "(step budget exhausted)", steps, bool(text))


def _deliver(sender, reply, ok):
    """Send `reply` to `sender`. Operator replies carry a kind that reflects the
    turn outcome: accomplishment on success, failure when the turn errored —
    NOT a blanket accomplishment (an error tagged accomplishment is theater)."""
    sender = sender.strip()
    if sender in ("operator", "human", "shepherd"):
        kind = "accomplishment" if ok else "failure"
        mcp_call("alert_human", {"body": "[%s] %s" % (NAME, reply), "kind": kind})
    else:
        mcp_call("send_to_session", {"name": sender, "body": reply})
    print("[tool-coder] delivered to %s (ok=%s)" % (sender, ok), flush=True)


def handle_knock(task_id, sender):
    print("[tool-coder] knock taskID=%s from=%s" % (task_id, sender), flush=True)
    env = mcp_call("get_task", {"taskID": task_id})
    prompt = task_text(env)
    print("[tool-coder] task: %s" % prompt[:160], flush=True)
    try:
        reply, steps, ok = run_tool_loop(prompt)
    except Exception as e:
        # A hard error mid-turn is a failed turn — tell the sender, tagged
        # failure for the operator, instead of silently swallowing it.
        print("[tool-coder] tool loop ERROR: %s" % e, flush=True)
        _deliver(sender, "tool-coder errored handling this task: %s" % e, ok=False)
        return
    print("[tool-coder] %d tool step(s); reply: %s" % (len(steps), reply[:200]), flush=True)
    _deliver(sender, reply, ok)


def handle_interactive(line):
    reply, steps, _ok = run_tool_loop(line)
    print("\n%s> %s\n" % (NAME, reply), flush=True)


def main():
    print("[tool-coder] %s online — model=%s via %s, MCP=%s | native tool-calling "
          "(read_file/write_file/run_bash), max_steps=%d"
          % (NAME, LLM_MODEL, LLM_BASE, MCP_URL, MAX_STEPS), flush=True)
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
            print("[tool-coder] ERROR: %s" % e, flush=True)


if __name__ == "__main__":
    main()

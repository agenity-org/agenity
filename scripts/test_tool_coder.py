#!/usr/bin/env python3
"""Unit tests for tool-coder.py's NATIVE-FUNCTION-CALLING loop — the behavior
that distinguishes it from chat-only lean-coder. Sibling of
scripts/test_lean_coder.py and uses the same style: a fresh side-effect-free
import (parsing only; main() is __main__-guarded) with the network mocked.

What's covered (the tool-loop contract that was shipped untested):
  1. tool_calls turn → the assistant turn is echoed back WITH its tool_calls,
     the tool is EXECUTED locally, a role:"tool" result is appended, and the
     loop CONTINUES (per the OpenAI tool-calling message schema).
  2. plain-text turn → the loop TERMINATES and returns the final answer.
  3. --max-steps BOUNDS a runaway loop (model that never stops calling tools):
     exactly MAX_STEPS tool rounds, then one final no-tools call for the answer.
  4. handle_knock end-to-end: get_task → run loop → reply via send_to_session
     (peer) / alert_human (operator), mirroring test_lean_coder's knock test.
  5. provider/arg parsing (--max-steps, provider/model prefix) — the gotchas.

Run: python3 scripts/test_tool_coder.py
"""
import importlib.util
import os
import sys

TOOLCODER = os.path.join(os.path.dirname(__file__), "tool-coder.py")
_ENV_KEYS = [
    "LLM_MODEL", "LLM_BASE_URL", "LLM_API_KEY", "CHEPHERD_MCP_URL",
    "CHEPHERD_AGENT_MCP_URL", "CHEPHERD_TOKEN", "CHEPHERD_AGENT_NAME",
    "GROQ_API_KEY", "GEMINI_API_KEY", "CEREBRAS_API_KEY", "OPENAI_API_KEY",
]


def load(env=None, argv=None):
    """Fresh import of tool-coder.py with exactly the given env + argv.

    Provider selection AND --max-steps are parsed at module import from
    sys.argv, so argv must be set before exec_module (matches production: the
    daemon spawns `python3 tool-coder.py --model … --max-steps …`).
    """
    env = env or {}
    for k in _ENV_KEYS:
        os.environ.pop(k, None)
    os.environ.update(env)
    saved_argv = sys.argv
    sys.argv = ["tool-coder.py"] + (argv or [])
    try:
        spec = importlib.util.spec_from_file_location("toolcoder_under_test", TOOLCODER)
        m = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(m)
    finally:
        sys.argv = saved_argv
    return m


def check(name, got, want):
    if got != want:
        raise AssertionError("FAIL %s: got %r, want %r" % (name, got, want))
    print("  ok:", name)


def check_true(name, cond):
    if not cond:
        raise AssertionError("FAIL %s: condition false" % name)
    print("  ok:", name)


# ─── a scripted fake model: returns the next queued "choice" per _chat call ───
def scripted_chat(turns):
    """Return a fake _chat(messages) that yields turns[0], turns[1], … and
    records every `messages` snapshot it was called with (so the test can
    assert the assistant-echo + tool-result threading)."""
    state = {"i": 0, "seen": []}

    def _chat(messages):
        state["seen"].append([dict(msg) for msg in messages])
        turn = turns[min(state["i"], len(turns) - 1)]
        state["i"] += 1
        return turn

    _chat.state = state
    return _chat


def tool_call(call_id, name, args_json):
    return {"message": {"content": "", "tool_calls": [
        {"id": call_id, "type": "function",
         "function": {"name": name, "arguments": args_json}}]}}


def final_text(text):
    return {"message": {"content": text}}


def main():
    # ── 1. tool_calls → execute + echo assistant turn + append role:tool, CONTINUE
    # Model: turn 1 calls run_bash("echo hi"); turn 2 returns the final answer.
    m = load()
    m._chat = scripted_chat([
        tool_call("call-1", "run_bash", '{"cmd": "echo TOOLCODER_MARKER"}'),
        final_text("the command printed TOOLCODER_MARKER"),
    ])
    reply, steps = m.run_tool_loop("run echo and tell me what it printed")
    check("tool loop final reply", reply, "the command printed TOOLCODER_MARKER")
    check("tool loop executed exactly 1 tool", len(steps), 1)
    tname, targs, result = steps[0]
    check("tool name executed", tname, "run_bash")
    check_true("tool actually ran (stdout captured)", "TOOLCODER_MARKER" in result)

    # The SECOND _chat call's message list must contain: the assistant turn
    # echoed back WITH its tool_calls, immediately followed by a role:"tool"
    # result for that call_id. This is the OpenAI schema requirement that, if
    # broken, makes providers 400 the follow-up request.
    second_call_msgs = m._chat.state["seen"][1]
    roles = [msg.get("role") for msg in second_call_msgs]
    check("message roles after tool round", roles, ["system", "user", "assistant", "tool"])
    asst = second_call_msgs[2]
    check_true("assistant turn echoed WITH tool_calls", bool(asst.get("tool_calls")))
    check("echoed tool_call id", asst["tool_calls"][0]["id"], "call-1")
    toolmsg = second_call_msgs[3]
    check("tool result role", toolmsg["role"], "tool")
    check("tool result threaded to call id", toolmsg["tool_call_id"], "call-1")
    check_true("tool result content carries stdout", "TOOLCODER_MARKER" in toolmsg["content"])

    # ── 2. plain text immediately → terminate, ZERO tool executions.
    # strict_chat raises if called MORE times than turns queued, so a loop that
    # fails to terminate on plain text surfaces as an explicit FAIL (not a hang).
    def strict_chat(turns):
        state = {"i": 0}

        def _chat(messages):
            if state["i"] >= len(turns):
                raise AssertionError(
                    "loop did not terminate: _chat called %d times for %d queued turns "
                    "(plain-text turn should have ended the loop)" % (state["i"] + 1, len(turns)))
            turn = turns[state["i"]]
            state["i"] += 1
            return turn

        _chat.state = state
        return _chat

    m = load()
    m._chat = strict_chat([final_text("42")])
    reply, steps = m.run_tool_loop("what is the answer?")
    check("plain-text terminates immediately", reply, "42")
    check("plain-text runs no tools", len(steps), 0)
    check("plain-text issues exactly one _chat call", m._chat.state["i"], 1)

    # ── 2b. <think> scratchpad is stripped from the final answer (qwen3)
    m = load()
    m._chat = scripted_chat([final_text("<think>scratch</think>clean answer")])
    reply, _ = m.run_tool_loop("x")
    check("final answer strips <think>", reply, "clean answer")

    # ── 3. --max-steps BOUNDS a runaway loop. Model ALWAYS asks for a tool.
    m = load(argv=["--max-steps", "3"])
    check("max-steps parsed from argv", m.MAX_STEPS, 3)
    always_tool = scripted_chat([
        tool_call("c", "run_bash", '{"cmd": "true"}')  # repeated forever
    ])
    m._chat = always_tool
    # After MAX_STEPS rounds the loop falls through to ONE final no-tools call
    # via _post; mock that so no network is touched and it returns a forced answer.
    post_calls = {"n": 0}

    def fake_post(url, payload, headers, timeout=60):
        post_calls["n"] += 1
        # The fall-through call MUST omit the tools key (forces a text answer).
        check_true("fall-through _post sends NO tools", "tools" not in payload)
        return {"choices": [{"message": {"content": "forced final after budget"}}]}

    m._post = fake_post
    reply, steps = m.run_tool_loop("loop forever")
    check("runaway bounded to MAX_STEPS _chat rounds", m._chat.state["i"], 3)
    check("runaway executed MAX_STEPS tools", len(steps), 3)
    check("fall-through made exactly one final _post", post_calls["n"], 1)
    check("runaway returns the forced final text", reply, "forced final after budget")

    # ── 4. handle_knock: get_task → loop → reply routing (mirrors lean-coder test)
    # 4a. peer sender → send_to_session
    m = load({"CHEPHERD_AGENT_NAME": "tool-coder"})
    m._chat = scripted_chat([final_text("done, boss")])
    calls = []

    def fake_mcp(tool, args):
        calls.append((tool, args))
        if tool == "get_task":
            return {"input": {"message": {"parts": [{"text": "do the thing"}]}}}
        return {"ok": True}

    m.mcp_call = fake_mcp
    m.handle_knock("019ed169-1092-7170-9efc-ee681e7e9177", "alice")
    tools_called = [c[0] for c in calls]
    check("knock(peer): get_task then send_to_session", tools_called,
          ["get_task", "send_to_session"])
    check("knock(peer): get_task used the knock taskID", calls[0][1]["taskID"],
          "019ed169-1092-7170-9efc-ee681e7e9177")
    check("knock(peer): reply addressed to sender", calls[1][1]["name"], "alice")
    check("knock(peer): reply body is the loop answer", calls[1][1]["body"], "done, boss")

    # 4b. operator sender → alert_human (NOT send_to_session)
    m = load({"CHEPHERD_AGENT_NAME": "tool-coder"})
    m._chat = scripted_chat([final_text("status: green")])
    calls = []
    m.mcp_call = fake_mcp = (lambda tool, args: (
        calls.append((tool, args)) or
        ({"input": {"message": {"parts": [{"text": "report"}]}}} if tool == "get_task" else {"ok": True})))
    m.handle_knock("tid", "operator")
    check("knock(operator): routes via alert_human", [c[0] for c in calls],
          ["get_task", "alert_human"])
    check_true("knock(operator): alert body carries the answer",
               "status: green" in calls[1][1]["body"])

    # ── 5. provider/arg parsing gotchas (same scheme as lean-coder)
    m = load(argv=["--model", "groq/llama-3.3-70b-versatile"])
    check("groq base from prefix", m.LLM_BASE, "https://api.groq.com/openai/v1")
    check("groq model stripped", m.LLM_MODEL, "llama-3.3-70b-versatile")
    m = load()
    check("default base (cerebras)", m.LLM_BASE, "https://api.cerebras.ai/v1")
    check("default model", m.LLM_MODEL, "gpt-oss-120b")
    check("default max-steps", m.MAX_STEPS, 8)

    # KNOCK_RE + task_text parity with lean-coder
    m = load()
    mt = m.KNOCK_RE.search(
        "[chepherd-knock taskID=019ed169-1092-7170-9efc-ee681e7e9177 from=operator]")
    check("knock taskID", mt.group(1), "019ed169-1092-7170-9efc-ee681e7e9177")
    check("knock from", mt.group(2), "operator")
    check("task_text from A2A envelope",
          m.task_text({"input": {"message": {"parts": [{"text": "ping"}]}}}), "ping")

    print("ALL PASS")


if __name__ == "__main__":
    main()

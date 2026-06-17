#!/usr/bin/env python3
"""Unit tests for lean-coder.py's config parsing — the gotchas that each once
blocked it (ws→http MCP URL, provider self-config from a "provider/model"
prefix, nested model strings, default provider). The parsing runs at module
import; main() is __main__-guarded, so importing is side-effect-free beyond the
parse. Proven live (Cerebras+Groq round-trips, commits d4485e1/a3fe462) — this
guards against regression.

Run: python3 scripts/test_lean_coder.py
"""
import importlib.util
import os

LEANCODER = os.path.join(os.path.dirname(__file__), "lean-coder.py")
_ENV_KEYS = [
    "LLM_MODEL", "LLM_BASE_URL", "LLM_API_KEY", "CHEPHERD_MCP_URL",
    "CHEPHERD_AGENT_MCP_URL", "CHEPHERD_TOKEN", "CHEPHERD_AGENT_NAME",
    "GROQ_API_KEY", "GEMINI_API_KEY", "CEREBRAS_API_KEY", "OPENAI_API_KEY",
]


def load(env):
    """Fresh import of lean-coder.py with exactly the given env (parsing only)."""
    for k in _ENV_KEYS:
        os.environ.pop(k, None)
    os.environ.update(env)
    spec = importlib.util.spec_from_file_location("leancoder_under_test", LEANCODER)
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    return m


def check(name, got, want):
    if got != want:
        raise AssertionError("FAIL %s: got %r, want %r" % (name, got, want))
    print("  ok:", name)


def main():
    # Gotcha 1 — daemon injects ws://…/mcp/ws; HTTP client must convert to http://…/mcp
    m = load({"CHEPHERD_MCP_URL": "ws://127.0.0.1:9090/mcp/ws"})
    check("ws->http + /mcp/ws->/mcp", m.MCP_URL, "http://127.0.0.1:9090/mcp")

    # wss -> https
    m = load({"CHEPHERD_MCP_URL": "wss://host:9090/mcp/ws"})
    check("wss->https", m.MCP_URL, "https://host:9090/mcp")

    # CHEPHERD_AGENT_MCP_URL (already http) is preferred over the ws one
    m = load({"CHEPHERD_AGENT_MCP_URL": "http://x:9090/mcp",
              "CHEPHERD_MCP_URL": "ws://y/mcp/ws"})
    check("agent-mcp-url preferred", m.MCP_URL, "http://x:9090/mcp")

    # default MCP url when neither env is set
    m = load({})
    check("default mcp url", m.MCP_URL, "http://127.0.0.1:9090/mcp")

    # Gotcha — provider self-config from a "provider/model" prefix: groq
    m = load({"LLM_MODEL": "groq/llama-3.3-70b-versatile"})
    check("groq base", m.LLM_BASE, "https://api.groq.com/openai/v1")
    check("groq model", m.LLM_MODEL, "llama-3.3-70b-versatile")
    check("groq keyenv", m._keyenv, "GROQ_API_KEY")

    # gemini provider
    m = load({"LLM_MODEL": "gemini/gemini-2.5-flash"})
    check("gemini base", m.LLM_BASE, "https://generativelanguage.googleapis.com/v1beta/openai")
    check("gemini model", m.LLM_MODEL, "gemini-2.5-flash")
    check("gemini keyenv", m._keyenv, "GEMINI_API_KEY")

    # default (no model) -> Cerebras gpt-oss-120b
    m = load({})
    check("default base (cerebras)", m.LLM_BASE, "https://api.cerebras.ai/v1")
    check("default model", m.LLM_MODEL, "gpt-oss-120b")

    # nested model "groq/qwen/qwen3-32b" — split on FIRST "/" only
    m = load({"LLM_MODEL": "groq/qwen/qwen3-32b"})
    check("nested provider base", m.LLM_BASE, "https://api.groq.com/openai/v1")
    check("nested model preserved", m.LLM_MODEL, "qwen/qwen3-32b")

    # unknown provider prefix is NOT treated as a provider (kept as model id)
    m = load({"LLM_MODEL": "openai/gpt-4"})
    check("unknown-prefix keeps default base", m.LLM_BASE, "https://api.cerebras.ai/v1")
    check("unknown-prefix keeps full model", m.LLM_MODEL, "openai/gpt-4")

    # KNOCK_RE — parse the daemon's knock marker into (taskID, from)
    m = load({})
    mt = m.KNOCK_RE.search(
        "[chepherd-knock taskID=019ed169-1092-7170-9efc-ee681e7e9177 from=operator]")
    check("knock taskID", mt.group(1), "019ed169-1092-7170-9efc-ee681e7e9177")
    check("knock from", mt.group(2), "operator")

    # task_text — extract the sender's text from an A2A get_task envelope
    check("task_text from A2A envelope",
          m.task_text({"input": {"message": {"parts": [{"text": "what is 7x8?"}]}}}),
          "what is 7x8?")

    # ask_llm — <think> stripping (qwen3), reasoning-field fallback, and bounded
    # HISTORY (the amnesia fix). Mock _post so no network is touched.
    m = load({})
    m._post = lambda url, payload, headers, timeout=60: {
        "choices": [{"message": {"content": "<think>scratchpad</think>56"}}]}
    check("ask_llm strips <think> (qwen3)", m.ask_llm("7*8?"), "56")
    check("ask_llm appends turn to HISTORY (amnesia fix)", len(m.HISTORY), 2)

    m = load({})
    m._post = lambda *a, **k: {
        "choices": [{"message": {"reasoning": "answer is 7", "content": ""}}]}
    check("ask_llm reasoning-field fallback", m.ask_llm("x"), "answer is 7")

    # amnesia fix is BOUNDED — HISTORY caps at HISTORY_MAX (2 entries/turn)
    m = load({})
    m._post = lambda *a, **k: {"choices": [{"message": {"content": "ok"}}]}
    for i in range(20):
        m.ask_llm("q%d" % i)
    check("ask_llm HISTORY bounded (<=HISTORY_MAX)", len(m.HISTORY) <= m.HISTORY_MAX, True)

    print("ALL PASS")


if __name__ == "__main__":
    main()

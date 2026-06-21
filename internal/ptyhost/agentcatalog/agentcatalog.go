// Package agentcatalog is the single source of truth for the
// agent-slug -> binary mapping inside pty-server.
//
// The slugs MUST stay in lock-step with three sibling sites:
//
//   - FE catalogue:    products/catalyst/bootstrap/ui/src/lib/sandbox.api.ts
//     (SANDBOX_AGENTS)
//   - catalyst-api:    products/catalyst/bootstrap/api/internal/handler/
//     sandbox_sessions.go (sandboxAllowedAgents)
//   - Chart CRD enum:  spec.agentCatalogue.items.enum
//
// Adding an agent to pty-server WITHOUT also adding it to the three
// upstream sites will surface as a 400 invalid-agent from catalyst-api
// long before the request reaches pty-server, so the four-site update
// is a single PR by convention.
//
// The hardcoded Builtin table can be augmented at runtime by mounting a
// JSON override at /etc/openova/sandbox-agents.json (path overridable
// via the CHEPHERD_AGENTS_PATH env var); entries in the override
// supersede builtins by Slug. The override is a deployment escape
// hatch for one-off experiments — production should always extend
// Builtin instead so the four-site invariant is auditable in git.
//
// Design source: tracked in TBD-P4 #1986 sub-break B3.
package agentcatalog

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
)

// ErrUnknownAgent is returned by Lookup when the slug is not present
// in either the builtin table or the override file.
var ErrUnknownAgent = errors.New("agentcatalog: unknown agent slug")

// defaultOverridePath is where the optional JSON override is read
// from. Operators can re-point it via CHEPHERD_AGENTS_PATH.
const defaultOverridePath = "/etc/openova/sandbox-agents.json"

// Agent is one row in the catalogue — the canonical record of how to
// spawn a particular agent slug.
type Agent struct {
	// Slug is the wire-level identifier. Must match the FE catalogue.
	Slug string `json:"slug"`
	// Binary is the absolute path inside the pty-server / agent-bundle
	// image. The B1 agent-bundle image installs the canonical binaries
	// under /usr/local/bin/<slug>.
	Binary string `json:"binary"`
	// DefaultArgs is appended verbatim after Binary; ExtraArgs from the
	// caller are appended after these.
	DefaultArgs []string `json:"defaultArgs,omitempty"`
	// DefaultCwd is the child's working directory. "" = inherit
	// pty-server's CWD (typically /workspace per the StatefulSet).
	DefaultCwd string `json:"defaultCwd,omitempty"`
	// RequiredEnv lists environment variables whose ABSENCE returns a
	// clean 400 at create() time — a fail-fast guard against the
	// black-screen pattern when the controller hasn't wired the agent's
	// required gateway / API-key env yet.
	RequiredEnv []string `json:"requiredEnv,omitempty"`
	// Notes is free-form; surfaced in /healthz/agents (future) and
	// useful for code-reviewers.
	Notes string `json:"notes,omitempty"`
	// SubmitSequence is the byte sequence written to the PTY after a
	// message body to submit it (e.g. claude-code et al. interpret
	// CR (0x0d) as submit). Empty defaults to []byte{0x0d}. Flavor-
	// specific override lets multi-line / Ctrl+Enter modes plumb a
	// different sequence (per architect 2026-05-29 scope-lock).
	// Refs #208.
	SubmitSequence []byte `json:"submitSequence,omitempty"`
}

// EffectiveSubmitSequence returns the flavor's SubmitSequence, or the
// canonical CR default when empty. Callers use this rather than
// referencing SubmitSequence directly so the default stays centralized.
func (a Agent) EffectiveSubmitSequence() []byte {
	if len(a.SubmitSequence) == 0 {
		return []byte{0x0d}
	}
	return a.SubmitSequence
}

// Builtin is the compiled-in registry. Operator overrides via
// /etc/openova/sandbox-agents.json supersede entries here by Slug.
//
// Order is significant only as a coding convention (lexicographic).
// The 6 real-agent rows match the four upstream sites; `sovereign-shell`
// is an extra rescue row guaranteed to be present so a broken agent
// bundle never leaves the operator staring at a black screen.
var Builtin = []Agent{
	{
		Slug:        "aider",
		Binary:      "/usr/local/bin/aider",
		DefaultArgs: []string{"--yes-always", "--no-auto-commits"},
		DefaultCwd:  "/workspace",
		RequiredEnv: []string{"OPENAI_BASE_URL", "OPENAI_API_KEY"},
	},
	{
		Slug:        "claude-code",
		Binary:      "/usr/bin/claude",
		DefaultArgs: []string{"--dangerously-skip-permissions"},
		DefaultCwd:  "/workspace",
		RequiredEnv: []string{"LLM_GATEWAY_URL"},
		Notes:       "Anthropic Claude Code CLI. The Bypass-permissions confirmation dialog is suppressed via autoPermissionsNotificationCount=99 written into the agent's ~/.claude.json by chepherd's secrets materializer.",
	},
	{
		Slug:        "copilot",
		Binary:      "/usr/local/bin/copilot",
		DefaultArgs: []string{"--allow-all-tools"},
		DefaultCwd:  "/workspace",
		RequiredEnv: nil,
		Notes:       "GitHub Copilot CLI (@github/copilot). --allow-all-tools auto-approves all tool executions (documented; --allow-all/--yolo additionally bundles --allow-all-paths/--allow-all-urls). RequiredEnv empty: auth is GitHub-OAuth login (copilot-oauth provider).",
	},
	{
		Slug:        "cursor-agent",
		Binary:      "/usr/local/bin/cursor-agent",
		DefaultArgs: []string{},
		DefaultCwd:  "/workspace",
		RequiredEnv: []string{"LLM_GATEWAY_URL"},
	},
	{
		Slug:   "gemini-cli",
		Binary: "/usr/local/bin/gemini",
		// #79 — pin gemini-3.1-flash-lite. The earlier gemini-2.5-flash pin
		// (#743) does NOT stick: gemini-cli v0.46 under gemini-api-key auth
		// force-remaps EVERY 2.x/3.x "flash" model to gemini-3.5-flash
		// (resolveModel: useGemini3_5Flash && isFlashModel(resolved) →
		// DEFAULT_GEMINI_FLASH_MODEL, set to "gemini-3.5-flash" by
		// setFlashModels for api-key auth). Proven live 2026-06-21: a
		// --model gemini-2.5-flash run hit "limit: 20, model: gemini-3.5-flash".
		// gemini-3.1-flash-lite ends in "flash-lite" (NOT "flash"), so
		// isFlashModel() is false and the pin survives — verified live: the
		// agent replied on gemini-3.1-flash-lite with NO remap. It also has
		// free daily headroom right now (200) when 2.5/3.5-flash + their
		// flash-lite siblings are 20/day-exhausted. NOTE: all free-tier gemini
		// models are 20 req/day; if 3.1-flash-lite exhausts, gemini-cli still
		// raises the interactive "Usage limit reached / Keep trying" modal,
		// which an unattended agent CANNOT dismiss (gemini-cli has no
		// non-interactive auto-fallback for a daily TerminalQuotaError). For a
		// resilient Google slot under sustained load, prefer opencode pointed
		// at the Gemini API or a different provider — see #79 report.
		DefaultArgs: []string{"--yolo", "--model", "gemini-3.1-flash-lite"},
		DefaultCwd:  "/workspace",
		RequiredEnv: nil,
		Notes:       "Google Gemini CLI (@google/gemini-cli). --yolo auto-approves all tool calls (the documented full-autonomy approval mode). RequiredEnv is empty by design: the free path is Google-OAuth login dir (gemini-oauth provider, file-mount) — a GEMINI_API_KEY (google-api provider) is the alternative, so neither is mandatory at create() time.",
	},
	{
		Slug:        "little-coder",
		Binary:      "/usr/local/bin/little-coder",
		DefaultArgs: []string{},
		DefaultCwd:  "/workspace",
		RequiredEnv: []string{"OPENAI_BASE_URL", "OPENAI_API_KEY"},
	},
	{
		Slug:        "opencode",
		Binary:      "/usr/local/bin/opencode",
		DefaultArgs: []string{},
		DefaultCwd:  "/workspace",
		RequiredEnv: []string{"OPENAI_BASE_URL", "OPENAI_API_KEY"},
		Notes:       "Standard agent for raw OpenAI-compatible free providers (Cerebras 30k TPM, Groq 12k TPM). The daemon's writeFlavorMCPConfig emits a TPM-fit opencode.json (measured live 2026-06-21): tools allow-list (37→4 tools), focused build-agent system prompt (system 21,850→1,257 chars), instructions:[], and a per-model output-token cap (max_tokens 40,960→1,024 so Groq's prompt+reserved-output total fits 12k). A trivial turn drops 11,034→730 prompt_tokens; a full knock→get_task→reply round-trip fits Cerebras 30k (max 3,783/req) and Groq 12k (sum 8,272). The output cap also avoids the Cerebras reasoning_content replay error (3/3 clean round-trips).",
	},
	{
		Slug:        "qwen-code",
		Binary:      "/usr/local/bin/qwen-code",
		DefaultArgs: []string{"--yolo"},
		DefaultCwd:  "/workspace",
		RequiredEnv: nil,
		Notes:       "Qwen Code CLI (@qwen-code/qwen-code, a gemini-cli fork; npm binary `qwen` symlinked to /usr/local/bin/qwen-code). --yolo auto-approves all tool calls. RequiredEnv cleared (#741): the free path is Qwen-OAuth login (qwen-oauth provider, file-mount); a DASHSCOPE_API_KEY (dashscope-api) or OpenAI-compatible base URL are alternatives — none mandatory at create() time.",
	},
	{
		Slug:        "lean-coder",
		Binary:      "/usr/local/bin/lean-coder",
		DefaultArgs: []string{},
		DefaultCwd:  "/workspace",
		RequiredEnv: nil,
		Notes:       "chepherd-native ultra-lean MCP mesh agent (scripts/lean-coder.py). Speaks chepherd MCP over HTTP directly and keeps each LLM request tiny (one system line + the task text), so a full knock->reply round-trip is a single small request that fits free-tier TPM (Cerebras 30k/5RPM, Groq 6k) — the caps opencode busts. Reads CEREBRAS_API_KEY by default (or LLM_API_KEY/LLM_BASE_URL/LLM_MODEL to point elsewhere). The free mesh node that off-the-shelf CLIs can't be: opencode too heavy, gemini/qwen don't emit tool calls, aider has no MCP.",
	},
	{
		Slug:        "tool-coder",
		Binary:      "/usr/local/bin/tool-coder",
		DefaultArgs: []string{},
		DefaultCwd:  "/workspace",
		RequiredEnv: nil,
		Notes:       "lean-coder's NATIVE-FUNCTION-CALLING sibling (scripts/tool-coder.py). lean-coder is chat-only by design; tool-coder runs a REAL tool loop: the model emits OpenAI-style tool_calls, tool-coder executes read_file/write_file/run_bash locally, feeds results back, and repeats until a final answer, then replies over chepherd MCP. Context is kept tight (system + task + capped tool results) so a multi-step loop still fits free TPM. Proven live 2026-06-21: Cerebras gpt-oss-120b, Groq llama-3.3-70b-versatile and Gemini 2.5-flash all emit native tool_calls (free is a QUANTITY cap, not a capability one). Same provider selection as lean-coder (--model provider/model, or CEREBRAS_API_KEY default).",
	},
	{
		Slug:        "sovereign-shell",
		Binary:      "/bin/sh",
		DefaultArgs: []string{"-l"},
		DefaultCwd:  "/workspace",
		RequiredEnv: nil,
		Notes:       "Rescue shell. Always present even if the agent bundle is broken — black-screen prevention. No third-party binary needed for smoke tests.",
	},
}

// tableMu guards the lazily-loaded table cache. We load once per
// process; an operator who edits the override file must restart
// pty-server (same lifecycle contract as the Helm-rendered StatefulSet
// env vars).
var (
	tableMu    sync.Mutex
	tableCache map[string]Agent
	// overridePathOverride lets tests point at a temp file without
	// touching the real /etc/openova path. Empty = use env var or
	// default.
	overridePathOverride string
)

// setOverridePath is a test seam. Production callers leave this empty.
func setOverridePath(path string) {
	tableMu.Lock()
	defer tableMu.Unlock()
	overridePathOverride = path
	tableCache = nil
}

// reset is a test seam — drops the cache so the next Lookup re-reads.
func reset() {
	tableMu.Lock()
	defer tableMu.Unlock()
	tableCache = nil
}

// overridePath returns the effective override file path: explicit test
// override > CHEPHERD_AGENTS_PATH env > defaultOverridePath.
func overridePath() string {
	if overridePathOverride != "" {
		return overridePathOverride
	}
	if p := os.Getenv("CHEPHERD_AGENTS_PATH"); p != "" {
		return p
	}
	return defaultOverridePath
}

// load builds the effective catalogue: Builtin layered with the
// optional override file. Cached process-lifetime after first call.
func load() map[string]Agent {
	tableMu.Lock()
	defer tableMu.Unlock()
	if tableCache != nil {
		return tableCache
	}
	out := make(map[string]Agent, len(Builtin)+2)
	for _, a := range Builtin {
		out[a.Slug] = a
	}
	if extras, ok := loadOverride(overridePath()); ok {
		// Each entry in the override is a complete row by design — no
		// partial merge, to keep diffability obvious for ops review.
		for _, a := range extras {
			if a.Slug == "" || a.Binary == "" {
				// Skip malformed rows rather than panic at startup;
				// log to stderr so operators can spot the misconfig.
				continue
			}
			out[a.Slug] = a
		}
	}
	tableCache = out
	return out
}

// loadOverride attempts to read + parse the override file. Returns
// (nil, false) on any error or missing file — those cases simply fall
// back to the builtin table.
func loadOverride(path string) ([]Agent, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var rows []Agent
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, false
	}
	return rows, true
}

// Lookup returns the catalogue entry for slug, or ErrUnknownAgent.
func Lookup(slug string) (Agent, error) {
	table := load()
	if a, ok := table[slug]; ok {
		return a, nil
	}
	return Agent{}, ErrUnknownAgent
}

// AllSlugs returns the catalogue keyset, sorted lexicographically.
// Surfaced in the invalid-agent 400 detail string so operators know
// what they should have asked for.
func AllSlugs() []string {
	table := load()
	out := make([]string, 0, len(table))
	for slug := range table {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}

// Resolve builds the exec argv + final env slice for spawning the
// agent. The returned argv is [Binary, DefaultArgs..., extraArgs...].
// The returned env is os.Environ() with the entries from envOverride
// applied on top (later wins on key collision).
//
// session.New owns the actual exec.Command call; Resolve is just the
// data-shaping step.
func (a Agent) Resolve(extraArgs []string, envOverride map[string]string) ([]string, []string) {
	argv := make([]string, 0, 1+len(a.DefaultArgs)+len(extraArgs))
	// Resolve the binary path. The Builtin table uses /usr/local/bin/<cli>
	// per openova's StatefulSet convention; chepherd's local installs may
	// have the agent in /usr/bin or ~/.local/bin instead. Fall back to
	// exec.LookPath using the binary's basename when the configured path
	// doesn't exist — keeps openova's Sandbox compat AND lets chepherd
	// work on any laptop without env-var-tuning.
	bin := a.Binary
	if _, err := os.Stat(bin); err != nil {
		base := bin
		if idx := lastSlash(bin); idx >= 0 {
			base = bin[idx+1:]
		}
		if found, err := execLookPath(base); err == nil {
			bin = found
		}
	}
	argv = append(argv, bin)
	argv = append(argv, a.DefaultArgs...)
	argv = append(argv, extraArgs...)

	base := os.Environ()
	if len(envOverride) == 0 {
		return argv, base
	}
	// Apply override on top: keys present in envOverride replace
	// matching keys in base; new keys are appended in sorted order so
	// the result is deterministic for tests.
	idx := make(map[string]int, len(base))
	for i, kv := range base {
		eq := indexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		idx[kv[:eq]] = i
	}
	overrideKeys := make([]string, 0, len(envOverride))
	for k := range envOverride {
		overrideKeys = append(overrideKeys, k)
	}
	sort.Strings(overrideKeys)
	out := make([]string, len(base), len(base)+len(envOverride))
	copy(out, base)
	for _, k := range overrideKeys {
		v := envOverride[k]
		if i, ok := idx[k]; ok {
			out[i] = k + "=" + v
		} else {
			out = append(out, k+"="+v)
		}
	}
	return argv, out
}

// lastSlash returns the index of the last '/' in s, or -1.
func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// execLookPath wraps exec.LookPath via a var so tests can stub.
var execLookPath = func(name string) (string, error) {
	return execLookPathReal(name)
}

// indexByte is a tiny stdlib-free helper to avoid pulling strings just
// for the equals split.
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

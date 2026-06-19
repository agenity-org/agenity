# Graphify code-graph plugin (#725)

Chepherd ships **Graphify** as a default-on plugin: when an agent is spawned
into a repo, the daemon builds a per-agent **code knowledge graph** of that repo
and exposes it to the agent over MCP. The agent can ask "what calls this?",
"how do these two symbols connect?" — grounded structural answers about the
codebase without burning context re-reading files.

## What it does

- **Build-on-spawn.** On each spawn the daemon runs `graphify update <repo>
  --no-cluster` (code-only: tree-sitter parse, **no LLM**, fast) and writes the
  graph to `<repo>/graphify-out/graph.json`. Wrapper: `internal/graphify`
  (`Client.BuildCodeOnly`). Hook: `cmd/run.go` registers a spawn hook that fires
  per session in a 90s-bounded goroutine; failures log to stderr and never block
  the spawn.
- **Per-agent scoping.** The query tools resolve the caller's own `cwd` →
  `<cwd>/graphify-out/graph.json`, so an agent only ever reads the graph for the
  repo it was assigned (`graphPathForCaller` in `internal/mcpserver`).
- **Code-only, no database.** Graphify uses NetworkX + a `graph.json` file (no
  Neo4j/service). `graphify-out/` is git-ignored.

## How an agent queries it (MCP tools)

Both are surfaced through chepherd's existing MCP server (the agent already
reaches it — no separate `.mcp.json` entry, and no loopback `graphify.serve`
that would be unreachable from inside the agent's own container):

- `chepherd.graph_explain { node }` — explain a symbol/file and its immediate
  graph neighborhood (`graphify explain <node> --graph <path>`).
- `chepherd.graph_path { from, to }` — shortest dependency path between two nodes
  (`graphify path <from> <to> --graph <path>`).

If the graph hasn't been built yet (or the plugin was disabled for that agent),
the tool returns an actionable error rather than empty data.

## Opting out

Default is **on**. Operators opt out **per launch** via the spawn wizard's
**Plugins** toggle ("Graphify code-graph plugin"). The flag threads end-to-end:

```
wizard checkbox (graphifyEnabled)
  → POST /api/v1/sessions { disable_graphify: !graphifyEnabled }
  → SpawnSpec.DisableGraphify
  → SessionInfo.GraphifyDisabled
  → spawn-hook gate (skips the build when set)
```

Backend: PR #727. Toggle UI: `web/src/components/v09/Stage5Launch.svelte`.

## Implementation map

| Concern | Location |
|---|---|
| CLI wrapper (build / explain / path) | `internal/graphify/{graphify,query,serve}.go` |
| Build-on-spawn hook | `cmd/run.go` (spawn hook, `#725` block) |
| MCP tools + per-caller scoping | `internal/mcpserver/server.go` (`graph_explain`, `graph_path`, `graphPathForCaller`) |
| Opt-out flag plumbing | `internal/runtime/runtime.go`, `internal/runtimehttp/server.go` |
| Opt-out toggle UI | `web/src/components/v09/Stage5Launch.svelte` |
| Image dependency | `Containerfile` (`python3` + `graphify==0.8.35`) |

## Notes

- Build is **code-only** by design — Graphify's LLM-backed doc/media enrichment
  is intentionally not run on spawn (latency + cost). The structural code graph
  is what agents need for navigation.
- The graph is rebuilt fresh on each spawn, so it reflects the repo state the
  agent starts from.

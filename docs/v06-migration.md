# v0.5 → v0.6 migration guide

> v0.6 is daily-driveable when this guide is published. v0.5 stays installable + supported as the rollback path.

## tl;dr

v0.5 and v0.6 run side-by-side. You don't have to switch — you choose which to point your browser at.

| | v0.5 | v0.6 |
|---|---|---|
| Binary | `~/.local/bin/chepherd-v05` | `~/.local/bin/chepherd-v06` |
| Runtime port | `127.0.0.1:8080` | `127.0.0.1:8081` |
| State dir | `~/.local/state/chepherd-v05/` | `~/.local/state/chepherd-v06/` |
| Dashboard URL | `http://localhost:4321/app` | `http://localhost:4322/v06` |
| MCP socket | `<v05-state>/runtime.sock` | `<v06-state>/runtime.sock` |

## What's new in v0.6

The big shifts. Each is opt-in — your existing workflow doesn't break.

### Workspace canvas (replaces the fixed 3-pane layout)

The dashboard is now a tree of resizable panes, each containing one widget from a catalog. Default layouts ship as named templates (Focus / Council / Board / Multi). Click a view-switcher button to swap, or split / close / change-widget per pane.

- Drag the dividers to resize
- Click a pane header `≡` dropdown to change which widget it shows
- `⬌` splits horizontally, `⬍` vertically, `×` removes
- Layout auto-saves to the runtime so it survives reload (and is shared across operators on the same runtime)

### Unified data model — Agent + Team + Membership

v0.5 had `SessionInfo` with embedded team field. v0.6 promotes team and membership to first-class objects with a many-to-many join:

- An agent can be in multiple teams with different roles in each
- A shepherd can watch multiple teams (one shepherd, many subtrees)
- Tribe → team rename

Practical impact: spawn modal asks for team + role explicitly. Old single-team workflow keeps working (default team auto-applied if you leave it blank).

### Catalog templates

Five templates ship in `catalog/`:
- `solo` — single agent, no shepherd, mesh
- `solo-supervised` — 1 worker + 1 shepherd (the daily default — equivalent to v0.5)
- `pair` — implementer + reviewer + shepherd
- `council` — implementer + tester + 2 specialist reviewers + shepherd-orchestrator (heavy / risky work)
- `multi-team` — placeholder for operators with multiple projects

Apply via `📦 templates` button in the top bar, or `chepherd-v06 template list`.

### Anti-rot for shepherds

- Shepherd respawns fresh every 50 ticks (~50min) to prevent context drift
- Shepherd brief is now identity-aware (each shepherd knows its name + calls `list_memberships(agent=<self>)` to find its teams)
- New MCP tool `chepherd.read_canon(team)` so shepherds re-read CLAUDE.md per tick instead of working from a stale baked-in copy

### Events log (replaces inbox flood)

v0.5 inbox got every spawn / exit / scorecard / verdict event mixed in. v0.6 splits:

- **Events strip** (bottom of dashboard) — every runtime + MCP event, chronological, scrolls past
- **Inbox** (right pane) — ONLY `alert_human` calls with `kind: accomplishment | failure | stuck | question`

Shepherds writing routine observations use new `chepherd.note(target, body)` and `chepherd.record_event(kind, body)` tools. Operator only sees high-signal items in the inbox.

### Multi-shepherd telemetry

Each MCP call is now attributed to the actual caller, not generic `actor=shepherd`. When you have multiple shepherds (one per team), the events log distinguishes their actions.

## Running v0.6 today

You already have it. Both runtimes are alive:

```
~/.local/bin/chepherd-v05 → port 8080 → http://localhost:4321/app
~/.local/bin/chepherd-v06 → port 8081 → http://localhost:4322/v06
```

To restart v0.6 manually:

```bash
~/repos/chepherd/scripts/dev-restart-v06.sh
```

The Astro dev server for v0.6 is started by:

```bash
cd ~/repos/chepherd/web
CHEPHERD_PORT=8081 npm run dev -- --port 4322
```

## Migrating your daily-use settings

### Claude OAuth credentials
**No migration needed.** Both runtimes read `~/.claude/.credentials.json` — same Max account drives all sessions.

### Resumable Claude sessions
**No migration needed.** Both runtimes read `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl` (Claude's standard location). The v0.6 spawn modal's resume picker shows the same session list as v0.5.

### Open sessions
v0.5 sessions don't auto-transfer to v0.6. Two clean options:

1. **Let them finish in v0.5.** v0.5 stays running; no rush.
2. **Note their session UUIDs, kill the v0.5 runtime, resume in v0.6.** Each agent in the v0.5 dashboard's right pane shows its `uuid`. In v0.6, use the spawn modal's "Resume previous Claude session" toggle + paste the uuid (or filter the list).

### Per-Wave issues
GitHub issue references in CLAUDE.md / commits / branches are untouched. Both runtimes use the same git context.

### Custom layouts
v0.5 had a fixed 3-pane. v0.6 stores layouts at `~/.local/state/chepherd-v06/workspaces/current.json`. First time you open v0.6, you get the Focus template. Switch via view-switcher or build your own.

## Rollback

If v0.6 misbehaves, v0.5 is your safety net. Just point your browser back at `http://localhost:4321/app` and continue. No state loss; the two runtimes never touched each other's data.

## When to switch permanently

**Status: ready to switch.** EPIC #80 closed 2026-05-25 — all 6 blockers shipped, 8/8 e2e tests green.

Recommended path before flipping the symlink:
1. Spend one full work-day driving v0.6 (`http://localhost:4322/v06`) for real tasks
2. Keep v0.5 running in parallel so you can flip back instantly if anything feels off
3. Once you've gone 24h on v0.6 without a rollback, run the symlink step below

## After permanent switch

Symlink to make `chepherd` default to v0.6:

```bash
ln -sf ~/.local/bin/chepherd-v06 ~/.local/bin/chepherd
```

Keep `chepherd-v05` installed for ~30 days as rollback insurance. Once you've forgotten it's there, remove with:

```bash
rm ~/.local/bin/chepherd-v05
rm -rf ~/.local/state/chepherd-v05
```

That's the migration.

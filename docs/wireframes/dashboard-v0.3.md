# Dashboard v0.3 — 4-pane layout

**Status**: design locked, awaiting build go-ahead
**Tracks**: chepherd/chepherd#39
**Founder ideation**: 2026-05-24

## Final wireframe (96 cols × 19 rows)

```
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│  chepherd  ·  4 sessions · 3 active · 14:22 UTC                            ▰ chepherd 0.3   │
├────────────────┬──────────────────────────────────────────────┬──────────────────────────────┤
│ Sessions       │  tmux: openova-1                             │  Scorecard                   │
│ ●▶ openova-1   │                                              │  ● trusted · next 28m        │
│ ● iogrid-8     │  $ npm run dev                               │                              │
│ ● talent-2     │  > openova@1.0.0 dev                         │  G  9 ████████░░             │
│ ○ vcard-3 🔒   │  > vite                                      │  V  7 ███████░░░             │
│                │  VITE v5.0.0  ready in 432 ms                │  F  6 ██████░░░░             │
│                │  ➜  Local:   http://localhost:3000/          │  E  3 ███░░░░░░░             │
│                │                                              │  trend G ▂▃▅▆▇█▇█            │
│ ─────────────  │  watching for file changes...                ├──────────────────────────────┤
│ active 3       │                                              │  Recent log                  │
│ paused 1       │                                              │  14:21 silent V=7            │
│ lapsed 1       │                                              │  14:18 coach: anti-theater   │
│                │                                              │  14:15 silent V=6            │
│                │                                              │  14:12 silent V=8            │
├────────────────┴──────────────────────────────────────────────┴──────────────────────────────┤
│ ↑↓ select   t attach   L login   l fullscreen log   /  filter   p/u pause   ?  q quit        │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
```

## Spec (locked decisions)

| Concern | Decision | Notes |
|---|---|---|
| Layout | 4-pane grid (header + 3 body columns + footer) | Option B from ideation |
| Column proportions | left 18 · center 48 · right 30 (of 96) | center is the visual anchor |
| Right pane split | top 60% scorecard · bottom 40% recent log | vertical split |
| Center render | read-only `tmux capture-pane -t <name> -p -e -E -` @ 500ms | preserves ANSI colors |
| `t` hotkey | suspend chepherd + `tmux attach -t <name>` | full interactive attach |
| No `i` hotkey | dropped — `t` does it all | simpler keymap |
| Header logo | right-anchored tiny mark + wordmark + version: `▰ chepherd 0.3` | ~13 cols, dim color |
| Left wordmark | "chepherd" on left of header = breadcrumb | not duplicating brand |
| Right bottom content | daemon stdout log feed (last 5–8 lines) | global, not per-session |
| Narrow < 100 cols | hide right pane | center stays |
| Narrow < 70 cols | hide left pane too (center-only) | minimum viable |
| Center never drops | mandate | the live tmux mirror is sacred |
| Pre-attach detach hint | dismissible modal, persisted in `~/.config/chepherd/state.json` | shown until user ticks "don't show again" |
| Persistent detach reminder | tmux `status-right` set to `[ Ctrl-B D → return to chepherd ]` for the attached session | restored on detach via tmux `set-hook` |

## Hotkeys (final)

```
↑ ↓     select session
enter   open detail overlay
t       tmux attach (suspend chepherd → full attach → C-b d returns)
L       login (send '/login\n' + attach — for auth-lapsed sessions)
l       fullscreen log view
/       filter list
p / u   pause / unpause selected session
n       new session
s       start daemon (if down)
r       refresh state
?       help overlay
q       quit
```

## Teaching the user to detach back to chepherd

The `t` hotkey hands the terminal to a raw tmux session. New users don't know
`Ctrl-B D`. Two-layer solution:

### Layer 1 — Pre-attach modal (dismissible)

Before chepherd suspends + attaches, show a modal:

```
  ┌─ Attaching to openova-1 ─────────────────────────────┐
  │                                                       │
  │   You're about to enter the live tmux session.        │
  │                                                       │
  │   To return to chepherd, press:                       │
  │                                                       │
  │        │ Ctrl + B │  then  │ D │                      │
  │                                                       │
  │   [ ✓ ] don't show this again                         │
  │                                                       │
  │   [ Enter ] attach   [ Esc ] cancel                   │
  └───────────────────────────────────────────────────────┘
```

State stored in `~/.config/chepherd/state.json`:
```json
{ "hide_attach_hint": true }
```

Once dismissed, `t` attaches instantly with no modal. User can reset via
`chepherd config reset` (out of scope for v0.3.0).

### Layer 2 — Persistent reminder in the tmux status bar

Before attach, chepherd runs:

```bash
# Save the user's current status-right so we can restore it on detach
ORIG=$(tmux show-options -t openova-1 -v status-right 2>/dev/null)
echo "$ORIG" > /tmp/chepherd-statusbar-openova-1.orig

# Set our reminder
tmux set-option -t openova-1 status-right \
  "[ Ctrl-B D → return to chepherd ]"

# Then attach
tmux attach -t openova-1
```

Hooks `tmux detach-client` via `set-hook` to restore the original status-right
when the user detaches, so we don't permanently mutate their config.

```
[openova-1] 0:zsh*                       [ Ctrl-B D → return to chepherd ]
                                         ↑ always visible while attached
```

Safety: if chepherd crashes mid-attach, the user is left with a chepherd
status-right until they manually `tmux set -t openova-1 status-right ""` or
restart their tmux server. Acceptable given the 2-line cost.

## Why hybrid was NOT chosen (and why current is right)

Founder confirmed read-only mirror + `t` for attach is the right shape. Constraints
that ruled out "true embedded attach" (`tmux -CC` control mode):

- nested tmux clients fight over the prefix key (`C-b`)
- chepherd would have to run *inside* tmux (regression vs. plain-terminal launch)
- focus-routing for keystrokes between chepherd shortcuts and the embedded session
  is fragile (mouse, paste, OSC sequences all need passthrough)
- we'd own tmux window-layout management = config editor, not a dashboard

`capture-pane @ 500ms` gives 95% of the value for 5% of the effort. `t` is the
explicit escape valve when full interaction is needed. k9s uses the same pattern
(read-only by default, `s` for `kubectl exec`).

## Implementation plan

1. `internal/tui/center.go` — capture-pane renderer, 500ms tick, ANSI passthrough
2. Refactor `dashboard.go` Flex → Grid: header(1) / body(0) / footer(1)
3. Body Grid: 3 columns 18/48/30 with a vertical split inside right column
4. Move daemon log pane from current location into right-bottom slot
5. Add resize observer: hide right < 100 cols, hide left < 70 cols
6. Header right-anchor render: `▰ chepherd 0.3` in `style.Logo` dim color
7. Drop the existing 3 bordered single-pane layout from `dashboard.go` body
8. `internal/tui/attachmodal.go` — pre-attach modal + dismissal persistence
   reading/writing `~/.config/chepherd/state.json`
9. `cmd/daemon.go` or `internal/tui/app.go` — `TmuxAttachSelected` updates:
   - save target session's `status-right` to a temp file
   - set chepherd's reminder via `tmux set-option`
   - register `set-hook -t <session> client-detached`  to restore
   - then suspend + attach
10. Playwright walk on tmux + screenshot → comment on #39 → sub-agent reviewer → close

## Out of scope for v0.3.0

- Multi-session quad-view (watch all 4 sessions at once)
- Resize-handle drag (proportions are fixed)
- Center-pane scroll-back beyond what `capture-pane` returns
- Custom keymaps (deferred to v0.4.0)

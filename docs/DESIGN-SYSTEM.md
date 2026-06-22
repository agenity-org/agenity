# chepherd Design System

**Status:** Canon. Source of truth for every chepherd surface — TUI, web, iOS, Android, landing page. When a client implementation drifts from this doc, the client is wrong.

**Why one doc:** three client teams (web/iOS/Android) plus the TUI must produce the SAME visual language. Operators recognise chepherd in any surface within 100ms. Drift between platforms makes the product feel cheap. This doc is the contract.

---

## 1. Identity

### 1.1 Brand

| Property | Value |
|---|---|
| Name | `chepherd` (lowercase, never capitalised) |
| Pronunciation | /ʃep·hɜrd/ — "shepherd" |
| Tagline | "TUI supervisor for parallel AI coding agents" |
| Voice | direct, peer-to-peer, no flattery, no hedging. Tone matches `prompts/judge.md`. |

The intentional-typo brand (sh→ch) is part of the identity. Don't "correct" it.

### 1.2 Logo

The wordmark IS the logo. No standalone mark in v1.

```
chepherd
```

Single weight, single colour (`logo` token §2), tracked at default. Never tilted, gradiented, or boxed.

For square-frame contexts (favicon, app icon), use just the `c` glyph at full-size on the background, with a small green dot at top-right (the "session healthy" indicator) — mirrors the `●` band glyph used throughout the UI.

### 1.3 Voice in copy

| Don't | Do |
|---|---|
| "Welcome! Let's get you set up." | "chepherd watches your sessions. sign in to start." |
| "Awesome! You've connected." | "connected · 3 sessions" |
| "Oops, something went wrong." | "transport error: peer unreachable" |

No emojis in product copy. Status icons (●○⚠◴) are part of the visual language, NOT emoji decorations.

---

## 2. Color tokens

Source-of-truth values mirror the k9s `stock.yaml` skin. Every client maps these to its native colour system.

### 2.1 Body

| Token | Hex | RGB | Role |
|---|---|---|---|
| `--c-body` | `#5F9EA0` | cadetblue | Ambient default text (low-importance) |
| `--c-primary` | `#FFFFFF` | white | Primary readable content |
| `--c-background` | `#000000` | black | Universal background |

### 2.2 Brand

| Token | Hex | Role |
|---|---|---|
| `--c-logo` | `#FFA500` | orange — chepherd brand pop |

### 2.3 Frame chrome

| Token | Hex | Role |
|---|---|---|
| `--c-title` | `#00FFFF` | aqua — section + pane titles |
| `--c-title-rule` | `#87CEFA` | lightskyblue — the ─── under titles |
| `--c-border` | `#1E90FF` | dodgerblue — pane borders |
| `--c-border-focus` | `#87CEFA` | lightskyblue — focused border |

### 2.4 Menu / footer

| Token | Hex | Role |
|---|---|---|
| `--c-key-letter` | `#1E90FF` | dodgerblue — bold shortcut key |
| `--c-key-desc` | `#FFFFFF` | white — shortcut description |
| `--c-key-numeric` | `#FF00FF` | fuchsia — numeric key (rare) |

### 2.5 Breadcrumbs

| Token | Hex | Role |
|---|---|---|
| `--c-crumb-fg` | `#000000` | breadcrumb fg |
| `--c-crumb-bg` | `#4682B4` | steelblue background |
| `--c-crumb-active` | `#FFA500` | orange — current leaf |

### 2.6 Trust bands

| Token | Hex | Band |
|---|---|---|
| `--c-band-trusted` | `#ADFF2F` | greenyellow |
| `--c-band-standard` | `#5F9EA0` | cadetblue |
| `--c-band-concerned` | `#FF8C00` | darkorange |
| `--c-band-crisis` | `#FF4500` | orangered |
| `--c-band-paused` | `#778899` | lightslategray |

### 2.7 Verdict

| Token | Hex | Verdict |
|---|---|---|
| `--c-verdict-silent` | `#5F9EA0` | cadetblue (neutral) |
| `--c-verdict-praise` | `#ADFF2F` | greenyellow |
| `--c-verdict-coach` | `#FF8C00` | darkorange |
| `--c-verdict-intervene` | `#FF4500` | orangered |

### 2.8 Special events

| Token | Hex | Role |
|---|---|---|
| `--c-injected` | `#9370DB` | mediumpurple — coach message landed |
| `--c-escalating` | `#FFEFD5` | papayawhip — model upgraded |
| `--c-api-error` | `#FF4500` | orangered — judge call failed |
| `--c-adopted` | `#00CED1` | darkturquoise — externally-started session |

### 2.9 Metrics + refs

| Token | Hex | Role |
|---|---|---|
| `--c-metric` | `#FFEFD5` | papayawhip — counts |
| `--c-issue-ref` | `#4682B4` | steelblue — #1234 style references |
| `--c-marked` | `#B8860B` | darkgoldenrod — flagged item |
| `--c-timestamp` | `#778899` | lightslategray — dim history |

### 2.10 Age + cost (semantic — graded thresholds)

```
age (minutes)            cost (USD)
< 5    → --c-band-trusted   < 0.10 → --c-band-trusted
5-30   → --c-band-concerned 0.10-0.20 → --c-band-concerned
> 30   → --c-band-crisis    > 0.20 → --c-band-crisis
```

### 2.11 Scorecard digit (per-axis 0-10)

```
0-3   → --c-band-crisis
4-6   → --c-band-concerned
7-10  → --c-band-trusted
```

---

## 3. Typography

### 3.1 Stack

| Platform | Primary | Fallback |
|---|---|---|
| TUI | terminal monospace (inherits user's setting) | n/a |
| Web | `ui-monospace, SFMono-Regular, "Cascadia Mono", Menlo, Consolas, monospace` | system monospace |
| iOS | SF Mono (system) | SF Pro Text on non-mono surfaces |
| Android | Roboto Mono (system) | Roboto on non-mono surfaces |

chepherd is a developer tool. Monospace is the default everywhere. Variable-width fonts are forbidden in data-display areas.

### 3.2 Scale

| Token | px | use |
|---|---|---|
| `--fs-xs` | 11 | tertiary chrome, log timestamps |
| `--fs-sm` | 13 | log lines, table cells |
| `--fs-base` | 16 | body text, field labels, primary readable |
| `--fs-lg` | 20 | pane titles, hero pitch |
| `--fs-xl` | 24 | section H2 |
| `--fs-2xl` | 32 | landing hero H1 |
| `--fs-3xl` | 48 | brand wordmark only |

### 3.3 Weights

| Token | Weight | use |
|---|---|---|
| `--fw-normal` | 400 | default |
| `--fw-bold` | 700 | shortcut keys, section titles, brand wordmark |

Italics + underline are reserved for inline emphasis (rare). Strikethrough is for retracted log lines.

---

## 4. Spacing

Single scale, doubling progression. Every layout aligns to this grid.

| Token | px | use |
|---|---|---|
| `--space-0` | 0 | flush |
| `--space-1` | 4 | tight inline (icon ↔ label) |
| `--space-2` | 8 | between adjacent fields |
| `--space-3` | 12 | within a card body |
| `--space-4` | 16 | between cards in a row |
| `--space-6` | 24 | between sections |
| `--space-8` | 32 | section padding edges |
| `--space-12` | 48 | major rhythm break (header ↔ body) |

No `5`, `7`, `9`, `10`, `11` etc. Use the scale.

Per-component padding rules:
- TUI pane: 2 col on each side
- Web card: `--space-4` all sides
- Web button: `--space-2` vertical, `--space-3` horizontal
- Mobile card: `--space-4` all sides, `--space-6` between cards on small screens

---

## 5. Layout primitives

### 5.1 Pane

A `pane` is the basic chrome unit. One bordered box with a title row, optional rule, content area, optional footer.

```
[--c-title]TITLE[/]              ← --fs-base, --fw-bold
[--c-title-rule]─────[/]          ← rule character only, NOT a full-width line
                                  ← --space-3 below

  body content                    ← --fs-base, --c-primary

[--c-key-letter]esc[/] back  …   ← footer row
```

### 5.2 Selected row

The k9s table-cursor pattern.

| State | fg | bg | bold |
|---|---|---|---|
| Unselected | platform-default | `--c-background` | no |
| Selected | `--c-background` (black) | `--c-title` (aqua) | yes |

The selection spans the FULL row width including trailing whitespace — NOT just the text prefix.

### 5.3 Breadcrumb

Pinned to top of any drill-in view.

```
[--c-crumb-bg + --c-crumb-fg] chepherd › sessions › openova-27 [/]
                                          ──┬──
                                            └ rightmost leaf in --c-crumb-active (orange)
```

3-row band (blank line above + below the text) for breathing room.

### 5.4 Sparkline

For G/V/F/E trend display. 8 cells, value-graded colour.

```
G  ▆▆▅▅▄▄▃▃▃3   ← each bar is one of: ▁▂▃▄▅▆▇█
                  height = (value / 10) * 7
                  colour = per-value band (red/orange/green)
```

### 5.5 Gauge bar

For counts (in-progress, unclaimed, backlog).

```
▰▰▰▰▰▰▱▱▱▱  ← 10 cells, filled=`--c-band-{value-band}`, empty=`--c-band-paused`
              max scale: 0-30 issues; clamp at 30
```

### 5.6 Status dot

The `●` glyph (or `○` for paused) in front of every session name.

| State | Glyph | Colour token |
|---|---|---|
| Trusted | ● | `--c-band-trusted` |
| Standard | ● | `--c-band-standard` |
| Concerned | ● | `--c-band-concerned` |
| Crisis | ● | `--c-band-crisis` |
| Paused | ○ | `--c-band-paused` |

---

## 6. Motion

### 6.1 Durations

| Token | ms | use |
|---|---|---|
| `--motion-instant` | 0 | state-flip with no animation (data updates) |
| `--motion-quick` | 100 | hover, focus ring, key feedback |
| `--motion-normal` | 200 | pane swap, drawer open, dialog enter |
| `--motion-slow` | 400 | first-paint reveals, route transitions |

### 6.2 Easings

| Token | Curve | use |
|---|---|---|
| `--ease-out` | `cubic-bezier(.16,1,.3,1)` | entering motion |
| `--ease-in` | `cubic-bezier(.7,0,.84,0)` | exiting motion |
| `--ease-in-out` | `cubic-bezier(.65,0,.35,1)` | bidirectional, e.g. pane swap |

### 6.3 Named motions

| Name | Tokens | Description |
|---|---|---|
| `pane-swap` | duration: `--motion-normal`, easing: `--ease-in-out` | swap content of a pane (drill-in / drill-back) |
| `selection-pulse` | duration: `--motion-quick`, easing: `--ease-out` | when arrow key moves selection |
| `band-transition` | duration: `--motion-normal`, easing: `--ease-out` | session goes trusted → concerned (or any band change) — pulse the dot |
| `log-append` | duration: `--motion-instant` | new log line appears (NO animation — disrupts reading) |
| `verdict-flash` | duration: `--motion-quick`, easing: `--ease-out` | one-time flash on new intervene/coach verdict |

### 6.4 Reduced-motion

When `prefers-reduced-motion: reduce` is set:
- All motion durations → `--motion-instant`
- `band-transition` skips the pulse, the dot just changes colour
- `verdict-flash` skips the flash, the row just appears

---

## 7. Sound (optional, off by default)

| Event | Sound |
|---|---|
| New intervene verdict (operator's session enters crisis) | Single descending chime, ~200ms, ~440Hz → 220Hz |
| New praise verdict (rare) | Single ascending tone, ~150ms, ~440Hz → 880Hz |
| Coach landed (`addressed_last_coach: true`) | None |
| Daemon down | Three short beeps, ~50ms each |
| Connection lost | Single low tone, ~300ms, 110Hz |

Defaults to OFF. Operator enables via `/config sound on` in TUI or settings in web/mobile.

Web uses Web Audio API with synthesised tones (no asset downloads). Mobile uses native system sounds.

---

## 8. Iconography

chepherd uses single-character glyphs over icon fonts. They render in every monospace context (TUI, web, mobile, even SSH sessions).

| Glyph | Meaning |
|---|---|
| `●` | active session (band-coloured) |
| `○` | paused session |
| `⫪` | agent-team lead session |
| `└─ ⊳` | teammate (indented under lead) |
| `⌥` | git worktree branch |
| `↑` `→` `↓` | scorecard trend up / flat / down |
| `▰` `▱` | gauge bar filled / empty |
| `▁▂▃▄▅▆▇█` | sparkline heights |
| `›` | breadcrumb separator |
| `─` `│` `┌` `┐` `└` `┘` `┤` `├` | box drawing |
| `✓` `✗` | done / not-done in checklists |
| `⚠` | daemon-down warning |
| `◴` | daemon-stale warning |

Non-ASCII fallback: when the terminal has `LANG=C` or `LC_CTYPE=POSIX`, all unicode glyphs degrade to ASCII letters per the chepherd palette package's fallback table.

---

## 9. Components — visual specs

### 9.1 Session row (W1, web list, mobile card)

```
●  [name]  [score]  [trend]  [band]  [next]
   cyan    color    arrow    color   ambient
```

Spacing: `--space-2` between groups, `--space-3` between left dot and name.

### 9.2 Scorecard panel

Four rows, label-aligned at `:` axis, gauge bar to the right of each value.

```
G  goal     :   3 / 10   ▰▰▰▱▱▱▱▱▱▱
V  velocity :   1 / 10   ▰▱▱▱▱▱▱▱▱▱
F  focus    :   1 / 10   ▰▱▱▱▱▱▱▱▱▱
E  end-state:   0 / 10   ▱▱▱▱▱▱▱▱▱▱
```

### 9.3 Shortcut footer

```
[key] [desc]    [key] [desc]    ...
 bold  normal    bold  normal
```

Separator: 4 spaces between pairs. Key letters in `--c-key-letter` (dodgerblue), descriptions in `--c-key-desc` (white).

### 9.4 Coach injection (when received in a tmux pane)

```
[SUPERVISOR — P21, D14 | G/V/F/E=4/5/3/3] Last assistant msg ended with text-not-tool-call. P21 HARD-STOP. See ~/.claude/CLAUDE.md §4 P21.

Before your next tool call, ack in 2-4 sentences:
1) State the SPECIFIC divergence I caught.
2) State your immediate 1-2 concrete next actions.
3) Then ship the first tool call.
```

`[SUPERVISOR ...]` prefix in `--c-injected` (mediumpurple), bold. Body in `--c-primary` (white).

---

## 10. Accessibility (WCAG 2.2 AA)

Every chepherd surface MUST pass:

- Contrast ratios ≥ 4.5:1 for normal text, ≥ 3:1 for large + UI components
- Full keyboard nav — every action reachable via keyboard
- Screen reader labels — VoiceOver / TalkBack / NVDA / JAWS
- `prefers-reduced-motion` honoured per §6.4
- Focus indicators visible (use `--c-border-focus`, never rely on colour-only)
- Text resizable to 200% without horizontal scroll
- Time-based content (sparklines, gauges) has text-equivalent alt info

The TUI inherits the terminal's accessibility behaviour. Web/mobile clients must pass axe-core/lighthouse in CI as a gate.

---

## 11. Implementation references

| Surface | Repo | Token file |
|---|---|---|
| TUI (Go) | github.com/agenity-org/agenity | internal/style/palette.go |
| Web (TS) | github.com/agenity-org/agenity-rc-web | src/styles/tokens.css |
| iOS (Swift) | github.com/agenity-org/agenity-rc-ios | Sources/Style/Tokens.swift |
| Android (Kotlin) | github.com/agenity-org/agenity-rc-android | core/style/src/main/kotlin/Tokens.kt |
| Landing page | github.com/agenity-org/agenity.github.io | index.html (CSS custom properties) |

When this doc changes, every reference implementation gets a PR within 7 days. A drift detector in CI flags any client where the rendered output diverges from a reference screenshot.

---

## 12. Changes to this doc

Edits go through a PR + at least one founder review. Tokens never rename without a major version bump. Adding tokens is non-breaking. Removing tokens is breaking — requires deprecation period + client migration time.

**Version: v1.0** (locked 2026-05-23).

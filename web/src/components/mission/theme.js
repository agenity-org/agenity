// mission/theme.js — full dark + light token sets for the "mission control"
// dashboard. Both modes are deliberately designed (NASA/ops-console dark is
// the hero; light is a clean daylight-ops variant with matched contrast).
//
// Tokens are applied to the dashboard ROOT element as inline CSS custom
// properties (not html[data-theme], so this dashboard never fights the
// global :root dark-only tokens in styles/global.css). Every surface in
// every mission component reads ONLY these variables — there are no
// hard-coded hex colors in component markup that would break a mode.

export const THEMES = {
  dark: {
    // page + surfaces
    '--m-bg':            '#06080c',   // deep space black-blue
    '--m-bg-grid':       'rgba(120,160,210,0.045)', // faint console grid
    '--m-panel':         '#0c1118',   // pane / rail surface
    '--m-panel-2':       '#10161f',   // elevated (headers, chips)
    '--m-panel-3':       '#161e2a',   // hover / active
    '--m-term-bg':       '#05070a',   // terminal canvas surface
    // text
    '--m-fg':            '#e8eef6',
    '--m-fg-dim':        '#9fb0c4',
    '--m-fg-faint':      '#5f7187',
    // lines
    '--m-border':        '#1c2733',
    '--m-border-strong': '#2b3a4a',
    '--m-grid-line':     '#11202c',   // splitter rail
    // accents
    '--m-accent':        '#ffb300',   // mission amber
    '--m-accent-2':      '#3fc8ff',   // telemetry cyan
    '--m-ok':            '#39d98a',
    '--m-warn':          '#ffb84d',
    '--m-danger':        '#ff5d5d',
    '--m-live':          '#39d98a',
    '--m-paused':        '#ffb84d',
    '--m-dead':          '#5f7187',
    // chrome
    '--m-glow':          'rgba(63,200,255,0.55)',
    '--m-shadow':        'rgba(0,0,0,0.55)',
    '--m-select':        'rgba(63,200,255,0.16)',
    '--m-scroll':        '#23303d',
    '--m-scroll-hover':  '#33455a',
    // terminal palette hooks (xterm theme)
    '--m-term-fg':       '#e8eef6',
    '--m-term-cursor':   '#3fc8ff',
    '--m-term-sel':      '#1d3346',
  },
  light: {
    '--m-bg':            '#eef1f5',
    '--m-bg-grid':       'rgba(40,70,110,0.05)',
    '--m-panel':         '#ffffff',
    '--m-panel-2':       '#f3f6fa',
    '--m-panel-3':       '#e7edf4',
    '--m-term-bg':       '#fbfcfe',
    '--m-fg':            '#10202f',
    '--m-fg-dim':        '#46586b',
    '--m-fg-faint':      '#7d8da0',
    '--m-border':        '#d4dde7',
    '--m-border-strong': '#b7c5d4',
    '--m-grid-line':     '#dde5ee',
    '--m-accent':        '#c77800',
    '--m-accent-2':      '#0c84c2',
    '--m-ok':            '#198f5b',
    '--m-warn':          '#b8761b',
    '--m-danger':        '#cc3838',
    '--m-live':          '#198f5b',
    '--m-paused':        '#b8761b',
    '--m-dead':          '#9aa8b8',
    '--m-glow':          'rgba(12,132,194,0.35)',
    '--m-shadow':        'rgba(40,60,90,0.18)',
    '--m-select':        'rgba(12,132,194,0.14)',
    '--m-scroll':        '#c2cedb',
    '--m-scroll-hover':  '#a7b7c8',
    '--m-term-fg':       '#10202f',
    '--m-term-cursor':   '#0c84c2',
    '--m-term-sel':      '#cfe2f0',
  },
};

// xterm.js Terminal `theme` object derived from the active mode's tokens.
// Only background/foreground/cursor/selection are set — the 16 ANSI slots
// fall back to xterm stock palette (matches the project's WidgetTerminal
// decision so claude-code TUI colors render as designed).
export function xtermTheme(mode) {
  const t = THEMES[mode] || THEMES.dark;
  return {
    background: t['--m-term-bg'],
    foreground: t['--m-term-fg'],
    cursor: t['--m-term-cursor'],
    cursorAccent: t['--m-term-bg'],
    selectionBackground: t['--m-term-sel'],
  };
}

// Serialize a theme's tokens into a style="" string for the root wrapper.
export function themeStyle(mode) {
  const t = THEMES[mode] || THEMES.dark;
  return Object.entries(t).map(([k, v]) => `${k}:${v}`).join(';');
}

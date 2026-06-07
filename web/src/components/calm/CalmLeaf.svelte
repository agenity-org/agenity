<!--
  CalmLeaf — a single framed widget in the calm split-tree.

  Header (calm, low-chrome): an identity glyph + an inline agent picker
  for terminals (HARD REQ #1 — operator chooses which agent's live
  terminal this pane shows), a widget switcher, and split/close controls.

  Body hosts ONE of:
    terminal   → the existing live WidgetTerminal (real xterm + PTY WS)
    inspector  → CalmInspector (identity / scorecard / actions)
    transcript → the existing TeamTranscript (team messaging)
-->
<script>
  import WidgetTerminal from '../v08/widgets/WidgetTerminal.svelte';
  import TeamTranscript from '../TeamTranscript.svelte';
  import CalmInspector from './CalmInspector.svelte';
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let {
    node,
    sessions = [],
    focused = false,
    canClose = true,
    maximized = false,
    // collapse affordance: null when the leaf has no parent split, else
    // { axis: 'h'|'v', collapsed: bool } describing the parent's axis and
    // whether this leaf is currently collapsed toward its sibling.
    collapse = null,
    onfocus = () => {},
    onsplit = () => {},
    onclose = () => {},
    onsetagent = () => {},
    onsetwidget = () => {},
    onmaximize = () => {},
    oncollapse = () => {},
  } = $props();

  let pickerOpen = $state(false);
  let widgetMenuOpen = $state(false);

  // Live, non-shepherd agents float to the top of the picker.
  let agentOptions = $derived(
    [...sessions].sort((a, b) => {
      const al = a.exited ? 1 : 0, bl = b.exited ? 1 : 0;
      if (al !== bl) return al - bl;
      return (a.name || '').localeCompare(b.name || '');
    })
  );

  let boundName = $derived(node.config?.agent || '');
  let boundSession = $derived(sessions.find((s) => s.name === boundName) || null);
  let id = $derived(boundSession ? agentIdentity(boundSession) : agentIdentity(boundName || '?'));

  const WIDGETS = [
    { id: 'terminal', label: 'Terminal', glyph: '▦' },
    { id: 'inspector', label: 'Inspector', glyph: '◉' },
    { id: 'transcript', label: 'Transcript', glyph: '✉' },
  ];
  let curWidget = $derived(WIDGETS.find((w) => w.id === node.widget) || WIDGETS[0]);

  function choose(name) {
    onsetagent(name);
    pickerOpen = false;
  }
  function chooseWidget(w) {
    onsetwidget(w);
    widgetMenuOpen = false;
  }
  function statusLabel(s) {
    if (!s) return '';
    if (s.exited) return 'exited';
    if (s.paused) return 'paused';
    if (s.live === false) return 'offline';
    return 'live';
  }
</script>

<div
  class="leaf {focused ? 'is-focused' : ''}"
  data-leaf-id={node.id}
  onmousedown={onfocus}
  role="presentation"
>
  <header class="leaf-head">
    <div class="head-left">
      <span class="wbtn-wrap">
        <button
          class="wbtn"
          title="Switch widget"
          onclick={(e) => { e.stopPropagation(); widgetMenuOpen = !widgetMenuOpen; pickerOpen = false; }}
        >
          <span class="wglyph">{curWidget.glyph}</span>
          <span class="wlabel">{curWidget.label}</span>
          <span class="caret">⌄</span>
        </button>
        {#if widgetMenuOpen}
          <div class="menu" role="menu">
            {#each WIDGETS as w}
              <button class="menu-item" class:active={w.id === node.widget} onclick={(e) => { e.stopPropagation(); chooseWidget(w.id); }}>
                <span class="wglyph">{w.glyph}</span>{w.label}
              </button>
            {/each}
          </div>
        {/if}
      </span>

      {#if node.widget === 'terminal'}
        <span class="picker-wrap">
          <button
            class="agent-pill"
            title="Choose which agent this terminal shows"
            onclick={(e) => { e.stopPropagation(); pickerOpen = !pickerOpen; widgetMenuOpen = false; }}
          >
            <span class="dot" style={`background:${id.color}`}></span>
            <span class="ic" style={`color:${id.color}`}>{id.icon}</span>
            <span class="aname">{boundName || 'pick agent'}</span>
            {#if boundSession}
              <span class="status {statusLabel(boundSession)}">{statusLabel(boundSession)}</span>
            {/if}
            <span class="caret">⌄</span>
          </button>
          {#if pickerOpen}
            <div class="menu agent-menu" role="menu">
              {#if agentOptions.length === 0}
                <div class="menu-empty">no sessions yet</div>
              {/if}
              {#each agentOptions as s}
                {@const sid = agentIdentity(s)}
                <button
                  class="menu-item"
                  class:active={s.name === boundName}
                  onclick={(e) => { e.stopPropagation(); choose(s.name); }}
                >
                  <span class="ic" style={`color:${sid.color}`}>{sid.icon}</span>
                  <span class="mname">{s.name}</span>
                  <span class="mrole">{s.role || ''}</span>
                  <span class="status {statusLabel(s)}">{statusLabel(s)}</span>
                </button>
              {/each}
            </div>
          {/if}
        </span>
      {/if}
    </div>

    <div class="head-right">
      {#if collapse}
        {@const c = collapse.collapsed}
        {@const glyph = collapse.axis === 'h' ? (c ? '›' : '‹') : (c ? '⌄' : '⌃')}
        <button
          class="ctl"
          title={c ? 'Expand pane' : 'Collapse pane'}
          aria-label={c ? 'Expand pane' : 'Collapse pane'}
          onclick={(e) => { e.stopPropagation(); oncollapse(); }}
        >{glyph}</button>
      {/if}
      <button class="ctl" title="Split right" onclick={(e) => { e.stopPropagation(); onsplit('h'); }} aria-label="Split right">⬓</button>
      <button class="ctl" title="Split down" onclick={(e) => { e.stopPropagation(); onsplit('v'); }} aria-label="Split down">⬒</button>
      <button
        class="ctl"
        title={maximized ? 'Restore layout' : 'Maximize pane'}
        aria-label={maximized ? 'Restore layout' : 'Maximize pane'}
        onclick={(e) => { e.stopPropagation(); onmaximize(); }}
      >{maximized ? '🗗' : '⛶'}</button>
      {#if canClose}
        <button class="ctl ctl-close" title="Close pane" onclick={(e) => { e.stopPropagation(); onclose(); }} aria-label="Close pane">✕</button>
      {/if}
    </div>
  </header>

  <div class="leaf-body">
    {#if node.widget === 'terminal'}
      {#key node.id + '|' + (boundName || '')}
        <WidgetTerminal selectedAgent={boundName} {sessions} {node} />
      {/key}
    {:else if node.widget === 'inspector'}
      <CalmInspector {boundSession} {sessions} />
    {:else if node.widget === 'transcript'}
      <div class="transcript-host">
        <TeamTranscript team="all" />
      </div>
    {/if}
  </div>
</div>

<style>
  .leaf {
    display: flex; flex-direction: column;
    width: 100%; height: 100%;
    min-width: 0; min-height: 0;
    background: var(--calm-surface);
    border: 1px solid var(--calm-border);
    border-radius: 6px;
    overflow: hidden;
    box-shadow: var(--calm-shadow-sm);
    transition: border-color 0.18s ease, box-shadow 0.18s ease;
  }
  .leaf.is-focused {
    border-color: color-mix(in srgb, var(--calm-accent) 55%, var(--calm-border));
    box-shadow: var(--calm-shadow-focus);
  }

  .leaf-head {
    display: flex; align-items: center; justify-content: space-between;
    gap: 0.5rem;
    padding: 0.4rem 0.55rem;
    border-bottom: 1px solid var(--calm-border);
    background: var(--calm-surface-2);
    flex: 0 0 auto;
    min-height: 0;
  }
  /* #728 — head-left must take remaining space, shrink, AND clip: without
     overflow:hidden its pills overflowed and overlapped (intercepting clicks
     on) the fixed head-right controls at narrow pane widths. */
  .head-left { display: flex; align-items: center; gap: 0.4rem; min-width: 0; flex: 1 1 auto; overflow: hidden; }
  .head-right { display: flex; align-items: center; gap: 0.15rem; flex: 0 0 auto; }

  .wbtn-wrap, .picker-wrap { position: relative; }

  .wbtn, .agent-pill {
    display: inline-flex; align-items: center; gap: 0.35rem;
    background: var(--calm-chip);
    border: 1px solid var(--calm-border);
    color: var(--calm-fg);
    border-radius: 8px;
    padding: 0.2rem 0.55rem;
    font-size: 0.78rem;
    cursor: pointer;
    max-width: 16rem;
    min-width: 0; /* #728 — allow the pill to shrink (aname ellipsizes) so it never spills over head-right */
    transition: background 0.14s ease, border-color 0.14s ease;
  }
  .wbtn:hover, .agent-pill:hover { background: var(--calm-chip-hover); border-color: var(--calm-border-strong); }
  .wglyph { opacity: 0.85; font-size: 0.82rem; }
  .wlabel { font-weight: 500; }
  .caret { opacity: 0.5; font-size: 0.7rem; }

  .agent-pill .dot { width: 7px; height: 7px; border-radius: 50%; flex: 0 0 auto; }
  .agent-pill .ic { font-size: 0.85rem; }
  .agent-pill .aname { font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

  .status {
    font-size: 0.62rem; text-transform: uppercase; letter-spacing: 0.04em;
    padding: 0.04rem 0.34rem; border-radius: 6px; font-weight: 700;
    flex: 0 0 auto;
  }
  .status.live { color: var(--calm-ok); background: color-mix(in srgb, var(--calm-ok) 16%, transparent); }
  .status.paused { color: var(--calm-warn); background: color-mix(in srgb, var(--calm-warn) 16%, transparent); }
  .status.exited, .status.offline { color: var(--calm-fg-faint); background: color-mix(in srgb, var(--calm-fg-faint) 16%, transparent); }

  .ctl {
    width: 26px; height: 26px;
    display: inline-flex; align-items: center; justify-content: center;
    background: transparent; border: 1px solid transparent;
    color: var(--calm-fg-muted); border-radius: 8px;
    cursor: pointer; font-size: 0.85rem;
    transition: background 0.14s ease, color 0.14s ease;
  }
  .ctl:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }
  .ctl-close:hover { color: var(--calm-danger); background: color-mix(in srgb, var(--calm-danger) 14%, transparent); }

  .menu {
    position: absolute; top: calc(100% + 6px); left: 0;
    z-index: 40;
    min-width: 12rem;
    background: var(--calm-surface);
    border: 1px solid var(--calm-border-strong);
    border-radius: 6px;
    padding: 0.3rem;
    box-shadow: var(--calm-shadow-lg);
    display: flex; flex-direction: column; gap: 0.1rem;
    max-height: 60vh; overflow: auto;
  }
  .agent-menu { min-width: 16rem; }
  .menu-item {
    display: flex; align-items: center; gap: 0.5rem;
    padding: 0.4rem 0.55rem;
    background: transparent; border: 0; border-radius: 8px;
    color: var(--calm-fg); font: inherit; font-size: 0.8rem;
    text-align: left; cursor: pointer; width: 100%;
  }
  .menu-item:hover { background: var(--calm-chip-hover); }
  .menu-item.active { background: color-mix(in srgb, var(--calm-accent) 16%, transparent); }
  .menu-item .mname { font-weight: 600; flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .menu-item .mrole { color: var(--calm-fg-faint); font-size: 0.72rem; }
  .menu-empty { padding: 0.6rem; color: var(--calm-fg-faint); font-size: 0.78rem; text-align: center; }

  .leaf-body { flex: 1; min-height: 0; min-width: 0; overflow: hidden; position: relative; background: var(--calm-bg); }
  .transcript-host { height: 100%; overflow: hidden; }
</style>

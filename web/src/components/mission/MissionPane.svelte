<!--
  MissionPane — recursive layout-tree renderer for the mission center grid.

  Node shapes (same vocabulary as the project's Pane.svelte / spec layout):
    { kind:'pane', id, widget, config, tabs?, activeTab? }   leaf
    { kind:'h'|'v', id, ratio, a, b }                        split

  HARD REQUIREMENTS implemented here:
    1. PANE SWITCHING — each terminal leaf has a per-pane agent <select> in
       its header; switching is local to that pane (config.agent). Clicking a
       roster row rebinds the focused pane (handled in the root).
    2. PANE RESIZING — every split renders a draggable splitter bar that
       updates node.ratio live (pointer events; works H + V).
    3. LAYOUT FLEXIBILITY — split ◫ / ▭ buttons add panes; ✕ removes; tabs (+)
       add more widgets per pane. Arbitrary nested trees.

  Self-contained: this component is recursive (imports itself).
-->
<script>
  import MissionPane from './MissionPane.svelte';
  import MissionTerminal from './MissionTerminal.svelte';
  import MissionTranscript from './MissionTranscript.svelte';
  import MissionRoster from './MissionRoster.svelte';
  import MissionInspector from './MissionInspector.svelte';
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let {
    node, sessions = [], teams = [], memberships = [], events = [], mode = 'dark',
    selectedAgent = null, focusedPaneID = '',
    onSplit, onClose, onSetAgent, onSetWidget, onSetRatio, onFocusPane,
    onAddTab, onCloseTab, onActivateTab, onSelectAgent,
  } = $props();

  const WIDGET_LABELS = {
    terminal: '▮ Terminal', 'team-transcript': '✉ Transcript',
    kanban: '◫ Roster', inspector: '◉ Inspector',
  };

  let containerEl;
  function startDrag(e) {
    e.preventDefault();
    const rect = containerEl.getBoundingClientRect();
    const horizontal = node.kind === 'h';
    const move = (ev) => {
      const pt = ev.touches ? ev.touches[0] : ev;
      let r = horizontal
        ? (pt.clientX - rect.left) / rect.width
        : (pt.clientY - rect.top) / rect.height;
      r = Math.max(0.12, Math.min(0.88, r));
      onSetRatio?.(node.id, r);
    };
    const up = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  }

  function tabsOf(p) {
    return Array.isArray(p.tabs) && p.tabs.length
      ? p.tabs
      : [{ widget: p.widget, config: p.config || {} }];
  }
  let menuOpen = $state(false);
  let addOpen = $state(false);
</script>

{#if !node}
  <!-- empty -->
{:else if node.kind === 'pane'}
  {@const tabs = tabsOf(node)}
  {@const active = typeof node.activeTab === 'number' ? node.activeTab : 0}
  {@const cur = tabs[active] || tabs[0]}
  <section
    class="leaf"
    class:focused={node.id === focusedPaneID}
    data-pane-id={node.id}
    onpointerdown={() => onFocusPane?.(node.id)}
    role="group"
  >
    <header class="leaf-head">
      <div class="tabs">
        {#each tabs as t, ti}
          {@const ident = t.widget === 'terminal' && t.config?.agent ? agentIdentity(t.config.agent) : null}
          <button
            class="tab"
            class:active={ti === active}
            onclick={(e) => { e.stopPropagation(); onActivateTab?.(node.id, ti); }}
            title={WIDGET_LABELS[t.widget] || t.widget}
          >
            {#if ident}
              <span class="dot" style="background:{ident.color}"></span><span class="tlabel">{ident.icon} {t.config.agent}</span>
            {:else}
              <span class="tlabel">{(WIDGET_LABELS[t.widget] || t.widget)}</span>
            {/if}
            {#if tabs.length > 1}
              <span class="tabx" role="button" tabindex="0" onclick={(e) => { e.stopPropagation(); onCloseTab?.(node.id, ti); }} onkeydown={() => {}}>×</span>
            {/if}
          </button>
        {/each}
        <div class="addwrap">
          <button class="ctl add" title="Add tab (widget picker)" onclick={(e) => { e.stopPropagation(); addOpen = !addOpen; menuOpen = false; }}>＋</button>
          {#if addOpen}
            <div class="popover" role="menu">
              <div class="pop-h">New tab</div>
              {#each Object.entries(WIDGET_LABELS) as [w, label]}
                <button role="menuitem" onclick={(e) => { e.stopPropagation(); onAddTab?.(node.id, w); addOpen = false; }}>{label}</button>
              {/each}
            </div>
          {/if}
        </div>
      </div>

      <div class="head-right">
        {#if cur.widget === 'terminal'}
          <select
            class="agent-sel"
            value={cur.config?.agent || ''}
            onclick={(e) => e.stopPropagation()}
            onchange={(e) => onSetAgent?.(node.id, e.currentTarget.value)}
            title="Bind this pane to an agent"
          >
            <option value="">(pick agent)</option>
            {#each sessions.filter(s => !s.exited) as s}
              <option value={s.name}>{agentIdentity(s).icon} {s.name}{s.live === false ? ' ·off' : ''}</option>
            {/each}
          </select>
        {/if}
        <div class="ctl-group">
          <button class="ctl" title="Split right" onclick={(e) => { e.stopPropagation(); onSplit?.(node.id, 'h'); }}>◧</button>
          <button class="ctl" title="Split down" onclick={(e) => { e.stopPropagation(); onSplit?.(node.id, 'v'); }}>⬓</button>
          <button class="ctl danger" title="Close pane" onclick={(e) => { e.stopPropagation(); onClose?.(node.id); }}>✕</button>
        </div>
      </div>
    </header>

    <div class="leaf-body">
      {#if cur.widget === 'terminal'}
        <MissionTerminal agent={cur.config?.agent || selectedAgent || ''} {mode} />
      {:else if cur.widget === 'team-transcript'}
        <MissionTranscript {teams} {mode} initialTeam={cur.config?.team || 'all'} />
      {:else if cur.widget === 'kanban'}
        <MissionRoster {sessions} {teams} {memberships} {selectedAgent} compact={true} {onSelectAgent} />
      {:else if cur.widget === 'inspector'}
        <MissionInspector {sessions} {memberships} {events} {selectedAgent} {mode} />
      {:else}
        <div class="unknown">unknown widget: {cur.widget}</div>
      {/if}
    </div>
  </section>
{:else}
  {@const ratio = node.ratio ?? 0.5}
  <div class="split {node.kind}" bind:this={containerEl}>
    <div class="split-a" style={node.kind === 'h' ? `width:${ratio * 100}%` : `height:${ratio * 100}%`}>
      <MissionPane node={node.a} {sessions} {teams} {memberships} {events} {mode} {selectedAgent} {focusedPaneID}
        {onSplit} {onClose} {onSetAgent} {onSetWidget} {onSetRatio} {onFocusPane} {onAddTab} {onCloseTab} {onActivateTab} {onSelectAgent} />
    </div>
    <div
      class="splitter {node.kind}"
      role="separator"
      aria-orientation={node.kind === 'h' ? 'vertical' : 'horizontal'}
      tabindex="0"
      onpointerdown={startDrag}
      title="Drag to resize"
    ><span class="grip"></span></div>
    <div class="split-b">
      <MissionPane node={node.b} {sessions} {teams} {memberships} {events} {mode} {selectedAgent} {focusedPaneID}
        {onSplit} {onClose} {onSetAgent} {onSetWidget} {onSetRatio} {onFocusPane} {onAddTab} {onCloseTab} {onActivateTab} {onSelectAgent} />
    </div>
  </div>
{/if}

<style>
  .split { display: flex; height: 100%; width: 100%; min-height: 0; min-width: 0; }
  .split.h { flex-direction: row; }
  .split.v { flex-direction: column; }
  .split-a { min-width: 0; min-height: 0; flex: 0 0 auto; }
  .split-b { flex: 1 1 0; min-width: 0; min-height: 0; }

  .splitter { background: var(--m-grid-line); flex: 0 0 auto; position: relative; }
  .splitter.h { width: 5px; cursor: col-resize; }
  .splitter.v { height: 5px; cursor: row-resize; }
  .splitter:hover, .splitter:focus-visible { background: var(--m-accent-2); outline: none; }
  .splitter .grip {
    position: absolute; inset: 0; display: block;
  }
  .splitter.h .grip::before {
    content: ''; position: absolute; top: 50%; left: 50%; transform: translate(-50%,-50%);
    width: 2px; height: 26px; border-radius: 2px;
    background: var(--m-border-strong);
  }
  .splitter.v .grip::before {
    content: ''; position: absolute; top: 50%; left: 50%; transform: translate(-50%,-50%);
    width: 26px; height: 2px; border-radius: 2px;
    background: var(--m-border-strong);
  }
  .splitter:hover .grip::before { background: var(--m-bg); }

  .leaf {
    display: flex; flex-direction: column; height: 100%; min-height: 0;
    background: var(--m-panel);
    border: 1px solid var(--m-border);
    border-radius: 4px;
    overflow: hidden;
    margin: 2px;
  }
  .leaf.focused { border-color: var(--m-accent-2); box-shadow: 0 0 0 1px var(--m-accent-2) inset, 0 0 14px -4px var(--m-glow); }

  .leaf-head {
    display: flex; align-items: center; justify-content: space-between; gap: 0.4rem;
    background: var(--m-panel-2);
    border-bottom: 1px solid var(--m-border);
    padding: 0 0.3rem; min-height: 30px;
  }
  .tabs { display: flex; align-items: center; gap: 2px; overflow-x: auto; scrollbar-width: none; }
  .tabs::-webkit-scrollbar { display: none; }
  .tab {
    display: inline-flex; align-items: center; gap: 0.35rem;
    background: transparent; border: 1px solid transparent; border-radius: 4px 4px 0 0;
    color: var(--m-fg-dim); font: inherit; font-size: 0.72rem; line-height: 1;
    padding: 0.32rem 0.5rem; cursor: pointer; white-space: nowrap;
    font-family: ui-monospace, monospace;
  }
  .tab:hover { color: var(--m-fg); background: var(--m-panel-3); }
  .tab.active { color: var(--m-fg); background: var(--m-panel); border-color: var(--m-border); border-bottom-color: var(--m-panel); }
  .tab .dot { width: 8px; height: 8px; border-radius: 2px; flex: 0 0 auto; }
  .tab .tlabel { letter-spacing: 0.02em; }
  .tabx { color: var(--m-fg-faint); padding: 0 0.1rem; border-radius: 3px; }
  .tabx:hover { color: var(--m-danger); }

  .addwrap { position: relative; }
  .head-right { display: flex; align-items: center; gap: 0.4rem; flex: 0 0 auto; }
  .agent-sel {
    background: var(--m-panel-3); color: var(--m-fg); border: 1px solid var(--m-border-strong);
    border-radius: 4px; font: inherit; font-size: 0.72rem; font-family: ui-monospace, monospace;
    padding: 0.18rem 0.3rem; max-width: 12rem;
  }
  .ctl-group { display: flex; gap: 1px; }
  .ctl {
    background: var(--m-panel-3); color: var(--m-fg-dim);
    border: 1px solid var(--m-border); border-radius: 4px;
    width: 22px; height: 22px; display: inline-flex; align-items: center; justify-content: center;
    cursor: pointer; font-size: 0.78rem; padding: 0;
  }
  .ctl:hover { color: var(--m-fg); border-color: var(--m-border-strong); background: var(--m-panel-2); }
  .ctl.add { color: var(--m-accent-2); }
  .ctl.danger:hover { color: var(--m-danger); border-color: var(--m-danger); }

  .popover {
    position: absolute; top: calc(100% + 4px); left: 0; z-index: 40;
    background: var(--m-panel-2); border: 1px solid var(--m-border-strong);
    border-radius: 6px; padding: 0.25rem; min-width: 10rem;
    box-shadow: 0 10px 28px -8px var(--m-shadow);
    display: flex; flex-direction: column; gap: 1px;
  }
  .pop-h { font-size: 0.66rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--m-fg-faint); padding: 0.25rem 0.45rem; }
  .popover button {
    text-align: left; background: transparent; border: 0; border-radius: 4px;
    color: var(--m-fg); font: inherit; font-size: 0.78rem; padding: 0.35rem 0.45rem; cursor: pointer;
  }
  .popover button:hover { background: var(--m-panel-3); color: var(--m-accent-2); }

  .leaf-body { flex: 1; min-height: 0; position: relative; }
  .unknown { padding: 1rem; color: var(--m-fg-faint); font-size: 0.8rem; }
</style>

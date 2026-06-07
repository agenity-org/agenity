<!--
  StudioPane — recursive editor-area renderer for the "studio" dashboard.

  A layout node is one of:
    - { kind:'pane', id, tabs:[{widget, config}], activeTab }  (leaf)
    - { kind:'h', a, b, ratio }   horizontal split (left | right) — drag X
    - { kind:'v', a, b, ratio }   vertical split (top / bottom)   — drag Y

  THE THREE FOUNDER REQUIREMENTS LIVE HERE:
    1. PANE SWITCHING  — each terminal tab carries its own config.agent; the
       tab header has an agent <select> so the operator re-points which agent
       a pane shows; clicking an Explorer row also re-binds the active pane.
       Multiple terminals are visible because each leaf renders independently.
    2. PANE RESIZING   — the hdivider / vdivider between split children is a
       draggable splitter that mutates node.ratio live (mouse + touch).
    3. LAYOUT FLEXIBILITY — split-right (⫶) and split-down (⫻) buttons grow
       the tree arbitrarily; tab '+' adds tabs; tab '×' / pane close prune it.
-->
<script>
  import { onMount, onDestroy } from 'svelte';
  import Self from './StudioPane.svelte';
  import StudioTerminal from './StudioTerminal.svelte';
  import StudioInspector from './StudioInspector.svelte';
  import StudioEvents from './StudioEvents.svelte';
  import TeamTranscript from '../TeamTranscript.svelte';
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let {
    node, sessions = [], teams = [], memberships = [], events = [],
    selectedAgent = null, focusTerminal = () => {},
    splitPane = () => {}, removePane = () => {}, closeTab = () => {},
    addTab = () => {}, setTab = () => {}, setTabWidget = () => {},
    setTabAgent = () => {}, focusedPaneID = '', setFocusedPane = () => {},
    onLayoutChange = () => {},
  } = $props();

  const WIDGETS = [
    { id: 'terminal', label: 'Terminal', glyph: '▣' },
    { id: 'team-transcript', label: 'Conversation', glyph: '💬' },
    { id: 'inspector', label: 'Inspector', glyph: 'ⓘ' },
    { id: 'events', label: 'Event log', glyph: '≣' },
  ];
  function widgetMeta(w) { return WIDGETS.find(x => x.id === w) || { label: w, glyph: '▢' }; }

  function tabsOf(p) {
    if (Array.isArray(p.tabs) && p.tabs.length) return p.tabs;
    return [{ widget: p.widget || 'terminal', config: p.config || {} }];
  }
  function activeIdx(p) {
    const n = tabsOf(p).length;
    let i = typeof p.activeTab === 'number' ? p.activeTab : 0;
    return Math.max(0, Math.min(i, n - 1));
  }

  function tabLabel(t) {
    const m = widgetMeta(t.widget);
    if (t.widget === 'terminal') return (t.config?.agent || 'pick agent');
    return m.label;
  }

  let liveAgents = $derived((sessions || []).filter(s => !s.exited));

  // --- splitter drag (resize) ---
  let containerEl;
  let dragging = false;
  let dragStart = 0;
  let dragStartRatio = 0.5;
  function startDrag(e) {
    dragging = true;
    const pt = (e.touches && e.touches[0]) || e;
    dragStart = node.kind === 'h' ? pt.clientX : pt.clientY;
    dragStartRatio = node.ratio ?? 0.5;
    document.addEventListener('mousemove', onDrag);
    document.addEventListener('mouseup', endDrag);
    document.addEventListener('touchmove', onDrag, { passive: false });
    document.addEventListener('touchend', endDrag);
    document.body.style.userSelect = 'none';
    document.body.style.cursor = node.kind === 'h' ? 'col-resize' : 'row-resize';
    e.preventDefault();
  }
  function onDrag(e) {
    if (!dragging || !containerEl) return;
    if (e.cancelable) e.preventDefault();
    const rect = containerEl.getBoundingClientRect();
    const pt = (e.touches && e.touches[0]) || e;
    const cur = node.kind === 'h' ? pt.clientX : pt.clientY;
    const span = node.kind === 'h' ? rect.width : rect.height;
    if (span <= 0) return;
    let r = dragStartRatio + (cur - dragStart) / span;
    r = Math.max(0.12, Math.min(0.88, r));
    node.ratio = r; // mutate in place — Svelte $state tree tracks it
  }
  function endDrag() {
    if (!dragging) return;
    dragging = false;
    document.removeEventListener('mousemove', onDrag);
    document.removeEventListener('mouseup', endDrag);
    document.removeEventListener('touchmove', onDrag);
    document.removeEventListener('touchend', endDrag);
    document.body.style.userSelect = '';
    document.body.style.cursor = '';
    onLayoutChange(); // persist resize
  }
  onDestroy(() => { if (dragging) endDrag(); });

  // --- per-pane tab "+" widget picker ---
  let pickerOpen = $state(false);
  function pickWidget(w) {
    pickerOpen = false;
    if (w === 'terminal') {
      const a = (liveAgents.find(s => s.role !== 'shepherd') || liveAgents[0])?.name || '';
      addTab(node.id, 'terminal', { agent: a });
    } else if (w === 'team-transcript') {
      addTab(node.id, 'team-transcript', { team: 'all' });
    } else {
      addTab(node.id, w, {});
    }
  }

  // keyboard tab-cycle dispatched from root
  function cycle(detail) {
    if (!node || node.kind !== 'pane' || detail.paneID !== node.id) return;
    const tabs = tabsOf(node);
    const i = (activeIdx(node) + (detail.direction || 1) + tabs.length) % tabs.length;
    setTab(node.id, i);
  }
  function newTabEvt(detail) {
    if (!node || node.kind !== 'pane' || detail.paneID !== node.id) return;
    pickWidget('terminal');
  }
  onMount(() => {
    const c = (e) => cycle(e.detail || {});
    const n = (e) => newTabEvt(e.detail || {});
    window.addEventListener('studio-cycle-tab', c);
    window.addEventListener('studio-new-tab', n);
    return () => {
      window.removeEventListener('studio-cycle-tab', c);
      window.removeEventListener('studio-new-tab', n);
    };
  });
</script>

{#if node.kind === 'pane'}
  {@const tabs = tabsOf(node)}
  {@const ai = activeIdx(node)}
  {@const active = tabs[ai]}
  <div
    class="pane"
    class:focused={node.id === focusedPaneID}
    data-pane-id={node.id}
    onmousedown={() => setFocusedPane(node.id)}
    role="group"
  >
    <div class="tabbar">
      <div class="tabs">
        {#each tabs as t, ti}
          <button
            class="tab"
            class:active={ti === ai}
            onclick={() => { setTab(node.id, ti); if (t.widget === 'terminal' && t.config?.agent) focusTerminal(t.config.agent); }}
            title={widgetMeta(t.widget).label}
          >
            {#if t.widget === 'terminal' && t.config?.agent}
              <span class="tdot" style="background:{agentIdentity(t.config.agent).color}"></span>
            {:else}
              <span class="tglyph">{widgetMeta(t.widget).glyph}</span>
            {/if}
            <span class="tlabel">{tabLabel(t)}</span>
            {#if tabs.length > 1}
              <span class="tclose" role="button" tabindex="0"
                    onclick={(e) => { e.stopPropagation(); closeTab(node.id, ti); }}
                    onkeydown={(e) => { if (e.key === 'Enter') { e.stopPropagation(); closeTab(node.id, ti); } }}
                    title="Close tab">×</span>
            {/if}
          </button>
        {/each}
        <div class="addwrap">
          <button class="tadd" onclick={() => (pickerOpen = !pickerOpen)} title="New tab">+</button>
          {#if pickerOpen}
            <div class="picker-backdrop" onclick={() => (pickerOpen = false)} role="presentation"></div>
            <div class="picker" role="menu">
              {#each WIDGETS as w}
                <button role="menuitem" onclick={() => pickWidget(w.id)}>
                  <span class="pglyph">{w.glyph}</span>{w.label}
                </button>
              {/each}
            </div>
          {/if}
        </div>
      </div>
      <div class="paneops">
        {#if active.widget === 'terminal'}
          <select
            class="agentpick"
            value={active.config?.agent || ''}
            onchange={(e) => { setTabAgent(node.id, ai, e.currentTarget.value); focusTerminal(e.currentTarget.value); }}
            title="Show another agent in this pane"
            onmousedown={(e) => e.stopPropagation()}
          >
            <option value="" disabled>pick agent…</option>
            {#each liveAgents as s}
              <option value={s.name}>{agentIdentity(s).icon} {s.name}</option>
            {/each}
          </select>
        {:else}
          <select
            class="agentpick"
            value={active.widget}
            onchange={(e) => setTabWidget(node.id, ai, e.currentTarget.value)}
            title="Change this pane's widget"
            onmousedown={(e) => e.stopPropagation()}
          >
            {#each WIDGETS as w}<option value={w.id}>{w.glyph} {w.label}</option>{/each}
          </select>
        {/if}
        <button class="op" onclick={() => splitPane(node.id, 'h')} title="Split right">⫶</button>
        <button class="op" onclick={() => splitPane(node.id, 'v')} title="Split down">⫻</button>
        <button class="op close" onclick={() => removePane(node.id)} title="Close pane">✕</button>
      </div>
    </div>

    <div class="pane-body">
      {#if active.widget === 'terminal'}
        <StudioTerminal {selectedAgent} {sessions} node={active} />
      {:else if active.widget === 'team-transcript'}
        <TeamTranscript team={active.config?.team || 'all'} />
      {:else if active.widget === 'inspector'}
        <StudioInspector {sessions} {memberships} {selectedAgent} />
      {:else if active.widget === 'events'}
        <StudioEvents {events} />
      {:else}
        <div class="unknown">Unknown widget: {active.widget}</div>
      {/if}
    </div>
  </div>

{:else if node.kind === 'h'}
  <div class="split h" bind:this={containerEl}>
    <div class="cell" style="width:{(node.ratio ?? 0.5) * 100}%">
      <Self node={node.a} {sessions} {teams} {memberships} {events} {selectedAgent}
        {focusTerminal} {splitPane} {removePane} {closeTab} {addTab} {setTab}
        {setTabWidget} {setTabAgent} {focusedPaneID} {setFocusedPane} {onLayoutChange} />
    </div>
    <div class="divider hdiv" class:dragging onmousedown={startDrag} ontouchstart={startDrag} role="separator" aria-label="Resize panes horizontally" tabindex="-1"></div>
    <div class="cell" style="width:{(1 - (node.ratio ?? 0.5)) * 100}%">
      <Self node={node.b} {sessions} {teams} {memberships} {events} {selectedAgent}
        {focusTerminal} {splitPane} {removePane} {closeTab} {addTab} {setTab}
        {setTabWidget} {setTabAgent} {focusedPaneID} {setFocusedPane} {onLayoutChange} />
    </div>
  </div>

{:else if node.kind === 'v'}
  <div class="split v" bind:this={containerEl}>
    <div class="cell" style="height:{(node.ratio ?? 0.5) * 100}%">
      <Self node={node.a} {sessions} {teams} {memberships} {events} {selectedAgent}
        {focusTerminal} {splitPane} {removePane} {closeTab} {addTab} {setTab}
        {setTabWidget} {setTabAgent} {focusedPaneID} {setFocusedPane} {onLayoutChange} />
    </div>
    <div class="divider vdiv" class:dragging onmousedown={startDrag} ontouchstart={startDrag} role="separator" aria-label="Resize panes vertically" tabindex="-1"></div>
    <div class="cell" style="height:{(1 - (node.ratio ?? 0.5)) * 100}%">
      <Self node={node.b} {sessions} {teams} {memberships} {events} {selectedAgent}
        {focusTerminal} {splitPane} {removePane} {closeTab} {addTab} {setTab}
        {setTabWidget} {setTabAgent} {focusedPaneID} {setFocusedPane} {onLayoutChange} />
    </div>
  </div>
{/if}

<style>
  .pane { display: flex; flex-direction: column; height: 100%; min-height: 0; min-width: 0;
    background: var(--st-bg); border: 1px solid var(--st-border); border-radius: 8px; overflow: hidden; }
  .pane.focused { border-color: var(--st-accent); box-shadow: 0 0 0 1px var(--st-accent) inset; }

  .tabbar { display: flex; align-items: stretch; justify-content: space-between; gap: 0.4rem;
    background: var(--st-panel); border-bottom: 1px solid var(--st-border); min-height: 2.1rem; }
  .tabs { display: flex; align-items: stretch; overflow-x: auto; scrollbar-width: thin; }
  .tab { display: flex; align-items: center; gap: 0.4rem; padding: 0 0.7rem; background: transparent;
    border: 0; border-right: 1px solid var(--st-border); color: var(--st-fg-muted); cursor: pointer;
    font: inherit; font-size: 0.78rem; white-space: nowrap; max-width: 14rem; }
  .tab:hover { background: var(--st-hover); color: var(--st-fg); }
  .tab.active { background: var(--st-bg); color: var(--st-fg);
    box-shadow: inset 0 2px 0 var(--st-accent); }
  .tdot { width: 0.55rem; height: 0.55rem; border-radius: 50%; flex-shrink: 0; }
  .tglyph { font-size: 0.85rem; }
  .tlabel { overflow: hidden; text-overflow: ellipsis; }
  .tclose { margin-left: 0.1rem; padding: 0 0.2rem; border-radius: 4px; color: var(--st-fg-faint); font-size: 0.95rem; line-height: 1; }
  .tclose:hover { background: var(--st-danger); color: #fff; }
  .addwrap { position: relative; display: flex; align-items: center; }
  .tadd { background: transparent; border: 0; color: var(--st-fg-muted); font-size: 1.05rem; cursor: pointer; padding: 0 0.55rem; height: 100%; }
  .tadd:hover { color: var(--st-fg); background: var(--st-hover); }
  .picker-backdrop { position: fixed; inset: 0; z-index: 40; }
  .picker { position: absolute; top: 100%; left: 0; z-index: 41; min-width: 11rem; margin-top: 0.2rem;
    background: var(--st-panel); border: 1px solid var(--st-border-strong); border-radius: 8px;
    padding: 0.3rem; box-shadow: var(--st-shadow); display: flex; flex-direction: column; gap: 0.1rem; }
  .picker button { display: flex; align-items: center; gap: 0.55rem; background: transparent; border: 0;
    border-radius: 5px; color: var(--st-fg); padding: 0.45rem 0.6rem; font: inherit; font-size: 0.82rem; cursor: pointer; text-align: left; }
  .picker button:hover { background: var(--st-hover); }
  .pglyph { width: 1.2rem; text-align: center; }

  .paneops { display: flex; align-items: center; gap: 0.2rem; padding: 0 0.4rem; flex-shrink: 0; }
  .agentpick { background: var(--st-chip); border: 1px solid var(--st-border); border-radius: 6px;
    color: var(--st-fg); font: inherit; font-size: 0.74rem; padding: 0.15rem 0.3rem; max-width: 10rem; }
  .agentpick:focus { outline: none; border-color: var(--st-accent); }
  .op { background: transparent; border: 0; color: var(--st-fg-muted); cursor: pointer; font-size: 0.9rem;
    width: 1.7rem; height: 1.7rem; border-radius: 5px; display: grid; place-items: center; }
  .op:hover { background: var(--st-hover); color: var(--st-fg); }
  .op.close:hover { background: var(--st-danger); color: #fff; }

  .pane-body { flex: 1; min-height: 0; min-width: 0; position: relative; overflow: hidden; }
  .unknown { padding: 1rem; color: var(--st-fg-faint); }

  .split { display: flex; height: 100%; width: 100%; min-height: 0; min-width: 0; }
  .split.h { flex-direction: row; }
  .split.v { flex-direction: column; }
  .cell { min-width: 0; min-height: 0; overflow: hidden; }
  .split.h > .cell { height: 100%; }
  .split.v > .cell { width: 100%; }
  .divider { flex-shrink: 0; background: transparent; position: relative; z-index: 5; }
  .hdiv { width: 8px; cursor: col-resize; margin: 0 -1px; }
  .vdiv { height: 8px; cursor: row-resize; margin: -1px 0; }
  .divider::after { content: ''; position: absolute; background: var(--st-border); transition: background 0.12s; }
  .hdiv::after { top: 0; bottom: 0; left: 50%; width: 2px; transform: translateX(-50%); }
  .vdiv::after { left: 0; right: 0; top: 50%; height: 2px; transform: translateY(-50%); }
  .divider:hover::after, .divider.dragging::after { background: var(--st-accent); }
  .hdiv:hover::after, .hdiv.dragging::after { width: 3px; }
  .vdiv:hover::after, .vdiv.dragging::after { height: 3px; }
</style>

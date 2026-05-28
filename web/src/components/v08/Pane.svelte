<!--
  Pane.svelte — recursive workspace renderer. Each node is either:
  - kind: 'pane'   → one widget (leaf)
  - kind: 'h'      → HSplit (left | right) with draggable divider
  - kind: 'v'      → VSplit (top / bottom) with draggable divider
-->
<script>
  import { onMount, onDestroy } from 'svelte';
  import Self from './Pane.svelte';
  import WidgetTerminal from './widgets/WidgetTerminal.svelte';
  import WidgetSessionList from './widgets/WidgetSessionList.svelte';
  import WidgetSessionBoard from './widgets/WidgetSessionBoard.svelte';
  import WidgetCard from './widgets/WidgetCard.svelte';
  import WidgetInbox from './widgets/WidgetInbox.svelte';
  import WidgetEvents from './widgets/WidgetEvents.svelte';
  import WidgetSpider from './widgets/WidgetSpider.svelte';
  import WidgetAgentPrompt from './widgets/WidgetAgentPrompt.svelte';
  import WidgetAgentSkills from './widgets/WidgetAgentSkills.svelte';
  import WidgetAgentDetails from './widgets/WidgetAgentDetails.svelte';
  import WidgetAccounts from './widgets/WidgetAccounts.svelte';
  import WidgetRoleMatrix from './widgets/WidgetRoleMatrix.svelte';
  import WidgetAgentIdentity from './widgets/WidgetAgentIdentity.svelte';
  import WidgetAgentRuntime from './widgets/WidgetAgentRuntime.svelte';
  import WidgetCanon from './widgets/WidgetCanon.svelte';
  import WidgetMCPLog from './widgets/WidgetMCPLog.svelte';
  import WidgetKanban from './widgets/WidgetKanban.svelte';

  let { node, sessions, teams, memberships, inbox, events, selectedAgent, selectAgent, changeWidget, splitPane, removePane, refresh, focusedPaneID = '', setFocusedPane = () => {}, saveLayout = () => {} } = $props();

  // --- Tabs (operator 2026-05-29: any pane can have multiple tabs;
  // each tab carries a widget + config snapshot; the active tab's
  // state lives in node.widget / node.config for backwards-compat
  // with the existing render path). -------------------------------
  function ensureTabs() {
    if (node.kind !== 'pane') return;
    if (!Array.isArray(node.tabs) || node.tabs.length === 0) {
      node.tabs = [{ widget: node.widget, config: node.config || {} }];
      node.activeTab = 0;
    }
    if (typeof node.activeTab !== 'number' || node.activeTab < 0 || node.activeTab >= node.tabs.length) {
      node.activeTab = 0;
    }
  }
  function tabLabel(t) {
    const w = WIDGET_LABELS[t.widget] || t.widget || '?';
    // Strip the leading icon glyph to keep tab labels compact.
    const noGlyph = w.replace(/^[^a-zA-Z]+\s*/, '');
    if (t.widget === 'terminal' && t.config?.agent) return noGlyph + ' · ' + t.config.agent;
    return noGlyph;
  }
  function snapshotActive() {
    if (node.kind !== 'pane') return;
    ensureTabs();
    node.tabs[node.activeTab] = { widget: node.widget, config: node.config || {} };
  }
  function applyTab(i) {
    if (node.kind !== 'pane') return;
    ensureTabs();
    if (i < 0 || i >= node.tabs.length) return;
    const t = node.tabs[i];
    node.activeTab = i;
    node.widget = t.widget;
    node.config = t.config || {};
  }
  function switchTab(i) {
    if (i === node.activeTab) return;
    snapshotActive();
    applyTab(i);
    saveLayout();
  }
  function addTab(opts = {}) {
    ensureTabs();
    snapshotActive();
    // Default: duplicate the current widget with a fresh agent slot.
    // For terminal widgets, an empty agent means "auto-pick" (matches
    // WidgetTerminal's default behaviour).
    const fresh = {
      widget: opts.widget || node.widget,
      config: opts.widget === 'terminal' || node.widget === 'terminal'
        ? { agent: '' }
        : { ...(node.config || {}) },
    };
    node.tabs = [...node.tabs, fresh];
    applyTab(node.tabs.length - 1);
    saveLayout();
  }
  function closeTab(i, ev) {
    ev?.stopPropagation?.();
    ensureTabs();
    if (node.tabs.length <= 1) return; // last tab — keep it
    const wasActive = i === node.activeTab;
    node.tabs = node.tabs.filter((_, idx) => idx !== i);
    if (wasActive) {
      const next = Math.min(i, node.tabs.length - 1);
      applyTab(next);
    } else if (i < node.activeTab) {
      node.activeTab = node.activeTab - 1;
    }
    saveLayout();
  }
  function cycleTab(direction) {
    ensureTabs();
    if (node.tabs.length <= 1) return;
    const j = (node.activeTab + direction + node.tabs.length) % node.tabs.length;
    switchTab(j);
  }

  // Window-level event wiring — Workspace's keydown handler dispatches
  // these whenever the focused pane id matches.
  function onNewTabEvent(ev) {
    if (ev.detail?.paneID === node.id) addTab();
  }
  function onCycleTabEvent(ev) {
    if (ev.detail?.paneID === node.id) cycleTab(ev.detail?.direction || +1);
  }
  onMount(() => {
    if (node.kind !== 'pane') return;
    ensureTabs();
    if (typeof window === 'undefined') return;
    window.addEventListener('chepherd-pane-new-tab', onNewTabEvent);
    window.addEventListener('chepherd-pane-cycle-tab', onCycleTabEvent);
  });
  onDestroy(() => {
    if (typeof window === 'undefined') return;
    window.removeEventListener('chepherd-pane-new-tab', onNewTabEvent);
    window.removeEventListener('chepherd-pane-cycle-tab', onCycleTabEvent);
  });

  function selectedAgentObject() {
    return sessions?.find(s => s.name === selectedAgent) || null;
  }

  let containerEl;
  let dividerDragging = false;
  let dragStartPos = 0;
  let dragStartRatio = 0;

  function startDrag(e) {
    dividerDragging = true;
    dragStartPos = node.kind === 'h' ? e.clientX : e.clientY;
    dragStartRatio = node.ratio;
    document.addEventListener('mousemove', onDrag);
    document.addEventListener('mouseup', endDrag);
  }
  function onDrag(e) {
    if (!dividerDragging || !containerEl) return;
    const rect = containerEl.getBoundingClientRect();
    const total = node.kind === 'h' ? rect.width : rect.height;
    const delta = (node.kind === 'h' ? e.clientX : e.clientY) - dragStartPos;
    let r = dragStartRatio + delta / total;
    r = Math.max(0.1, Math.min(0.9, r));
    node.ratio = r; // mutate in place — Svelte tracks via $state
  }
  function endDrag() {
    dividerDragging = false;
    document.removeEventListener('mousemove', onDrag);
    document.removeEventListener('mouseup', endDrag);
  }

  // #144 — removed legacy "details (both)" combo widget. Identity +
  // Runtime are independent addable widgets. Saved layouts using the
  // old 'agent-details' key are auto-migrated to 'agent-identity' in
  // Pane.svelte's render branch below (kept that case so loaded
  // workspaces don't crash).
  const WIDGET_LABELS = {
    'terminal': '▦ terminal',
    'session-list': '☰ sessions',
    'session-board': '▤ board',
    'agent-identity': 'ⓘ identity',
    'agent-runtime': '⚙ runtime',
    'shepherd-assessment-card': '✻ scorecard',
    'inbox': '✉ inbox',
    'events': '⏱ events',
    'mcp-log': '🔧 MCP log',
    'canon-viewer': '📜 canon',
    'agent-prompt': '✏ prompt',
    'agent-skills': '🎮 skills',
    'kanban': '⊞ kanban',
    'accounts': '⚓ accounts',
    'role-matrix': '🎮 roles',
  };

  // Per-pane derived agent for the terminal widget header chips.
  let paneAgent = $derived(() => {
    if (node?.kind !== 'pane') return null;
    if (node.widget !== 'terminal') return null;
    const want = node.config?.agent || selectedAgent;
    return (sessions || []).find(s => s.name === want) || null;
  });

  function relAge(at) {
    if (!at) return '—';
    const s = Math.floor((Date.now() - new Date(at).getTime()) / 1000);
    if (s < 60) return `${s}s`;
    if (s < 3600) return `${Math.floor(s/60)}m`;
    if (s < 86400) return `${Math.floor(s/3600)}h`;
    return `${Math.floor(s/86400)}d`;
  }
  function ctxPct(a) {
    if (!a?.context_size || !a?.context_tokens) return null;
    return Math.min(100, (a.context_tokens / a.context_size) * 100);
  }

  function pickPaneAgent(ev) {
    if (!node) return;
    if (!node.config) node.config = {};
    node.config.agent = ev.target.value;
  }

  const WIDGETS = Object.keys(WIDGET_LABELS);

  let fullscreen = $state(false);
  function toggleFullscreen() { fullscreen = !fullscreen; }
</script>

{#if node.kind === 'h'}
  <div class="hsplit" bind:this={containerEl}>
    <div class="hcell" style="width: {node.ratio * 100}%;">
      <Self node={node.a} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {changeWidget} {splitPane} {removePane} {refresh} {focusedPaneID} {setFocusedPane} {saveLayout} />
    </div>
    <div class="hdivider" on:mousedown={startDrag}></div>
    <div class="hcell" style="width: {(1 - node.ratio) * 100}%;">
      <Self node={node.b} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {changeWidget} {splitPane} {removePane} {refresh} {focusedPaneID} {setFocusedPane} {saveLayout} />
    </div>
  </div>
{:else if node.kind === 'v'}
  <div class="vsplit" bind:this={containerEl}>
    <div class="vcell" style="height: {node.ratio * 100}%;">
      <Self node={node.a} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {changeWidget} {splitPane} {removePane} {refresh} {focusedPaneID} {setFocusedPane} {saveLayout} />
    </div>
    <div class="vdivider" on:mousedown={startDrag}></div>
    <div class="vcell" style="height: {(1 - node.ratio) * 100}%;">
      <Self node={node.b} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {changeWidget} {splitPane} {removePane} {refresh} {focusedPaneID} {setFocusedPane} {saveLayout} />
    </div>
  </div>
{:else}
  <!-- leaf pane: widget container -->
  <div
    class="pane"
    class:fullscreen
    class:is-focused={focusedPaneID === node.id}
    data-pane-id={node.id}
    on:mousedown={() => setFocusedPane(node.id)}
  >
    <!-- Tab strip (operator request 2026-05-29 — per-pane tabs; any
         widget can host them). Always shows "+" so the first extra
         tab is one click away. -->
    {#if Array.isArray(node.tabs) && node.tabs.length > 0}
      <div class="pane-tabs" role="tablist" aria-label="pane tabs">
        {#each node.tabs as t, i (i)}
          <button
            role="tab"
            class="pane-tab"
            class:active={i === (node.activeTab || 0)}
            on:click={() => switchTab(i)}
            title="Ctrl+Tab cycles · Ctrl+Alt+Tab if browser steals Ctrl+Tab"
          >
            <span class="pt-label">{tabLabel(t)}</span>
            {#if node.tabs.length > 1}
              <span class="pt-close" on:click={(e) => closeTab(i, e)} title="close tab">×</span>
            {/if}
          </button>
        {/each}
        <button
          class="pane-tab-add"
          on:click={() => addTab()}
          title="new tab (Ctrl+T · Ctrl+Alt+T if browser steals Ctrl+T)"
        >+</button>
      </div>
    {/if}
    <header class="pane-header">
      <select class="widget-pick" value={node.widget} on:change={(e) => changeWidget(node.id, e.target.value)}>
        {#each WIDGETS as w}
          <option value={w}>{WIDGET_LABELS[w]}</option>
        {/each}
      </select>

      {#if node.widget === 'terminal'}
        <!-- Terminal-specific header content: agent picker + status chips,
             in the same single header line per operator request. No
             secondary header inside WidgetTerminal. -->
        <select class="agent-pick" value={node.config?.agent || selectedAgent || ''} on:change={pickPaneAgent} title="agent attached to this terminal">
          <option value="">(pick agent)</option>
          {#each (sessions || []) as s}
            <option value={s.name}>{s.role === 'shepherd' ? '✻ ' : '● '}{s.name}</option>
          {/each}
        </select>
        {@const a = paneAgent()}
        {#if a}
          <span class="chip ok" title="agent is live + reading from its PTY">● Live</span>
          <span class="chip muted" title="time since spawn">{relAge(a.created_at)} ago</span>
          {@const p = ctxPct(a)}
          {#if p != null}<span class="chip ctx" title="{a.context_tokens?.toLocaleString()} / {a.context_size?.toLocaleString()} context tokens">Ctx: {p.toFixed(0)}%</span>{/if}
          <button class="gear" title="agent settings (prompt / skills / canon / membership / actions)" on:click={() => window.dispatchEvent(new CustomEvent('chepherd-open-agent-settings', { detail: { agentName: a.name } }))}>⚙</button>
        {/if}
      {/if}

      <div class="spacer"></div>
      <button title="split horizontally (add right)" on:click={() => splitPane(node.id, 'h')}>⬌</button>
      <button title="split vertically (add below)" on:click={() => splitPane(node.id, 'v')}>⬍</button>
      <button title={fullscreen ? 'exit fullscreen' : 'fullscreen'} on:click={toggleFullscreen} class:active={fullscreen}>
        {#if fullscreen}
          <svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 2H2v4M14 6V2h-4M10 14h4v-4M2 10v4h4"/></svg>
        {:else}
          <svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M2 6V2h4M14 10v4h-4M10 2h4v4M6 14H2v-4"/></svg>
        {/if}
      </button>
      <button title="close" on:click={() => removePane(node.id)}>×</button>
    </header>
    <div class="pane-body">
      {#if node.widget === 'terminal'}
        <WidgetTerminal {selectedAgent} {sessions} {node} />
      {:else if node.widget === 'session-list'}
        <WidgetSessionList {sessions} {teams} {memberships} {selectedAgent} {selectAgent} />
      {:else if node.widget === 'session-board'}
        <WidgetSessionBoard {sessions} {selectedAgent} {selectAgent} />
      {:else if node.widget === 'agent-details'}
        <WidgetAgentDetails agent={(sessions || []).find(s => s.name === selectedAgent) || null} />
      {:else if node.widget === 'agent-identity'}
        <WidgetAgentIdentity agent={(sessions || []).find(s => s.name === selectedAgent) || null} />
      {:else if node.widget === 'agent-runtime'}
        <WidgetAgentRuntime agent={(sessions || []).find(s => s.name === selectedAgent) || null} />
      {:else if node.widget === 'shepherd-assessment-card'}
        <WidgetSpider {selectedAgent} {sessions} />
      {:else if node.widget === 'inbox'}
        <WidgetInbox {inbox} />
      {:else if node.widget === 'events'}
        <WidgetEvents {events} />
      {:else if node.widget === 'mcp-log'}
        <WidgetMCPLog {events} />
      {:else if node.widget === 'agent-prompt'}
        <WidgetAgentPrompt agent={selectedAgentObject()} />
      {:else if node.widget === 'agent-skills'}
        <WidgetAgentSkills agent={selectedAgentObject()} />
      {:else if node.widget === 'canon-viewer'}
        <WidgetCanon agent={selectedAgentObject()} {teams} />
      {:else if node.widget === 'kanban'}
        <WidgetKanban agent={selectedAgentObject()} {sessions} />
      {:else if node.widget === 'accounts'}
        <WidgetAccounts />
      {:else if node.widget === 'role-matrix'}
        <WidgetRoleMatrix />
      {:else}
        <div class="empty">widget: {node.widget}</div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .hsplit { display: flex; width: 100%; height: 100%; overflow: hidden; }
  .vsplit { display: flex; flex-direction: column; width: 100%; height: 100%; overflow: hidden; }
  .hcell, .vcell { overflow: hidden; min-width: 0; min-height: 0; }
  .hdivider { width: 6px; cursor: col-resize; background: var(--border); transition: background 0.1s; }
  .vdivider { height: 6px; cursor: row-resize; background: var(--border); transition: background 0.1s; }
  .hdivider:hover, .vdivider:hover { background: var(--accent); }
  .pane { display: flex; flex-direction: column; height: 100%; background: var(--bg); border: 1px solid var(--border); border-radius: 4px; overflow: hidden; }
  /* Per-pane tab strip — operator request 2026-05-29. Each tab
     stores its own (widget, config) so a single pane can host e.g.
     several agent terminals or several Kanbans side-by-side via
     tab switch instead of a split. */
  .pane-tabs {
    display: flex; align-items: stretch; gap: 0.1rem;
    padding: 0 0.35rem; height: 26px;
    background: var(--bg); border-bottom: 1px solid var(--border);
    overflow-x: auto; overflow-y: hidden;
    flex-shrink: 0;
  }
  .pane-tab {
    display: inline-flex; align-items: center; gap: 0.35rem;
    padding: 0 0.55rem; height: 100%;
    background: transparent; border: 0;
    color: var(--fg-muted); font: inherit; font-size: 0.74rem;
    cursor: pointer; max-width: 18rem;
    border-bottom: 2px solid transparent;
    white-space: nowrap;
  }
  .pane-tab:hover { background: var(--bg-elev); color: var(--fg); }
  .pane-tab.active { color: var(--fg); border-bottom-color: var(--accent, #87ceeb); background: var(--bg-elev); }
  .pt-label { overflow: hidden; text-overflow: ellipsis; }
  .pt-close { color: var(--fg-faint); padding: 0 0.15rem; font-size: 0.9rem; line-height: 1; border-radius: 3px; opacity: 0.6; }
  .pane-tab:hover .pt-close { opacity: 1; }
  .pt-close:hover { background: rgba(231,76,60,0.18); color: #e74c3c; }
  .pane-tab-add {
    background: transparent; border: 0; color: var(--fg-muted);
    font: inherit; font-size: 0.95rem; padding: 0 0.6rem; cursor: pointer;
  }
  .pane-tab-add:hover { color: var(--accent, #87ceeb); }
  .pane.fullscreen { position: fixed; inset: 0; z-index: 900; border-radius: 0; border: none; }
  /* Ctrl+Arrow pane focus indicator (operator request 2026-05-29). */
  .pane.is-focused { border-color: var(--accent, #87ceeb); box-shadow: inset 0 0 0 1px var(--accent, #87ceeb); }
  .pane-header button.active { color: var(--accent); }
  .pane-header { display: flex; align-items: center; padding: 0.25rem 0.4rem; background: var(--bg-elev); border-bottom: 1px solid var(--border); font-size: 0.78rem; gap: 0.3rem; }
  .pane-header .spacer { flex: 1; }
  .pane-header button { background: transparent; color: var(--fg-muted); border: none; padding: 0 0.3rem; cursor: pointer; font-size: 0.85rem; }
  .pane-header button:hover { color: var(--accent); }
  .widget-pick { background: var(--bg-input); color: var(--fg); border: 1px solid var(--border); border-radius: 4px; padding: 0.15rem 0.3rem; font-size: 0.78rem; cursor: pointer; }
  .agent-pick { background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 4px; padding: 0.15rem 0.4rem; font-size: 0.78rem; cursor: pointer; max-width: 180px; }
  .gear { background: transparent; color: var(--fg-muted); border: none; padding: 0 0.3rem; cursor: pointer; }
  .gear:hover { color: var(--accent); }
  .chip { display: inline-flex; align-items: center; padding: 0.05rem 0.5rem; border-radius: 999px; font-size: 0.7rem; font-weight: 600; line-height: 1.4; }
  .chip.ok { background: rgba(80, 200, 120, 0.15); color: #5cd57f; }
  .chip.muted { background: rgba(150, 150, 150, 0.12); color: var(--fg-muted); }
  .chip.ctx { background: rgba(135, 206, 235, 0.15); color: var(--accent-2); }
  .pane-body { flex: 1; overflow: hidden; min-height: 0; }
  .empty { color: var(--fg-faint); padding: 1rem; text-align: center; font-size: 0.85rem; }
</style>

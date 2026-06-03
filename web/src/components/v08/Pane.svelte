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
  import TeamTranscript from '../TeamTranscript.svelte';
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
  import WidgetInspector from './widgets/WidgetInspector.svelte';
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let { node, sessions, teams, memberships, inbox, events, selectedAgent, selectAgent, focusTerminal = () => {}, terminalClosed = () => {}, changeWidget, splitPane, removePane, refresh, focusedPaneID = '', setFocusedPane = () => {}, saveLayout = () => {} } = $props();

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
    if (!t.widget) return '+ pick widget';
    const w = WIDGET_LABELS[t.widget] || t.widget;
    const noGlyph = w.replace(/^[^a-zA-Z]+\s*/, '');
    if (t.widget === 'terminal' && t.config?.agent) return noGlyph + ' · ' + t.config.agent;
    if (t.widget === 'terminal') return noGlyph + ' · pick agent';
    return noGlyph;
  }

  // Right-click context picker (operator 2026-05-29: 'if the user is
  // willing to change the content of the tab, let him right clicking
  // on the tab'). For terminal tabs starts at agent level; the agent
  // picker has a "← change widget" back button.
  let menu = $state(null); // { tabIdx, level: 'widget'|'agent' }
  function onTabContext(ev, i) {
    ev.preventDefault();
    const t = (node.tabs || [])[i] || { widget: '' };
    const level = t.widget === 'terminal' ? 'agent' : 'widget';
    pickerFilter = ''; // #692 review nit — don't carry stale filter text between picker surfaces
    menu = { tabIdx: i, level };
  }
  function closeMenu() { menu = null; }

  // Role logos for agent cards (operator 2026-05-29: 'didnt show the
  // icons of the roles accordingly'). Same Lucide-style set used on
  // Stage 1 of the spawn wizard so the visual language is consistent.
  const ROLE_LOGO = {
    'product-owner':        '<rect x="5" y="4" width="14" height="17" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><path d="M9 4v2h6V4" fill="none" stroke="currentColor" stroke-width="2"/><path d="M9 12h6M9 16h6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
    'architect':            '<path d="M3 21l9-18 9 18" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/><path d="M7 13h10" fill="none" stroke="currentColor" stroke-width="2"/>',
    'tech-lead':            '<path d="M13 2L4 14h7l-1 8 9-12h-7l1-8z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/>',
    'scrum-master':         '<path d="M21 12a9 9 0 1 1-3-6.7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/><path d="M21 4v5h-5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>',
    'generalist':           '<circle cx="12" cy="8" r="4" fill="none" stroke="currentColor" stroke-width="2"/><path d="M4 22a8 8 0 0 1 16 0" fill="none" stroke="currentColor" stroke-width="2"/>',
    'full-stack-developer': '<polyline points="8,18 2,12 8,6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/><polyline points="16,6 22,12 16,18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>',
    'frontend-developer':   '<rect x="2" y="4" width="20" height="14" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><path d="M8 22h8M12 18v4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
    'backend-developer':    '<rect x="3" y="4" width="18" height="6" rx="1" fill="none" stroke="currentColor" stroke-width="2"/><rect x="3" y="14" width="18" height="6" rx="1" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="7" cy="7" r="0.8" fill="currentColor"/><circle cx="7" cy="17" r="0.8" fill="currentColor"/>',
    'devops-sre':           '<circle cx="6" cy="6" r="2.5" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="6" cy="18" r="2.5" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="18" cy="12" r="2.5" fill="none" stroke="currentColor" stroke-width="2"/><path d="M8 6h8M8 18h8M6 8.5v7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
    'qa-engineer':          '<path d="M12 2L4 5v7c0 5 3.5 8.5 8 10 4.5-1.5 8-5 8-10V5l-8-3z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/><path d="M9 12l2 2 4-4" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/>',
    'security-engineer':    '<rect x="5" y="11" width="14" height="10" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4" fill="none" stroke="currentColor" stroke-width="2"/>',
    'code-reviewer':        '<circle cx="11" cy="11" r="6" fill="none" stroke="currentColor" stroke-width="2"/><path d="M21 21l-5.5-5.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
    'qa':                   '<path d="M12 2L4 5v7c0 5 3.5 8.5 8 10 4.5-1.5 8-5 8-10V5l-8-3z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/><path d="M9 12l2 2 4-4" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/>',
    'frontend':             '<rect x="2" y="4" width="20" height="14" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><path d="M8 22h8M12 18v4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
    'backend':              '<rect x="3" y="4" width="18" height="6" rx="1" fill="none" stroke="currentColor" stroke-width="2"/><rect x="3" y="14" width="18" height="6" rx="1" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="7" cy="7" r="0.8" fill="currentColor"/><circle cx="7" cy="17" r="0.8" fill="currentColor"/>',
    'devops':               '<circle cx="6" cy="6" r="2.5" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="6" cy="18" r="2.5" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="18" cy="12" r="2.5" fill="none" stroke="currentColor" stroke-width="2"/><path d="M8 6h8M8 18h8M6 8.5v7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
    'security':             '<rect x="5" y="11" width="14" height="10" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4" fill="none" stroke="currentColor" stroke-width="2"/>',
    'full-stack':           '<polyline points="8,18 2,12 8,6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/><polyline points="16,6 22,12 16,18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>',
  };
  function roleIcon(s) {
    // Sessions ship role_id post-v0.9.1; fall back to the agent's name
    // (which IS the role kebab for builtin slots after the 5158a15
    // rename) so we still get the right glyph for live agents.
    const key = s?.role_id || s?.name || '';
    return ROLE_LOGO[key] || ROLE_LOGO['generalist'];
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
    // #691 rule 2 — selecting a terminal tab moves the global focus to
    // its agent (inspector follows terminal selection, nothing else).
    if (node.widget === 'terminal' && node.config?.agent) focusTerminal(node.config.agent);
    saveLayout();
  }
  function addTab() {
    // Operator 2026-05-29: new tabs open EMPTY with a center card picker
    // — pick a widget; for terminal, cascade to pick an agent.
    ensureTabs();
    snapshotActive();
    node.tabs = [...node.tabs, { widget: '', config: {} }];
    applyTab(node.tabs.length - 1);
    menu = null;
    saveLayout();
  }
  // Set the (widget, config) of a tab by index — used by both the
  // center empty-tab picker and the right-click context menu.
  function setTabWidget(tabIdx, w) {
    ensureTabs();
    const t = node.tabs[tabIdx] || { widget: '', config: {} };
    const newConfig = w === 'terminal' ? { agent: t.config?.agent || '' } : {};
    node.tabs[tabIdx] = { widget: w, config: newConfig };
    if (tabIdx === node.activeTab) {
      node.widget = w;
      node.config = newConfig;
    }
    saveLayout();
  }
  function setTabAgent(tabIdx, agentName) {
    ensureTabs();
    const cfg = { agent: agentName };
    node.tabs[tabIdx] = { widget: 'terminal', config: cfg };
    if (tabIdx === node.activeTab) {
      node.widget = 'terminal';
      node.config = cfg;
    }
    menu = null;
    // #691 rule 2 — binding a terminal to an agent is a terminal
    // selection: focus follows.
    if (tabIdx === node.activeTab) focusTerminal(agentName);
    saveLayout();
  }
  // Reset a tab back to the widget-cards view (used by the "← change
  // widget" button in the agent picker for terminal tabs).
  function resetTabToWidgetPicker(tabIdx) {
    ensureTabs();
    node.tabs[tabIdx] = { widget: '', config: {} };
    if (tabIdx === node.activeTab) {
      node.widget = '';
      node.config = {};
    }
    saveLayout();
  }
  function closeTab(i, ev) {
    ev?.stopPropagation?.();
    ensureTabs();
    if (node.tabs.length <= 1) return; // last tab — keep it
    const wasActive = i === node.activeTab;
    const removed = node.tabs[i];
    node.tabs = node.tabs.filter((_, idx) => idx !== i);
    if (wasActive) {
      const next = Math.min(i, node.tabs.length - 1);
      applyTab(next);
    } else if (i < node.activeTab) {
      node.activeTab = node.activeTab - 1;
    }
    // #691 rule 3 — closing a terminal tab: the focus engine falls back
    // to the previously focused live agent (MRU) so the inspector keeps
    // showing "the last terminal" instead of a stale/blank agent.
    if (removed?.widget === 'terminal' && removed.config?.agent) terminalClosed(removed.config.agent);
    // The tab that replaced the active slot may itself be a terminal —
    // that's a terminal selection too (rule 2).
    if (wasActive && node.widget === 'terminal' && node.config?.agent) focusTerminal(node.config.agent);
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
    'events': '⏱ events',
    'mcp-log': '🔧 MCP log',
    'canon-viewer': '📜 canon',
    'agent-prompt': '✏ prompt',
    'agent-skills': '🎮 skills',
    'kanban': '⊞ kanban',
    'accounts': '⚓ accounts',
    'role-matrix': '🎮 roles',
    'team-transcript': '💬 team transcript',
    'inspector': 'ⓘ inspector',
  };

  // #692/#693 — the picker offers ONLY monitor-mode surfaces. Configure
  // widgets (accounts/roles/skills/canon) live in ⚙ Settings (#693);
  // legacy context widgets (identity/runtime/details/scorecard/prompt/
  // mcp-log/events/board) still RENDER for saved layouts but are no
  // longer pickable — the Inspector (#691) replaced them.
  const PICKER_GROUPS = [
    { label: 'monitor', items: [
      { id: 'terminal',        desc: 'live PTY for an agent' },
      { id: 'team-transcript', desc: 'team conversation' },
      { id: 'kanban',          desc: 'work board' },
      { id: 'inspector',       desc: 'focused-agent context (identity · scorecard · debug)' },
    ]},
  ];
  let pickerFilter = $state('');
  function pickerMatches(item) {
    const f = pickerFilter.trim().toLowerCase();
    if (!f) return true;
    const label = (WIDGET_LABELS[item.id] || item.id).toLowerCase();
    return label.includes(f) || item.desc.toLowerCase().includes(f) || item.id.includes(f);
  }

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


  let fullscreen = $state(false);
  function toggleFullscreen() { fullscreen = !fullscreen; }
</script>

{#if node.kind === 'h'}
  <div class="hsplit" bind:this={containerEl}>
    <div class="hcell" style="width: {node.ratio * 100}%;">
      <Self node={node.a} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {focusTerminal} {terminalClosed} {changeWidget} {splitPane} {removePane} {refresh} {focusedPaneID} {setFocusedPane} {saveLayout} />
    </div>
    <div class="hdivider" on:mousedown={startDrag}></div>
    <div class="hcell" style="width: {(1 - node.ratio) * 100}%;">
      <Self node={node.b} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {focusTerminal} {terminalClosed} {changeWidget} {splitPane} {removePane} {refresh} {focusedPaneID} {setFocusedPane} {saveLayout} />
    </div>
  </div>
{:else if node.kind === 'v'}
  <div class="vsplit" bind:this={containerEl}>
    <div class="vcell" style="height: {node.ratio * 100}%;">
      <Self node={node.a} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {focusTerminal} {terminalClosed} {changeWidget} {splitPane} {removePane} {refresh} {focusedPaneID} {setFocusedPane} {saveLayout} />
    </div>
    <div class="vdivider" on:mousedown={startDrag}></div>
    <div class="vcell" style="height: {(1 - node.ratio) * 100}%;">
      <Self node={node.b} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {focusTerminal} {terminalClosed} {changeWidget} {splitPane} {removePane} {refresh} {focusedPaneID} {setFocusedPane} {saveLayout} />
    </div>
  </div>
{:else}
  <!-- leaf pane: widget container -->
  <div
    class="pane"
    class:fullscreen
    class:is-focused={focusedPaneID === node.id}
    data-pane-id={node.id}
    on:mousedown={() => {
      setFocusedPane(node.id);
      // #691 rule 2 — clicking into a terminal pane selects that
      // terminal: global focus moves to its agent.
      if (node.widget === 'terminal' && node.config?.agent) focusTerminal(node.config.agent);
    }}
  >
    <header class="pane-header">
      <!-- Tabs integrated INTO the existing header (operator 2026-05-29:
           'what is the point of adding one more additional header').
           Each tab is a compact button; the active tab gets an underline.
           "+" adds a tab. The widget-pick dropdown still exists but is
           now hidden behind a ▾ on the ACTIVE tab — second-click reveals
           it so operator can change which widget/agent that tab holds. -->
      <div class="ph-tabs" role="tablist" aria-label="pane tabs">
        {#each (node.tabs || [{ widget: node.widget, config: node.config || {} }]) as t, i (i)}
          {@const isActive = i === (node.activeTab || 0)}
          {@const tabAgent = t.widget === 'terminal' && t.config?.agent ? (sessions || []).find(x => x.name === t.config.agent) : null}
          {@const tabIdent = tabAgent ? agentIdentity(tabAgent) : null}
          <button
            role="tab"
            class="ph-tab"
            class:active={isActive}
            class:empty={!t.widget}
            style={tabIdent && isActive ? `border-bottom-color: ${tabIdent.color}` : ''}
            on:click={() => switchTab(i)}
            on:contextmenu={(e) => onTabContext(e, i)}
            title="left-click: switch · right-click: change widget/agent · Ctrl+Alt+Tab cycles"
          >
            {#if tabIdent}<span class="ph-tab-ident" style="color: {tabIdent.color}">{tabIdent.icon}</span>{/if}
            <span class="ph-tab-label">{tabLabel(t)}</span>
            {#if (node.tabs?.length || 0) > 1}
              <span class="ph-tab-close" on:click={(e) => closeTab(i, e)} title="close tab">×</span>
            {/if}
          </button>
        {/each}
        <button
          class="ph-tab-add"
          on:click={() => addTab()}
          title="new tab (Ctrl+Alt+T · plain Ctrl+T works only when chepherd is installed as a PWA — Chrome steals Ctrl+T from regular tabs)"
        >+</button>
      </div>

      {#if node.widget === 'terminal' && node.config?.agent}
        <!-- Chips for the active terminal tab. Right-click the tab to
             pick a different agent — the inline dropdown is gone. -->
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
      {#if !node.widget}
        <!-- Empty tab — operator 2026-05-29: 'when user opens a new tab,
             let the center of the pane show the options in card view'. -->
        <!-- #692 — compact filterable LIST (operator 2026-06-04: 'current
             way of selecting the panes is terrible the large icons etc,
             lets make a reasonable list view'). -->
        <div class="pane-picker center" data-testid="pane-picker">
          <input class="pp-filter" type="text" placeholder="filter…" bind:value={pickerFilter} />
          {#each PICKER_GROUPS as g}
            {@const items = g.items.filter(pickerMatches)}
            {#if items.length}
              <div class="pp-group-label">{g.label}</div>
              <ul class="pp-list">
                {#each items as item}
                  <li>
                    <button class="pp-row" on:click={() => { pickerFilter = ''; setTabWidget(node.activeTab || 0, item.id); }}>
                      <span class="pp-row-icon">{(WIDGET_LABELS[item.id] || item.id).split(' ')[0]}</span>
                      <span class="pp-row-name">{(WIDGET_LABELS[item.id] || item.id).replace(/^[^a-zA-Z]+\s*/, '')}</span>
                      <span class="pp-row-desc">{item.desc}</span>
                    </button>
                  </li>
                {/each}
              </ul>
            {/if}
          {/each}
        </div>
      {:else if node.widget === 'terminal' && !node.config?.agent}
        <!-- Terminal cascade — operator 2026-05-29: 'since terminal has
             a cascaded 2-level behaviour selecting the terminal card
             should list all the agents to select'. -->
        <div class="pane-picker center">
          <button class="pp-back" on:click={() => resetTabToWidgetPicker(node.activeTab || 0)} title="back to widget picker">← change widget</button>
          <h4 class="pp-title">Pick an agent</h4>
          <div class="pp-grid agents">
            {#each (sessions || []) as s}
              <button class="pp-card agent" on:click={() => setTabAgent(node.activeTab || 0, s.name)}>
                <span class="pp-role-icon" aria-hidden="true">
                  <svg viewBox="0 0 24 24" width="22" height="22">{@html roleIcon(s)}</svg>
                </span>
                <span class="pp-name">{s.name}</span>
                <span class="pp-dot" class:live={!s.exited} class:dead={s.exited}>{s.exited ? '○' : '●'}</span>
              </button>
            {/each}
            {#if (sessions || []).length === 0}
              <div class="pp-empty">No agents running. Spawn one via <kbd>+ new</kbd> in the top bar.</div>
            {/if}
          </div>
        </div>
      {:else if node.widget === 'terminal'}
        <WidgetTerminal {selectedAgent} {sessions} {node} />
      {:else if node.widget === 'session-list'}
        <WidgetSessionList {sessions} {teams} {memberships} {selectedAgent} {selectAgent} />
      {:else if node.widget === 'session-board'}
        <WidgetSessionBoard {sessions} {selectedAgent} {selectAgent} />
      {:else if node.widget === 'inspector'}
        <WidgetInspector {sessions} {memberships} {events} {selectedAgent} {node} {saveLayout} />
      {:else if node.widget === 'agent-details'}
        <WidgetAgentDetails agent={(sessions || []).find(s => s.name === selectedAgent) || null} />
      {:else if node.widget === 'agent-identity'}
        <WidgetAgentIdentity agent={(sessions || []).find(s => s.name === selectedAgent) || null} />
      {:else if node.widget === 'agent-runtime'}
        <WidgetAgentRuntime agent={(sessions || []).find(s => s.name === selectedAgent) || null} />
      {:else if node.widget === 'shepherd-assessment-card'}
        <WidgetSpider {selectedAgent} {sessions} />
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
      {:else if node.widget === 'team-transcript'}
        <TeamTranscript team={node.config?.team || 'default'} />
      {:else if node.widget === 'role-matrix'}
        <WidgetRoleMatrix />
      {:else if node.widget === 'inbox' || node.widget === 'a2a-inbox'}
        <!-- #666 — removed widgets. Graceful degradation for saved layouts
             that still reference them. Team Transcript subsumes both. -->
        <div class="empty removed">
          <p><strong>Widget no longer available</strong></p>
          <p>The <code>{node.widget}</code> widget has been removed — the Team Transcript subsumes both A2A inbox and user inbox.</p>
          <p>Right-click this tab → change widget → pick <strong>team transcript</strong> (or any other).</p>
        </div>
      {:else}
        <div class="empty">widget: {node.widget}</div>
      {/if}

      {#if menu}
        <!-- Right-click context picker (operator 2026-05-29). Overlay
             over the pane body. For terminal tabs starts at agent level
             with a "← change widget" back button. -->
        <div class="pane-picker overlay" on:click|self={closeMenu}>
          <div class="pane-picker-card" on:click|stopPropagation>
            <header class="pp-head">
              {#if menu.level === 'agent'}
                <button class="pp-back" on:click={() => menu = { ...menu, level: 'widget' }}>← change widget</button>
                <h4 class="pp-title">Pick an agent</h4>
              {:else}
                <h4 class="pp-title">Pick a widget</h4>
              {/if}
              <button class="pp-close" on:click={closeMenu} title="close">×</button>
            </header>
            {#if menu.level === 'widget'}
              <!-- #692 — same compact list as the empty-tab picker. -->
              <input class="pp-filter" type="text" placeholder="filter…" bind:value={pickerFilter} />
              {#each PICKER_GROUPS as g}
                {@const items = g.items.filter(pickerMatches)}
                {#if items.length}
                  <div class="pp-group-label">{g.label}</div>
                  <ul class="pp-list">
                    {#each items as item}
                      <li>
                        <button class="pp-row" on:click={() => { pickerFilter = ''; setTabWidget(menu.tabIdx, item.id); if (item.id !== 'terminal') closeMenu(); else menu = { ...menu, level: 'agent' }; }}>
                          <span class="pp-row-icon">{(WIDGET_LABELS[item.id] || item.id).split(' ')[0]}</span>
                          <span class="pp-row-name">{(WIDGET_LABELS[item.id] || item.id).replace(/^[^a-zA-Z]+\s*/, '')}</span>
                          <span class="pp-row-desc">{item.desc}</span>
                        </button>
                      </li>
                    {/each}
                  </ul>
                {/if}
              {/each}
            {:else}
              <div class="pp-grid agents">
                {#each (sessions || []) as s}
                  <button class="pp-card agent" on:click={() => setTabAgent(menu.tabIdx, s.name)}>
                    <span class="pp-role-icon" aria-hidden="true">
                      <svg viewBox="0 0 24 24" width="22" height="22">{@html roleIcon(s)}</svg>
                    </span>
                    <span class="pp-name">{s.name}</span>
                    <span class="pp-dot" class:live={!s.exited} class:dead={s.exited}>{s.exited ? '○' : '●'}</span>
                  </button>
                {/each}
                {#if (sessions || []).length === 0}
                  <div class="pp-empty">No agents running.</div>
                {/if}
              </div>
            {/if}
          </div>
        </div>
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
  /* In-header tabs (operator 2026-05-29: no extra row; use the existing
     pane-header). Each tab is a small button; the active one is
     underlined. Click the active tab again to expose the widget-pick
     dropdown for changing what that tab shows. */
  .ph-tabs {
    display: inline-flex; align-items: stretch; gap: 0.1rem;
    overflow-x: auto; overflow-y: hidden;
    max-width: 60%; min-width: 0;
  }
  .ph-tab {
    display: inline-flex; align-items: center; gap: 0.3rem;
    padding: 0.05rem 0.4rem;
    background: transparent; border: 0;
    color: var(--fg-muted); font: inherit; font-size: 0.74rem;
    cursor: pointer; max-width: 16rem;
    border-bottom: 2px solid transparent;
    white-space: nowrap;
  }
  .ph-tab:hover { background: var(--bg-elev); color: var(--fg); }
  .ph-tab.active { color: var(--fg); border-bottom-color: var(--accent, #87ceeb); }
  .ph-tab-label { overflow: hidden; text-overflow: ellipsis; }
  .ph-tab-ident { margin-right: 0.25rem; } /* #694 identity chip */
  .ph-tab-close { color: var(--fg-faint); padding: 0 0.1rem; font-size: 0.88rem; line-height: 1; border-radius: 3px; opacity: 0.55; }
  .ph-tab:hover .ph-tab-close { opacity: 1; }
  .ph-tab-close:hover { background: rgba(231,76,60,0.18); color: #e74c3c; }
  .ph-tab-add {
    background: transparent; border: 0; color: var(--fg-muted);
    font: inherit; font-size: 0.95rem; padding: 0 0.45rem; cursor: pointer;
  }
  .ph-tab-add:hover { color: var(--accent, #87ceeb); }
  .ph-tab.empty { color: var(--accent-2, #87ceeb); font-style: italic; }

  /* Pane picker — card grid shown either in the centre of an empty tab
     (operator: 'when user opens a new tab, let the centre of the pane
     show the options in card view') or as an overlay over the body
     when the operator right-clicks a tab. Same component, two
     contexts. */
  /* #248 — `container-type: size` on the .pane-picker.center makes the
     wrapper a CSS-container-query context so the .pp-grid below can
     react to both pane WIDTH and HEIGHT (default `auto-fill` only
     reacts to width, which collapses to a 1-col pillar in tall+narrow
     panes — operator quote 2026-05-30 "pillar, 8-10 cards vertically
     listed"). `container-name: pickerpane` lets the @container queries
     below target this specific wrapper, not any ancestor. */
  .pane-picker.center {
    container-type: size;
    container-name: pickerpane;
    height: 100%; width: 100%;
    display: flex; flex-direction: column;
    align-items: stretch; justify-content: center;
    padding: 1rem; gap: 0.85rem;
    overflow-y: auto;
    box-sizing: border-box;
  }
  .pane-picker.center .pp-title { text-align: center; }
  .pane-picker.center .pp-back { align-self: flex-start; }
  .pane-picker.overlay {
    position: absolute; inset: 0;
    background: rgba(0,0,0,0.4);
    display: flex; align-items: flex-start; justify-content: center;
    padding-top: 2rem;
    z-index: 50;
  }
  /* Overlay variant uses the same container-query approach so the
     right-click pick-widget popover also distributes homogeneously. */
  .pane-picker-card {
    container-type: size;
    container-name: pickerpane;
    background: var(--bg-elev); border: 1px solid var(--border);
    border-radius: 8px; padding: 0.9rem 1rem;
    max-width: min(90%, 32rem);
    box-shadow: 0 8px 28px rgba(0,0,0,0.4);
  }
  .pp-head { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.65rem; }
  .pp-title { margin: 0; flex: 1; font-size: 0.92rem; color: var(--fg); font-weight: 600; }
  .pp-back {
    background: transparent; border: 0; color: var(--accent-2, #87ceeb);
    font: inherit; font-size: 0.78rem; cursor: pointer; padding: 0.15rem 0.4rem;
    border-radius: 4px;
  }
  .pp-back:hover { background: rgba(135,206,235,0.12); }
  .pp-close {
    background: transparent; border: 0; color: var(--fg-muted);
    font: inherit; font-size: 1.1rem; line-height: 1; cursor: pointer;
    padding: 0 0.3rem;
  }
  .pp-close:hover { color: #e74c3c; }
  /* #248 — `align-content: center` makes the grid SIT in the middle
     of the available vertical space rather than glued to the top
     (operator complaint: cards stuck at the top in tall panes). The
     base auto-fill rule below is the WIDE-pane fallback; the
     @container queries override for tall-narrow / very-narrow cases
     so cards distribute homogeneously across both axes. */
  .pp-grid {
    display: grid; width: 100%;
    grid-template-columns: repeat(auto-fill, minmax(min(8rem, 100%), 1fr));
    gap: 0.5rem;
    align-content: center;
  }
  .pp-grid.agents {
    grid-template-columns: repeat(auto-fill, minmax(min(10rem, 100%), 1fr));
  }
  /* Tall-narrow panes (aspect-ratio < 0.5) — auto-fill would
     resolve to 1 column and stack into a pillar. Force 2 cols so
     cards spread horizontally even in a 240×600 right-rail pane. */
  @container pickerpane (aspect-ratio < 0.5) {
    .pp-grid { grid-template-columns: repeat(2, 1fr); }
    .pp-grid.agents { grid-template-columns: repeat(2, 1fr); }
  }
  /* Short-wide panes (aspect-ratio > 2) — make the cards smaller
     so the horizontal space is fully utilised. min 6rem instead of
     8rem packs more columns. */
  @container pickerpane (aspect-ratio > 2) {
    .pp-grid { grid-template-columns: repeat(auto-fill, minmax(6rem, 1fr)); }
    .pp-grid.agents { grid-template-columns: repeat(auto-fill, minmax(8rem, 1fr)); }
  }
  /* Genuinely tiny panes (< 200px wide) — single column is the
     only readable option. Falls THROUGH the aspect-ratio rules
     above because both layouts collapse meaningfully at that size. */
  @container pickerpane (width < 200px) {
    .pp-grid, .pp-grid.agents { grid-template-columns: 1fr; }
  }
  .pp-card {
    display: flex; flex-direction: column; align-items: center; gap: 0.25rem;
    padding: 0.7rem 0.5rem; border-radius: 7px;
    background: var(--bg); border: 1px solid var(--border);
    color: var(--fg); cursor: pointer; font: inherit; text-align: center;
    transition: border-color 80ms, background 80ms;
  }
  .pp-card:hover { border-color: var(--accent-2, #87ceeb); background: rgba(135,206,235,0.06); }
  .pp-card.agent { flex-direction: row; align-items: center; justify-content: flex-start; gap: 0.55rem; text-align: left; padding: 0.5rem 0.65rem; }
  .pp-icon { font-size: 1.25rem; line-height: 1; color: var(--accent-2, #87ceeb); }
  /* #692 — compact list picker */
  .pp-filter { width: 100%; max-width: 360px; margin: 0 auto 0.5rem; display: block; background: var(--bg, #111); border: 1px solid var(--border, #2a2a2a); border-radius: 5px; color: var(--fg, #ddd); padding: 0.3rem 0.55rem; font-size: 0.85rem; }
  .pp-group-label { font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--fg-muted, #777); margin: 0.5rem auto 0.15rem; max-width: 360px; text-align: left; }
  .pp-list { list-style: none; margin: 0 auto; padding: 0; max-width: 360px; text-align: left; }
  .pp-row { display: flex; align-items: baseline; gap: 0.5rem; width: 100%; background: none; border: none; border-radius: 5px; padding: 0.3rem 0.45rem; cursor: pointer; color: var(--fg, #ddd); font-size: 0.85rem; }
  .pp-row:hover, .pp-row:focus { background: var(--bg-elevated, #1d1d1d); outline: none; }
  .pp-row-icon { width: 1.3rem; text-align: center; color: var(--accent-2, #87ceeb); flex-shrink: 0; }
  .pp-row-name { font-weight: 600; white-space: nowrap; }
  .pp-row-desc { color: var(--fg-muted, #888); font-size: 0.76rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .pp-role-icon { color: var(--accent-2, #87ceeb); line-height: 0; flex-shrink: 0; }
  .pp-name { font-size: 0.82rem; font-weight: 600; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .pp-meta { font-size: 0.7rem; color: var(--fg-muted); }
  .pp-dot { font-size: 0.7rem; line-height: 1; margin-left: auto; flex-shrink: 0; }
  .pp-dot.live { color: #2ed573; }
  .pp-dot.dead { color: var(--fg-faint); }
  .pp-empty { grid-column: 1 / -1; color: var(--fg-muted); font-size: 0.85rem; text-align: center; padding: 1rem 0; }
  .pp-empty kbd { background: var(--bg); border: 1px solid var(--border); border-radius: 3px; padding: 0 0.25rem; }

  /* Make the leaf pane a positioning context so overlay sits above body. */
  .pane { position: relative; }
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
  .empty.removed { color: var(--fg-muted); padding: 1.25rem; font-size: 0.82rem; }
  .empty.removed p { margin: 0.35rem 0; }
  .empty.removed code { background: var(--bg-elev); padding: 0.05rem 0.3rem; border-radius: 3px; }
</style>

<!--
  Pane.svelte — recursive workspace renderer. Each node is either:
  - kind: 'pane'   → one widget (leaf)
  - kind: 'h'      → HSplit (left | right) with draggable divider
  - kind: 'v'      → VSplit (top / bottom) with draggable divider
-->
<script>
  import { onMount } from 'svelte';
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
  import WidgetCanon from './widgets/WidgetCanon.svelte';
  import WidgetMCPLog from './widgets/WidgetMCPLog.svelte';

  let { node, sessions, teams, memberships, inbox, events, selectedAgent, selectAgent, changeWidget, splitPane, removePane, refresh } = $props();

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

  const WIDGET_LABELS = {
    'terminal': '▦ terminal',
    'session-list': '☰ sessions',
    'session-board': '▤ board',
    'agent-details': 'ⓘ details',
    'shepherd-assessment-card': '✻ scorecard',
    'inbox': '✉ inbox',
    'events': '⏱ events',
    'mcp-log': '🔧 MCP log',
    'canon-viewer': '📜 canon',
    'agent-prompt': '✏ prompt',
    'agent-skills': '🎮 skills',
    // Legacy single-purpose cards kept for back-compat (saved layouts).
    'identity-card': 'ⓘ identity (legacy)',
    'location-card': '📍 location (legacy)',
    'process-card': '⚙ process (legacy)',
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
</script>

{#if node.kind === 'h'}
  <div class="hsplit" bind:this={containerEl}>
    <div class="hcell" style="width: {node.ratio * 100}%;">
      <Self node={node.a} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {changeWidget} {splitPane} {removePane} {refresh} />
    </div>
    <div class="hdivider" on:mousedown={startDrag}></div>
    <div class="hcell" style="width: {(1 - node.ratio) * 100}%;">
      <Self node={node.b} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {changeWidget} {splitPane} {removePane} {refresh} />
    </div>
  </div>
{:else if node.kind === 'v'}
  <div class="vsplit" bind:this={containerEl}>
    <div class="vcell" style="height: {node.ratio * 100}%;">
      <Self node={node.a} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {changeWidget} {splitPane} {removePane} {refresh} />
    </div>
    <div class="vdivider" on:mousedown={startDrag}></div>
    <div class="vcell" style="height: {(1 - node.ratio) * 100}%;">
      <Self node={node.b} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {changeWidget} {splitPane} {removePane} {refresh} />
    </div>
  </div>
{:else}
  <!-- leaf pane: widget container -->
  <div class="pane">
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
      {:else if node.widget === 'identity-card'}
        <WidgetCard kind="identity" {selectedAgent} {sessions} {memberships} />
      {:else if node.widget === 'location-card'}
        <WidgetCard kind="location" {selectedAgent} {sessions} />
      {:else if node.widget === 'process-card'}
        <WidgetCard kind="process" {selectedAgent} {sessions} />
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

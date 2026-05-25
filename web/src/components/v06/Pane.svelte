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
    'identity-card': 'ⓘ identity',
    'location-card': '📍 location',
    'process-card': '⚙ process',
    'shepherd-assessment-card': '✻ scorecard',
    'inbox': '✉ inbox',
    'events': '⏱ events',
    'mcp-log': '🔧 MCP log',
    'canon-viewer': '📜 canon',
    'agent-prompt': '✏ prompt',
    'agent-skills': '🎮 skills',
  };

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
      <div class="spacer"></div>
      <button title="split horizontally (add right)" on:click={() => splitPane(node.id, 'h')}>⬌</button>
      <button title="split vertically (add below)" on:click={() => splitPane(node.id, 'v')}>⬍</button>
      <button title="close" on:click={() => removePane(node.id)}>×</button>
    </header>
    <div class="pane-body">
      {#if node.widget === 'terminal'}
        <WidgetTerminal {selectedAgent} {sessions} />
      {:else if node.widget === 'session-list'}
        <WidgetSessionList {sessions} {teams} {memberships} {selectedAgent} {selectAgent} />
      {:else if node.widget === 'session-board'}
        <WidgetSessionBoard {sessions} {selectedAgent} {selectAgent} />
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
  .pane-body { flex: 1; overflow: hidden; min-height: 0; }
  .empty { color: var(--fg-faint); padding: 1rem; text-align: center; font-size: 0.85rem; }
</style>

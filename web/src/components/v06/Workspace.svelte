<!--
  v0.6 Workspace canvas — recursive tree of splits + widget catalog.
  Refs #80, #85.

  Layout = Pane | HSplit{a,b,ratio} | VSplit{a,b,ratio}
  Pane    = { id, widget, config }

  Widgets: terminal | session-list | session-board | identity-card |
  location-card | process-card | shepherd-assessment-card | inbox |
  events | scorecard-aggregate | canon-viewer

  Loaded from / saved to /api/v1/workspaces/<name> (shared across operators).
-->
<script>
  import { onMount } from 'svelte';
  import '@xterm/xterm/css/xterm.css';
  import Pane from './Pane.svelte';
  import SpawnModal from './SpawnModal.svelte';
  import TemplatePicker from './TemplatePicker.svelte';

  // --- props / state ---
  let sessions = $state([]);
  let teams = $state([]);
  let memberships = $state([]);
  let inbox = $state([]);
  let events = $state([]);
  let selectedAgent = $state(null);
  let theme = $state('dark');
  let showSpawn = $state(false);
  let showTemplates = $state(false);
  let confirmDialog = $state(null);

  // Workspace layout — default = Focus template
  let layout = $state(defaultFocusLayout());

  function defaultFocusLayout() {
    return {
      kind: 'h',
      ratio: 0.18,
      a: { kind: 'pane', id: 'p1', widget: 'session-list', config: {} },
      b: {
        kind: 'h', ratio: 0.78,
        a: {
          kind: 'v', ratio: 0.78,
          a: { kind: 'pane', id: 'p2', widget: 'terminal', config: {} },
          b: { kind: 'pane', id: 'p3', widget: 'events', config: {} },
        },
        b: {
          kind: 'v', ratio: 0.5,
          a: { kind: 'pane', id: 'p4', widget: 'identity-card', config: {} },
          b: {
            kind: 'v', ratio: 0.5,
            a: { kind: 'pane', id: 'p5', widget: 'shepherd-assessment-card', config: {} },
            b: { kind: 'pane', id: 'p6', widget: 'inbox', config: {} },
          }
        },
      },
    };
  }

  // --- API ---
  const API = '/api/v1';
  async function refresh() {
    try {
      const [s, t, m, ib, ev] = await Promise.all([
        fetch(`${API}/sessions`).then(r => r.json()),
        fetch(`${API}/teams`).then(r => r.json()),
        fetch(`${API}/memberships`).then(r => r.json()),
        fetch(`${API}/inbox`).then(r => r.json()),
        fetch(`${API}/events?limit=80`).then(r => r.json()),
      ]);
      sessions = s.sessions || [];
      teams = t.teams || [];
      memberships = m.memberships || [];
      inbox = ib.inbox || [];
      events = ev.events || [];
      if (selectedAgent && !sessions.find(s => s.name === selectedAgent)) {
        selectedAgent = null;
      }
    } catch {}
  }

  let evStream = null;
  function startEventStream() {
    if (evStream) return;
    evStream = new EventSource(`${API}/events/stream`);
    evStream.onmessage = (ev) => {
      try {
        const e = JSON.parse(ev.data);
        events = [...events, e].slice(-200);
      } catch {}
    };
    evStream.onerror = () => { evStream?.close(); evStream = null; setTimeout(startEventStream, 3000); };
  }

  // --- workspace save/load ---
  async function saveLayout(name = 'current') {
    await fetch(`${API}/workspaces/${name}`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(layout),
    });
  }
  async function loadLayout(name) {
    try {
      const r = await fetch(`${API}/workspaces/${name}`);
      if (r.ok) layout = await r.json();
    } catch {}
  }

  // --- pane operations ---
  function selectAgent(name) {
    selectedAgent = name;
  }

  function changeWidget(paneId, newWidget) {
    layout = setWidget(layout, paneId, newWidget);
    saveLayout();
  }
  function setWidget(node, id, widget) {
    if (node.kind === 'pane') {
      if (node.id === id) return { ...node, widget };
      return node;
    }
    return { ...node, a: setWidget(node.a, id, widget), b: setWidget(node.b, id, widget) };
  }
  function splitPane(paneId, direction) {
    layout = doSplit(layout, paneId, direction);
    saveLayout();
  }
  function doSplit(node, id, direction) {
    if (node.kind === 'pane' && node.id === id) {
      const newId = 'p' + Date.now();
      const newPane = { kind: 'pane', id: newId, widget: 'identity-card', config: {} };
      return { kind: direction, ratio: 0.5, a: node, b: newPane };
    }
    if (node.kind !== 'pane') {
      return { ...node, a: doSplit(node.a, id, direction), b: doSplit(node.b, id, direction) };
    }
    return node;
  }
  function removePane(paneId) {
    layout = doRemove(layout, paneId) ?? layout;
    saveLayout();
  }
  function doRemove(node, id) {
    if (node.kind === 'pane') return node.id === id ? null : node;
    const ra = doRemove(node.a, id);
    const rb = doRemove(node.b, id);
    if (ra === null) return rb;
    if (rb === null) return ra;
    return { ...node, a: ra, b: rb };
  }

  // --- template apply ---
  async function applyTemplate(name, team, cwd) {
    const r = await fetch(`${API}/templates/${name}/apply`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ team, cwd }),
    });
    showTemplates = false;
    await refresh();
    return r.json();
  }

  // --- workspace templates ---
  function applyWorkspaceTemplate(name) {
    switch (name) {
      case 'focus': layout = defaultFocusLayout(); break;
      case 'council': layout = councilLayout(); break;
      case 'board': layout = boardLayout(); break;
      case 'multi-team': layout = multiTeamLayout(); break;
    }
    saveLayout();
  }
  function councilLayout() {
    return {
      kind: 'h', ratio: 0.18,
      a: { kind: 'pane', id: 'p1', widget: 'session-list', config: {} },
      b: {
        kind: 'v', ratio: 0.78,
        a: { kind: 'pane', id: 'p2', widget: 'session-board', config: {} },
        b: { kind: 'pane', id: 'p3', widget: 'events', config: {} },
      },
    };
  }
  function boardLayout() {
    return {
      kind: 'v', ratio: 0.85,
      a: { kind: 'pane', id: 'p1', widget: 'session-board', config: {} },
      b: { kind: 'pane', id: 'p2', widget: 'events', config: {} },
    };
  }
  function multiTeamLayout() {
    return {
      kind: 'h', ratio: 0.22,
      a: { kind: 'pane', id: 'p1', widget: 'session-list', config: { groupBy: 'team' } },
      b: { kind: 'pane', id: 'p2', widget: 'terminal', config: {} },
    };
  }

  // --- mount ---
  onMount(() => {
    try { theme = localStorage.getItem('chepherd-theme') || 'dark'; document.documentElement.dataset.theme = theme; } catch {}
    refresh();
    const intv = setInterval(refresh, 2500);
    startEventStream();
    loadLayout('current');
    return () => { clearInterval(intv); evStream?.close(); };
  });

  function toggleTheme() {
    theme = theme === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = theme;
    try { localStorage.setItem('chepherd-theme', theme); } catch {}
  }
</script>

<div class="workspace">
  <header class="topbar">
    <a href="/" class="brand">✻ chepherd <span class="ver">v0.6</span></a>
    <div class="stats">
      {sessions.length} agents · {teams.length} teams · {memberships.length} memberships
    </div>
    <div class="view-switcher">
      <button on:click={() => applyWorkspaceTemplate('focus')}>Focus</button>
      <button on:click={() => applyWorkspaceTemplate('council')}>Council</button>
      <button on:click={() => applyWorkspaceTemplate('board')}>Board</button>
      <button on:click={() => applyWorkspaceTemplate('multi-team')}>Multi</button>
    </div>
    <button class="icon-btn" on:click={toggleTheme} title="Toggle theme">{theme === 'dark' ? '☀' : '☾'}</button>
    <button class="secondary" on:click={() => (showTemplates = true)}>📦 templates</button>
    <button class="primary" on:click={() => (showSpawn = true)}>+ spawn</button>
  </header>

  <div class="canvas">
    <Pane node={layout} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {changeWidget} {splitPane} {removePane} />
  </div>
</div>

{#if showSpawn}
  <SpawnModal onClose={() => (showSpawn = false)} onSpawned={refresh} />
{/if}
{#if showTemplates}
  <TemplatePicker onClose={() => (showTemplates = false)} onApplied={refresh} />
{/if}

<style>
  :global(html[data-theme="dark"]) {
    --bg: #0a0a0a; --bg-elev: #111; --bg-input: #0a0a0a;
    --border: #1e1e1e; --border-strong: #2a2a2a;
    --fg: #f5f5f5; --fg-muted: #aaa; --fg-faint: #666;
    --accent: #ffa500; --accent-2: #87ceeb; --danger: #ff6b6b;
    --select-bg: #1a2530; --select-border: #5f9ea0;
  }
  :global(html[data-theme="light"]) {
    --bg: #fafafa; --bg-elev: #ffffff; --bg-input: #ffffff;
    --border: #e5e7eb; --border-strong: #cbd5e1;
    --fg: #1a1a1a; --fg-muted: #555; --fg-faint: #888;
    --accent: #c97900; --accent-2: #2563eb; --danger: #c92020;
    --select-bg: #e0f2fe; --select-border: #2563eb;
  }
  :global(html), :global(body) { background: var(--bg); color: var(--fg); margin: 0; padding: 0; height: 100vh; overflow: hidden; font-family: ui-sans-serif, system-ui, sans-serif; font-size: 14px; }
  .workspace { display: flex; flex-direction: column; height: 100vh; background: var(--bg); color: var(--fg); }
  .topbar { display: flex; align-items: center; gap: 0.9rem; padding: 0.55rem 1rem; background: var(--bg-elev); border-bottom: 1px solid var(--border); }
  .topbar .brand { color: var(--accent); font-weight: 600; text-decoration: none; font-size: 1.1rem; }
  .topbar .brand .ver { font-size: 0.72rem; color: var(--fg-muted); margin-left: 0.4rem; }
  .topbar .stats { flex: 1; color: var(--fg-muted); font-size: 0.85rem; }
  .view-switcher { display: flex; gap: 0.2rem; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; padding: 0.18rem; }
  .view-switcher button { padding: 0.32rem 0.7rem; background: transparent; color: var(--fg-muted); border: none; border-radius: 4px; cursor: pointer; font-size: 0.82rem; }
  .view-switcher button:hover { color: var(--accent); }
  button.primary { background: var(--accent); color: #000; border: none; border-radius: 6px; padding: 0.42rem 0.95rem; font-weight: 600; cursor: pointer; font-size: 0.88rem; }
  button.secondary { background: var(--bg-elev); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.42rem 0.85rem; cursor: pointer; font-size: 0.88rem; }
  button.icon-btn { background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 6px; width: 32px; height: 32px; cursor: pointer; display: flex; align-items: center; justify-content: center; }
  .canvas { flex: 1; min-height: 0; overflow: hidden; }
</style>

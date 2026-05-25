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
  // v0.6 uses its own /api-v06 namespace so it always hits the v0.6 runtime
  // (:8081) regardless of which Astro dev server / port served the page.
  const API = '/api-v06/v1';
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
      // Auto-pick first non-shepherd worker when nothing is selected — so
      // AgentDetails / terminal / prompt / skills widgets render real
      // content without requiring an extra click after opening the dashboard.
      if (!selectedAgent && sessions.length) {
        const w = sessions.find(s => !s.exited && s.role !== 'shepherd')
              || sessions.find(s => !s.exited);
        if (w) selectedAgent = w.name;
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

  // --- workspace font-size (granular, applies to ALL widgets uniformly) ---
  // Uses a CSS variable --ws-font so every widget inherits.
  let fontSize = $state(14);
  function applyFontSize(n) {
    fontSize = Math.max(9, Math.min(22, n));
    document.documentElement.style.setProperty('--ws-font', fontSize + 'px');
    try { localStorage.setItem('chepherd-font', String(fontSize)); } catch {}
  }
  // Default font on first load: 14 (overridable via localStorage from prior session)

  // --- save-as named layout ---
  let showSaveAs = $state(false);
  let saveAsName = $state('');
  let savedLayouts = $state([]);
  async function listSavedLayouts() {
    try { const r = await fetch(`${API}/workspaces`); const d = await r.json(); savedLayouts = d.workspaces || []; } catch {}
  }
  async function saveAs() {
    if (!saveAsName.trim()) return;
    await saveLayout(saveAsName.trim());
    await listSavedLayouts();
    showSaveAs = false;
    saveAsName = '';
  }
  async function loadSaved(n) { await loadLayout(n); }

  // --- agent action menu (stop/pause/restart) ---
  let showAgentMenu = $state(false);
  async function agentAction(act) {
    if (!selectedAgent) return;
    let url, method, body;
    switch (act) {
      case 'pause':   url = `${API}/sessions/${selectedAgent}/pause`; method = 'POST'; body = JSON.stringify({ paused: true });  break;
      case 'unpause': url = `${API}/sessions/${selectedAgent}/pause`; method = 'POST'; body = JSON.stringify({ paused: false }); break;
      case 'restart': url = `${API}/sessions/${selectedAgent}/restart`; method = 'POST'; body = null; break;
      case 'stop':    url = `${API}/sessions/${selectedAgent}`; method = 'DELETE'; body = null; break;
    }
    try {
      const r = await fetch(url, { method, headers: body ? { 'Content-Type': 'application/json' } : {}, body });
      if (!r.ok) { const e = await r.json().catch(()=>({})); alert(e.error || `HTTP ${r.status}`); }
    } catch (e) { alert(String(e)); }
    await refresh();
    showAgentMenu = false;
  }

  // --- mount ---
  onMount(() => {
    try { theme = localStorage.getItem('chepherd-theme') || 'dark'; document.documentElement.dataset.theme = theme; } catch {}
    try { const f = +(localStorage.getItem('chepherd-font') || 14); applyFontSize(f); } catch { applyFontSize(14); }
    refresh();
    listSavedLayouts();
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
    <div class="font-knob" title="font size (applies to all widgets)">
      <button class="icon-btn small" on:click={() => applyFontSize(fontSize - 1)} aria-label="smaller">A-</button>
      <span class="font-num">{fontSize}px</span>
      <button class="icon-btn small" on:click={() => applyFontSize(fontSize + 1)} aria-label="larger">A+</button>
    </div>
    {#if selectedAgent}
      <div class="agent-menu">
        <button class="secondary" on:click={() => (showAgentMenu = !showAgentMenu)} title="agent actions">⚙ {selectedAgent}</button>
        {#if showAgentMenu}
          <div class="dropdown">
            <button on:click={() => agentAction('pause')}>⏸ Pause</button>
            <button on:click={() => agentAction('unpause')}>▶ Resume</button>
            <button on:click={() => agentAction('restart')}>↻ Restart</button>
            <button class="danger" on:click={() => agentAction('stop')}>■ Stop</button>
          </div>
        {/if}
      </div>
    {/if}
    <button class="icon-btn" on:click={toggleTheme} title="Toggle theme">{theme === 'dark' ? '☀' : '☾'}</button>
    <button class="secondary" on:click={() => (showSaveAs = true)} title="Save current layout as a named view">💾 save view</button>
    {#if savedLayouts.length > 1}
      <select class="layout-pick" on:change={(e) => loadSaved(e.target.value)} title="Load saved layout">
        <option value="">— views —</option>
        {#each savedLayouts as n}<option value={n}>{n}</option>{/each}
      </select>
    {/if}
    <button class="secondary" on:click={() => (showTemplates = true)}>📦 templates</button>
    <button class="primary" on:click={() => (showSpawn = true)}>+ spawn</button>
  </header>

  <div class="canvas">
    <Pane node={layout} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {changeWidget} {splitPane} {removePane} {refresh} />
  </div>
</div>

{#if showSpawn}
  <SpawnModal onClose={() => (showSpawn = false)} onSpawned={refresh} />
{/if}
{#if showTemplates}
  <TemplatePicker onClose={() => (showTemplates = false)} onApplied={refresh} />
{/if}
{#if showSaveAs}
  <div class="backdrop" on:click={() => (showSaveAs = false)}>
    <div class="modal-saveas" on:click|stopPropagation>
      <h3>Save layout as…</h3>
      <input bind:value={saveAsName} placeholder="my-view" autofocus />
      <p class="hint">Saved views persist on the runtime + can be switched via the dropdown in the top bar.</p>
      <footer>
        <button class="secondary" on:click={() => (showSaveAs = false)}>Cancel</button>
        <button class="primary" on:click={saveAs} disabled={!saveAsName.trim()}>Save</button>
      </footer>
    </div>
  </div>
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
  :global(html) { --ws-font: 13px; }
  :global(html), :global(body) { background: var(--bg); color: var(--fg); margin: 0; padding: 0; height: 100vh; overflow: hidden; font-family: ui-sans-serif, system-ui, sans-serif; font-size: 14px; }
  /* Widget root → font scales with --ws-font. Children inherit by default
     unless they explicitly override (rem, px). Using em on widget body
     elements keeps relative scales consistent. */
  :global(.pane-body) { font-size: var(--ws-font); }
  /* Force-scale common typographic descendants (some widgets set explicit
     px/rem sizes that wouldn't otherwise pick up --ws-font). */
  :global(.pane-body p), :global(.pane-body li), :global(.pane-body td), :global(.pane-body th),
  :global(.pane-body small), :global(.pane-body span), :global(.pane-body button),
  :global(.pane-body select), :global(.pane-body input), :global(.pane-body textarea),
  :global(.pane-body pre), :global(.pane-body code), :global(.pane-body dt), :global(.pane-body dd),
  :global(.pane-body label), :global(.pane-body summary), :global(.pane-body div) {
    font-size: var(--ws-font) !important;
  }
  :global(.pane-body h1), :global(.pane-body h2), :global(.pane-body h3) { font-size: calc(var(--ws-font) * 1.2) !important; }
  :global(.pane-body h4) { font-size: calc(var(--ws-font) * 1.05) !important; }
  :global(.pane-body h5), :global(.pane-body h6) { font-size: var(--ws-font) !important; }
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
  button.icon-btn.small { width: 28px; height: 24px; font-size: 0.7rem; padding: 0; }
  .font-knob { display: flex; align-items: center; gap: 0.15rem; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; padding: 0.14rem; }
  .font-num { font-size: 0.7rem; color: var(--fg-muted); padding: 0 0.3rem; min-width: 28px; text-align: center; font-family: ui-monospace, monospace; }
  .agent-menu { position: relative; }
  .agent-menu .dropdown { position: absolute; top: 100%; right: 0; margin-top: 4px; background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.25rem; display: flex; flex-direction: column; gap: 0.1rem; z-index: 100; min-width: 140px; box-shadow: 0 4px 12px rgba(0,0,0,0.4); }
  .agent-menu .dropdown button { padding: 0.4rem 0.7rem; background: transparent; color: var(--fg); border: none; border-radius: 4px; cursor: pointer; text-align: left; font-size: 0.82rem; }
  .agent-menu .dropdown button:hover { background: var(--bg); }
  .agent-menu .dropdown button.danger { color: var(--danger); }
  .layout-pick { background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.4rem 0.55rem; font-size: 0.82rem; cursor: pointer; max-width: 140px; }
  .canvas { flex: 1; min-height: 0; overflow: hidden; }
  .backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.65); display: flex; align-items: center; justify-content: center; z-index: 1000; backdrop-filter: blur(2px); }
  .modal-saveas { width: min(420px, 92vw); background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 10px; padding: 1.2rem 1.3rem; }
  .modal-saveas h3 { margin: 0 0 0.7rem 0; color: var(--accent); }
  .modal-saveas input { width: 100%; padding: 0.5rem 0.7rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; font-family: ui-monospace, monospace; }
  .modal-saveas .hint { color: var(--fg-muted); font-size: 0.78rem; margin: 0.5rem 0 1rem 0; }
  .modal-saveas footer { display: flex; justify-content: flex-end; gap: 0.6rem; }
</style>

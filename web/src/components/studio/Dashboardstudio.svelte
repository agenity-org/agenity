<!--
  Dashboardstudio — "studio" dashboard for chepherd (a self-contained,
  VS-Code / IDE-style alternative to the v08 shell).

  Layout regions (all real, all functional):
    ┌────┬───────────────┬───────────────────────────────┐
    │ A  │  side panel   │   editor area (split tree)     │
    │ c  │  (Explorer /  │   tabbed panes w/ draggable    │
    │ t  │  Conversation │   splitters — terminals,       │
    │ B  │  / Settings)  │   conversation, inspector,     │
    │ a  │  collapsible  │   event log                    │
    │ r  │  + resizable  ├───────────────────────────────┤
    │    │               │   bottom dock (event log) ▲▼   │
    └────┴───────────────┴───────────────────────────────┘
                          status bar (counts · capture · theme)

  FOUNDER HARD REQUIREMENTS:
    #1 pane switching — agent <select> in each terminal pane header + Explorer
       click re-binds the focused pane; multiple terminals coexist as leaves.
    #2 pane resizing  — draggable h/v splitters (StudioPane) + draggable side
       panel width + draggable bottom dock height.
    #3 layout flexibility — split-right/down, tabs, close; arbitrary tree;
       persisted to /api/v1/workspaces/studio.
    #4 all views      — terminals, sessions/roster (Explorer), conversation
       (TeamTranscript), settings/accounts (StudioSettings), spawn (wizard).
    #5 real data      — reuses /api/v1 endpoints + xterm WS attach.
    #6 identity colors — agentIdentity everywhere.
    #7 dark + light   — full CSS-var theme coverage + visible toggle + persist
       + prefers-color-scheme on first load.

  Reuses the codebase fetch/auth wrapper (installed at AuthGate/Workspace
  module top; we self-install here too so this route works standalone).
-->
<script>
  import { onMount } from 'svelte';
  import '@xterm/xterm/css/xterm.css';
  import StudioPane from './StudioPane.svelte';
  import StudioExplorer from './StudioExplorer.svelte';
  import StudioInspector from './StudioInspector.svelte';
  import StudioEvents from './StudioEvents.svelte';
  import StudioSettings from './StudioSettings.svelte';
  import TeamTranscript from '../TeamTranscript.svelte';
  import SpawnWizardV9 from '../v09/SpawnWizardV9.svelte';
  import { registerRoster } from '../../lib/agentIdentity.js';

  let { version = 'studio' } = $props();

  // --- fetch/auth wrapper (idempotent — shared flag with the rest of the app) ---
  if (typeof window !== 'undefined' && !window.__chepherdFetchPatched) {
    window.__chepherdFetchPatched = true;
    const _orig = window.fetch.bind(window);
    window.fetch = (input, init) => {
      const url = typeof input === 'string' ? input : (input?.url || '');
      if (url.startsWith('/api/') || url.startsWith('/api-v')) {
        let tok = '';
        try { tok = localStorage.getItem('chepherd-token') || ''; } catch {}
        init = init || {};
        init.headers = new Headers(init.headers || (typeof input !== 'string' ? input.headers : undefined));
        if (tok && !init.headers.has('Authorization')) init.headers.set('Authorization', 'Bearer ' + tok);
        return _orig(input, init).then(r => {
          if (r.status === 401) { try { window.dispatchEvent(new CustomEvent('chepherd-401')); } catch {} }
          return r;
        });
      }
      return _orig(input, init);
    };
  }

  const API = '/api/v1';

  // --- data ---
  let sessions = $state([]);
  let teams = $state([]);
  let memberships = $state([]);
  let inbox = $state([]);
  let events = $state([]);
  let selectedAgent = $state(null);
  let mru = $state([]);

  // --- auth gate ---
  let authStatus = $state('checking'); // checking | login | ok
  let tokenInput = $state('');
  let loginError = $state('');
  let probing = $state(false);

  // --- chrome state ---
  let theme = $state('dark');
  let fontSize = $state(14);
  let activeView = $state('explorer'); // explorer | conversation | settings
  let sidePanelOpen = $state(true);
  let sideWidth = $state(280);
  let dockOpen = $state(false);
  let dockHeight = $state(220);
  let showWizard = $state(false);
  let focusedPaneID = $state('');

  // --- layout tree (editor area) ---
  let layout = $state(defaultLayout());
  function defaultLayout() {
    return { kind: 'pane', id: 'p-root', tabs: [{ widget: 'terminal', config: {} }], activeTab: 0 };
  }

  // ===== layout-tree ops =====
  function walk(node, fn) {
    if (!node) return;
    if (node.kind === 'pane') { fn(node); return; }
    walk(node.a, fn); walk(node.b, fn);
  }
  function findPane(id) {
    let hit = null;
    walk(layout, p => { if (p.id === id) hit = p; });
    return hit;
  }
  function leafIDs(node, out = []) {
    if (!node) return out;
    if (node.kind === 'pane') { out.push(node.id); return out; }
    leafIDs(node.a, out); leafIDs(node.b, out);
    return out;
  }
  function newID() { return 'p' + Date.now() + Math.floor(Math.random() * 1000); }

  function splitPane(paneId, direction) {
    layout = doSplit(layout, paneId, direction);
    bump(); saveLayout();
  }
  function doSplit(node, id, direction) {
    if (node.kind === 'pane' && node.id === id) {
      const fresh = { kind: 'pane', id: newID(), tabs: [{ widget: 'terminal', config: {} }], activeTab: 0 };
      return { kind: direction, ratio: 0.5, a: node, b: fresh };
    }
    if (node.kind !== 'pane') return { ...node, a: doSplit(node.a, id, direction), b: doSplit(node.b, id, direction) };
    return node;
  }
  function removePane(paneId) {
    const next = doRemove(layout, paneId);
    layout = next || defaultLayout();
    if (!findPane(focusedPaneID)) focusedPaneID = leafIDs(layout)[0] || '';
    bump(); saveLayout();
  }
  function doRemove(node, id) {
    if (node.kind === 'pane') return node.id === id ? null : node;
    const ra = doRemove(node.a, id);
    const rb = doRemove(node.b, id);
    if (ra === null) return rb;
    if (rb === null) return ra;
    return { ...node, a: ra, b: rb };
  }

  // ----- tabs -----
  function ensureTabs(p) {
    if (!Array.isArray(p.tabs) || !p.tabs.length) {
      p.tabs = [{ widget: p.widget || 'terminal', config: p.config || {} }];
      p.activeTab = 0;
    }
  }
  function setTab(paneId, i) {
    const p = findPane(paneId); if (!p) return;
    ensureTabs(p);
    p.activeTab = Math.max(0, Math.min(i, p.tabs.length - 1));
    bump(); saveLayout();
  }
  function addTab(paneId, widget, config) {
    const p = findPane(paneId); if (!p) return;
    ensureTabs(p);
    p.tabs = [...p.tabs, { widget, config: config || {} }];
    p.activeTab = p.tabs.length - 1;
    bump(); saveLayout();
  }
  function closeTab(paneId, i) {
    const p = findPane(paneId); if (!p) return;
    ensureTabs(p);
    if (p.tabs.length <= 1) { removePane(paneId); return; }
    p.tabs = p.tabs.filter((_, idx) => idx !== i);
    if (p.activeTab >= p.tabs.length) p.activeTab = p.tabs.length - 1;
    bump(); saveLayout();
  }
  function setTabWidget(paneId, i, widget) {
    const p = findPane(paneId); if (!p) return;
    ensureTabs(p);
    const cfg = widget === 'team-transcript' ? { team: 'all' } : (widget === 'terminal' ? (p.tabs[i].config || {}) : {});
    p.tabs[i] = { widget, config: cfg };
    bump(); saveLayout();
  }
  function setTabAgent(paneId, i, agent) {
    const p = findPane(paneId); if (!p) return;
    ensureTabs(p);
    p.tabs[i] = { widget: 'terminal', config: { agent } };
    bump(); saveLayout();
  }

  // mutate-in-place reactivity nudge for the deep $state tree
  let rev = $state(0);
  function bump() { layout = layout; rev++; }

  // ----- focus engine (#691-style) -----
  function pushMRU(name) { mru = [name, ...mru.filter(n => n !== name)].slice(0, 12); }

  // re-bind the focused (or first) terminal pane to `name`; the canonical
  // "pane switching" gesture from the Explorer.
  function selectAgent(name) {
    selectedAgent = name;
    const s = (sessions || []).find(x => x.name === name);
    if (s && (s.agent === 'external-a2a' || s.external)) return; // hub peers: inspector only
    pushMRU(name);
    // existing terminal tab for this agent? activate it.
    let found = null;
    walk(layout, p => {
      if (found) return;
      ensureTabs(p);
      p.tabs.forEach((t, i) => { if (!found && t.widget === 'terminal' && t.config?.agent === name) found = { p, i }; });
    });
    if (found) { found.p.activeTab = found.i; focusedPaneID = found.p.id; bump(); return; }
    // else rebind the active terminal of the focused pane, else first terminal pane
    let target = findPane(focusedPaneID);
    const isTermPane = (p) => { ensureTabs(p); return p.tabs[p.activeTab]?.widget === 'terminal'; };
    if (!target || !isTermPane(target)) {
      target = null;
      walk(layout, p => { if (!target && isTermPane(p)) target = p; });
    }
    if (!target) { addTab(leafIDs(layout)[0], 'terminal', { agent: name }); return; }
    ensureTabs(target);
    target.tabs[target.activeTab] = { widget: 'terminal', config: { agent: name } };
    focusedPaneID = target.id;
    bump(); // VIEW change (rebind) — not persisted, per #709.S1.2 rule
  }
  function selectAgentNewTab(name) {
    selectedAgent = name; pushMRU(name);
    const pid = findPane(focusedPaneID) ? focusedPaneID : (leafIDs(layout)[0] || '');
    if (pid) addTab(pid, 'terminal', { agent: name });
  }
  function focusTerminal(name) { if (!name) return; selectedAgent = name; pushMRU(name); }

  // ===== persistence =====
  async function saveLayout() {
    try {
      await fetch(`${API}/workspaces/studio`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ layout }),
      });
    } catch {}
  }
  function sanitize(node) {
    if (!node) return null;
    if (node.kind === 'pane') {
      const tabs = (Array.isArray(node.tabs) && node.tabs.length ? node.tabs : [{ widget: node.widget || 'terminal', config: node.config || {} }])
        .filter(t => ['terminal', 'team-transcript', 'inspector', 'events'].includes(t.widget));
      if (!tabs.length) return null;
      return { kind: 'pane', id: node.id || newID(), tabs, activeTab: Math.min(node.activeTab || 0, tabs.length - 1) };
    }
    const a = sanitize(node.a), b = sanitize(node.b);
    if (!a && !b) return null;
    if (!a) return b; if (!b) return a;
    return { kind: node.kind, ratio: node.ratio ?? 0.5, a, b };
  }
  async function loadLayout() {
    try {
      const r = await fetch(`${API}/workspaces/studio`);
      if (!r.ok) return;
      const d = await r.json();
      const tree = d?.layout || d;
      const clean = sanitize(tree);
      if (clean) layout = clean;
    } catch {}
  }

  // ===== data refresh =====
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
      registerRoster([...sessions]
        .sort((a, b) => (a.created_at || '').localeCompare(b.created_at || '') || a.name.localeCompare(b.name))
        .map(x => x.name));
      teams = t.teams || [];
      memberships = m.memberships || [];
      inbox = ib.inbox || [];
      events = ev.events || [];
      if (selectedAgent && !sessions.find(x => x.name === selectedAgent)) {
        const fb = mru.find(n => n !== selectedAgent && sessions.find(x => x.name === n && !x.exited));
        selectedAgent = fb || null;
      }
      if (!selectedAgent && sessions.length) {
        const w = sessions.find(x => !x.exited && x.role !== 'shepherd') || sessions.find(x => !x.exited);
        if (w) selectedAgent = w.name;
      }
    } catch {}
  }

  let evStream = null;
  function startEventStream() {
    if (evStream) return;
    let tok = '';
    try { tok = localStorage.getItem('chepherd-token') || ''; } catch {}
    const q = tok ? ('?token=' + encodeURIComponent(tok)) : '';
    try {
      evStream = new EventSource(`${API}/events/stream${q}`);
      evStream.onmessage = (ev) => { try { events = [...events, JSON.parse(ev.data)].slice(-200); } catch {} };
      evStream.onerror = () => { evStream?.close(); evStream = null; setTimeout(startEventStream, 3000); };
    } catch {}
  }

  // ===== theme + font =====
  function applyTheme(t) {
    theme = t;
    if (typeof document !== 'undefined') document.documentElement.dataset.theme = t;
    try { localStorage.setItem('chepherd-theme', t); } catch {}
  }
  function toggleTheme() { applyTheme(theme === 'dark' ? 'light' : 'dark'); }
  function applyFont(n) {
    fontSize = Math.max(9, Math.min(22, n));
    if (typeof document !== 'undefined') {
      document.documentElement.style.setProperty('--ws-font', fontSize + 'px');
    }
    try { localStorage.setItem('chepherd-font', String(fontSize)); } catch {}
  }

  // ===== side panel + dock resize =====
  function startSideResize(e) {
    e.preventDefault();
    const move = (ev) => { sideWidth = Math.max(180, Math.min(520, ev.clientX - 48)); };
    const up = () => { document.removeEventListener('mousemove', move); document.removeEventListener('mouseup', up); document.body.style.cursor = ''; };
    document.addEventListener('mousemove', move); document.addEventListener('mouseup', up);
    document.body.style.cursor = 'col-resize';
  }
  function startDockResize(e) {
    e.preventDefault();
    const startY = e.clientY, startH = dockHeight;
    const move = (ev) => { dockHeight = Math.max(120, Math.min(window.innerHeight - 180, startH + (startY - ev.clientY))); };
    const up = () => { document.removeEventListener('mousemove', move); document.removeEventListener('mouseup', up); document.body.style.cursor = ''; };
    document.addEventListener('mousemove', move); document.addEventListener('mouseup', up);
    document.body.style.cursor = 'row-resize';
  }

  function activityClick(view) {
    if (activeView === view && sidePanelOpen) { sidePanelOpen = false; return; }
    activeView = view; sidePanelOpen = true;
  }

  // ===== auth =====
  function storedToken() { try { return localStorage.getItem('chepherd-token') || ''; } catch { return ''; } }
  async function probe(tok) {
    if (!tok) return false;
    try { return (await fetch(`${API}/sessions`)).status === 200; } catch { return false; }
  }
  async function attemptLogin() {
    const t = tokenInput.trim();
    if (!t) { loginError = 'paste the bootstrap token'; return; }
    probing = true; loginError = '';
    try { localStorage.setItem('chepherd-token', t); } catch {}
    const ok = await probe(t);
    probing = false;
    if (ok) { authStatus = 'ok'; tokenInput = ''; startSession(); }
    else { try { localStorage.removeItem('chepherd-token'); } catch {} loginError = 'token rejected — paste a fresh one'; }
  }
  function logout() {
    try { localStorage.removeItem('chepherd-token'); } catch {}
    authStatus = 'login';
  }

  let pollIntv = null;
  function startSession() {
    refresh();
    loadLayout();
    startEventStream();
    if (!pollIntv) pollIntv = setInterval(refresh, 2500);
    setTimeout(() => { if (!focusedPaneID) focusedPaneID = leafIDs(layout)[0] || ''; }, 200);
  }

  onMount(() => {
    // theme: stored, else OS preference
    let t = '';
    try { t = localStorage.getItem('chepherd-theme') || ''; } catch {}
    if (!t) t = (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches) ? 'light' : 'dark';
    applyTheme(t);
    let f = 14;
    try { f = +(localStorage.getItem('chepherd-font') || 14) || 14; } catch {}
    applyFont(f);

    // keyboard: Ctrl+Alt+T new tab, Ctrl+` cycle, Ctrl+Arrow pane focus
    const onKey = (e) => {
      const cmd = e.ctrlKey || e.metaKey;
      if (cmd && e.altKey && (e.key === 't' || e.key === 'T')) {
        e.preventDefault();
        if (focusedPaneID) window.dispatchEvent(new CustomEvent('studio-new-tab', { detail: { paneID: focusedPaneID } }));
        return;
      }
      if (cmd && e.key === '`') {
        e.preventDefault();
        if (focusedPaneID) window.dispatchEvent(new CustomEvent('studio-cycle-tab', { detail: { paneID: focusedPaneID, direction: e.shiftKey ? -1 : 1 } }));
        return;
      }
      if (cmd && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) {
        e.preventDefault();
        const ids = leafIDs(layout);
        if (!ids.length) return;
        let i = ids.indexOf(focusedPaneID);
        if (i < 0) i = 0;
        const d = e.key === 'ArrowLeft' ? -1 : 1;
        focusedPaneID = ids[(i + d + ids.length) % ids.length];
        queueMicrotask(() => {
          const el = document.querySelector(`[data-pane-id="${focusedPaneID}"]`);
          const target = el?.querySelector('.xterm-helper-textarea, textarea, input, button') || el;
          try { target?.focus?.(); } catch {}
        });
      }
    };
    window.addEventListener('keydown', onKey, true);

    // mid-session 401 / logout
    const on401 = () => logout();
    const onLogout = () => logout();
    window.addEventListener('chepherd-401', on401);
    window.addEventListener('chepherd-logout', onLogout);

    // ingest ?token=
    try {
      const u = new URL(location.href);
      const urlTok = u.searchParams.get('token');
      if (urlTok) {
        try { localStorage.setItem('chepherd-token', urlTok); } catch {}
        u.searchParams.delete('token');
        history.replaceState(null, '', u.toString());
      }
    } catch {}

    // initial auth probe
    (async () => {
      const tok = storedToken();
      if (!tok) { authStatus = 'login'; return; }
      const ok = await probe(tok);
      if (ok) { authStatus = 'ok'; startSession(); }
      else { try { localStorage.removeItem('chepherd-token'); } catch {} authStatus = 'login'; }
    })();

    return () => {
      window.removeEventListener('keydown', onKey, true);
      window.removeEventListener('chepherd-401', on401);
      window.removeEventListener('chepherd-logout', onLogout);
      if (pollIntv) clearInterval(pollIntv);
      evStream?.close();
    };
  });

  let liveCount = $derived((sessions || []).filter(s => !s.exited).length);
  let meshCount = $derived((sessions || []).filter(s => s.agent === 'external-a2a' || s.external).length);
</script>

{#if authStatus === 'checking'}
  <div class="studio-boot"><span class="spin"></span> Loading studio…</div>
{:else if authStatus === 'login'}
  <div class="login">
    <div class="login-card">
      <div class="login-brand"><span class="mark">◧</span> chepherd <span class="ver">studio</span></div>
      <p class="prose">Paste the bootstrap token chepherd printed at startup.</p>
      <textarea bind:value={tokenInput} placeholder="eyJhbGc…" rows="4" spellcheck="false" autocomplete="off" aria-label="bootstrap token"></textarea>
      {#if loginError}<div class="login-err" role="alert">{loginError}</div>{/if}
      <button class="login-btn" onclick={attemptLogin} disabled={probing}>{probing ? 'Verifying…' : 'Sign in'}</button>
    </div>
  </div>
{:else}
  <div class="studio">
    <!-- ACTIVITY BAR -->
    <nav class="activity" aria-label="Activity bar">
      <div class="act-top">
        <button class="act" class:on={activeView === 'explorer' && sidePanelOpen} onclick={() => activityClick('explorer')} title="Explorer — agents (Ctrl-click panel)" aria-label="Explorer">
          <span class="act-glyph">▤</span>
        </button>
        <button class="act" class:on={activeView === 'conversation' && sidePanelOpen} onclick={() => activityClick('conversation')} title="Conversation" aria-label="Conversation">
          <span class="act-glyph">💬</span>
        </button>
        <button class="act" class:on={activeView === 'inspector' && sidePanelOpen} onclick={() => activityClick('inspector')} title="Inspector" aria-label="Inspector">
          <span class="act-glyph">ⓘ</span>
        </button>
        <button class="act spawn" onclick={() => (showWizard = true)} title="Spawn agent / apply team" aria-label="Spawn">
          <span class="act-glyph">⊕</span>
        </button>
      </div>
      <div class="act-bottom">
        <button class="act" onclick={toggleTheme} title="Toggle dark / light theme" aria-label="Toggle theme">
          <span class="act-glyph">{theme === 'dark' ? '🌙' : '☀'}</span>
        </button>
        <button class="act" class:on={activeView === 'settings' && sidePanelOpen} onclick={() => activityClick('settings')} title="Settings" aria-label="Settings">
          <span class="act-glyph">⚙</span>
        </button>
        <button class="act" onclick={logout} title="Sign out" aria-label="Sign out">
          <span class="act-glyph">⎋</span>
        </button>
      </div>
    </nav>

    <!-- SIDE PANEL -->
    {#if sidePanelOpen}
      <aside class="side" style="width:{sideWidth}px">
        <header class="side-head">
          <span class="side-title">
            {activeView === 'explorer' ? 'EXPLORER'
              : activeView === 'conversation' ? 'CONVERSATION'
              : activeView === 'inspector' ? 'INSPECTOR'
              : 'SETTINGS'}
          </span>
          <button class="side-x" onclick={() => (sidePanelOpen = false)} title="Hide panel" aria-label="Hide panel">⟨</button>
        </header>
        <div class="side-body">
          {#if activeView === 'explorer'}
            <StudioExplorer {sessions} {teams} {memberships} {selectedAgent}
              onSelect={selectAgent} onNewTab={selectAgentNewTab} />
          {:else if activeView === 'conversation'}
            <TeamTranscript team="all" />
          {:else if activeView === 'inspector'}
            <StudioInspector {sessions} {memberships} {selectedAgent} />
          {:else if activeView === 'settings'}
            <StudioSettings {theme} {fontSize} {events}
              onToggleTheme={toggleTheme} onFontSize={applyFont} />
          {/if}
        </div>
        <div class="side-resizer" onmousedown={startSideResize} role="separator" aria-label="Resize side panel" tabindex="-1"></div>
      </aside>
    {/if}

    <!-- MAIN COLUMN: editor area + bottom dock + status bar -->
    <div class="main">
      <header class="topbar">
        <div class="tb-left">
          {#if !sidePanelOpen}
            <button class="tb-icon" onclick={() => (sidePanelOpen = true)} title="Show panel" aria-label="Show panel">☰</button>
          {/if}
          <span class="tb-brand"><span class="tb-mark">◧</span>chepherd<span class="tb-ver">{version}</span></span>
        </div>
        <div class="tb-mid">
          <button class="lay-btn" onclick={() => { layout = defaultLayout(); focusedPaneID = leafIDs(layout)[0]; bump(); saveLayout(); }} title="Reset to a single pane">Reset layout</button>
          <button class="lay-btn" onclick={() => splitPane(focusedPaneID || leafIDs(layout)[0], 'h')} title="Split focused pane right">Split ▕</button>
          <button class="lay-btn" onclick={() => splitPane(focusedPaneID || leafIDs(layout)[0], 'v')} title="Split focused pane down">Split ▁</button>
        </div>
        <div class="tb-right">
          {#if meshCount}<button class="tb-chip mesh" onclick={() => activityClick('settings')} title="hub mesh peers">⇄ {meshCount}</button>{/if}
          {#if selectedAgent}<span class="tb-chip">focus: {selectedAgent}</span>{/if}
          <button class="tb-icon" onclick={() => (showWizard = true)} title="Spawn">⊕</button>
        </div>
      </header>

      <div class="editor" data-rev={rev}>
        <StudioPane node={layout} {sessions} {teams} {memberships} {events} {selectedAgent}
          {focusTerminal} {splitPane} {removePane} {closeTab} {addTab} {setTab}
          {setTabWidget} {setTabAgent} {focusedPaneID}
          setFocusedPane={(id) => (focusedPaneID = id)}
          onLayoutChange={saveLayout} />
      </div>

      {#if dockOpen}
        <div class="dock-resizer" onmousedown={startDockResize} role="separator" aria-label="Resize dock" tabindex="-1"></div>
        <section class="dock" style="height:{dockHeight}px">
          <header class="dock-head">
            <span class="dock-title">EVENT LOG</span>
            <button class="dock-x" onclick={() => (dockOpen = false)} aria-label="Hide dock">×</button>
          </header>
          <div class="dock-body"><StudioEvents {events} /></div>
        </section>
      {/if}

      <footer class="statusbar">
        <button class="sb-item" onclick={() => (dockOpen = !dockOpen)} title="Toggle event log dock">
          ≣ {events.length} events
        </button>
        <span class="sb-item">{liveCount} live · {sessions.length} agents · {teams.length} teams</span>
        <span class="sb-spacer"></span>
        <button class="sb-item" onclick={toggleTheme} title="Toggle theme">{theme === 'dark' ? '🌙 dark' : '☀ light'}</button>
        <span class="sb-item">⌨ Ctrl+Alt+T tab · Ctrl+` cycle · Ctrl+◂▸ pane</span>
      </footer>
    </div>
  </div>

  {#if showWizard}
    <div class="wiz-overlay" role="dialog" aria-modal="true">
      <SpawnWizardV9 onclose={() => { showWizard = false; refresh(); }} />
    </div>
  {/if}
{/if}

<style>
  /* ============ THEME TOKENS — full coverage, both modes ============ */
  :global(html[data-theme='dark']) {
    --st-bg: #0d1117;          /* page / pane body */
    --st-bg-deep: #010409;     /* activity bar */
    --st-panel: #161b22;       /* surfaces, tabbar, menus */
    --st-chip: #1c232c;        /* chips, inputs */
    --st-hover: #21262d;
    --st-sel-bg: #1f2d3d;
    --st-border: #283039;
    --st-border-strong: #3a444f;
    --st-fg: #e6edf3;
    --st-fg-muted: #9aa7b4;
    --st-fg-faint: #5c6773;
    --st-accent: #ffa500;      /* chepherd orange */
    --st-accent-2: #79c0ff;    /* sky */
    --st-ok: #3fb950;
    --st-danger: #f85149;
    --st-term-bg: #0d1117;
    --st-scroll-thumb: #30363d;
    --st-shadow: 0 8px 28px rgba(0,0,0,0.55);
  }
  :global(html[data-theme='light']) {
    --st-bg: #ffffff;
    --st-bg-deep: #1f2733;     /* keep activity bar dark-ish for contrast anchor */
    --st-panel: #f3f5f8;
    --st-chip: #eaeef3;
    --st-hover: #e4e9f0;
    --st-sel-bg: #dbeafe;
    --st-border: #d7dde5;
    --st-border-strong: #b9c2cd;
    --st-fg: #1f2430;
    --st-fg-muted: #5b6675;
    --st-fg-faint: #98a2b0;
    --st-accent: #c8780a;      /* darker orange for AA contrast on light */
    --st-accent-2: #1f6feb;
    --st-ok: #1a7f37;
    --st-danger: #cf222e;
    --st-term-bg: #ffffff;
    --st-scroll-thumb: #c4ccd6;
    --st-shadow: 0 8px 28px rgba(15,30,55,0.18);
  }
  /* default before JS sets data-theme: assume dark to avoid white flash */
  :global(html:not([data-theme])) {
    --st-bg: #0d1117; --st-bg-deep: #010409; --st-panel: #161b22; --st-chip: #1c232c;
    --st-hover: #21262d; --st-sel-bg: #1f2d3d; --st-border: #283039; --st-border-strong: #3a444f;
    --st-fg: #e6edf3; --st-fg-muted: #9aa7b4; --st-fg-faint: #5c6773; --st-accent: #ffa500;
    --st-accent-2: #79c0ff; --st-ok: #3fb950; --st-danger: #f85149; --st-term-bg: #0d1117;
    --st-scroll-thumb: #30363d; --st-shadow: 0 8px 28px rgba(0,0,0,0.55);
  }

  /* scrollbars themed in both modes */
  .studio :global(*::-webkit-scrollbar) { width: 10px; height: 10px; }
  .studio :global(*::-webkit-scrollbar-track) { background: transparent; }
  .studio :global(*::-webkit-scrollbar-thumb) { background: var(--st-scroll-thumb); border-radius: 6px; }
  .studio :global(*::-webkit-scrollbar-thumb:hover) { background: var(--st-border-strong); }

  /* ============ shell layout ============ */
  .studio {
    position: fixed; inset: 0; display: flex; overflow: hidden;
    background: var(--st-bg); color: var(--st-fg);
    font-family: ui-sans-serif, system-ui, -apple-system, sans-serif;
    font-size: 0.9rem;
  }

  .activity {
    width: 48px; flex-shrink: 0; background: var(--st-bg-deep);
    display: flex; flex-direction: column; justify-content: space-between;
    align-items: center; padding: 0.5rem 0; border-right: 1px solid var(--st-border);
  }
  .act-top, .act-bottom { display: flex; flex-direction: column; gap: 0.2rem; }
  .act { width: 40px; height: 40px; display: grid; place-items: center; background: transparent;
    border: 0; border-left: 2px solid transparent; color: #9aa7b4; cursor: pointer; border-radius: 0; }
  .act:hover { color: #fff; }
  .act.on { color: #fff; border-left-color: var(--st-accent); }
  .act.spawn { color: var(--st-accent); }
  .act.spawn:hover { color: #fff; }
  .act-glyph { font-size: 1.1rem; line-height: 1; }

  .side { flex-shrink: 0; background: var(--st-panel); border-right: 1px solid var(--st-border);
    display: flex; flex-direction: column; min-width: 0; position: relative; }
  .side-head { display: flex; align-items: center; justify-content: space-between;
    padding: 0.55rem 0.7rem; border-bottom: 1px solid var(--st-border); }
  .side-title { font-size: 0.7rem; letter-spacing: 0.07em; color: var(--st-fg-muted); font-weight: 600; }
  .side-x { background: transparent; border: 0; color: var(--st-fg-muted); cursor: pointer; font-size: 0.9rem; }
  .side-x:hover { color: var(--st-fg); }
  .side-body { flex: 1; min-height: 0; overflow: hidden; display: flex; flex-direction: column; }
  .side-body :global(> *) { flex: 1; min-height: 0; }
  .side-resizer { position: absolute; top: 0; right: -3px; width: 6px; height: 100%; cursor: col-resize; z-index: 10; }
  .side-resizer:hover { background: color-mix(in srgb, var(--st-accent) 40%, transparent); }

  .main { flex: 1; min-width: 0; display: flex; flex-direction: column; }

  .topbar { display: flex; align-items: center; gap: 0.8rem; height: 38px; flex-shrink: 0;
    padding: 0 0.7rem; background: var(--st-panel); border-bottom: 1px solid var(--st-border); }
  .tb-left { display: flex; align-items: center; gap: 0.5rem; }
  .tb-brand { font-weight: 700; font-size: 0.85rem; display: flex; align-items: center; gap: 0.25rem; }
  .tb-mark { color: var(--st-accent); }
  .tb-ver { color: var(--st-accent); font-weight: 600; margin-left: 0.2rem; font-size: 0.72rem;
    background: var(--st-chip); padding: 0.05rem 0.4rem; border-radius: 5px; }
  .tb-mid { display: flex; gap: 0.3rem; }
  .lay-btn { background: var(--st-chip); border: 1px solid var(--st-border); color: var(--st-fg-muted);
    border-radius: 6px; cursor: pointer; font: inherit; font-size: 0.74rem; padding: 0.2rem 0.55rem; }
  .lay-btn:hover { color: var(--st-fg); border-color: var(--st-accent); }
  .tb-right { margin-left: auto; display: flex; align-items: center; gap: 0.45rem; }
  .tb-chip { font-size: 0.72rem; color: var(--st-fg-muted); background: var(--st-chip);
    border: 1px solid var(--st-border); border-radius: 999px; padding: 0.12rem 0.55rem; }
  button.tb-chip { cursor: pointer; }
  button.tb-chip.mesh { color: var(--st-accent-2); border-color: var(--st-accent-2); }
  .tb-icon { background: transparent; border: 0; color: var(--st-fg-muted); cursor: pointer; font-size: 1rem; padding: 0.1rem 0.3rem; }
  .tb-icon:hover { color: var(--st-fg); }

  .editor { flex: 1; min-height: 0; padding: 0.5rem; overflow: hidden; }
  .editor :global(> *) { height: 100%; }

  .dock-resizer { height: 6px; cursor: row-resize; background: transparent; flex-shrink: 0; }
  .dock-resizer:hover { background: color-mix(in srgb, var(--st-accent) 40%, transparent); }
  .dock { flex-shrink: 0; display: flex; flex-direction: column; background: var(--st-bg);
    border-top: 1px solid var(--st-border); min-height: 0; }
  .dock-head { display: flex; align-items: center; justify-content: space-between;
    padding: 0.3rem 0.7rem; background: var(--st-panel); border-bottom: 1px solid var(--st-border); }
  .dock-title { font-size: 0.7rem; letter-spacing: 0.06em; color: var(--st-fg-muted); font-weight: 600; }
  .dock-x { background: transparent; border: 0; color: var(--st-fg-muted); cursor: pointer; font-size: 1rem; }
  .dock-x:hover { color: var(--st-fg); }
  .dock-body { flex: 1; min-height: 0; }

  .statusbar { height: 24px; flex-shrink: 0; display: flex; align-items: center; gap: 0.9rem;
    padding: 0 0.7rem; background: var(--st-bg-deep); border-top: 1px solid var(--st-border);
    font-size: 0.72rem; color: #9aa7b4; }
  .sb-item { background: transparent; border: 0; color: inherit; font: inherit; cursor: default; padding: 0; }
  button.sb-item { cursor: pointer; }
  button.sb-item:hover { color: #fff; }
  .sb-spacer { flex: 1; }

  /* ============ boot + login ============ */
  .studio-boot { position: fixed; inset: 0; display: flex; align-items: center; justify-content: center;
    gap: 0.6rem; background: var(--st-bg); color: var(--st-fg-muted); font-size: 0.9rem; }
  .spin { width: 1rem; height: 1rem; border: 2px solid var(--st-border); border-top-color: var(--st-accent);
    border-radius: 50%; animation: spin 0.7s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }

  .login { position: fixed; inset: 0; display: flex; align-items: center; justify-content: center;
    background: var(--st-bg); padding: 1rem; }
  .login-card { width: 100%; max-width: 30rem; background: var(--st-panel);
    border: 1px solid var(--st-border); border-radius: 12px; padding: 1.8rem;
    display: flex; flex-direction: column; gap: 0.8rem; box-shadow: var(--st-shadow); }
  .login-brand { font-size: 1.2rem; font-weight: 700; display: flex; align-items: center; gap: 0.4rem; }
  .login-brand .mark { color: var(--st-accent); }
  .login-brand .ver { color: var(--st-accent); font-size: 0.8rem; background: var(--st-chip);
    padding: 0.1rem 0.45rem; border-radius: 6px; }
  .prose { color: var(--st-fg-muted); font-size: 0.86rem; margin: 0; }
  .login textarea { width: 100%; box-sizing: border-box; padding: 0.55rem; background: var(--st-bg);
    color: var(--st-fg); border: 1px solid var(--st-border); border-radius: 8px;
    font-family: ui-monospace, monospace; font-size: 0.82rem; resize: vertical; }
  .login textarea:focus { outline: none; border-color: var(--st-accent); }
  .login-err { color: var(--st-danger); font-size: 0.84rem; background: color-mix(in srgb, var(--st-danger) 10%, transparent);
    border-left: 3px solid var(--st-danger); padding: 0.4rem 0.6rem; border-radius: 3px; }
  .login-btn { background: var(--st-accent); color: #0a0a0a; border: 0; border-radius: 8px;
    padding: 0.6rem; font-weight: 700; font-size: 0.92rem; cursor: pointer; }
  .login-btn:disabled { opacity: 0.6; cursor: progress; }
  .login-btn:hover:not(:disabled) { filter: brightness(1.08); }

  .wiz-overlay { position: fixed; inset: 0; z-index: 200; background: rgba(0,0,0,0.55);
    display: flex; align-items: center; justify-content: center; padding: 1rem; overflow: auto; }
</style>

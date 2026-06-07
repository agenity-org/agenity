<!--
  Dashboardmission — root of the "mission control" dashboard (codename
  mission). A dense, dark, ops-console layout: a status-rich topbar
  (telemetry), a collapsible roster rail, a fully drag-resizable tiling
  pane grid of live agent terminals + widgets, and a collapsible context
  column (inspector + transcript).

  This component owns:
    - Auth (token from ?token= / localStorage; module-top fetch wrapper;
      login screen; 401 handling) — same contract as AuthGate/Workspace.
    - The real data layer: polls /api/v1/{sessions,teams,memberships,events}
      every 2.5s + an EventSource on /api/v1/events/stream.
    - Theme: dark|light token sets applied as inline CSS vars on the root
      (so this dashboard never collides with global :root dark-only tokens).
      Persisted in localStorage; first load respects prefers-color-scheme.
    - The center LAYOUT TREE (panes / h+v splits / tabs) + every mutation:
      split, close, set-agent, set-ratio (drag resize), add/close/activate
      tab. Persisted to /api/v1/workspaces/{name} on explicit structural
      intents; focus-driven rebinds are view-only (no save) per #709 rule.

  HARD REQUIREMENTS:
    1 pane switching → per-pane agent <select> + roster-click rebind
    2 pane resizing  → draggable splitters (MissionPane startDrag → setRatio)
    3 layout flex    → arbitrary nested splits + tabs, rearrangeable
    4 all views      → terminals, roster, transcript, inspector, settings, spawn
    5 real data + live terminal (MissionTerminal WebSocket attach)
    6 per-agent colors+icons via agentIdentity
    7 polished dark + light themes with a visible toggle (topbar + settings)
-->
<script>
  import { onMount } from 'svelte';
  import '@xterm/xterm/css/xterm.css';
  import MissionPane from './MissionPane.svelte';
  import MissionRoster from './MissionRoster.svelte';
  import MissionInspector from './MissionInspector.svelte';
  import MissionTranscript from './MissionTranscript.svelte';
  import MissionSpawn from './MissionSpawn.svelte';
  import MissionSettings from './MissionSettings.svelte';
  import { registerRoster } from '../../lib/agentIdentity.js';
  import { themeStyle } from './theme.js';

  let { version = 'v0.9.4' } = $props();

  // ── module-top fetch-auth wrapper (idempotent; mirrors AuthGate) ──
  if (typeof window !== 'undefined' && !window.__chepherdFetchPatched) {
    window.__chepherdFetchPatched = true;
    const _orig = window.fetch.bind(window);
    window.fetch = (input, init) => {
      const url = typeof input === 'string' ? input : (input?.url || '');
      if (url.startsWith('/api/') || url.startsWith('/api-v')) {
        let tok = ''; try { tok = localStorage.getItem('chepherd-token') || ''; } catch {}
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

  // ── auth state ──
  let authStatus = $state('checking'); // checking | login | ok
  let tokenInput = $state('');
  let loginError = $state('');
  let probing = $state(false);

  function readToken() { try { return localStorage.getItem('chepherd-token') || ''; } catch { return ''; } }
  function storeToken(t) { try { localStorage.setItem('chepherd-token', t); } catch {} }
  function clearToken() { try { localStorage.removeItem('chepherd-token'); } catch {} }
  async function probe() { try { const r = await fetch(`${API}/sessions`); return r.status === 200; } catch { return false; } }
  async function attemptLogin() {
    const t = tokenInput.trim();
    if (!t) { loginError = 'paste the bootstrap token'; return; }
    probing = true; loginError = ''; storeToken(t);
    const ok = await probe(); probing = false;
    if (ok) { authStatus = 'ok'; tokenInput = ''; bootData(); }
    else { clearToken(); loginError = 'token rejected — paste a fresh one'; }
  }
  function signout() { clearToken(); authStatus = 'login'; }

  // ── theme ──
  let mode = $state('dark');
  let fontSize = $state(14);
  function applyTheme(m) {
    mode = m;
    try { localStorage.setItem('chepherd-mission-theme', m); } catch {}
    // keep WidgetTerminal-style consumers + global xterm in sync if any
    if (typeof document !== 'undefined') document.documentElement.dataset.theme = (m === 'light' ? 'light' : 'dark');
  }
  function toggleTheme() { applyTheme(mode === 'dark' ? 'light' : 'dark'); }
  function applyFont(delta) {
    fontSize = Math.max(9, Math.min(22, fontSize + delta));
    try { document.documentElement.style.setProperty('--ws-font', fontSize + 'px'); } catch {}
    try { localStorage.setItem('chepherd-font', String(fontSize)); } catch {}
  }
  const rootStyle = $derived(themeStyle(mode) + `;--ws-font:${fontSize}px`);

  // ── data layer ──
  let sessions = $state([]);
  let teams = $state([]);
  let memberships = $state([]);
  let events = $state([]);
  let selectedAgent = $state(null);
  let mruFocus = [];

  function pushMRU(name) { mruFocus = [name, ...mruFocus.filter(n => n !== name)].slice(0, 12); }

  async function refresh() {
    try {
      const [s, t, m, ev] = await Promise.all([
        fetch(`${API}/sessions`).then(r => r.json()),
        fetch(`${API}/teams`).then(r => r.json()),
        fetch(`${API}/memberships`).then(r => r.json()),
        fetch(`${API}/events?limit=80`).then(r => r.json()),
      ]);
      sessions = s.sessions || [];
      registerRoster([...sessions]
        .sort((a, b) => (a.created_at || '').localeCompare(b.created_at || '') || a.name.localeCompare(b.name))
        .map(x => x.name));
      teams = t.teams || [];
      memberships = m.memberships || [];
      events = ev.events || [];
      if (selectedAgent && !sessions.find(x => x.name === selectedAgent)) {
        const fb = mruFocus.find(n => n !== selectedAgent && sessions.find(x => x.name === n && !x.exited));
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
    const tok = readToken();
    const q = tok ? ('?token=' + encodeURIComponent(tok)) : '';
    try {
      evStream = new EventSource(`${API}/events/stream${q}`);
      evStream.onmessage = (e) => { try { events = [...events, JSON.parse(e.data)].slice(-200); } catch {} };
      evStream.onerror = () => { evStream?.close(); evStream = null; setTimeout(startEventStream, 3000); };
    } catch {}
  }

  // ── layout tree (center grid) ──
  let layout = $state(defaultLayout());
  let focusedPaneID = $state('');
  function defaultLayout() {
    return {
      kind: 'h', id: 'root', ratio: 0.62,
      a: { kind: 'pane', id: 'p1', widget: 'terminal', config: {} },
      b: {
        kind: 'v', id: 's1', ratio: 0.5,
        a: { kind: 'pane', id: 'p2', widget: 'terminal', config: {} },
        b: { kind: 'pane', id: 'p3', widget: 'inspector', config: {} },
      },
    };
  }
  function uid() { return 'p' + Math.random().toString(36).slice(2, 8); }

  // tree helpers (pure, return new trees so $state updates)
  function mapNode(node, id, fn) {
    if (!node) return node;
    if (node.kind === 'pane') return node.id === id ? fn(node) : node;
    return { ...node, a: mapNode(node.a, id, fn), b: mapNode(node.b, id, fn) };
  }
  function setRatioNode(node, id, ratio) {
    if (!node || node.kind === 'pane') return node;
    if (node.id === id) return { ...node, ratio };
    return { ...node, a: setRatioNode(node.a, id, ratio), b: setRatioNode(node.b, id, ratio) };
  }
  function removeNode(node, id) {
    if (!node) return node;
    if (node.kind === 'pane') return node.id === id ? null : node;
    const a = removeNode(node.a, id), b = removeNode(node.b, id);
    if (!a) return b; if (!b) return a; return { ...node, a, b };
  }
  function tabsOf(p) {
    return Array.isArray(p.tabs) && p.tabs.length ? p.tabs : [{ widget: p.widget, config: p.config || {} }];
  }
  function walk(node, fn) {
    if (!node) return; if (node.kind === 'pane') { fn(node); return; } walk(node.a, fn); walk(node.b, fn);
  }

  // ── workspace persistence ──
  async function saveLayout(name = 'current') {
    try {
      const bodyObj = name === 'current' ? layout : { layout, cwd: '' };
      await fetch(`${API}/workspaces/${name}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(bodyObj) });
    } catch {}
  }
  async function loadLayout(name = 'current') {
    try {
      const r = await fetch(`${API}/workspaces/${name}`);
      if (!r.ok) return;
      const d = await r.json();
      const tree = d && d.layout ? d.layout : d;
      if (tree && tree.kind) layout = tree;
    } catch {}
  }

  // ── pane operations (HARD REQ 1-3) ──
  function splitPane(id, dir) {
    layout = mapNode(layout, id, (p) => ({
      kind: dir, id: 's' + uid().slice(1), ratio: 0.5,
      a: p,
      b: { kind: 'pane', id: uid(), widget: 'terminal', config: {} },
    }));
    saveLayout();
  }
  function closePane(id) {
    const next = removeNode(layout, id);
    layout = next || { kind: 'pane', id: uid(), widget: 'terminal', config: {} };
    saveLayout();
  }
  function setRatio(id, ratio) { layout = setRatioNode(layout, id, ratio); }
  function setPaneAgent(id, agent) {
    // pane switching — view change, no save (rule 1b). Update active tab.
    layout = mapNode(layout, id, (p) => {
      const tabs = tabsOf(p); const act = typeof p.activeTab === 'number' ? p.activeTab : 0;
      const newTabs = tabs.map((t, i) => i === act ? { widget: 'terminal', config: { ...(t.config || {}), agent } } : t);
      return { ...p, widget: 'terminal', config: { agent }, tabs: newTabs.length > 1 ? newTabs : undefined, activeTab: newTabs.length > 1 ? act : undefined };
    });
    if (agent) { selectedAgent = agent; pushMRU(agent); }
  }
  function focusPane(id) { focusedPaneID = id; }
  function addTab(id, widget) {
    layout = mapNode(layout, id, (p) => {
      const tabs = tabsOf(p);
      const cfg = widget === 'terminal' ? { agent: selectedAgent || '' } : (widget === 'team-transcript' ? { team: 'all' } : {});
      const newTabs = [...tabs, { widget, config: cfg }];
      return { ...p, tabs: newTabs, activeTab: newTabs.length - 1, widget, config: cfg };
    });
    saveLayout();
  }
  function closeTab(id, idx) {
    layout = mapNode(layout, id, (p) => {
      const tabs = tabsOf(p);
      if (tabs.length <= 1) return p;
      const newTabs = tabs.filter((_, i) => i !== idx);
      let act = typeof p.activeTab === 'number' ? p.activeTab : 0;
      if (act >= newTabs.length) act = newTabs.length - 1;
      const cur = newTabs[act];
      return { ...p, tabs: newTabs.length > 1 ? newTabs : undefined, activeTab: newTabs.length > 1 ? act : undefined, widget: cur.widget, config: cur.config || {} };
    });
    saveLayout();
  }
  function activateTab(id, idx) {
    layout = mapNode(layout, id, (p) => {
      const tabs = tabsOf(p); if (!tabs[idx]) return p;
      const cur = tabs[idx];
      return { ...p, activeTab: idx, widget: cur.widget, config: cur.config || {} };
    });
  }

  // roster click → rebind the focused terminal pane (view change). If the
  // focused pane isn't a terminal, rebind the first terminal pane.
  function selectAgentFromRoster(name) {
    selectedAgent = name; pushMRU(name);
    const sess = sessions.find(s => s.name === name);
    if (sess && (sess.agent === 'external-a2a' || sess.external)) return; // no PTY
    let target = null, first = null;
    walk(layout, (p) => {
      const cur = tabsOf(p)[typeof p.activeTab === 'number' ? p.activeTab : 0];
      if (cur.widget !== 'terminal') return;
      if (p.id === focusedPaneID) target = p;
      if (!first) first = p;
    });
    const t = target || first;
    if (t) { setPaneAgent(t.id, name); focusedPaneID = t.id; }
  }

  // ── chrome state ──
  let railOpen = $state(true);
  let contextOpen = $state(true);
  let showSpawn = $state(false);
  let showSettings = $state(false);

  // ── derived telemetry ──
  const liveCount = $derived(sessions.filter(s => !s.exited && s.live !== false && !s.paused).length);
  const pausedCount = $derived(sessions.filter(s => s.paused).length);
  const meshCount = $derived(sessions.filter(s => s.agent === 'external-a2a' || s.external).length);

  function bootData() {
    refresh(); loadLayout('current'); startEventStream();
  }

  let pollId = null;
  onMount(() => {
    // theme: persisted, else prefers-color-scheme
    let saved = '';
    try { saved = localStorage.getItem('chepherd-mission-theme') || ''; } catch {}
    if (saved === 'dark' || saved === 'light') applyTheme(saved);
    else applyTheme(typeof matchMedia !== 'undefined' && matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark');
    try { const f = +(localStorage.getItem('chepherd-font') || 14); fontSize = (f >= 9 && f <= 22) ? f : 14; } catch {}
    try { document.documentElement.style.setProperty('--ws-font', fontSize + 'px'); } catch {}

    // auth + ?token=
    (async () => {
      try {
        const urlTok = new URL(location.href).searchParams.get('token');
        if (urlTok) { storeToken(urlTok); const c = new URL(location.href); c.searchParams.delete('token'); history.replaceState(null, '', c.toString()); }
      } catch {}
      const tok = readToken();
      if (!tok) { authStatus = 'login'; return; }
      const ok = await probe();
      if (ok) { authStatus = 'ok'; bootData(); pollId = setInterval(refresh, 2500); }
      else { clearToken(); authStatus = 'login'; }
    })();

    const on401 = () => { clearToken(); authStatus = 'login'; };
    window.addEventListener('chepherd-401', on401);
    return () => { if (pollId) clearInterval(pollId); evStream?.close(); window.removeEventListener('chepherd-401', on401); };
  });

  // start polling once auth flips to ok via the login form too
  $effect(() => {
    if (authStatus === 'ok' && !pollId) pollId = setInterval(refresh, 2500);
  });
</script>

<div class="mission" data-mode={mode} style={rootStyle}>
  {#if authStatus === 'checking'}
    <div class="boot">initializing mission control…</div>
  {:else if authStatus === 'login'}
    <div class="login">
      <div class="login-card">
        <div class="login-brand"><span class="lb-mark">◎</span> chepherd <span class="lb-mode">mission</span></div>
        <p class="login-p">Paste the bootstrap token chepherd printed at startup.</p>
        <textarea bind:value={tokenInput} rows="4" placeholder="eyJhbGc…" spellcheck="false" autocomplete="off"></textarea>
        {#if loginError}<div class="login-err">{loginError}</div>{/if}
        <button class="login-go" onclick={attemptLogin} disabled={probing}>{probing ? 'Verifying…' : 'Sign in'}</button>
      </div>
    </div>
  {:else}
    <!-- ── TOPBAR (telemetry) ── -->
    <header class="topbar">
      <div class="brand">
        <span class="brand-mark">◎</span>
        <span class="brand-name">chepherd</span>
        <span class="brand-sub">MISSION</span>
        <span class="brand-ver">{version}</span>
      </div>

      <div class="telemetry">
        <span class="tm"><span class="led live"></span>{liveCount} live</span>
        <span class="tm"><span class="led paused"></span>{pausedCount} paused</span>
        <span class="tm">{sessions.length} agents</span>
        <span class="tm">{teams.length} teams</span>
        {#if meshCount}<span class="tm mesh" title="hub-discovered peers">⇄ {meshCount} peers</span>{/if}
      </div>

      <div class="tb-actions">
        <button class="tb-btn" class:on={railOpen} title="Toggle roster rail" onclick={() => (railOpen = !railOpen)}>☰ Roster</button>
        <button class="tb-btn" class:on={contextOpen} title="Toggle context column" onclick={() => (contextOpen = !contextOpen)}>◧ Context</button>
        <!-- theme toggle (visible, top-level) -->
        <button class="tb-btn theme" title="Toggle dark / light" onclick={toggleTheme}>{mode === 'dark' ? '🌙 Dark' : '☀ Light'}</button>
        <button class="tb-btn" title="Settings" onclick={() => (showSettings = true)}>⚙</button>
        <button class="tb-btn primary" title="Spawn agent or team" onclick={() => (showSpawn = true)}>◈ Spawn</button>
      </div>
    </header>

    <!-- ── SHELL: rail | center grid | context ── -->
    <div class="shell">
      {#if railOpen}
        <aside class="rail">
          <MissionRoster {sessions} {teams} {memberships} {selectedAgent} onSelectAgent={selectAgentFromRoster} />
        </aside>
      {/if}

      <main class="center">
        <MissionPane
          node={layout} {sessions} {teams} {memberships} {events} {mode} {selectedAgent} {focusedPaneID}
          onSplit={splitPane} onClose={closePane} onSetAgent={setPaneAgent} onSetRatio={setRatio}
          onFocusPane={focusPane} onAddTab={addTab} onCloseTab={closeTab} onActivateTab={activateTab}
          onSelectAgent={selectAgentFromRoster}
        />
      </main>

      {#if contextOpen}
        <aside class="context">
          <div class="ctx-top">
            <MissionInspector {sessions} {memberships} {events} {selectedAgent} {mode} />
          </div>
          <div class="ctx-bottom">
            <MissionTranscript {teams} {mode} initialTeam="all" />
          </div>
        </aside>
      {/if}
    </div>
  {/if}

  {#if showSpawn}
    <MissionSpawn {teams} onclose={() => (showSpawn = false)} onLaunched={refresh} />
  {/if}
  {#if showSettings}
    <MissionSettings {mode} {fontSize} {events}
      onToggleTheme={toggleTheme} onFont={applyFont} onSignout={() => { showSettings = false; signout(); }}
      onclose={() => (showSettings = false)} />
  {/if}
</div>

<style>
  .mission {
    position: fixed; inset: 0;
    display: flex; flex-direction: column;
    background:
      linear-gradient(var(--m-bg-grid) 1px, transparent 1px) 0 0 / 100% 28px,
      linear-gradient(90deg, var(--m-bg-grid) 1px, transparent 1px) 0 0 / 28px 100%,
      var(--m-bg);
    color: var(--m-fg);
    font-family: ui-sans-serif, system-ui, -apple-system, sans-serif;
    overflow: hidden;
  }

  /* boot + login */
  .boot { flex: 1; display: flex; align-items: center; justify-content: center; color: var(--m-fg-faint); font-family: ui-monospace, monospace; font-size: 0.85rem; letter-spacing: 0.1em; }
  .login { flex: 1; display: flex; align-items: center; justify-content: center; padding: 1rem; }
  .login-card { width: 100%; max-width: 28rem; background: var(--m-panel); border: 1px solid var(--m-border-strong); border-radius: 12px; padding: 1.8rem; display: flex; flex-direction: column; gap: 0.8rem; box-shadow: 0 24px 60px -16px var(--m-shadow); }
  .login-brand { font-size: 1.3rem; font-weight: 700; display: flex; align-items: center; gap: 0.5rem; }
  .lb-mark { color: var(--m-accent-2); }
  .lb-mode { font-size: 0.7rem; letter-spacing: 0.2em; color: var(--m-accent); align-self: flex-end; padding-bottom: 0.25rem; }
  .login-p { color: var(--m-fg-dim); font-size: 0.85rem; margin: 0; }
  .login textarea { background: var(--m-bg); color: var(--m-fg); border: 1px solid var(--m-border-strong); border-radius: 6px; font-family: ui-monospace, monospace; font-size: 0.82rem; padding: 0.55rem; resize: vertical; }
  .login-err { color: var(--m-danger); font-size: 0.8rem; border-left: 3px solid var(--m-danger); padding: 0.3rem 0.6rem; background: color-mix(in srgb, var(--m-danger) 8%, transparent); }
  .login-go { background: var(--m-accent-2); color: var(--m-bg); border: 0; border-radius: 6px; padding: 0.6rem; font-weight: 700; font-size: 0.9rem; cursor: pointer; }
  .login-go:disabled { opacity: 0.6; cursor: progress; }

  /* topbar */
  .topbar {
    display: flex; align-items: center; gap: 1.2rem;
    height: 46px; flex: 0 0 auto;
    padding: 0 0.8rem;
    background: linear-gradient(180deg, var(--m-panel-2), var(--m-panel));
    border-bottom: 1px solid var(--m-border-strong);
    box-shadow: 0 1px 0 var(--m-glow);
  }
  .brand { display: flex; align-items: baseline; gap: 0.45rem; }
  .brand-mark { color: var(--m-accent-2); font-size: 1.05rem; align-self: center; text-shadow: 0 0 10px var(--m-glow); }
  .brand-name { font-weight: 800; font-size: 1rem; letter-spacing: 0.01em; }
  .brand-sub { font-size: 0.6rem; letter-spacing: 0.22em; color: var(--m-accent); font-weight: 700; }
  .brand-ver { font-size: 0.66rem; color: var(--m-fg-faint); font-family: ui-monospace, monospace; }

  .telemetry { display: flex; align-items: center; gap: 1rem; flex: 1; font-size: 0.74rem; color: var(--m-fg-dim); font-family: ui-monospace, monospace; }
  .tm { display: inline-flex; align-items: center; gap: 0.35rem; white-space: nowrap; }
  .tm.mesh { color: var(--m-accent-2); }
  .led { width: 7px; height: 7px; border-radius: 50%; }
  .led.live { background: var(--m-live); box-shadow: 0 0 7px -1px var(--m-live); }
  .led.paused { background: var(--m-paused); }

  .tb-actions { display: flex; align-items: center; gap: 0.35rem; }
  .tb-btn {
    background: var(--m-panel-3); color: var(--m-fg-dim);
    border: 1px solid var(--m-border); border-radius: 6px;
    padding: 0.35rem 0.6rem; font: inherit; font-size: 0.74rem; cursor: pointer; white-space: nowrap;
  }
  .tb-btn:hover { color: var(--m-fg); border-color: var(--m-border-strong); }
  .tb-btn.on { color: var(--m-accent-2); border-color: var(--m-accent-2); }
  .tb-btn.theme { min-width: 5.2rem; text-align: center; }
  .tb-btn.primary { background: var(--m-accent); color: var(--m-bg); border-color: var(--m-accent); font-weight: 700; }
  .tb-btn.primary:hover { filter: brightness(1.08); color: var(--m-bg); }

  /* shell */
  .shell { flex: 1; min-height: 0; display: flex; }
  .rail { width: 248px; flex: 0 0 auto; border-right: 1px solid var(--m-border-strong); background: var(--m-panel); min-height: 0; }
  .center { flex: 1; min-width: 0; min-height: 0; padding: 2px; }
  .context { width: 360px; flex: 0 0 auto; border-left: 1px solid var(--m-border-strong); background: var(--m-panel); display: flex; flex-direction: column; min-height: 0; }
  .ctx-top { flex: 0 0 48%; min-height: 0; border-bottom: 1px solid var(--m-border-strong); }
  .ctx-bottom { flex: 1; min-height: 0; }

  @media (max-width: 1100px) {
    .context { width: 300px; }
    .rail { width: 210px; }
  }
</style>

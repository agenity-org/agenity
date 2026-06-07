<!--
  ════════════════════════════════════════════════════════════════════
  "board" — Orchestration Board dashboard for chepherd.
  An independent UX revamp. Agents are living cards on a board; click a
  card to focus its live terminal; arrange and resize terminal panes
  freely. A visual fleet-management metaphor.

  Self-contained under web/src/components/board/. Reuses the REAL data
  layer (same endpoints + xterm wiring + agentIdentity as the shipping
  dashboard) — no mockups.

  HARD REQUIREMENTS:
    1. Pane switching  — per-pane agent picker + click-a-card-to-focus.
    2. Pane resizing   — draggable split dividers (BoardPane).
    3. Layout flex     — arbitrary split tree (⬌/⬍) + close, persisted.
    4. All views       — board(roster) · terminals · transcript ·
                         settings/accounts · spawn.
    5. Real live data  — /api/v1/* polling + EventSource + PTY WebSocket.
    6. Per-agent color + role icon via lib/agentIdentity.js.
    7. Full dark + light themes via --board-* CSS vars, toggle, persisted,
       honoring prefers-color-scheme on first load.
  ════════════════════════════════════════════════════════════════════
-->
<script>
  import { onMount, onDestroy } from 'svelte';
  import { registerRoster } from '../../lib/agentIdentity.js';
  import AgentCard from './AgentCard.svelte';
  import BoardPane from './BoardPane.svelte';
  import BoardTranscript from './BoardTranscript.svelte';
  import BoardSpawn from './BoardSpawn.svelte';
  import BoardSettings from './BoardSettings.svelte';

  let { version = 'board' } = $props();

  // ── fetch-auth wrapper (idempotent; identical to AuthGate/Workspace) ──
  if (typeof window !== 'undefined' && !window.__chepherdFetchPatched) {
    window.__chepherdFetchPatched = true;
    const _origFetch = window.fetch.bind(window);
    window.fetch = (input, init) => {
      const url = typeof input === 'string' ? input : (input?.url || '');
      if (url.startsWith('/api/') || url.startsWith('/api-v')) {
        let tok = '';
        try { tok = localStorage.getItem('chepherd-token') || ''; } catch {}
        init = init || {};
        init.headers = new Headers(init.headers || (typeof input !== 'string' ? input.headers : undefined));
        if (tok && !init.headers.has('Authorization')) init.headers.set('Authorization', 'Bearer ' + tok);
        return _origFetch(input, init).then(r => {
          if (r.status === 401) { try { window.dispatchEvent(new CustomEvent('chepherd-401')); } catch {} }
          return r;
        });
      }
      return _origFetch(input, init);
    };
  }

  const API = '/api/v1';

  // ── auth state ──
  let authStatus = $state('checking');   // 'checking' | 'login' | 'ok'
  let tokenInput = $state('');
  let loginError = $state('');
  let probing = $state(false);

  function storedToken() { try { return localStorage.getItem('chepherd-token') || ''; } catch { return ''; } }
  function storeToken(t) { try { localStorage.setItem('chepherd-token', t); } catch {} }
  function clearToken() { try { localStorage.removeItem('chepherd-token'); } catch {} }

  async function probe(tok) {
    if (!tok) return false;
    try { const r = await fetch(`${API}/sessions`); return r.status === 200; } catch { return false; }
  }
  async function attemptLogin() {
    const t = tokenInput.trim();
    if (!t) { loginError = 'paste the bootstrap token'; return; }
    probing = true; loginError = '';
    storeToken(t);
    const okk = await probe(t);
    probing = false;
    if (okk) { authStatus = 'ok'; tokenInput = ''; startData(); }
    else { clearToken(); loginError = 'token rejected — paste a fresh one'; }
  }
  function logout() { clearToken(); authStatus = 'login'; }

  // ── live data ──
  let sessions = $state([]);
  let teams = $state([]);
  let memberships = $state([]);
  let events = $state([]);
  let inbox = $state([]);
  let poll = null;
  let evStream = null;
  let started = false;

  async function refresh() {
    try {
      const [s, t, m, ib, ev] = await Promise.all([
        fetch(`${API}/sessions`).then(r => r.json()).catch(() => ({})),
        fetch(`${API}/teams`).then(r => r.json()).catch(() => ({})),
        fetch(`${API}/memberships`).then(r => r.json()).catch(() => ({})),
        fetch(`${API}/inbox`).then(r => r.json()).catch(() => ({})),
        fetch(`${API}/events?limit=80`).then(r => r.json()).catch(() => ({})),
      ]);
      sessions = s.sessions || [];
      registerRoster([...sessions]
        .sort((a, b) => (a.created_at || '').localeCompare(b.created_at || '') || a.name.localeCompare(b.name))
        .map(x => x.name));
      teams = t.teams || [];
      memberships = m.memberships || [];
      inbox = ib.inbox || [];
      if (Array.isArray(ev.events)) events = ev.events;
      reconcileLayout();
    } catch {}
  }

  function startEventStream() {
    if (evStream) return;
    const tok = storedToken();
    const q = tok ? ('?token=' + encodeURIComponent(tok)) : '';
    try {
      evStream = new EventSource(`${API}/events/stream${q}`);
      evStream.onmessage = (e) => { try { events = [...events, JSON.parse(e.data)].slice(-200); } catch {} };
      evStream.onerror = () => { evStream?.close(); evStream = null; setTimeout(startEventStream, 3000); };
    } catch {}
  }

  function startData() {
    if (started) return;
    started = true;
    refresh();
    poll = setInterval(refresh, 2500);
    startEventStream();
  }

  // ── theme + font ──
  let theme = $state('dark');
  let fontSize = $state(14);

  function applyTheme(t) {
    theme = t === 'light' ? 'light' : 'dark';
    if (typeof document !== 'undefined') document.documentElement.dataset.theme = theme;
    try { localStorage.setItem('chepherd-theme', theme); } catch {}
  }
  function applyFont(n) {
    fontSize = Math.max(9, Math.min(22, n));
    if (typeof document !== 'undefined') {
      document.documentElement.style.setProperty('--ws-font', fontSize + 'px');
    }
    try { localStorage.setItem('chepherd-font', String(fontSize)); } catch {}
  }

  // ── view router ──
  let view = $state('board');         // 'board' | 'terminals' | 'transcript'
  let showSpawn = $state(false);
  let showSettings = $state(false);
  let search = $state('');

  // ── terminal pane layout (the flexible, resizable workspace) ──
  let layout = $state(null);
  let focusedPaneId = $state('');
  let nextId = 1;
  function genId() { return 'bp' + (Date.now().toString(36)) + (nextId++); }

  const LS_LAYOUT = 'board-layout';
  function loadLayout() {
    try {
      const raw = localStorage.getItem(LS_LAYOUT);
      if (raw) { const parsed = JSON.parse(raw); if (parsed && parsed.kind) { layout = parsed; return; } }
    } catch {}
    layout = { kind: 'pane', id: genId(), agent: '' };
  }
  function saveLayout() {
    try { localStorage.setItem(LS_LAYOUT, JSON.stringify(layout)); } catch {}
  }

  // Bind the first empty pane to the focused/first live agent so the
  // workspace is never blank on first open.
  function reconcileLayout() {
    if (!layout) return;
    const live = sessions.filter(s => !s.exited);
    let changed = false;
    const walk = (n) => {
      if (!n) return;
      if (n.kind === 'pane') {
        if (!n.agent && live.length) {
          const pick = (focusedAgent && live.find(s => s.name === focusedAgent)) ? focusedAgent
                     : (live.find(s => s.role !== 'shepherd') || live[0]).name;
          n.agent = pick; changed = true;
        }
        return;
      }
      walk(n.a); walk(n.b);
    };
    walk(layout);
    if (changed) layout = layout;
  }

  // tree ops
  function findFirstPane(n) {
    if (!n) return null;
    if (n.kind === 'pane') return n;
    return findFirstPane(n.a) || findFirstPane(n.b);
  }
  function findPane(n, id) {
    if (!n) return null;
    if (n.kind === 'pane') return n.id === id ? n : null;
    return findPane(n.a, id) || findPane(n.b, id);
  }
  function setPaneAgent(id, agent) {
    const p = findPane(layout, id);
    if (p) { p.agent = agent; layout = layout; saveLayout(); }
  }
  function splitPane(id, dir) {
    const split = (n) => {
      if (!n) return n;
      if (n.kind === 'pane' && n.id === id) {
        return { kind: dir, id: genId(), ratio: 0.5, a: n, b: { kind: 'pane', id: genId(), agent: '' } };
      }
      if (n.kind !== 'pane') return { ...n, a: split(n.a), b: split(n.b) };
      return n;
    };
    layout = split(layout);
    reconcileLayout();
    saveLayout();
  }
  function closePane(id) {
    const remove = (n) => {
      if (!n) return null;
      if (n.kind === 'pane') return n.id === id ? null : n;
      const a = remove(n.a), b = remove(n.b);
      if (!a) return b;
      if (!b) return a;
      return { ...n, a, b };
    };
    const next = remove(layout);
    layout = next || { kind: 'pane', id: genId(), agent: '' };
    if (!findPane(layout, focusedPaneId)) focusedPaneId = findFirstPane(layout)?.id || '';
    saveLayout();
  }

  // ── focus engine (the board's primary gesture) ──
  let focusedAgent = $state('');
  function focusAgent(name) {
    focusedAgent = name;
    if (!layout) loadLayout();
    // rebind the focused pane (or the first terminal pane) to this agent
    const target = findPane(layout, focusedPaneId) || findFirstPane(layout);
    if (target) { target.agent = name; focusedPaneId = target.id; layout = layout; saveLayout(); }
    if (view === 'board') view = 'terminals';
  }
  function openInNewPane(name) {
    if (!layout) loadLayout();
    const first = findFirstPane(layout);
    if (first && !first.agent) { first.agent = name; focusedPaneId = first.id; layout = layout; }
    else {
      const targetId = focusedPaneId || findFirstPane(layout)?.id;
      if (targetId) {
        splitPane(targetId, 'h');
        // bind the freshly created (second/empty) pane
        const empty = (function findEmpty(n) {
          if (!n) return null;
          if (n.kind === 'pane') return n.agent ? null : n;
          return findEmpty(n.a) || findEmpty(n.b);
        })(layout);
        if (empty) { empty.agent = name; focusedPaneId = empty.id; }
      }
    }
    focusedAgent = name;
    layout = layout;
    saveLayout();
    view = 'terminals';
  }

  async function agentAction(name, act) {
    let url, method, body = null;
    switch (act) {
      case 'pause': url = `${API}/sessions/${name}/pause`; method = 'PATCH'; body = JSON.stringify({ paused: true }); break;
      case 'unpause': url = `${API}/sessions/${name}/pause`; method = 'PATCH'; body = JSON.stringify({ paused: false }); break;
      case 'restart': url = `${API}/sessions/${name}/restart`; method = 'POST'; break;
      case 'stop': url = `${API}/sessions/${name}`; method = 'DELETE'; break;
      default: return;
    }
    try { await fetch(url, { method, headers: body ? { 'Content-Type': 'application/json' } : {}, body }); } catch {}
    refresh();
  }

  // ── derived roster grouped by team ──
  let filtered = $derived.by(() => {
    const q = search.trim().toLowerCase();
    return (sessions || []).filter(s => !q || s.name.toLowerCase().includes(q) || (s.role || '').toLowerCase().includes(q) || (s.team || '').toLowerCase().includes(q));
  });
  let groups = $derived.by(() => {
    const map = new Map();
    for (const s of filtered) {
      const k = s.team || '— no team —';
      if (!map.has(k)) map.set(k, []);
      map.get(k).push(s);
    }
    for (const arr of map.values()) arr.sort((a, b) => a.name.localeCompare(b.name));
    return [...map.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  });
  let liveCount = $derived(sessions.filter(s => !s.exited && s.paused !== true && s.live !== false).length);

  // ── mount ──
  onMount(async () => {
    // theme: stored → prefers-color-scheme → dark
    let t = '';
    try { t = localStorage.getItem('chepherd-theme') || ''; } catch {}
    if (t !== 'dark' && t !== 'light') {
      t = (typeof window !== 'undefined' && window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches) ? 'light' : 'dark';
    }
    applyTheme(t);
    let f = 14;
    try { f = +(localStorage.getItem('chepherd-font') || 14) || 14; } catch {}
    applyFont(f);

    loadLayout();

    window.addEventListener('chepherd-401', () => { clearToken(); authStatus = 'login'; });
    window.addEventListener('chepherd-logout', logout);

    // ingest ?token= then probe
    try {
      const urlTok = new URL(location.href).searchParams.get('token');
      if (urlTok) { storeToken(urlTok); const clean = new URL(location.href); clean.searchParams.delete('token'); history.replaceState(null, '', clean.toString()); }
    } catch {}
    const tok = storedToken();
    if (!tok) { authStatus = 'login'; return; }
    const okk = await probe(tok);
    if (okk) { authStatus = 'ok'; startData(); }
    else { clearToken(); authStatus = 'login'; }
  });

  onDestroy(() => { if (poll) clearInterval(poll); evStream?.close(); });
</script>

{#if authStatus === 'checking'}
  <div class="board-root center"><p class="loading">Loading the board…</p></div>
{:else if authStatus === 'login'}
  <div class="board-root center">
    <div class="login">
      <div class="login-brand"><span class="logo">▦</span> chepherd <span class="ver">board</span></div>
      <p class="login-msg">Paste the bootstrap token chepherd printed at startup.</p>
      <textarea bind:value={tokenInput} placeholder="eyJhbGc…" rows="4" spellcheck="false" autocomplete="off"></textarea>
      {#if loginError}<div class="login-err">{loginError}</div>{/if}
      <button class="login-go" onclick={attemptLogin} disabled={probing}>{probing ? 'Verifying…' : 'Sign in'}</button>
    </div>
  </div>
{:else}
  <div class="board-root">
    <!-- ── top bar ── -->
    <header class="topbar">
      <div class="brand"><span class="logo">▦</span> chepherd <span class="ver">{version}</span></div>

      <nav class="tabs">
        <button class:on={view === 'board'} onclick={() => view = 'board'}>▦ Board</button>
        <button class:on={view === 'terminals'} onclick={() => view = 'terminals'}>▢ Terminals</button>
        <button class:on={view === 'transcript'} onclick={() => view = 'transcript'}>💬 Conversation</button>
      </nav>

      <div class="fleet-stat" title="live / total agents">
        <span class="pip live"></span>{liveCount} live · {sessions.length} agents · {teams.length} teams
      </div>

      <span class="grow"></span>

      {#if view === 'board'}
        <input class="search" bind:value={search} placeholder="filter fleet…" aria-label="filter agents" />
      {/if}
      <button class="theme-btn" onclick={() => applyTheme(theme === 'dark' ? 'light' : 'dark')} title="toggle theme" aria-label="toggle theme">{theme === 'dark' ? '☀' : '🌙'}</button>
      <button class="spawn-btn" onclick={() => showSpawn = true}>+ Launch</button>
      <button class="icon-btn" onclick={() => showSettings = true} title="settings" aria-label="settings">⚙</button>
      <button class="icon-btn" onclick={logout} title="sign out" aria-label="sign out">⎋</button>
    </header>

    <!-- ── main region ── -->
    <main class="main">
      {#if view === 'board'}
        <div class="board-scroll">
          {#if !filtered.length}
            <div class="empty-fleet">
              <p class="ef-title">No agents on the board yet.</p>
              <button class="spawn-btn big" onclick={() => showSpawn = true}>+ Launch your first agent</button>
            </div>
          {:else}
            {#each groups as [teamName, members] (teamName)}
              <section class="team-section">
                <h2 class="team-h">
                  <span class="team-name">{teamName}</span>
                  <span class="team-count">{members.length}</span>
                </h2>
                <div class="card-grid">
                  {#each members as s (s.name)}
                    <AgentCard
                      session={s}
                      focused={focusedAgent === s.name}
                      onfocus={focusAgent}
                      onopen={openInNewPane}
                      onaction={agentAction}
                    />
                  {/each}
                </div>
              </section>
            {/each}
          {/if}
        </div>

      {:else if view === 'terminals'}
        <div class="terminals">
          <aside class="mini-rail">
            <div class="mr-head">Fleet</div>
            <div class="mr-list">
              {#each sessions.filter(s => !s.exited) as s (s.name)}
                <AgentCard
                  session={s}
                  focused={focusedAgent === s.name}
                  onfocus={focusAgent}
                  onopen={openInNewPane}
                  onaction={agentAction}
                />
              {/each}
            </div>
          </aside>
          <div class="pane-wrap">
            {#if layout}
              {#key layout.id}
                <BoardPane
                  node={layout}
                  {sessions}
                  {focusedPaneId}
                  onsplit={splitPane}
                  onclose={closePane}
                  onpick={setPaneAgent}
                  onfocuspane={(id) => focusedPaneId = id}
                  closable={layout.kind !== 'pane'}
                />
              {/key}
            {/if}
          </div>
        </div>

      {:else if view === 'transcript'}
        <div class="tx-wrap">
          <BoardTranscript {teams} />
        </div>
      {/if}
    </main>
  </div>

  {#if showSpawn}
    <BoardSpawn {teams} onclose={() => showSpawn = false} onspawned={refresh} />
  {/if}
  {#if showSettings}
    <BoardSettings
      {theme} {fontSize} {events}
      ontheme={applyTheme}
      onfont={applyFont}
      onclose={() => showSettings = false}
    />
  {/if}
{/if}

<style>
  /* ════════════ THEME TOKENS — both modes, deliberate ════════════ */
  :global(html[data-theme="dark"]) {
    --board-bg: #0a0d13;
    --board-bg-grad: radial-gradient(1200px 600px at 80% -10%, #131a26 0%, #0a0d13 60%);
    --board-surface: #11151d;
    --board-surface-2: #161b24;
    --board-term-bg: #0b0e14;
    --board-input: #0c1017;
    --board-fg: #eef1f5;
    --board-fg-muted: #9aa4b2;
    --board-fg-faint: #5d6675;
    --board-border: #1d232e;
    --board-border-strong: #2a323f;
    --board-accent: #5b9dff;
    --board-accent-fg: #06101f;
    --board-accent-bg: rgba(91,157,255,0.14);
    --board-accent-soft: rgba(91,157,255,0.4);
    --board-ok: #46d39a;
    --board-ok-bg: rgba(70,211,154,0.13);
    --board-warn: #f3b14e;
    --board-warn-bg: rgba(243,177,78,0.14);
    --board-danger: #ff6b6b;
    --board-danger-bg: rgba(255,107,107,0.12);
    --board-chip-bg: rgba(255,255,255,0.06);
    --board-hover: rgba(255,255,255,0.06);
    --board-shadow: rgba(0,0,0,0.55);
    --board-scrim: rgba(2,4,8,0.66);
  }
  :global(html[data-theme="light"]) {
    --board-bg: #f4f6fa;
    --board-bg-grad: radial-gradient(1200px 600px at 80% -10%, #e8eef9 0%, #f4f6fa 60%);
    --board-surface: #ffffff;
    --board-surface-2: #ffffff;
    --board-term-bg: #ffffff;
    --board-input: #ffffff;
    --board-fg: #15191f;
    --board-fg-muted: #56606e;
    --board-fg-faint: #95a0ad;
    --board-border: #e3e8ef;
    --board-border-strong: #cdd5df;
    --board-accent: #2f6bdb;
    --board-accent-fg: #ffffff;
    --board-accent-bg: rgba(47,107,219,0.10);
    --board-accent-soft: rgba(47,107,219,0.35);
    --board-ok: #119166;
    --board-ok-bg: rgba(17,145,102,0.10);
    --board-warn: #b9791a;
    --board-warn-bg: rgba(185,121,26,0.12);
    --board-danger: #d23b3b;
    --board-danger-bg: rgba(210,59,59,0.09);
    --board-chip-bg: rgba(15,23,42,0.06);
    --board-hover: rgba(15,23,42,0.05);
    --board-shadow: rgba(20,40,80,0.16);
    --board-scrim: rgba(20,30,50,0.32);
  }
  /* default before JS sets data-theme: dark */
  :global(html:not([data-theme])) {
    --board-bg: #0a0d13; --board-surface: #11151d; --board-fg: #eef1f5;
  }

  :global(*) { scrollbar-width: thin; scrollbar-color: var(--board-border-strong) transparent; }
  :global(*::-webkit-scrollbar) { width: 10px; height: 10px; }
  :global(*::-webkit-scrollbar-thumb) { background: var(--board-border-strong); border-radius: 10px; border: 2px solid var(--board-bg); }
  :global(*::-webkit-scrollbar-track) { background: transparent; }

  .board-root {
    position: fixed; inset: 0;
    display: flex; flex-direction: column;
    background: var(--board-bg);
    background-image: var(--board-bg-grad);
    color: var(--board-fg);
    font-family: ui-sans-serif, system-ui, -apple-system, sans-serif;
    font-size: var(--ws-font, 14px);
  }
  .board-root.center { align-items: center; justify-content: center; }
  .loading { color: var(--board-fg-muted); }

  /* ── login ── */
  .login {
    width: min(420px, 92vw); background: var(--board-surface);
    border: 1px solid var(--board-border-strong); border-radius: 16px;
    padding: 1.6rem 1.7rem; display: flex; flex-direction: column; gap: 0.8rem;
    box-shadow: 0 24px 60px var(--board-shadow);
  }
  .login-brand { font-size: 1.3rem; font-weight: 750; color: var(--board-fg); display: flex; align-items: center; gap: 0.4rem; }
  .login-brand .logo { color: var(--board-accent); }
  .login-brand .ver { font-size: 0.7rem; color: var(--board-fg-faint); font-weight: 500; align-self: center; }
  .login-msg { color: var(--board-fg-muted); font-size: 0.85rem; margin: 0; }
  .login textarea {
    background: var(--board-input); color: var(--board-fg);
    border: 1px solid var(--board-border-strong); border-radius: 10px;
    padding: 0.6rem 0.7rem; font-family: ui-monospace, monospace; font-size: 0.82rem; resize: vertical;
  }
  .login textarea:focus { outline: none; border-color: var(--board-accent); }
  .login-err { color: var(--board-danger); font-size: 0.82rem; background: var(--board-danger-bg); border-left: 3px solid var(--board-danger); padding: 0.4rem 0.6rem; border-radius: 4px; }
  .login-go { background: var(--board-accent); color: var(--board-accent-fg); border: 0; border-radius: 10px; padding: 0.6rem; font-weight: 700; cursor: pointer; font-size: 0.92rem; }
  .login-go:disabled { opacity: 0.6; cursor: progress; }

  /* ── topbar ── */
  .topbar {
    display: flex; align-items: center; gap: 0.7rem;
    padding: 0.6rem 1rem; flex: 0 0 auto;
    background: color-mix(in srgb, var(--board-surface) 88%, transparent);
    border-bottom: 1px solid var(--board-border);
    backdrop-filter: blur(8px);
  }
  .brand { font-weight: 750; font-size: 1rem; display: flex; align-items: center; gap: 0.35rem; }
  .brand .logo { color: var(--board-accent); }
  .brand .ver { font-size: 0.62rem; color: var(--board-fg-faint); font-weight: 500; align-self: center; }

  .tabs { display: flex; gap: 0.25rem; margin-left: 0.5rem; }
  .tabs button {
    background: transparent; border: 0; color: var(--board-fg-muted);
    border-radius: 8px; padding: 0.35rem 0.7rem; font-size: 0.82rem; cursor: pointer; font-weight: 550;
  }
  .tabs button:hover { background: var(--board-hover); color: var(--board-fg); }
  .tabs button.on { background: var(--board-accent-bg); color: var(--board-accent); }

  .fleet-stat { font-size: 0.74rem; color: var(--board-fg-muted); display: flex; align-items: center; gap: 0.4rem; white-space: nowrap; }
  .pip { width: 7px; height: 7px; border-radius: 50%; display: inline-block; }
  .pip.live { background: var(--board-ok); box-shadow: 0 0 0 3px var(--board-ok-bg); }
  .grow { flex: 1; }

  .search {
    background: var(--board-input); color: var(--board-fg);
    border: 1px solid var(--board-border-strong); border-radius: 999px;
    padding: 0.35rem 0.8rem; font-size: 0.8rem; width: 180px;
  }
  .search:focus { outline: none; border-color: var(--board-accent); width: 220px; }

  .theme-btn, .icon-btn {
    background: transparent; border: 1px solid var(--board-border-strong); color: var(--board-fg-muted);
    border-radius: 9px; width: 34px; height: 34px; cursor: pointer; font-size: 0.95rem;
    display: inline-flex; align-items: center; justify-content: center;
  }
  .theme-btn:hover, .icon-btn:hover { background: var(--board-hover); color: var(--board-fg); }
  .spawn-btn {
    background: var(--board-accent); color: var(--board-accent-fg); border: 0;
    border-radius: 9px; padding: 0.42rem 0.95rem; font-weight: 650; font-size: 0.84rem; cursor: pointer;
  }
  .spawn-btn:hover { filter: brightness(1.08); }
  .spawn-btn.big { padding: 0.6rem 1.3rem; font-size: 0.92rem; }

  /* ── main ── */
  .main { flex: 1; min-height: 0; display: flex; }

  /* board view */
  .board-scroll { flex: 1; overflow-y: auto; padding: 1.2rem 1.4rem 2rem; }
  .team-section { margin-bottom: 1.6rem; }
  .team-h { display: flex; align-items: center; gap: 0.5rem; margin: 0 0 0.7rem; }
  .team-name { font-size: 0.82rem; font-weight: 700; letter-spacing: 0.04em; text-transform: uppercase; color: var(--board-fg-muted); }
  .team-count { font-size: 0.7rem; color: var(--board-fg-faint); background: var(--board-chip-bg); border-radius: 999px; padding: 0.05rem 0.5rem; }
  .card-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 0.85rem; }

  .empty-fleet { margin: auto; text-align: center; display: flex; flex-direction: column; align-items: center; gap: 1rem; padding: 4rem 1rem; }
  .ef-title { color: var(--board-fg-muted); font-size: 1rem; }

  /* terminals view */
  .terminals { flex: 1; min-height: 0; display: flex; }
  .mini-rail { flex: 0 0 230px; min-height: 0; display: flex; flex-direction: column; border-right: 1px solid var(--board-border); background: color-mix(in srgb, var(--board-surface) 50%, transparent); }
  .mr-head { padding: 0.6rem 0.8rem 0.4rem; font-size: 0.72rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.05em; color: var(--board-fg-faint); }
  .mr-list { flex: 1; min-height: 0; overflow-y: auto; padding: 0 0.55rem 0.8rem; display: flex; flex-direction: column; gap: 0.55rem; }
  .pane-wrap { flex: 1; min-width: 0; min-height: 0; padding: 0.7rem; }
  .pane-wrap > :global(*) { height: 100%; }

  /* transcript view */
  .tx-wrap { flex: 1; min-height: 0; display: flex; }
  .tx-wrap > :global(*) { flex: 1; }
</style>

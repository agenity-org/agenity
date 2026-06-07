<!--
  ┌──────────────────────────────────────────────────────────────────┐
  │  mux — a terminal-native, tmux-style multiplexer for agents.       │
  │  Independent UX revamp of the chepherd dashboard. Codename "mux".  │
  └──────────────────────────────────────────────────────────────────┘

  DESIGN: keyboard-first tiling window manager. The terminals are the
  hero — minimal chrome, monospace everywhere, a single thin status
  line at the bottom (à la tmux). Panes split/resize via BOTH drag and
  the tmux prefix key (Ctrl+A then |  -  x  o  w …). The whole center is
  a recursive split tree — arbitrary layouts, every pane independently
  bound to any agent's live PTY.

  HARD REQUIREMENTS satisfied:
   1. PANE SWITCHING — every pane has its own agent binding; click a rail
      row (focus active pane) or use the ⇄ pane picker; multiple live
      terminals visible at once.
   2. PANE RESIZING — draggable dividers (MuxPane) AND Prefix-arrow to
      grow the focused split.
   3. LAYOUT FLEXIBILITY — recursive h/v split tree, persisted to
      /api/v1/workspaces; presets + free-form splits.
   4. ALL VIEWS — terminals workspace, sessions rail, transcript pane,
      settings (accounts/roles/canon/mesh), spawn wizard.
   5. REAL DATA — reuses WidgetTerminal (xterm/WS attach), TeamTranscript,
      SpawnWizardV9, SettingsPage + the live /api/v1 polling+SSE wire.
   6. IDENTITY COLORS/ICONS — agentIdentity.js everywhere.
   7. DARK + LIGHT — full CSS-variable theme set for both, toggle in the
      status line, persisted + prefers-color-scheme on first load.

  Self-contained: lives entirely under web/src/components/mux/.
-->
<script>
  import { onMount } from 'svelte';
  import '@xterm/xterm/css/xterm.css';
  import MuxPane from './MuxPane.svelte';
  import MuxRail from './MuxRail.svelte';
  import TeamTranscript from '../TeamTranscript.svelte';
  import SpawnWizardV9 from '../v09/SpawnWizardV9.svelte';
  import SettingsPage from '../v08/SettingsPage.svelte';
  import { registerRoster } from '../../lib/agentIdentity.js';

  let { version = 'v0.9.4' } = $props();

  // ── fetch-auth wrapper (identical contract to AuthGate/Workspace) ──
  // Installed at module-eval time so the first /api fetch carries the
  // bearer + a 401 fires chepherd-401. Idempotent across mount sites.
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
        return _origFetch(input, init).then((r) => {
          if (r.status === 401) { try { window.dispatchEvent(new CustomEvent('chepherd-401')); } catch {} }
          return r;
        });
      }
      return _origFetch(input, init);
    };
  }

  const API = '/api/v1';

  // ── live data ─────────────────────────────────────────────────────
  let sessions = $state([]);
  let teams = $state([]);
  let memberships = $state([]);
  let events = $state([]);
  let selectedAgent = $state(null);

  // ── layout tree (the splittable center) ───────────────────────────
  function leaf(widget = 'terminal', config = {}) {
    return { kind: 'pane', id: 'p' + Math.random().toString(36).slice(2, 9), widget, config };
  }
  let layout = $state(leaf('terminal', {}));
  let focusedPaneID = $state('');

  // ── chrome state ──────────────────────────────────────────────────
  let theme = $state('dark');
  let fontSize = $state(14);
  let showSpawn = $state(false);
  let showSettings = $state(false);
  let settingsHash = $state('accounts');
  let showHelp = $state(false);
  let railOpen = $state(true);

  // ── auth gate (token prompt on first load / 401) ──────────────────
  let authState = $state('checking'); // 'checking' | 'login' | 'ok'
  let tokenInput = $state('');
  let loginError = $state('');
  let probing = $state(false);
  async function probeToken() {
    try { const r = await fetch(`${API}/sessions`); return r.status === 200; } catch { return false; }
  }
  async function attemptLogin() {
    const t = tokenInput.trim();
    if (!t) { loginError = 'paste the bootstrap token'; return; }
    probing = true; loginError = '';
    try { localStorage.setItem('chepherd-token', t); } catch {}
    const ok = await probeToken();
    probing = false;
    if (ok) { authState = 'ok'; tokenInput = ''; bootData(); }
    else { try { localStorage.removeItem('chepherd-token'); } catch {} loginError = 'token rejected — paste a fresh one'; }
  }

  // tmux prefix key state (Ctrl+A, then a command key).
  let prefixArmed = $state(false);
  let prefixTimer = null;
  let toast = $state('');
  function flash(msg) {
    toast = msg;
    setTimeout(() => { if (toast === msg) toast = ''; }, 1400);
  }

  // ── tree walking ──────────────────────────────────────────────────
  function leafIDs(node, out = []) {
    if (!node) return out;
    if (node.kind === 'pane') { out.push(node.id); return out; }
    leafIDs(node.a, out); leafIDs(node.b, out);
    return out;
  }
  function findPane(node, id) {
    if (!node) return null;
    if (node.kind === 'pane') return node.id === id ? node : null;
    return findPane(node.a, id) || findPane(node.b, id);
  }
  function focusedOrFirst() {
    return findPane(layout, focusedPaneID) || findPane(layout, leafIDs(layout)[0] || '');
  }

  // ── pane operations (persist on explicit intent) ──────────────────
  function splitPane(id, dir) {
    const target = findPane(layout, id) || focusedOrFirst();
    if (!target) return;
    const tid = target.id;
    function rec(node) {
      if (!node) return node;
      if (node.kind === 'pane' && node.id === tid) {
        const np = leaf('terminal', {});
        focusedPaneID = np.id;
        return { kind: dir, ratio: 0.5, a: node, b: np };
      }
      if (node.kind === 'pane') return node;
      return { ...node, a: rec(node.a), b: rec(node.b) };
    }
    layout = rec(layout);
    saveLayout();
  }
  function closePane(id) {
    if (leafIDs(layout).length <= 1) { flash('last pane — cannot close'); return; }
    function rec(node) {
      if (!node) return null;
      if (node.kind === 'pane') return node.id === id ? null : node;
      const a = rec(node.a), b = rec(node.b);
      if (!a) return b;
      if (!b) return a;
      return { ...node, a, b };
    }
    layout = rec(layout) || leaf('terminal', {});
    if (!findPane(layout, focusedPaneID)) focusedPaneID = leafIDs(layout)[0] || '';
    saveLayout();
  }
  function setRatio(id, r) {
    function rec(node) {
      if (!node || node.kind === 'pane') return node;
      if (node.id === id) return { ...node, ratio: r };
      return { ...node, a: rec(node.a), b: rec(node.b) };
    }
    layout = rec(layout);
    saveRatioDebounced();
  }
  function setPaneWidget(id, widget) {
    function rec(node) {
      if (!node) return node;
      if (node.kind === 'pane' && node.id === id) {
        const config = widget === 'terminal' ? { agent: node.config?.agent || '' } : {};
        return { ...node, widget, config };
      }
      if (node.kind === 'pane') return node;
      return { ...node, a: rec(node.a), b: rec(node.b) };
    }
    layout = rec(layout);
    saveLayout();
  }
  function setPaneAgent(id, agent) {
    function rec(node) {
      if (!node) return node;
      if (node.kind === 'pane' && node.id === id) return { ...node, widget: 'terminal', config: { agent } };
      if (node.kind === 'pane') return node;
      return { ...node, a: rec(node.a), b: rec(node.b) };
    }
    layout = rec(layout);
    selectedAgent = agent;
    saveLayout();
  }
  // Give split ids so setRatio can target interior nodes.
  function ensureSplitIDs(node) {
    if (!node || node.kind === 'pane') return node;
    if (!node.id) node.id = 's' + Math.random().toString(36).slice(2, 9);
    ensureSplitIDs(node.a); ensureSplitIDs(node.b);
    return node;
  }

  // Focus engine — clicking a rail row / pane swaps the FOCUSED terminal
  // pane to that agent (a view change). New-pane requests split instead.
  function selectAgent(name, opts = {}) {
    selectedAgent = name;
    const sess = sessions.find((s) => s.name === name);
    if (sess && (sess.agent === 'external-a2a' || sess.external)) return; // peers: focus only
    if (opts.fromPane) return; // pane click already bound; just track focus
    // bind the focused terminal pane (or first terminal pane) to this agent
    const ids = leafIDs(layout);
    let target = findPane(layout, focusedPaneID);
    if (!target || target.widget !== 'terminal') {
      for (const id of ids) { const p = findPane(layout, id); if (p && p.widget === 'terminal') { target = p; break; } }
    }
    if (!target) return;
    focusedPaneID = target.id;
    setPaneAgent(target.id, name);
  }
  function openInNewPane(name) {
    const t = focusedOrFirst();
    if (!t) { setPaneAgent(layout.id, name); return; }
    splitPane(t.id, 'h');
    // focusedPaneID now points at the freshly-created pane
    setPaneAgent(focusedPaneID, name);
  }
  function focusPane(id) { focusedPaneID = id; }

  function cyclePane(dir) {
    const ids = leafIDs(layout);
    if (!ids.length) return;
    let i = ids.indexOf(focusedPaneID);
    if (i < 0) i = 0;
    focusedPaneID = ids[(i + dir + ids.length) % ids.length];
    queueMicrotask(() => {
      const el = document.querySelector(`[data-pane-id="${focusedPaneID}"]`);
      const tgt = el?.querySelector('.xterm-helper-textarea, textarea, input, button') || el;
      try { tgt?.focus?.(); } catch {}
    });
  }
  function growFocused(axis, amount) {
    // grow the focused pane's nearest enclosing split along `axis`.
    function subtreeHas(node, id) {
      if (!node) return false;
      if (node.kind === 'pane') return node.id === id;
      return subtreeHas(node.a, id) || subtreeHas(node.b, id);
    }
    let done = false;
    function rec(node) {
      if (!node || node.kind === 'pane') return node;
      if (!done && node.kind === axis) {
        const inA = subtreeHas(node.a, focusedPaneID);
        const inB = subtreeHas(node.b, focusedPaneID);
        if (inA || inB) {
          done = true;
          const r = Math.max(0.12, Math.min(0.88, (node.ratio ?? 0.5) + (inA ? amount : -amount)));
          return { ...node, ratio: r };
        }
      }
      return { ...node, a: rec(node.a), b: rec(node.b) };
    }
    const next = rec(layout);
    if (done) { layout = next; saveRatioDebounced(); }
  }

  // ── presets ───────────────────────────────────────────────────────
  function preset(name) {
    if (name === 'single') layout = leaf('terminal', {});
    else if (name === 'duo') layout = ensureSplitIDs({ kind: 'h', ratio: 0.5, a: leaf('terminal'), b: leaf('terminal') });
    else if (name === 'ide') layout = ensureSplitIDs({ kind: 'h', ratio: 0.62, a: leaf('terminal'), b: { kind: 'v', ratio: 0.5, a: leaf('inspector'), b: leaf('team-transcript', { team: 'default' }) } });
    else if (name === 'grid') layout = ensureSplitIDs({ kind: 'h', ratio: 0.5, a: { kind: 'v', ratio: 0.5, a: leaf('terminal'), b: leaf('terminal') }, b: { kind: 'v', ratio: 0.5, a: leaf('terminal'), b: leaf('events') } });
    else if (name === 'comms') layout = ensureSplitIDs({ kind: 'h', ratio: 0.55, a: leaf('terminal'), b: leaf('team-transcript', { team: 'all' }) });
    focusedPaneID = leafIDs(layout)[0] || '';
    saveLayout();
  }

  // ── workspace persistence ─────────────────────────────────────────
  let saveTimer = null;
  async function saveLayout() {
    try {
      await fetch(`${API}/workspaces/mux`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ layout }),
      });
    } catch {}
  }
  function saveRatioDebounced() {
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(saveLayout, 500);
  }
  async function loadLayout() {
    try {
      const r = await fetch(`${API}/workspaces/mux`);
      if (!r.ok) return;
      const d = await r.json();
      const tree = d?.layout || d;
      if (tree && tree.kind) { layout = ensureSplitIDs(tree); focusedPaneID = leafIDs(layout)[0] || ''; }
    } catch {}
  }

  // ── data refresh ──────────────────────────────────────────────────
  async function refresh() {
    try {
      const [s, t, m, ev] = await Promise.all([
        fetch(`${API}/sessions`).then((r) => r.json()),
        fetch(`${API}/teams`).then((r) => r.json()),
        fetch(`${API}/memberships`).then((r) => r.json()),
        fetch(`${API}/events?limit=80`).then((r) => r.json()),
      ]);
      sessions = s.sessions || [];
      registerRoster([...sessions]
        .sort((a, b) => (a.created_at || '').localeCompare(b.created_at || '') || a.name.localeCompare(b.name))
        .map((x) => x.name));
      teams = t.teams || [];
      memberships = m.memberships || [];
      events = ev.events || [];
      // keep focus valid
      if (selectedAgent && !sessions.find((s2) => s2.name === selectedAgent)) selectedAgent = null;
      if (!selectedAgent && sessions.length) {
        const w = sessions.find((s2) => !s2.exited && s2.role !== 'shepherd') || sessions.find((s2) => !s2.exited);
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

  // ── theme + font ──────────────────────────────────────────────────
  function applyTheme(t) {
    theme = t;
    document.documentElement.dataset.theme = t;
    try { localStorage.setItem('chepherd-theme', t); } catch {}
  }
  function toggleTheme() { applyTheme(theme === 'dark' ? 'light' : 'dark'); }
  function applyFont(n) {
    fontSize = Math.max(9, Math.min(22, n));
    document.documentElement.style.setProperty('--ws-font', fontSize + 'px');
    try { localStorage.setItem('chepherd-font', String(fontSize)); } catch {}
  }

  function openSettings(section) { settingsHash = section || 'accounts'; showSettings = true; try { location.hash = '#settings/' + (section || 'accounts'); } catch {} }

  // ── keyboard: tmux prefix (Ctrl+A) ────────────────────────────────
  const PREFIX_HINT = '|  split right · -  split down · x  close · o  next pane · z … grow · w  picker · n  new agent · t  transcript · g  settings · d  theme · ?  help';
  function onKey(e) {
    // global: Ctrl+B legacy + Ctrl+A prefix arm
    if (e.ctrlKey && !e.shiftKey && !e.altKey && (e.key === 'a' || e.key === 'A')) {
      e.preventDefault(); e.stopPropagation();
      prefixArmed = true;
      flash('PREFIX — ' + PREFIX_HINT);
      if (prefixTimer) clearTimeout(prefixTimer);
      prefixTimer = setTimeout(() => { prefixArmed = false; }, 2500);
      return;
    }
    if (e.key === '?' && !e.ctrlKey && !e.metaKey && !e.altKey) {
      const t = e.target;
      const isText = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable || t.closest?.('.xterm'));
      if (!isText) { e.preventDefault(); showHelp = !showHelp; return; }
    }
    if (e.key === 'Escape') {
      if (showHelp) { showHelp = false; return; }
      if (prefixArmed) { prefixArmed = false; return; }
    }
    if (!prefixArmed) return;

    // a prefixed command key
    const k = e.key.toLowerCase();
    let handled = true;
    switch (k) {
      case '|': case '\\': case '%': { const t = focusedOrFirst(); if (t) splitPane(t.id, 'h'); break; }
      case '-': case '"': { const t = focusedOrFirst(); if (t) splitPane(t.id, 'v'); break; }
      case 'x': { if (focusedPaneID) closePane(focusedPaneID); break; }
      case 'o': cyclePane(+1); break;
      case 'w': { const t = focusedOrFirst(); if (t) { focusedPaneID = t.id; document.querySelector(`[data-pane-id="${t.id}"] [aria-label="switch pane"]`)?.click?.(); } break; }
      case 'n': showSpawn = true; break;
      case 't': { const t = focusedOrFirst(); if (t) setPaneWidget(t.id, 'team-transcript'); break; }
      case 'g': openSettings('accounts'); break;
      case 'd': toggleTheme(); break;
      case 'r': railOpen = !railOpen; break;
      case 'arrowleft': growFocused('h', -0.06); break;
      case 'arrowright': growFocused('h', +0.06); break;
      case 'arrowup': growFocused('v', -0.06); break;
      case 'arrowdown': growFocused('v', +0.06); break;
      case '1': preset('single'); break;
      case '2': preset('duo'); break;
      case '3': preset('ide'); break;
      case '4': preset('grid'); break;
      case '5': preset('comms'); break;
      case '?': showHelp = true; break;
      default: handled = false;
    }
    if (handled) { e.preventDefault(); e.stopPropagation(); }
    prefixArmed = false;
    if (prefixTimer) clearTimeout(prefixTimer);
  }

  // ── data boot (after auth resolves) ───────────────────────────────
  let booted = false;
  let dataIntv = null;
  function bootData() {
    if (booted) return;
    booted = true;
    refresh();
    loadLayout();
    startEventStream();
    dataIntv = setInterval(refresh, 2500);
  }

  // ── mount ─────────────────────────────────────────────────────────
  onMount(() => {
    let t = 'dark';
    try {
      t = localStorage.getItem('chepherd-theme')
        || (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark');
    } catch {}
    applyTheme(t);
    try { applyFont(+(localStorage.getItem('chepherd-font') || 14)); } catch { applyFont(14); }

    // ingest ?token= from the URL (then strip it to avoid Referer leak).
    try {
      const urlTok = new URL(location.href).searchParams.get('token');
      if (urlTok) {
        localStorage.setItem('chepherd-token', urlTok);
        const clean = new URL(location.href);
        clean.searchParams.delete('token');
        history.replaceState(null, '', clean.toString());
      }
    } catch {}

    // auth probe before booting the data wire.
    (async () => {
      let stored = '';
      try { stored = localStorage.getItem('chepherd-token') || ''; } catch {}
      if (!stored) { authState = 'login'; return; }
      const ok = await probeToken();
      if (ok) { authState = 'ok'; bootData(); }
      else { try { localStorage.removeItem('chepherd-token'); } catch {} authState = 'login'; }
    })();

    const on401 = () => { authState = 'login'; };
    window.addEventListener('chepherd-401', on401);
    window.addEventListener('keydown', onKey, true);
    const onHash = () => {
      const h = location.hash || '';
      if (h.startsWith('#settings')) { settingsHash = h.split('/')[1] || 'accounts'; showSettings = true; }
    };
    window.addEventListener('hashchange', onHash);
    onHash();

    return () => {
      if (dataIntv) clearInterval(dataIntv);
      evStream?.close();
      window.removeEventListener('chepherd-401', on401);
      window.removeEventListener('keydown', onKey, true);
      window.removeEventListener('hashchange', onHash);
    };
  });

  // status-line counts
  let liveN = $derived(sessions.filter((s) => !s.exited && s.live !== false).length);
  let peerN = $derived(sessions.filter((s) => s.agent === 'external-a2a' || s.external).length);
</script>

{#if authState !== 'ok'}
  <div class="mux gate">
    {#if authState === 'checking'}
      <div class="gate-load">loading mux…</div>
    {:else}
      <div class="gate-card" role="dialog" aria-label="chepherd login">
        <h2><span class="brand">▚ mux</span> · login</h2>
        <p class="gate-p">Paste the bootstrap token chepherd printed at startup.</p>
        <p class="gate-p tiny">Read it from <code>$STATE_DIR/auth.printed</code> (the path passed to <code>--state-dir</code>).</p>
        <textarea bind:value={tokenInput} placeholder="eyJhbGc…" rows="4" spellcheck="false" autocomplete="off"></textarea>
        {#if loginError}<div class="gate-err" role="alert">{loginError}</div>{/if}
        <button class="gate-btn" onclick={attemptLogin} disabled={probing}>{probing ? 'verifying…' : 'sign in'}</button>
      </div>
    {/if}
  </div>
{:else}
<div class="mux">
  <!-- thin top brand strip (kept minimal — terminals are the hero) -->
  <header class="topstrip">
    <span class="brand">▚ mux<span class="ver">{version}</span></span>
    <span class="preset-row">
      <button class="ps" onclick={() => preset('single')} title="Prefix 1">single</button>
      <button class="ps" onclick={() => preset('duo')} title="Prefix 2">duo</button>
      <button class="ps" onclick={() => preset('ide')} title="Prefix 3">ide</button>
      <button class="ps" onclick={() => preset('grid')} title="Prefix 4">grid</button>
      <button class="ps" onclick={() => preset('comms')} title="Prefix 5">comms</button>
    </span>
    <span class="spacer"></span>
    <button class="tb" onclick={() => (railOpen = !railOpen)} title="toggle rail (Prefix r)">{railOpen ? '⯇ rail' : '⯈ rail'}</button>
    <button class="tb" onclick={() => (showSpawn = true)} title="spawn agent (Prefix n)">+ spawn</button>
    <button class="tb" onclick={() => openSettings('accounts')} title="settings (Prefix g)">⚙ settings</button>
    <button class="tb" onclick={() => (showHelp = true)} title="keybindings (?)">?</button>
    <button class="tb theme" onclick={toggleTheme} title="toggle theme (Prefix d)">{theme === 'dark' ? '☾ dark' : '☀ light'}</button>
  </header>

  <div class="frame">
    {#if railOpen}
      <MuxRail {sessions} {teams} {memberships} {selectedAgent}
               {selectAgent} {openInNewPane} spawn={() => (showSpawn = true)} />
    {/if}
    <main class="canvas">
      <MuxPane node={layout} {sessions} {memberships} {events} {selectedAgent} {focusedPaneID}
               {selectAgent} {focusPane} {splitPane} {closePane} {setPaneWidget} {setPaneAgent} {setRatio} />
    </main>
  </div>

  <!-- tmux status line -->
  <footer class="status {prefixArmed ? 'armed' : ''}">
    <span class="st-left">
      <span class="st-pill">⌥ C-a</span>
      <span class="st-sess">{liveN}/{sessions.length} live</span>
      {#if peerN}<span class="st-peer" onclick={() => openSettings('mesh')} role="button" tabindex="0" onkeydown={() => {}}>⇄ {peerN} peers</span>{/if}
      <span class="st-panes">{leafIDs(layout).length} pane{leafIDs(layout).length === 1 ? '' : 's'}</span>
    </span>
    <span class="st-mid">
      {#if toast}<span class="st-toast">{toast}</span>{:else if selectedAgent}<span class="st-focus">▸ {selectedAgent}</span>{:else}<span class="st-hint">prefix: Ctrl+A then a key · drag dividers to resize</span>{/if}
    </span>
    <span class="st-right">
      <button class="st-font" onclick={() => applyFont(fontSize - 1)} title="smaller">A-</button>
      <span class="st-fontn">{fontSize}</span>
      <button class="st-font" onclick={() => applyFont(fontSize + 1)} title="larger">A+</button>
      <span class="st-clock">{theme}</span>
    </span>
  </footer>

  {#if showHelp}
    <div class="modal-scrim" onmousedown={(e) => { if (e.target === e.currentTarget) showHelp = false; }} role="presentation">
      <div class="help">
        <header class="help-h"><h3>mux keybindings</h3><button class="x" onclick={() => (showHelp = false)}>✕</button></header>
        <p class="help-p">Press <kbd>Ctrl</kbd>+<kbd>A</kbd> (the prefix), then a command key:</p>
        <table class="help-t">
          <tbody>
            <tr><td><kbd>|</kbd></td><td>split pane right</td></tr>
            <tr><td><kbd>-</kbd></td><td>split pane down</td></tr>
            <tr><td><kbd>x</kbd></td><td>close focused pane</td></tr>
            <tr><td><kbd>o</kbd></td><td>cycle to next pane</td></tr>
            <tr><td><kbd>w</kbd></td><td>open pane switcher (agent / widget)</td></tr>
            <tr><td><kbd>←</kbd><kbd>→</kbd><kbd>↑</kbd><kbd>↓</kbd></td><td>grow focused pane</td></tr>
            <tr><td><kbd>n</kbd></td><td>spawn a new agent</td></tr>
            <tr><td><kbd>t</kbd></td><td>turn focused pane into transcript</td></tr>
            <tr><td><kbd>g</kbd></td><td>open settings</td></tr>
            <tr><td><kbd>d</kbd></td><td>toggle dark / light theme</td></tr>
            <tr><td><kbd>r</kbd></td><td>toggle the rail</td></tr>
            <tr><td><kbd>1</kbd>–<kbd>5</kbd></td><td>layout presets (single·duo·ide·grid·comms)</td></tr>
          </tbody>
        </table>
        <p class="help-p dim">Also: drag any divider to resize · click a rail row to focus it in the active pane · Alt-click a row to open it in a NEW pane · click the ⇄ in a pane header to rebind it.</p>
        <p class="help-p dim"><kbd>?</kbd> toggles this sheet · <kbd>Esc</kbd> closes overlays.</p>
      </div>
    </div>
  {/if}

  {#if showSpawn}
    <div class="modal-scrim" role="dialog">
      <SpawnWizardV9 onclose={() => { showSpawn = false; refresh(); }} />
    </div>
  {/if}

  {#if showSettings}
    <SettingsPage {teams} {events} onclose={() => { showSettings = false; try { if (location.hash.startsWith('#settings')) history.replaceState(null, '', location.pathname + location.search); } catch {} }} />
  {/if}
</div>
{/if}

<style>
  /* ───────── base theme tokens for REUSED legacy components ─────────
     SpawnWizardV9 / SettingsPage / TeamTranscript / WidgetTerminal read
     the v08 token set (--bg, --bg-elev, --fg-muted, --border-strong, …).
     Those are normally declared by Workspace.svelte's global theme blocks,
     which are NOT loaded on this route — so we declare the full set here
     (both modes) so every reused surface themes correctly. Kept in sync
     with Workspace.svelte's palette. -->  */
  :global(html[data-theme='dark']) {
    --bg: #0a0b0d; --bg-elev: #121417; --bg-elevated: #15181c; --bg-input: #0a0b0d;
    --border: #1f2329; --border-strong: #2c323a;
    --fg: #e8eaed; --fg-muted: #9aa1ab; --fg-faint: #5e656e;
    --accent: #ffb454; --accent-2: #6cc7ff;
    --danger: #ff6b6b; --err: #ff6b6b; --success: #5ad17e; --ok: #5ad17e; --warn: #f0b429;
    --btn-bg: #15181c; --btn-hover: #1f2329;
    --select-bg: #0d1f3c; --select-border: #1769b5;
    --scrollbar-track: transparent; --scrollbar-thumb: #2c323a; --scrollbar-thumb-hover: #5e656e;
  }
  :global(html[data-theme='light']) {
    --bg: #f6f7f9; --bg-elev: #ffffff; --bg-elevated: #ffffff; --bg-input: #ffffff;
    --border: #dde2e9; --border-strong: #c4ccd6;
    --fg: #1b1f24; --fg-muted: #5a626c; --fg-faint: #97a0ab;
    --accent: #c2700a; --accent-2: #1769b5;
    --danger: #d6453f; --err: #d6453f; --success: #1f9d4f; --ok: #1f9d4f; --warn: #b07d06;
    --btn-bg: #eef1f5; --btn-hover: #e2e7ee;
    --select-bg: #e0f2fe; --select-border: #0057c7;
    --scrollbar-track: transparent; --scrollbar-thumb: #c4ccd6; --scrollbar-thumb-hover: #97a0ab;
  }
  :global(html, body) { margin: 0; padding: 0; background: var(--bg, #0a0b0d); }

  /* ───────── theme tokens — full coverage, BOTH modes ───────── */
  .mux {
    /* dark (default) */
    --mux-bg: #0a0b0d;
    --mux-bar: #121417;
    --mux-bar-2: #15181c;
    --mux-fg: #e8eaed;
    --mux-fg-muted: #9aa1ab;
    --mux-fg-faint: #5e656e;
    --mux-border: #1f2329;
    --mux-border-strong: #2c323a;
    --mux-border-faint: #15181c;
    --mux-accent: #ffb454;           /* amber — the chepherd-orange family, terminal-warm */
    --mux-accent-soft: rgba(255,180,84,0.12);
    --mux-on-accent: #1a1205;
    --mux-accent-2: #6cc7ff;
    --mux-accent-2-soft: rgba(108,199,255,0.14);
    --mux-ok: #5ad17e;
    --mux-ok-soft: rgba(90,209,126,0.14);
    --mux-warn: #f0b429;
    --mux-warn-soft: rgba(240,180,41,0.14);
    --mux-danger: #ff6b6b;
    --mux-danger-soft: rgba(255,107,107,0.14);
    --mux-hover: rgba(255,255,255,0.05);
    --mux-sel: rgba(255,180,84,0.14);
    --mux-scrim: rgba(0,0,0,0.55);
    --mux-shadow: 0 10px 34px rgba(0,0,0,0.55);
    --mux-mono: ui-monospace, "JetBrains Mono", "SFMono-Regular", Menlo, Consolas, monospace;

    position: fixed; inset: 0;
    display: flex; flex-direction: column;
    background: var(--mux-bg); color: var(--mux-fg);
    font-family: var(--mux-mono);
    overflow: hidden;
  }
  /* light mode — deliberately designed, paper-terminal feel */
  :global(html[data-theme='light']) .mux {
    --mux-bg: #f6f7f9;
    --mux-bar: #ffffff;
    --mux-bar-2: #eef1f5;
    --mux-fg: #1b1f24;
    --mux-fg-muted: #5a626c;
    --mux-fg-faint: #97a0ab;
    --mux-border: #dde2e9;
    --mux-border-strong: #c4ccd6;
    --mux-border-faint: #eceff3;
    --mux-accent: #c2700a;           /* darkened amber for contrast on light */
    --mux-accent-soft: rgba(194,112,10,0.10);
    --mux-on-accent: #ffffff;
    --mux-accent-2: #1769b5;
    --mux-accent-2-soft: rgba(23,105,181,0.10);
    --mux-ok: #1f9d4f;
    --mux-ok-soft: rgba(31,157,79,0.12);
    --mux-warn: #b07d06;
    --mux-warn-soft: rgba(176,125,6,0.12);
    --mux-danger: #d6453f;
    --mux-danger-soft: rgba(214,69,63,0.10);
    --mux-hover: rgba(0,0,0,0.045);
    --mux-sel: rgba(194,112,10,0.12);
    --mux-scrim: rgba(20,24,30,0.35);
    --mux-shadow: 0 10px 34px rgba(30,40,60,0.18);
  }

  /* auth gate */
  .mux.gate { align-items: center; justify-content: center; }
  .gate-load { color: var(--mux-fg-muted); font-size: 0.9rem; }
  .gate-card { width: min(94%, 28rem); background: var(--mux-bar); border: 1px solid var(--mux-border-strong); border-radius: 10px; padding: 1.4rem 1.6rem; box-shadow: var(--mux-shadow); display: flex; flex-direction: column; gap: 0.7rem; }
  .gate-card h2 { margin: 0; font-size: 1.05rem; color: var(--mux-fg); font-weight: 600; }
  .gate-card .brand { color: var(--mux-accent); }
  .gate-p { color: var(--mux-fg-muted); font-size: 0.85rem; margin: 0; line-height: 1.5; }
  .gate-p.tiny { font-size: 0.76rem; color: var(--mux-fg-faint); }
  .gate-card code { background: var(--mux-bg); border: 1px solid var(--mux-border); padding: 0.05rem 0.3rem; border-radius: 3px; font-size: 0.74rem; word-break: break-all; }
  .gate-card textarea { width: 100%; box-sizing: border-box; background: var(--mux-bg); color: var(--mux-fg); border: 1px solid var(--mux-border); border-radius: 6px; font-family: var(--mux-mono); font-size: 0.82rem; padding: 0.5rem 0.6rem; resize: vertical; }
  .gate-card textarea:focus { outline: none; border-color: var(--mux-accent); }
  .gate-err { color: var(--mux-danger); font-size: 0.8rem; background: var(--mux-danger-soft); border-left: 3px solid var(--mux-danger); padding: 0.4rem 0.6rem; border-radius: 3px; }
  .gate-btn { background: var(--mux-accent); color: var(--mux-on-accent); border: none; border-radius: 6px; padding: 0.55rem 1rem; font-family: var(--mux-mono); font-weight: 700; font-size: 0.88rem; cursor: pointer; }
  .gate-btn:hover:not(:disabled) { filter: brightness(1.08); }
  .gate-btn:disabled { opacity: 0.6; cursor: progress; }

  /* top strip */
  .topstrip {
    display: flex; align-items: center; gap: 0.6rem;
    padding: 0.3rem 0.7rem; height: 2rem; flex: 0 0 auto;
    background: var(--mux-bar); border-bottom: 1px solid var(--mux-border);
    font-size: 0.76rem;
  }
  .brand { color: var(--mux-accent); font-weight: 700; letter-spacing: 0.02em; }
  .brand .ver { color: var(--mux-fg-faint); font-weight: 500; margin-left: 0.4rem; font-size: 0.68rem; }
  .preset-row { display: inline-flex; gap: 0.15rem; margin-left: 0.4rem; }
  .ps { background: transparent; border: 1px solid transparent; color: var(--mux-fg-muted); cursor: pointer; font-family: var(--mux-mono); font-size: 0.7rem; padding: 0.15rem 0.45rem; border-radius: 5px; }
  .ps:hover { color: var(--mux-fg); background: var(--mux-hover); border-color: var(--mux-border); }
  .spacer { flex: 1; }
  .tb { background: transparent; border: 1px solid var(--mux-border); color: var(--mux-fg-muted); cursor: pointer; font-family: var(--mux-mono); font-size: 0.72rem; padding: 0.18rem 0.5rem; border-radius: 5px; }
  .tb:hover { color: var(--mux-fg); border-color: var(--mux-border-strong); background: var(--mux-hover); }
  .tb.theme { color: var(--mux-accent); border-color: var(--mux-accent-soft); }

  .frame { flex: 1; display: flex; min-height: 0; }
  .frame :global(aside.rail) { width: 232px; flex: 0 0 auto; }
  .canvas { flex: 1; min-width: 0; padding: 4px; overflow: hidden; }

  /* status line — the tmux signature */
  .status {
    display: flex; align-items: center; height: 1.7rem; flex: 0 0 auto;
    background: var(--mux-bar-2); border-top: 1px solid var(--mux-border);
    font-size: 0.71rem; padding: 0 0.5rem; gap: 0.6rem;
  }
  .status.armed { background: var(--mux-accent); color: var(--mux-on-accent); }
  .status.armed .st-pill, .status.armed .st-sess, .status.armed .st-panes,
  .status.armed .st-hint, .status.armed .st-focus, .status.armed .st-fontn,
  .status.armed .st-clock { color: var(--mux-on-accent); }
  .status.armed .st-font { color: var(--mux-on-accent); border-color: var(--mux-on-accent); }
  .st-left, .st-right { display: inline-flex; align-items: center; gap: 0.5rem; flex: 0 0 auto; }
  .st-mid { flex: 1; text-align: center; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .st-pill { background: var(--mux-accent); color: var(--mux-on-accent); font-weight: 700; padding: 0.05rem 0.4rem; border-radius: 4px; }
  .status.armed .st-pill { background: var(--mux-on-accent); color: var(--mux-accent); }
  .st-sess { color: var(--mux-ok); }
  .st-peer { color: var(--mux-accent-2); cursor: pointer; }
  .st-peer:hover { text-decoration: underline; }
  .st-panes { color: var(--mux-fg-muted); }
  .st-toast { color: var(--mux-accent); font-weight: 600; }
  .st-focus { color: var(--mux-fg); }
  .st-hint { color: var(--mux-fg-faint); }
  .st-font { background: transparent; border: 1px solid var(--mux-border); color: var(--mux-fg-muted); cursor: pointer; font-family: var(--mux-mono); font-size: 0.64rem; padding: 0 0.3rem; border-radius: 4px; line-height: 1.3; }
  .st-font:hover { color: var(--mux-accent); }
  .st-fontn { color: var(--mux-fg-muted); min-width: 1.1rem; text-align: center; }
  .st-clock { color: var(--mux-fg-faint); text-transform: uppercase; letter-spacing: 0.08em; font-size: 0.64rem; }

  /* modals */
  .modal-scrim { position: fixed; inset: 0; background: var(--mux-scrim); display: flex; align-items: center; justify-content: center; z-index: 500; padding: 1rem; }
  .help { width: min(94%, 30rem); background: var(--mux-bar); border: 1px solid var(--mux-border-strong); border-radius: 10px; padding: 1rem 1.2rem; box-shadow: var(--mux-shadow); }
  .help-h { display: flex; align-items: center; margin-bottom: 0.6rem; }
  .help-h h3 { margin: 0; flex: 1; font-size: 0.95rem; color: var(--mux-fg); }
  .help-h .x { background: transparent; border: none; color: var(--mux-fg-faint); cursor: pointer; font-size: 0.95rem; }
  .help-h .x:hover { color: var(--mux-danger); }
  .help-p { color: var(--mux-fg-muted); font-size: 0.8rem; margin: 0.5rem 0; line-height: 1.5; }
  .help-p.dim { color: var(--mux-fg-faint); font-size: 0.74rem; }
  .help-t { width: 100%; border-collapse: collapse; font-size: 0.8rem; }
  .help-t td { padding: 0.22rem 0.4rem; vertical-align: top; }
  .help-t td:first-child { width: 6.5rem; white-space: nowrap; }
  .help-t td:last-child { color: var(--mux-fg-muted); }
  kbd { font-family: var(--mux-mono); background: var(--mux-bg); border: 1px solid var(--mux-border-strong); border-bottom-width: 2px; border-radius: 4px; padding: 0.05rem 0.35rem; font-size: 0.72rem; color: var(--mux-fg); margin: 0 0.05rem; }

  /* scrollbars in both themes */
  .mux :global(*) { scrollbar-width: thin; scrollbar-color: var(--mux-border-strong) transparent; }
  .mux :global(*::-webkit-scrollbar) { width: 9px; height: 9px; }
  .mux :global(*::-webkit-scrollbar-thumb) { background: var(--mux-border-strong); border-radius: 8px; border: 2px solid var(--mux-bg); }
  .mux :global(*::-webkit-scrollbar-thumb:hover) { background: var(--mux-fg-faint); }
  .mux :global(*::-webkit-scrollbar-track) { background: transparent; }
</style>

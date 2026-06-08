<!--
  Dashboardcalm — root of the "calm" dashboard. A spacious, focus-first
  reimagining of the chepherd workspace.

  Design — CALM FOCUS:
    · One primary focus stage holds the split-tree of panes; every pane
      can maximize to fill the stage and collapse along its split axis.
    · A prominent, full-width Team Transcript pane sits BELOW the stage,
      visible by default and resizable via a horizontal splitter.
    · The rail (left) lists agents by team in their identity colors.
    · The context column (right) carries the Inspector and shares its
      width with the left rail for a symmetric workspace.
    · A top-right account menu signs out (clears the stored token).
    · Light & dark are both first-class, toggled in the topbar, persisted,
      and seeded from prefers-color-scheme on first load.

  Reuses the existing data layer + live terminal:
    · /api/v1/{sessions,teams,memberships,inbox,events} polled @2.5s
    · /api/v1/events/stream  (EventSource)
    · WidgetTerminal.svelte  → live xterm over /api-v08/.../attach
    · agentIdentity.js, SpawnWizardV9.svelte, TeamTranscript.svelte

  This component owns ALL calm theme tokens (--calm-*) so it is fully
  self-contained and never collides with the v08 --bg/--fg tokens. It
  ALSO sets html[data-theme] (dark|light) so the reused WidgetTerminal
  picks the right xterm palette.
-->
<script>
  import { onMount } from 'svelte';
  import '@xterm/xterm/css/xterm.css';
  import CalmPane from './CalmPane.svelte';
  import CalmRail from './CalmRail.svelte';
  import CalmInspector from './CalmInspector.svelte';
  import CalmSettings from './CalmSettings.svelte';
  import TeamTranscript from '../TeamTranscript.svelte';
  import SpawnWizardV9 from '../v09/SpawnWizardV9.svelte';
  import { registerRoster } from '../../lib/agentIdentity.js';
  import * as T from './layoutTree.js';

  let { version = 'calm' } = $props();

  // ---- fetch-auth wrapper (idempotent; matches AuthGate/Workspace) ----
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

  // ---- data state ----
  let sessions = $state([]);
  let teams = $state([]);
  let memberships = $state([]);
  let events = $state([]);
  let selectedAgent = $state('');
  let mruFocus = $state([]);

  // ---- layout (calm split-tree of the focus stage) ----
  let layout = $state(T.defaultLayout());
  let focusedLeafId = $state('');
  // When set, the stage renders ONLY this leaf, full-screen (#5).
  let maximizedLeafId = $state('');

  // ---- chrome / overlays ----
  let theme = $state('dark');
  let fontSize = $state(14);
  let railOpen = $state(true);
  let contextOpen = $state(true);
  let showSettings = $state(false);
  let showWizard = $state(false);
  let needLogin = $state(false);
  let notice = $state('');
  let userMenuOpen = $state(false);
  // Transcript pane below the stage (#11). Visible by default, generous
  // default height (~22% of the stage), resizable via a drag splitter.
  let transcriptOpen = $state(true);
  let transcriptPct = $state(22);
  let transcriptDragging = $state(false);
  // SSR/hydration guard: this island is entirely client-driven (reads
  // localStorage, opens WebSockets, mounts xterm). Render a stable, tiny
  // placeholder during SSR + first paint, then render the real UI only
  // after onMount so the hydration walker has nothing complex to
  // reconcile. Mirrors AuthGate's 'checking' phase.
  let mounted = $state(false);

  function flash(msg) { notice = msg; setTimeout(() => { if (notice === msg) notice = ''; }, 2600); }

  // ---------------- theme ----------------
  function applyTheme(t) {
    theme = t === 'light' ? 'light' : 'dark';
    try { document.documentElement.dataset.theme = theme; } catch {}
    try { localStorage.setItem('chepherd-theme', theme); } catch {}
  }
  function toggleTheme() { applyTheme(theme === 'dark' ? 'light' : 'dark'); }

  function applyFont(delta) {
    fontSize = Math.max(9, Math.min(22, fontSize + delta));
    try {
      document.documentElement.style.setProperty('--ws-font', fontSize + 'px');
      localStorage.setItem('chepherd-font', String(fontSize));
    } catch {}
  }

  // ---------------- data ----------------
  function getToken() {
    try { return localStorage.getItem('chepherd-token') || ''; } catch { return ''; }
  }

  function pushMRU(name) {
    if (!name) return;
    mruFocus = [name, ...mruFocus.filter((n) => n !== name)].slice(0, 12);
  }

  async function refresh() {
    try {
      const [s, t, m, ev] = await Promise.all([
        fetch(`${API}/sessions`).then((r) => r.json()),
        fetch(`${API}/teams`).then((r) => r.json()),
        fetch(`${API}/memberships`).then((r) => r.json()),
        fetch(`${API}/events?limit=80`).then((r) => r.json()),
      ]);
      // A successful poll means the daemon + token are healthy again, so let a
      // previously-capped SSE reconnect (transient-outage self-heal). The
      // backoff + the needLogin/token guard in startEventStream prevent this
      // from reintroducing a tight reconnect loop.
      evRetries = 0;
      sessions = s.sessions || [];
      registerRoster(
        [...sessions]
          .sort((a, b) => (a.created_at || '').localeCompare(b.created_at || '') || a.name.localeCompare(b.name))
          .map((x) => x.name)
      );
      teams = t.teams || [];
      memberships = m.memberships || [];
      events = ev.events || [];

      // Focus fallback when the selected agent vanishes.
      if (selectedAgent && !sessions.find((x) => x.name === selectedAgent)) {
        const fb = mruFocus.find((n) => n !== selectedAgent && sessions.find((x) => x.name === n && !x.exited));
        selectedAgent = fb || '';
      }
      // Auto-pick a first agent so the focus terminal renders real content.
      if (!selectedAgent && sessions.length) {
        const w = sessions.find((x) => !x.exited && x.role !== 'shepherd') || sessions.find((x) => !x.exited);
        if (w) { selectedAgent = w.name; bindFocusedTerminal(w.name, false); }
      }
    } catch {}
  }

  let evStream = null;
  let evRetries = 0;
  let evRetryTimer = null;   // queued bounded-retry setTimeout id (so we can cancel it)
  function startEventStream() {
    // No-op while signed out: a queued retry must NOT revive the stream after
    // sign-out/unmount (its `if (evStream) return` guard is null then, so it
    // would re-open and reflood). needLogin / no-token short-circuits that.
    if (needLogin || !getToken()) return;
    if (evStream) return;
    const tok = getToken();
    const q = tok ? '?token=' + encodeURIComponent(tok) : '';
    try {
      evStream = new EventSource(`${API}/events/stream${q}`);
      evStream.onopen = () => { evRetries = 0; };
      evStream.onmessage = (e) => { try { events = [...events, JSON.parse(e.data)].slice(-200); } catch {} };
      // BOUNDED backoff: the 2.5s poll already refreshes events, so a
      // misbehaving/expired-token SSE endpoint must NOT infinite-reconnect
      // (was flooding the console + hammering the daemon). Cap at 4 tries. The
      // retry timer id is stored so cleanup()/signOut() can clearTimeout it
      // (else a queued retry fires after sign-out + re-opens the stream).
      evStream.onerror = () => {
        evStream?.close(); evStream = null;
        if (evRetries < 4) { evRetries += 1; evRetryTimer = setTimeout(startEventStream, Math.min(30000, 3000 * evRetries)); }
      };
    } catch {}
  }

  // ---------------- focus engine ----------------
  // The "focused" leaf is the active pane. Picking an agent in the rail
  // rebinds the focused TERMINAL leaf to that agent (HARD REQ #1, view
  // change). If the focused leaf isn't a terminal, fall back to the
  // first terminal leaf, else open one.
  function firstTerminalLeafId() {
    const tl = T.leaves(layout).find((l) => l.widget === 'terminal');
    return tl ? tl.id : '';
  }
  function ensureFocusedLeaf() {
    const all = T.leaves(layout);
    if (!all.find((l) => l.id === focusedLeafId)) focusedLeafId = all[0]?.id || '';
  }

  function selectAgent(name) {
    selectedAgent = name;
    pushMRU(name);
    bindFocusedTerminal(name, true);
  }

  function bindFocusedTerminal(name, flashIt) {
    ensureFocusedLeaf();
    let target = T.findLeaf(layout, focusedLeafId);
    if (!target || target.widget !== 'terminal') {
      const tid = firstTerminalLeafId();
      if (tid) { focusedLeafId = tid; target = T.findLeaf(layout, tid); }
    }
    if (target && target.widget === 'terminal') {
      layout = T.setLeafConfig(layout, target.id, { agent: name });
      if (flashIt) flash(`Focus → ${name}`);
    } else {
      // No terminal leaf at all — convert the focused leaf into one.
      if (focusedLeafId) {
        layout = T.setLeafWidget(layout, focusedLeafId, 'terminal', { agent: name });
      }
    }
  }

  // Open an agent in a brand-new terminal pane beside the focused one.
  function openAgentInNewPane(name) {
    ensureFocusedLeaf();
    const base = focusedLeafId || T.leaves(layout)[0]?.id;
    if (!base) {
      layout = T.leaf('terminal', { agent: name });
      focusedLeafId = T.leaves(layout)[0].id;
    } else {
      const { tree, newId } = T.splitLeaf(layout, base, 'h', 'terminal', { agent: name });
      layout = tree;
      focusedLeafId = newId;
    }
    selectedAgent = name;
    pushMRU(name);
    flash(`Opened ${name} in a new pane`);
  }

  // ---------------- pane operations (passed to CalmPane) ----------------
  function onFocusLeaf(id) {
    focusedLeafId = id;
    const leaf = T.findLeaf(layout, id);
    if (leaf?.widget === 'terminal' && leaf.config?.agent) {
      selectedAgent = leaf.config.agent;
      pushMRU(selectedAgent);
    }
  }
  function onSetRatio(splitId, ratio) { layout = T.setSplitRatio(layout, splitId, ratio); }
  function onSplit(leafId, dir) {
    const cur = T.findLeaf(layout, leafId);
    const cfg = cur?.widget === 'terminal' ? { agent: cur.config?.agent || selectedAgent } : {};
    const { tree, newId } = T.splitLeaf(layout, leafId, dir, 'terminal', cfg);
    layout = tree;
    focusedLeafId = newId;
  }
  function onCloseLeaf(leafId) {
    if (T.countLeaves(layout) <= 1) { flash('At least one pane stays open'); return; }
    if (maximizedLeafId === leafId) maximizedLeafId = '';
    layout = T.removeLeaf(layout, leafId);
    if (!T.findLeaf(layout, focusedLeafId)) focusedLeafId = T.leaves(layout)[0]?.id || '';
  }
  function onSetAgent(leafId, name) {
    layout = T.setLeafConfig(layout, leafId, { agent: name });
    focusedLeafId = leafId;
    selectedAgent = name;
    pushMRU(name);
  }
  function onSetWidget(leafId, widget) {
    const cfg = widget === 'terminal' ? { agent: selectedAgent } : {};
    layout = T.setLeafWidget(layout, leafId, widget, cfg);
    focusedLeafId = leafId;
  }

  // Maximize / restore a single pane to fill the whole stage (#5).
  function onMaximize(leafId) {
    maximizedLeafId = maximizedLeafId === leafId ? '' : leafId;
    if (maximizedLeafId) { focusedLeafId = leafId; }
  }

  // Collapse / expand a pane along its parent split axis via the chevron
  // arrows in the leaf header (#6). Toggles the parent split's ratio to
  // an extreme and back.
  function onCollapse(leafId) {
    const p = T.parentOf(layout, leafId);
    if (!p) { flash('Single pane — nothing to collapse'); return; }
    const small = T.collapsedSide(p.split); // 'a' | 'b' | ''
    const isCollapsed = small === p.side;
    layout = T.collapseLeaf(layout, leafId, !isCollapsed);
  }

  // Add a fresh terminal pane (top-bar +Pane and peek-strip "＋").
  function addPane() {
    ensureFocusedLeaf();
    const base = focusedLeafId || T.leaves(layout)[0]?.id;
    const agent = selectedAgent || (sessions.find((s) => !s.exited)?.name || '');
    if (!base) {
      layout = T.leaf('terminal', { agent });
    } else {
      const { tree, newId } = T.splitLeaf(layout, base, 'h', 'terminal', { agent });
      layout = tree; focusedLeafId = newId;
    }
  }

  let focusedSession = $derived(sessions.find((s) => s.name === selectedAgent) || null);

  // ---------------- login ----------------
  let loginInput = $state('');
  let loginError = $state('');
  async function doLogin() {
    const t = loginInput.trim();
    if (!t) { loginError = 'paste the bootstrap token'; return; }
    try { localStorage.setItem('chepherd-token', t); } catch {}
    try {
      const r = await fetch(`${API}/sessions`);
      if (r.status === 401) { loginError = 'token rejected'; return; }
    } catch (e) { loginError = String(e); return; }
    needLogin = false; loginError = ''; loginInput = '';
    refresh(); startEventStream();
  }

  // ---------------- mount ----------------
  onMount(() => {
    mounted = true;
    // theme: stored → prefers-color-scheme → dark
    let t = '';
    try { t = localStorage.getItem('chepherd-theme') || ''; } catch {}
    if (t !== 'light' && t !== 'dark') {
      try { t = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'; } catch { t = 'dark'; }
    }
    applyTheme(t);

    let f = 14;
    try { f = +(localStorage.getItem('chepherd-font') || 14) || 14; } catch {}
    fontSize = Math.max(9, Math.min(22, f));
    try { document.documentElement.style.setProperty('--ws-font', fontSize + 'px'); } catch {}

    try { railOpen = localStorage.getItem('calm-rail') !== '0'; } catch {}
    try { contextOpen = localStorage.getItem('calm-context') !== '0'; } catch {}
    try { transcriptOpen = localStorage.getItem('calm-transcript') !== '0'; } catch {}
    try {
      const tp = +(localStorage.getItem('calm-transcript-pct') || 22);
      if (tp >= 10 && tp <= 60) transcriptPct = tp;
    } catch {}

    // ?token= ingest (mirror AuthGate).
    try {
      const urlTok = new URL(location.href).searchParams.get('token');
      if (urlTok) {
        localStorage.setItem('chepherd-token', urlTok);
        const clean = new URL(location.href); clean.searchParams.delete('token');
        history.replaceState(null, '', clean.toString());
      }
    } catch {}

    if (!getToken()) { needLogin = true; return; }

    focusedLeafId = T.leaves(layout)[0]?.id || '';
    refresh();
    startEventStream();
    const iv = setInterval(refresh, 2500);
    const on401 = () => { needLogin = true; };
    window.addEventListener('chepherd-401', on401);

    return () => {
      clearInterval(iv);
      window.removeEventListener('chepherd-401', on401);
      if (evRetryTimer) { clearTimeout(evRetryTimer); evRetryTimer = null; }   // kill any queued SSE retry
      evStream?.close();
      evStream = null;
    };
  });

  function toggleRail() { railOpen = !railOpen; try { localStorage.setItem('calm-rail', railOpen ? '1' : '0'); } catch {} }
  function toggleContext() { contextOpen = !contextOpen; try { localStorage.setItem('calm-context', contextOpen ? '1' : '0'); } catch {} }
  function toggleTranscript() { transcriptOpen = !transcriptOpen; try { localStorage.setItem('calm-transcript', transcriptOpen ? '1' : '0'); } catch {} }

  function onWizardClose() { showWizard = false; refresh(); }

  // ---------------- sign out (#9) ----------------
  function signOut() {
    userMenuOpen = false;
    try { localStorage.removeItem('chepherd-token'); } catch {}
    if (evRetryTimer) { clearTimeout(evRetryTimer); evRetryTimer = null; }   // kill any queued SSE retry
    try { evStream?.close(); } catch {}
    evStream = null;
    needLogin = true;
  }

  // ---------------- transcript splitter (#11) ----------------
  let stageColEl = $state(null);
  function startTranscriptDrag(ev) {
    ev.preventDefault();
    transcriptDragging = true;
    const rect = stageColEl?.getBoundingClientRect();
    function onMove(e) {
      if (!rect) return;
      const point = e.touches ? e.touches[0] : e;
      // Transcript occupies the BOTTOM portion → its height % is the
      // distance from the pointer down to the column bottom.
      let pct = ((rect.bottom - point.clientY) / rect.height) * 100;
      transcriptPct = Math.max(10, Math.min(60, pct));
    }
    function onUp() {
      transcriptDragging = false;
      try { localStorage.setItem('calm-transcript-pct', String(Math.round(transcriptPct))); } catch {}
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
      window.removeEventListener('touchmove', onMove);
      window.removeEventListener('touchend', onUp);
    }
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    window.addEventListener('touchmove', onMove, { passive: false });
    window.addEventListener('touchend', onUp);
  }
</script>

<div class="calm" data-calm-theme={theme}>
  {#if !mounted}
    <div class="boot"><span class="boot-mark">calm</span></div>
  {:else if needLogin}
    <div class="login">
      <div class="login-card">
        <div class="login-mark">calm</div>
        <h1>Welcome back</h1>
        <p>Paste the bootstrap token chepherd printed at startup.</p>
        <textarea bind:value={loginInput} rows="3" placeholder="eyJhbGc…" spellcheck="false" autocomplete="off"></textarea>
        {#if loginError}<div class="login-err">{loginError}</div>{/if}
        <button class="login-btn" onclick={doLogin}>Enter</button>
      </div>
    </div>
  {:else}
    <!-- ===================== TOP BAR ===================== -->
    <header class="topbar">
      <div class="top-left">
        <button class="icon-btn" onclick={toggleRail} title={railOpen ? 'Hide rail' : 'Show rail'} aria-label="Toggle rail">☰</button>
        <span class="brand">
          <span class="brand-dot"></span>
          calm
          <span class="brand-ver">{version}</span>
        </span>
      </div>

      <div class="top-center">
        <!-- Neutral center: the top bar must NOT echo the focused agent
             name (#4). The focused agent surfaces in its pane header +
             the Inspector instead. -->
        <span class="center-label">calm workspace</span>
      </div>

      <div class="top-right">
        <button class="pill-btn" onclick={addPane} title="Add a terminal pane">＋ Pane</button>
        <button class="pill-btn accent" onclick={() => (showWizard = true)} title="Spawn agents">✦ Spawn</button>
        <div class="divider-y"></div>
        <button class="icon-btn" onclick={() => applyFont(-1)} title="Smaller text" aria-label="Smaller text">A−</button>
        <button class="icon-btn" onclick={() => applyFont(1)} title="Larger text" aria-label="Larger text">A+</button>
        <button class="icon-btn" onclick={toggleTheme} title="Toggle light / dark" aria-label="Toggle theme">
          {theme === 'dark' ? '☀' : '☾'}
        </button>
        <button class="icon-btn" onclick={() => (showSettings = true)} title="Settings" aria-label="Settings">⚙</button>
        <button class="icon-btn" class:on={transcriptOpen} onclick={toggleTranscript} title={transcriptOpen ? 'Hide transcript' : 'Show transcript'} aria-label="Toggle transcript">✉</button>
        <button class="icon-btn" onclick={toggleContext} title={contextOpen ? 'Hide context' : 'Show context'} aria-label="Toggle context">⫶</button>
        <div class="divider-y"></div>
        <div class="user-wrap">
          <button
            class="icon-btn"
            onclick={() => (userMenuOpen = !userMenuOpen)}
            title="Account"
            aria-label="Account menu"
            aria-haspopup="menu"
            aria-expanded={userMenuOpen}
          >👤</button>
          {#if userMenuOpen}
            <div class="user-menu" role="menu">
              <div class="um-head">Signed in</div>
              <button class="um-item danger" role="menuitem" onclick={signOut}>⎋ Sign out</button>
            </div>
          {/if}
        </div>
      </div>
    </header>

    <!-- ===================== BODY ===================== -->
    <div class="body">
      {#if railOpen}
        <aside class="rail">
          <CalmRail
            {sessions}
            {teams}
            {memberships}
            {selectedAgent}
            onselect={selectAgent}
            onopennew={openAgentInNewPane}
          />
        </aside>
      {/if}

      <!-- focus stage (terminals) + transcript pane below it (#11) -->
      <main class="stage" bind:this={stageColEl}>
        {#if maximizedLeafId && T.findLeaf(layout, maximizedLeafId)}
          <!-- Maximized: stage shows ONLY the maximized leaf (#5). -->
          <div class="stage-grid">
            <CalmPane
              node={T.findLeaf(layout, maximizedLeafId)}
              {sessions}
              {focusedLeafId}
              {maximizedLeafId}
              onfocusleaf={onFocusLeaf}
              onsetratio={onSetRatio}
              onsplit={onSplit}
              onclose={onCloseLeaf}
              onsetagent={onSetAgent}
              onsetwidget={onSetWidget}
              onmaximize={onMaximize}
              oncollapse={onCollapse}
              canClose={T.countLeaves(layout) > 1}
            />
          </div>
        {:else}
          <div class="stage-grid">
            <CalmPane
              node={layout}
              {sessions}
              {focusedLeafId}
              {maximizedLeafId}
              onfocusleaf={onFocusLeaf}
              onsetratio={onSetRatio}
              onsplit={onSplit}
              onclose={onCloseLeaf}
              onsetagent={onSetAgent}
              onsetwidget={onSetWidget}
              onmaximize={onMaximize}
              oncollapse={onCollapse}
              canClose={T.countLeaves(layout) > 1}
            />
          </div>

          {#if transcriptOpen}
            <div
              class="t-splitter {transcriptDragging ? 'is-dragging' : ''}"
              onmousedown={startTranscriptDrag}
              ontouchstart={startTranscriptDrag}
              role="separator"
              aria-orientation="horizontal"
              aria-label="Resize transcript"
              tabindex="-1"
            >
              <span class="t-grip"></span>
            </div>
            <section class="transcript-pane" style={`height:${transcriptPct}%`}>
              <div class="tp-head">
                <span class="tp-tab">Team Transcript</span>
                <button class="tp-collapse" onclick={toggleTranscript} title="Hide transcript" aria-label="Hide transcript">⌄</button>
              </div>
              <div class="tp-inner">
                <TeamTranscript team="all" />
              </div>
            </section>
          {/if}
        {/if}
      </main>

      {#if contextOpen}
        <aside class="context">
          <div class="ctx-tab">Inspector</div>
          <div class="ctx-inner">
            <CalmInspector boundSession={focusedSession} {sessions} />
          </div>
        </aside>
      {/if}
    </div>

    {#if notice}
      <div class="toast" role="status">{notice}</div>
    {/if}

    {#if showSettings}
      <CalmSettings
        {theme}
        {fontSize}
        {events}
        {sessions}
        {teams}
        {focusedSession}
        ontheme={applyTheme}
        onfont={applyFont}
        onclose={() => (showSettings = false)}
      />
    {/if}

    {#if showWizard}
      <div class="wizard-overlay" role="dialog" aria-label="Spawn agents">
        <SpawnWizardV9 onclose={onWizardClose} />
      </div>
    {/if}
  {/if}
</div>

<style>
  /* ===================== CALM THEME TOKENS ===================== */
  /* Self-contained --calm-* tokens. Two complete palettes; neither is an
     afterthought. data-calm-theme on the root selects between them. */
  .calm[data-calm-theme="dark"] {
    --calm-bg: #0c0e12;
    --calm-surface: #14171d;
    --calm-surface-2: #181c23;
    --calm-chip: #1d222b;
    --calm-chip-hover: #242a34;
    --calm-input: #0f1217;
    --calm-border: #242a33;
    --calm-border-strong: #333b47;
    --calm-fg: #eef1f5;
    --calm-fg-muted: #a7b0bd;
    --calm-fg-faint: #6b7480;
    --calm-accent: #6ea8fe;
    --calm-accent-2: #5ce0c6;
    --calm-ok: #5cd6a0;
    --calm-warn: #f0c060;
    --calm-danger: #ff7a7a;
    --calm-shadow-sm: 0 1px 2px rgba(0,0,0,0.35);
    --calm-shadow-focus: 0 0 0 1px color-mix(in srgb, var(--calm-accent) 30%, transparent), 0 8px 28px rgba(0,0,0,0.45);
    --calm-shadow-lg: 0 20px 60px rgba(0,0,0,0.55);
    color-scheme: dark;
  }
  .calm[data-calm-theme="light"] {
    --calm-bg: #eef1f6;
    --calm-surface: #ffffff;
    --calm-surface-2: #f5f7fb;
    --calm-chip: #eef1f6;
    --calm-chip-hover: #e3e8f0;
    --calm-input: #ffffff;
    --calm-border: #e2e7ef;
    --calm-border-strong: #cdd5e1;
    --calm-fg: #1b2330;
    --calm-fg-muted: #51607a;
    --calm-fg-faint: #8a97aa;
    --calm-accent: #3b7bff;
    --calm-accent-2: #14a08a;
    --calm-ok: #1f9d6b;
    --calm-warn: #b67d18;
    --calm-danger: #d23d3d;
    --calm-shadow-sm: 0 1px 2px rgba(20,30,50,0.06);
    --calm-shadow-focus: 0 0 0 1px color-mix(in srgb, var(--calm-accent) 28%, transparent), 0 10px 30px rgba(30,50,90,0.12);
    --calm-shadow-lg: 0 24px 64px rgba(30,50,90,0.18);
    color-scheme: light;
  }

  /* Bridge: feed the reused v08 widgets (terminal/transcript) the tokens
     they expect (--bg/--fg/etc) from our calm palette so they blend in. */
  .calm {
    --bg: var(--calm-bg);
    --bg-elev: var(--calm-surface);
    --bg-elevated: var(--calm-surface);
    --bg-input: var(--calm-input);
    --fg: var(--calm-fg);
    --fg-muted: var(--calm-fg-muted);
    --fg-faint: var(--calm-fg-faint);
    --muted: var(--calm-fg-muted);
    --border: var(--calm-border);
    --border-strong: var(--calm-border-strong);
    --accent: var(--calm-accent);
    --accent-2: var(--calm-accent-2);
    --danger: var(--calm-danger);
    --success: var(--calm-ok);
    --select-bg: color-mix(in srgb, var(--calm-accent) 16%, transparent);
    --select-border: var(--calm-accent);
    --scrollbar-track: transparent;
    --scrollbar-thumb: var(--calm-border-strong);
    --scrollbar-thumb-hover: var(--calm-fg-faint);

    position: fixed; inset: 0;
    display: flex; flex-direction: column;
    background: var(--calm-bg);
    color: var(--calm-fg);
    font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
    -webkit-font-smoothing: antialiased;
    overflow: hidden;
  }

  /* ===================== TOP BAR ===================== */
  .topbar {
    display: flex; align-items: center; justify-content: space-between;
    gap: 1rem;
    padding: 0.5rem 0.85rem;
    background: var(--calm-surface);
    border-bottom: 1px solid var(--calm-border);
    flex: 0 0 auto;
    z-index: 10;
  }
  .top-left, .top-right { display: flex; align-items: center; gap: 0.35rem; }
  .top-center { flex: 1; display: flex; justify-content: center; min-width: 0; }

  .brand { display: inline-flex; align-items: baseline; gap: 0.4rem; font-weight: 800; font-size: 1.05rem; letter-spacing: -0.01em; padding-left: 0.2rem; }
  .brand-dot { width: 9px; height: 9px; border-radius: 50%; align-self: center; background: linear-gradient(135deg, var(--calm-accent), var(--calm-accent-2)); box-shadow: 0 0 0 3px color-mix(in srgb, var(--calm-accent) 20%, transparent); }
  .brand-ver { font-size: 0.66rem; font-weight: 600; color: var(--calm-fg-faint); background: var(--calm-chip); padding: 0.1rem 0.4rem; border-radius: 6px; }

  .center-label {
    font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.14em;
    color: var(--calm-fg-faint); font-weight: 600; user-select: none;
  }

  .icon-btn {
    width: 32px; height: 32px;
    display: inline-flex; align-items: center; justify-content: center;
    background: transparent; border: 1px solid transparent;
    color: var(--calm-fg-muted); border-radius: 6px;
    cursor: pointer; font-size: 0.92rem; font-weight: 600;
    transition: background 0.14s ease, color 0.14s ease;
  }
  .icon-btn:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }
  .icon-btn.on { color: var(--calm-accent); background: color-mix(in srgb, var(--calm-accent) 14%, transparent); }

  /* user menu (#9) */
  .user-wrap { position: relative; }
  .user-menu {
    position: absolute; top: calc(100% + 8px); right: 0;
    z-index: 60; min-width: 11rem;
    background: var(--calm-surface);
    border: 1px solid var(--calm-border-strong);
    border-radius: 6px; padding: 0.3rem;
    box-shadow: var(--calm-shadow-lg);
    display: flex; flex-direction: column; gap: 0.1rem;
  }
  .um-head {
    font-size: 0.64rem; text-transform: uppercase; letter-spacing: 0.08em;
    color: var(--calm-fg-faint); font-weight: 700; padding: 0.35rem 0.55rem 0.25rem;
  }
  .um-item {
    display: flex; align-items: center; gap: 0.4rem;
    padding: 0.45rem 0.55rem; border-radius: 6px;
    background: transparent; border: 0; color: var(--calm-fg);
    font: inherit; font-size: 0.82rem; text-align: left; cursor: pointer; width: 100%;
  }
  .um-item:hover { background: var(--calm-chip-hover); }
  .um-item.danger { color: var(--calm-danger); }
  .um-item.danger:hover { background: color-mix(in srgb, var(--calm-danger) 12%, transparent); }

  .pill-btn {
    display: inline-flex; align-items: center; gap: 0.35rem;
    padding: 0.35rem 0.8rem;
    background: var(--calm-chip); border: 1px solid var(--calm-border);
    color: var(--calm-fg); border-radius: 8px;
    font-size: 0.8rem; font-weight: 600; cursor: pointer;
    transition: background 0.14s ease, border-color 0.14s ease;
  }
  .pill-btn:hover { background: var(--calm-chip-hover); border-color: var(--calm-border-strong); }
  .pill-btn.accent {
    background: linear-gradient(135deg, var(--calm-accent), color-mix(in srgb, var(--calm-accent-2) 60%, var(--calm-accent)));
    color: #06121f; border-color: transparent;
  }
  .pill-btn.accent:hover { filter: brightness(1.06); }
  .divider-y { width: 1px; height: 22px; background: var(--calm-border); margin: 0 0.2rem; }

  /* ===================== BODY ===================== */
  /* Sessions rail (left) and context column (right) share ONE width so
     the workspace is symmetric (#12). */
  .body { flex: 1; display: flex; min-height: 0; min-width: 0; --calm-side-w: 264px; }

  .rail {
    width: var(--calm-side-w); flex: 0 0 var(--calm-side-w);
    background: var(--calm-surface);
    border-right: 1px solid var(--calm-border);
    min-height: 0; overflow: hidden;
  }

  .stage {
    flex: 1; min-width: 0; min-height: 0;
    display: flex; flex-direction: column;
    padding: 0.7rem;
    gap: 0;
  }
  .stage-grid { flex: 1; min-height: 0; min-width: 0; }

  /* Transcript pane below the terminal stage (#11). */
  .t-splitter {
    flex: 0 0 auto; height: 10px;
    display: flex; align-items: center; justify-content: center;
    cursor: row-resize; background: transparent;
    transition: background 0.15s ease;
  }
  .t-splitter:hover, .t-splitter.is-dragging { background: color-mix(in srgb, var(--calm-accent) 22%, transparent); }
  .t-grip { width: 38px; height: 3px; border-radius: 999px; background: var(--calm-border-strong); transition: background 0.15s ease; }
  .t-splitter:hover .t-grip, .t-splitter.is-dragging .t-grip { background: var(--calm-accent); }

  .transcript-pane {
    flex: 0 0 auto; min-height: 0;
    display: flex; flex-direction: column;
    background: var(--calm-surface);
    border: 1px solid var(--calm-border);
    border-radius: 6px;
    overflow: hidden;
    box-shadow: var(--calm-shadow-sm);
  }
  .tp-head {
    flex: 0 0 auto; display: flex; align-items: center; justify-content: space-between;
    padding: 0.35rem 0.6rem;
    border-bottom: 1px solid var(--calm-border);
    background: var(--calm-surface-2);
  }
  .tp-tab {
    font-size: 0.66rem; text-transform: uppercase; letter-spacing: 0.08em;
    color: var(--calm-fg-faint); font-weight: 700;
  }
  .tp-collapse {
    width: 24px; height: 24px; display: inline-flex; align-items: center; justify-content: center;
    background: transparent; border: 1px solid transparent; color: var(--calm-fg-muted);
    border-radius: 6px; cursor: pointer; font-size: 0.85rem;
  }
  .tp-collapse:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }
  .tp-inner { flex: 1; min-height: 0; overflow: hidden; }

  .context {
    width: var(--calm-side-w); flex: 0 0 var(--calm-side-w);
    background: var(--calm-surface);
    border-left: 1px solid var(--calm-border);
    display: flex; flex-direction: column;
    min-height: 0; overflow: hidden;
  }
  .ctx-tab {
    font-size: 0.66rem; text-transform: uppercase; letter-spacing: 0.08em;
    color: var(--calm-fg-faint); font-weight: 700;
    padding: 0.55rem 0.75rem 0.35rem; flex: 0 0 auto;
  }
  .ctx-inner { flex: 1; min-height: 0; overflow: hidden; }

  /* ===================== TOAST ===================== */
  .toast {
    position: fixed; bottom: 1.1rem; left: 50%; transform: translateX(-50%);
    background: var(--calm-surface); color: var(--calm-fg);
    border: 1px solid var(--calm-border-strong); border-radius: 8px;
    padding: 0.45rem 1rem; font-size: 0.82rem; font-weight: 600;
    box-shadow: var(--calm-shadow-lg); z-index: 1100;
  }

  /* ===================== BOOT (SSR placeholder) ===================== */
  .boot { position: fixed; inset: 0; display: grid; place-items: center; background: var(--calm-bg); }
  .boot-mark { font-weight: 800; font-size: 1.3rem; color: var(--calm-accent); letter-spacing: -0.01em; opacity: 0.85; }

  /* ===================== LOGIN ===================== */
  .login { position: fixed; inset: 0; display: grid; place-items: center; background: var(--calm-bg); padding: 1rem; }
  .login-card {
    width: 100%; max-width: 30rem;
    background: var(--calm-surface); border: 1px solid var(--calm-border);
    border-radius: 10px; padding: 2rem; display: flex; flex-direction: column; gap: 0.7rem;
    box-shadow: var(--calm-shadow-lg);
  }
  .login-mark { font-weight: 800; font-size: 1.1rem; color: var(--calm-accent); }
  .login-card h1 { font-size: 1.5rem; margin: 0; }
  .login-card p { color: var(--calm-fg-muted); font-size: 0.88rem; margin: 0; }
  .login-card textarea {
    width: 100%; box-sizing: border-box; padding: 0.6rem 0.7rem;
    background: var(--calm-input); color: var(--calm-fg);
    border: 1px solid var(--calm-border); border-radius: 6px;
    font-family: ui-monospace, monospace; font-size: 0.82rem; resize: vertical;
  }
  .login-card textarea:focus { outline: none; border-color: var(--calm-accent); }
  .login-err { color: var(--calm-danger); font-size: 0.84rem; }
  .login-btn {
    padding: 0.6rem; border: 0; border-radius: 8px; cursor: pointer;
    background: linear-gradient(135deg, var(--calm-accent), var(--calm-accent-2));
    color: #06121f; font-weight: 700; font-size: 0.92rem;
  }
  .login-btn:hover { filter: brightness(1.06); }

  /* ===================== WIZARD OVERLAY ===================== */
  .wizard-overlay {
    position: fixed; inset: 0; z-index: 1200;
    background: color-mix(in srgb, var(--calm-bg) 70%, transparent);
    backdrop-filter: blur(6px);
    display: flex; align-items: center; justify-content: center;
    padding: 2vh 2vw;
  }

  /* ===================== SCROLLBARS ===================== */
  .calm :global(*) { scrollbar-width: thin; scrollbar-color: var(--scrollbar-thumb) transparent; }
  .calm :global(*::-webkit-scrollbar) { width: 11px; height: 11px; }
  .calm :global(*::-webkit-scrollbar-track) { background: transparent; }
  .calm :global(*::-webkit-scrollbar-thumb) { background: var(--scrollbar-thumb); border-radius: 10px; border: 3px solid var(--calm-surface); }
  .calm :global(*::-webkit-scrollbar-thumb:hover) { background: var(--scrollbar-thumb-hover); }
</style>

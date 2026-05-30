<!--
  chepherd v0.5.1 dashboard. Issue #79 wave 2 (#292 sweep).
  - Single top bar: brand + stats + Docs/Download/GitHub + "+ spawn".
  - 3-pane body fills viewport vertically: sessions list / xterm / details.
  - Spawn modal: autocomplete folder input, auto-derived name, Scrum
    Master is opt-in (off by default), default system prompt opt-in.
  - Workers + Scrum Masters rendered as separate sections in the left
    list, so the operator can see who supervises whom at a glance.
  - Scorecard panel in the right column (stub for now: chepherd needs an
    analyzer goroutine to populate G/V/F/E — filed as follow-up).
  - Role enum: still accepts wire string "shepherd" (legacy/back-compat)
    AND "scrummaster" (canonical going forward) from the runtime.
-->
<script module>
  // SpiderChart — pure-SVG radar plot. Used inline by Dashboard for
  // the per-session scorecard. Each axis is {label, value (0..10)}.
  // 4 axes give the cleanest read at this size; more is supported.
</script>

<script>
  import { onMount } from 'svelte';
  // xterm ships its own CSS — without this, .xterm-screen and .xterm-rows
  // lack their absolute-positioning rules and render at the bottom of
  // the page instead of inside .xterm-viewport, leaving the terminal
  // pane visually empty even though chunks arrive over the WebSocket.
  import '@xterm/xterm/css/xterm.css';
  import SpiderChart from './SpiderChart.svelte';

  let sessions = $state([]);
  let selectedName = $state(null);
  let selectedInfo = $state(null);
  let connected = $state(false);
  let inbox = $state([]);
  // #225 row G1 — federation peers (cached agent cards) from /api/v1/peers
  let peers = $state([]);
  // #225 row G2 — recent A2A tasks from /api/v1/tasks (the A2A Inbox tab)
  let a2aTasks = $state([]);
  let showSpawn = $state(false);
  let theme = $state('dark'); // 'dark' | 'light' — persisted in localStorage

  let spawnForm = $state({
    name: '',
    agent: 'claude-code',
    team: 'default',
    role: 'worker',
    cwd: '',
    mode: 'fresh',
    resume_uuid: '',
    use_default_prompt: true, // "solo" mode ON by default per founder
  });
  let folderQuery = $state('');
  let folderFocused = $state(false);
  let folderSuppressed = $state(false); // true after pick — hide until user types
  let allFolders = $state([]);
  let confirmDialog = $state(null); // { title, body, onConfirm }

  // Esc dismisses the topmost open modal. Operator request 2026-05-29.
  $effect(() => {
    function onKey(e) {
      if (e.key !== 'Escape') return;
      if (confirmDialog) { confirmDialog = null; return; }
      if (typeof showSpawn !== 'undefined' && showSpawn) { showSpawn = false; return; }
    }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });

  let claudeSessions = $state([]);
  let claudeQuery = $state('');
  let spawnError = $state('');
  let spawnBusy = $state(false);

  const API = '/api/v1';
  const AGENTS = ['claude-code', 'qwen-code', 'aider', 'opencode', 'cursor-agent', 'little-coder', 'sovereign-shell'];

  // Worker/Scrum-Master partition for the left pane. Role enum
  // accepts both wire strings for back-compat (#292): legacy
  // back-compat: "shepherd" (legacy) + canonical "scrummaster".
  const SCRUM_MASTER_ROLES = ['shepherd', 'scrummaster'];
  let workers = $derived(sessions.filter(s => !SCRUM_MASTER_ROLES.includes(s.role)));
  let scrumMasters = $derived(sessions.filter(s => SCRUM_MASTER_ROLES.includes(s.role)));

  async function refreshSessions() {
    try {
      const res = await fetch(`${API}/sessions`);
      const data = await res.json();
      sessions = data.sessions || [];
      connected = true;
      if (selectedName) {
        selectedInfo = sessions.find(s => s.name === selectedName) || null;
        if (!selectedInfo) selectedName = null;
      }
    } catch (err) {
      connected = false;
    }
  }

  async function refreshInbox() {
    try {
      const res = await fetch(`${API}/inbox`);
      const data = await res.json();
      inbox = data.inbox || [];
    } catch {}
  }

  // #225 row G1 — refresh federation peers (cached agent cards)
  async function refreshPeers() {
    try {
      const res = await fetch(`${API}/peers`);
      const data = await res.json();
      peers = data.peers || [];
    } catch { peers = []; }
  }

  // #225 row G2 — refresh A2A tasks (Inbox tab)
  async function refreshA2ATasks() {
    try {
      const res = await fetch(`${API}/tasks`);
      const data = await res.json();
      a2aTasks = data.tasks || [];
    } catch { a2aTasks = []; }
  }

  async function loadAllFolders() {
    try {
      const res = await fetch(`${API}/folders/recent`);
      const data = await res.json();
      allFolders = data.folders || [];
    } catch {}
  }

  let folderResults = $derived.by(() => {
    const q = folderQuery.trim().toLowerCase();
    if (!q) return allFolders.slice(0, 8);
    return allFolders.filter(f => f.path.toLowerCase().includes(q)).slice(0, 8);
  });

  async function refreshClaudeSessions(cwd) {
    try {
      const url = cwd ? `${API}/claude-sessions?cwd=${encodeURIComponent(cwd)}` : `${API}/claude-sessions`;
      const res = await fetch(url);
      const data = await res.json();
      claudeSessions = data.sessions || [];
    } catch { claudeSessions = []; }
  }

  // Auto-derive a unique session name from cwd's LEAF folder name.
  // First spawn into ~/repos/talentmesh => "talentmesh".
  // Second spawn into the same folder => "talentmesh-1".
  // Third => "talentmesh-2". Etc. Per founder spec.
  function autoName(cwd) {
    const base = (cwd || '').split('/').filter(Boolean).pop() || 'agent';
    const slug = base.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/(^-|-$)/g, '');
    const existing = new Set(sessions.map(s => s.name));
    if (!existing.has(slug)) return slug;
    let n = 1;
    while (existing.has(`${slug}-${n}`)) n++;
    return `${slug}-${n}`;
  }

  let term = null;
  let termContainer = null;
  let ws = null;
  let fitAddon = null;
  let resizeObs = null;

  async function attachTo(name) {
    if (selectedName === name) return;
    selectedName = name;
    selectedInfo = sessions.find(s => s.name === name) || null;
    if (ws) { ws.close(); ws = null; }
    if (resizeObs) { resizeObs.disconnect(); resizeObs = null; }
    if (term) { term.dispose(); term = null; }

    const { Terminal } = await import('@xterm/xterm');
    const { FitAddon } = await import('@xterm/addon-fit');
    // xterm colors track the active theme. We pick foreground/background
    // explicitly so the terminal blends with the rest of the dashboard.
    const xtermTheme = theme === 'light'
      ? { background: '#fafafa', foreground: '#1a1a1a', cursor: '#1a1a1a', selectionBackground: '#cbd5e1' }
      : { background: '#0a0a0a', foreground: '#f5f5f5', cursor: '#f5f5f5', selectionBackground: '#2a3540' };
    term = new Terminal({
      convertEol: true,
      fontFamily: 'ui-monospace, "JetBrains Mono", monospace',
      fontSize: 14,
      theme: xtermTheme,
      cursorBlink: true,
      // Claude TUI assumes ~80 cols minimum; xterm will reflow but its
      // initial line breaks are determined by the dimensions at first
      // render, which is why we MUST fit() after the container is sized.
      cols: 120,
      rows: 32,
    });
    fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.open(termContainer);

    // Multiple fit() passes catch the layout settling. flex + Svelte
    // hydration means the container's first ClientRect can be wrong.
    const tryFit = () => {
      if (!fitAddon || !termContainer) return;
      const r = termContainer.getBoundingClientRect();
      if (r.width < 10 || r.height < 10) return;
      try { fitAddon.fit(); } catch {}
    };
    tryFit();
    requestAnimationFrame(tryFit);
    setTimeout(tryFit, 100);
    setTimeout(tryFit, 400);

    // Refit when the container size changes (sidebar widths, viewport
    // resize, browser zoom, etc.). Without this, the initial fit captures
    // a too-narrow viewport and Claude TUI output gets mangled.
    resizeObs = new ResizeObserver(() => tryFit());
    resizeObs.observe(termContainer);

    const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${wsProto}//${window.location.host}${API}/sessions/${name}/attach`);
    ws.binaryType = 'arraybuffer';
    ws.onmessage = (ev) => {
      if (!term) return;
      if (ev.data instanceof ArrayBuffer) term.write(new Uint8Array(ev.data));
      else term.write(ev.data);
    };
    // Push dimension changes to the backend so the PTY child gets SIGWINCH
    // matching xterm's actual rows/cols. Without this, Claude TUI's
    // initial render stays at 80×24 even when our pane is 120×40, and
    // mid-render content wraps badly to the smaller dimensions.
    const sendResize = () => {
      if (!ws || ws.readyState !== WebSocket.OPEN || !term) return;
      try {
        ws.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols }));
      } catch {}
    };
    term.onResize(sendResize);
    // Initial sync once the WS is open + fit() has settled.
    ws.addEventListener('open', () => setTimeout(sendResize, 200));
    term.onData((d) => { if (ws && ws.readyState === WebSocket.OPEN) ws.send(d); });

    // Auto-copy text selection to the OS clipboard. Without this, xterm
    // only stores selections in its internal buffer — Ctrl+Shift+C /
    // Ctrl+Insert work as fallbacks, but operators expect "select = copy"
    // (the gnome-terminal / iTerm / WezTerm default). Selection events
    // ARE a user gesture so navigator.clipboard.writeText is permitted.
    let lastCopied = '';
    term.onSelectionChange(() => {
      const sel = term.getSelection();
      if (!sel || sel === lastCopied) return;
      lastCopied = sel;
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(sel).catch(() => {});
      }
    });
    // Handle OSC52 — when Claude or the underlying agent emits
    // \x1b]52;c;BASE64\x07 to write to the clipboard, intercept it +
    // forward to navigator.clipboard so the operator's OS clipboard
    // gets the text. Without this, xterm reports "check clipboard
    // setting" because the OSC52 default handler is unset.
    term.parser.registerOscHandler(52, (data) => {
      // data is "c;BASE64" or "p;BASE64" (clipboard / primary)
      const parts = data.split(';');
      if (parts.length < 2) return true;
      try {
        const text = atob(parts[1]);
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text).catch(() => {});
        }
      } catch {}
      return true; // we handled it
    });
  }

  async function openSpawn() {
    spawnForm = { name: '', agent: 'claude-code', team: 'default', role: 'worker', cwd: '', mode: 'fresh', resume_uuid: '', use_default_prompt: true };
    folderQuery = '';
    folderFocused = false;
    folderSuppressed = false;
    claudeQuery = '';
    spawnError = '';
    showSpawn = true;
    await Promise.all([loadAllFolders(), refreshClaudeSessions(null)]);
  }

  function pickFolder(path) {
    spawnForm.cwd = path;
    folderQuery = path;
    folderFocused = false;
    folderSuppressed = true; // stays hidden until user types again
    if (spawnForm.mode === 'resume') refreshClaudeSessions(path);
    if (!spawnForm.name) spawnForm.name = autoName(path);
  }

  function pickResume(s) {
    spawnForm.resume_uuid = s.uuid;
    spawnForm.cwd = s.cwd;
    folderQuery = s.cwd;
    if (!spawnForm.name) spawnForm.name = autoName(s.cwd);
  }

  let filteredResumes = $derived.by(() => {
    const q = claudeQuery.trim().toLowerCase();
    if (!q) return claudeSessions.slice(0, 30);
    return claudeSessions.filter(s =>
      (s.cwd || '').toLowerCase().includes(q) ||
      (s.first_message || '').toLowerCase().includes(q) ||
      (s.uuid || '').toLowerCase().includes(q)
    ).slice(0, 30);
  });

  async function submitSpawn() {
    spawnError = '';
    // Use the folderQuery text as cwd if the user typed something but
    // didn't click a suggestion (the input is bound to folderQuery, not
    // spawnForm.cwd, until pickFolder fires).
    if (!spawnForm.cwd && folderQuery) spawnForm.cwd = folderQuery;
    // Auto-name if blank
    if (!spawnForm.name && spawnForm.cwd) spawnForm.name = autoName(spawnForm.cwd);
    if (!spawnForm.name) { spawnError = 'Pick a folder so I can auto-name, or enter a name'; return; }
    spawnBusy = true;
    const body = {
      name: spawnForm.name,
      agent: spawnForm.agent,
      team: spawnForm.team || 'default',
      role: spawnForm.role,
      cwd: spawnForm.cwd,
      use_default_prompt: spawnForm.use_default_prompt,
    };
    if (spawnForm.mode === 'resume' && spawnForm.resume_uuid) {
      body.resume_uuid = spawnForm.resume_uuid;
    }
    try {
      const res = await fetch(`${API}/sessions`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!res.ok) {
        const e = await res.json().catch(()=>({error:'spawn failed'}));
        spawnError = e.error || `HTTP ${res.status}`;
      } else {
        showSpawn = false;
        await refreshSessions();
        attachTo(spawnForm.name);
      }
    } catch (e) { spawnError = String(e); }
    finally { spawnBusy = false; }
  }

  async function markRead(id) {
    // Optimistic: flip locally then sync.
    inbox = inbox.map(m => m.id === id ? { ...m, read: true } : m);
    try { await fetch(`${API}/inbox/${encodeURIComponent(id)}/read`, { method: 'POST' }); } catch {}
  }
  async function markAllRead() {
    inbox = inbox.map(m => ({ ...m, read: true }));
    try { await fetch(`${API}/inbox/read-all`, { method: 'POST' }); } catch {}
  }

  function formatBytes(n) {
    if (n == null) return '—';
    if (n < 1024) return n + ' B';
    if (n < 1024*1024) return (n/1024).toFixed(1) + ' KiB';
    return (n/1024/1024).toFixed(2) + ' MiB';
  }
  function relTimeShort(seconds) {
    if (seconds == null) return '—';
    if (seconds < 60) return Math.floor(seconds) + 's';
    if (seconds < 3600) return Math.floor(seconds/60) + 'm';
    return Math.floor(seconds/3600) + 'h ' + Math.floor((seconds%3600)/60) + 'm';
  }
  // Convert the Scrum Master's 5-axis scorecard into spider-chart input.
  // The Scrum Master writes real scores via chepherd.set_scorecard MCP (not
  // synthetic activity proxies). G=Goal clarity, V=Velocity, F=Focus,
  // E=End-state proximity, D=Discipline (CLAUDE.md compliance).
  function scorecardAxesFor(sc) {
    if (!sc) return [];
    return [
      { label: 'Goal',       value: sc.G || 0 },
      { label: 'Velocity',   value: sc.V || 0 },
      { label: 'Focus',      value: sc.F || 0 },
      { label: 'End-state',  value: sc.E || 0 },
      { label: 'Discipline', value: sc.D || 0 },
    ];
  }

  function relTime(ts) {
    if (!ts) return '';
    const s = Math.floor((Date.now() - new Date(ts).getTime())/1000);
    if (s < 60) return `${s}s ago`;
    const m = Math.floor(s/60);
    if (m < 60) return `${m}m ago`;
    const h = Math.floor(m/60);
    if (h < 24) return `${h}h ago`;
    return `${Math.floor(h/24)}d ago`;
  }

  async function pauseSession(paused) {
    if (!selectedName) return;
    await fetch(`${API}/sessions/${selectedName}/pause`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ paused }),
    });
    refreshSessions();
  }

  function stopSession() {
    if (!selectedName) return;
    const name = selectedName;
    confirmDialog = {
      title: 'Stop session?',
      body: `The agent process for "${name}" will be terminated. This cannot be undone.`,
      confirmLabel: 'Stop session',
      danger: true,
      onConfirm: async () => {
        await fetch(`${API}/sessions/${name}`, { method: 'DELETE' });
        if (term) term.dispose();
        if (ws) ws.close();
        selectedName = null;
        selectedInfo = null;
        refreshSessions();
      },
    };
  }

  function ageString(createdAt) {
    if (!createdAt) return '—';
    const s = Math.floor((Date.now() - new Date(createdAt).getTime())/1000);
    if (s < 60) return `${s}s`;
    const m = Math.floor(s/60);
    if (m < 60) return `${m}m`;
    const h = Math.floor(m/60);
    if (h < 24) return `${h}h ${m%60}m`;
    return `${Math.floor(h/24)}d ${h%24}h`;
  }

  function applyTheme(t) {
    theme = t;
    document.documentElement.dataset.theme = t;
    try { localStorage.setItem('chepherd-theme', t); } catch {}
    // Re-tint xterm to match. xterm doesn't react to CSS vars, so we
    // dispose+re-create only when no session attached. When attached,
    // the user can pick a fresh theme on next attach.
  }
  function toggleTheme() {
    applyTheme(theme === 'dark' ? 'light' : 'dark');
  }

  onMount(() => {
    // Restore persisted theme.
    try {
      const stored = localStorage.getItem('chepherd-theme');
      if (stored === 'light' || stored === 'dark') applyTheme(stored);
      else applyTheme('dark');
    } catch { applyTheme('dark'); }

    refreshSessions(); refreshInbox(); refreshPeers(); refreshA2ATasks();
    const interval = setInterval(() => { refreshSessions(); refreshInbox(); refreshPeers(); refreshA2ATasks(); }, 5000);
    const onResize = () => fitAddon && fitAddon.fit();
    window.addEventListener('resize', onResize);
    return () => { clearInterval(interval); window.removeEventListener('resize', onResize); };
  });
</script>

<div class="dashboard">
  <!-- Single combined top bar — brand, stats, nav links, primary action -->
  <header class="topbar">
    <a href="/" class="brand">✻ chepherd</a>
    <div class="stats">
      {sessions.length} session{sessions.length === 1 ? '' : 's'} ·
      {workers.length} worker{workers.length === 1 ? '' : 's'} ·
      {scrumMasters.length} Scrum Master{scrumMasters.length === 1 ? '' : 's'}
      {#if !connected}<span class="warn">· runtime offline</span>{/if}
    </div>
    <nav class="links">
      <a href="/docs">Docs</a>
      <a href="/download">Download</a>
      <a href="https://github.com/chepherd/chepherd" target="_blank" rel="noopener">GitHub</a>
    </nav>
    <button class="icon-btn" on:click={toggleTheme} title="Toggle light/dark theme" aria-label="Toggle theme" data-testid="theme-toggle">
      {theme === 'light' ? '☾' : '☀'}
    </button>
    <button class="primary" on:click={openSpawn} data-testid="spawn-button">+ spawn agent</button>
  </header>

  <div class="body">
    <!-- Left: Workers + Scrum Masters + Inbox -->
    <aside class="left">
      <section>
        <h2>Workers <span class="count">({workers.length})</span></h2>
        <ul class="session-list">
          {#each workers as s (s.id)}
            <li class:selected={selectedName === s.name} class:paused={s.paused} on:click={() => attachTo(s.name)}>
              <div class="row1">
                <span class="dot worker">●</span>
                <span class="name">{s.name}</span>
                {#if s.paused}<span class="badge">paused</span>{/if}
              </div>
              <div class="row2">{s.agent} · {s.team} · {ageString(s.created_at)}</div>
            </li>
          {/each}
          {#if workers.length === 0}
            <li class="empty">No workers yet. Click "+ spawn agent" to start one.</li>
          {/if}
        </ul>
      </section>

      <section style="margin-top: 1.2rem;">
        <h2>Scrum Masters <span class="count">({scrumMasters.length})</span></h2>
        <ul class="session-list">
          {#each scrumMasters as s (s.id)}
            <li class:selected={selectedName === s.name} class:paused={s.paused} on:click={() => attachTo(s.name)}>
              <div class="row1">
                <span class="dot scrummaster">✻</span>
                <span class="name">{s.name}</span>
                {#if s.paused}<span class="badge">paused</span>{/if}
              </div>
              <div class="row2">
                {s.agent} · {ageString(s.created_at)}
                {#if s.shepherding && s.shepherding.length}
                  · watching {s.shepherding.join(', ')}
                {/if}
              </div>
            </li>
          {/each}
          {#if scrumMasters.length === 0}
            <li class="empty">No Scrum Masters. Spawn one to watch your workers (opt-in).</li>
          {/if}
        </ul>
      </section>

      {#if inbox.length}
        {@const unread = inbox.filter(m => !m.read).length}
        <section style="margin-top: 1.2rem;" data-testid="inbox">
          <h2 class="inbox-h2">
            Inbox
            {#if unread > 0}<span class="unread-badge">{unread}</span>{/if}
            {#if unread > 0}<button class="link-btn" on:click={markAllRead}>mark all read</button>{/if}
          </h2>
          <ul class="inbox">
            {#each inbox.slice(-10).reverse() as m (m.id)}
              <li class:unread={!m.read} on:click={() => markRead(m.id)} title="Click to mark read">
                <div class="inbox-meta">
                  <strong>@{m.from}</strong>
                  <span class="inbox-when">{relTime(m.at)}</span>
                </div>
                <div class="inbox-body">{m.body}</div>
              </li>
            {/each}
          </ul>
        </section>
      {/if}

      <!-- #225 row G1 — Federation Peers -->
      <section style="margin-top: 1.2rem;" data-testid="federation-peers">
        <h2>Federation <span class="count">({peers.length})</span></h2>
        <ul class="session-list">
          {#each peers.slice(0, 10) as p (p.sid)}
            <li>
              <div class="row1">
                <span class="dot worker">⇄</span>
                <span class="name">{p.name || p.sid}</span>
              </div>
              <div class="row2">synced {relTime(p.syncedAt)}</div>
            </li>
          {/each}
          {#if peers.length === 0}
            <li class="empty">No peers. Configure --federation-registry-url to discover other chepherd instances.</li>
          {/if}
        </ul>
      </section>

      <!-- #225 row G2 — A2A Inbox (recent tasks) -->
      <section style="margin-top: 1.2rem;" data-testid="a2a-inbox">
        <h2>A2A Inbox <span class="count">({a2aTasks.length})</span></h2>
        <ul class="session-list">
          {#each a2aTasks.slice(0, 10) as task (task.id)}
            <li>
              <div class="row1">
                <span class="dot worker" title={task.state}>◈</span>
                <span class="name">{task.method}</span>
                <span class="badge">{task.state}</span>
              </div>
              <div class="row2">{task.id.slice(0, 12)}… · {relTime(task.updatedAt)}</div>
            </li>
          {/each}
          {#if a2aTasks.length === 0}
            <li class="empty">No A2A tasks yet. Tasks land here when peers send messages.</li>
          {/if}
        </ul>
      </section>
    </aside>

    <!-- Center: xterm fills available height -->
    <section class="center">
      <div class="title">
        <div class="title-left">
          {#if selectedName}
            <span class="dot" class:scrummaster={SCRUM_MASTER_ROLES.includes(selectedInfo?.role)}>{SCRUM_MASTER_ROLES.includes(selectedInfo?.role) ? '✻' : '●'}</span>
            <span class="title-name">{selectedName}</span>
            <span class="subtitle">— live attach via WebSocket</span>
          {:else}
            <span class="subtitle">Pick a session ← or click "+ spawn agent" to create one</span>
          {/if}
        </div>
        {#if selectedInfo}
          <div class="title-actions">
            {#if selectedInfo.paused}
              <button class="secondary" on:click={() => pauseSession(false)} title="Resume session">▶ resume</button>
            {:else}
              <button class="secondary" on:click={() => pauseSession(true)} title="Pause session">⏸ pause</button>
            {/if}
            <button class="danger" on:click={stopSession} title="Stop session">⨯ stop</button>
          </div>
        {/if}
      </div>
      <div class="term" bind:this={termContainer}></div>
    </section>

    <!-- Right: 4 cards (Identity / Location / Process / Scrum Master) — dense inline rows -->
    <aside class="right">
      {#if selectedInfo}
        <!-- Card 1: Identity -->
        <section class="card">
          <h3>Identity</h3>
          <div class="kv">
            <span class="k">name</span><span class="v"><code>{selectedInfo.name}</code></span>
            <span class="k">agent</span><span class="v">{selectedInfo.agent}</span>
            <span class="k">role</span><span class="v"><span class="role-pill" class:scrummaster={SCRUM_MASTER_ROLES.includes(selectedInfo.role)}>{selectedInfo.role}</span></span>
            <span class="k">team</span><span class="v">{selectedInfo.team}</span>
            {#if selectedInfo.shepherding && selectedInfo.shepherding.length}
              <span class="k">watching</span><span class="v">{selectedInfo.shepherding.join(', ')}</span>
            {/if}
          </div>
        </section>

        <!-- Card 2: Location (cwd + git context) -->
        <section class="card">
          <h3>Location</h3>
          <div class="kv">
            <span class="k">cwd</span><span class="v wrap"><code class="cwd">{selectedInfo.cwd || '—'}</code></span>
            {#if selectedInfo.github_url}
              <span class="k">repo</span><span class="v wrap"><a href={selectedInfo.github_url} target="_blank" rel="noopener">{selectedInfo.github_url.replace('https://github.com/','')} ↗</a></span>
            {/if}
            {#if selectedInfo.branch}
              <span class="k">branch</span><span class="v"><code>{selectedInfo.branch}</code></span>
            {/if}
            <span class="k">started</span><span class="v">{ageString(selectedInfo.created_at)} ago</span>
            <span class="k">status</span><span class="v">
              {#if selectedInfo.exited}
                <span class="status-exited">⨯ exited (code {selectedInfo.exit_code})</span>
              {:else if selectedInfo.paused}
                <span>⏸ paused</span>
              {:else if selectedInfo.bytes_5m > 0}
                <span>● live</span>
              {:else}
                <span>○ idle</span>
              {/if}
            </span>
          </div>
        </section>

        <!-- Card 3: Process telemetry (pid + uuid + bytes) -->
        <section class="card">
          <h3>Process</h3>
          <div class="kv">
            <span class="k">pid</span><span class="v"><code>{selectedInfo.pid || '—'}</code></span>
            <span class="k">uuid</span><span class="v wrap"><code class="uuid">{selectedInfo.id}</code></span>
            <span class="k">bytes 5m</span><span class="v">{formatBytes(selectedInfo.bytes_5m)}</span>
            <span class="k">total</span><span class="v">{formatBytes(selectedInfo.total_bytes)}</span>
            <span class="k">idle</span><span class="v">{relTimeShort(selectedInfo.idle_seconds)}</span>
          </div>
        </section>

        <!-- Card 4: Scrum Master assessment (scorecard + verdicts) -->
        <section class="card">
          <h3>Scrum Master assessment</h3>
          {#if selectedInfo.scorecard}
            <SpiderChart axes={scorecardAxesFor(selectedInfo.scorecard)} />
            <div class="kv" style="margin-top:0.6rem;">
              <span class="k">scored</span><span class="v">{relTime(selectedInfo.scorecard.at)}</span>
              {#if selectedInfo.last_verdict}
                <span class="k">verdict</span><span class="v"><span class="verdict verdict-{selectedInfo.last_verdict}">{selectedInfo.last_verdict}</span></span>
                <span class="k">when</span><span class="v">{relTime(selectedInfo.last_verdict_at)}</span>
              {/if}
              {#if selectedInfo.intervention_count > 0}
                <span class="k">interventions</span><span class="v">{selectedInfo.intervention_count}</span>
              {/if}
            </div>
            {#if selectedInfo.last_verdict_msg}
              <p class="last-msg">"{selectedInfo.last_verdict_msg}"</p>
            {/if}
            {#if selectedInfo.scorecard.note}
              <p class="score-note">{selectedInfo.scorecard.note}</p>
            {/if}
          {:else}
            <p class="hint">The Scrum Master is assessing — first scorecard arrives on the next tick (≤60s).</p>
          {/if}
        </section>
      {:else}
        <section class="card">
          <h3>Details</h3>
          <p class="hint">Pick a session on the left to see identity, location, process telemetry, and Scrum Master assessment.</p>
        </section>
      {/if}
    </aside>
  </div>
</div>

<!-- Spawn modal -->
{#if showSpawn}
  <div class="modal-backdrop" on:click={() => showSpawn = false}>
    <div class="modal" on:click|stopPropagation>
      <header class="modal-header">
        <h2>+ spawn agent</h2>
        <button class="close" on:click={() => showSpawn = false}>×</button>
      </header>

      <div class="modal-body">
        <div class="mode-toggle">
          <button class:active={spawnForm.mode==='fresh'} on:click={() => spawnForm.mode='fresh'}>Fresh session</button>
          <button class:active={spawnForm.mode==='resume'} on:click={() => { spawnForm.mode='resume'; refreshClaudeSessions(spawnForm.cwd); }}>Resume previous</button>
        </div>

        <label>
          <span>Working directory <em>(start typing to filter)</em></span>
          <div class="autocomplete">
            <input type="text" bind:value={folderQuery}
                   on:input={() => { folderSuppressed = false; }}
                   on:focus={() => { folderFocused = true; folderSuppressed = false; if (allFolders.length===0) loadAllFolders(); }}
                   on:blur={() => setTimeout(() => folderFocused = false, 200)}
                   on:keydown={(e) => { if (e.key === 'Escape') { folderSuppressed = true; e.target.blur(); } }}
                   placeholder="/home/openova/repos/yourproject" autocomplete="off"
                   data-testid="spawn-cwd-input" />
            {#if !folderSuppressed && folderFocused && folderResults.length > 0}
              <ul class="suggestions" data-testid="spawn-cwd-suggestions">
                {#each folderResults as f}
                  <li on:mousedown|preventDefault={() => pickFolder(f.path)}>
                    <code>{f.path}</code>
                    <small>{f.sessions} session{f.sessions===1?'':'s'}</small>
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        </label>

        {#if spawnForm.mode === 'resume'}
          <label style="margin-top:1rem;">
            <span>Resume which Claude session? <em>(searches uuid, cwd, first message)</em></span>
            <input type="text" bind:value={claudeQuery} placeholder="filter by uuid / cwd / message text…" autocomplete="off" />
          </label>
          <ul class="resume-list">
            {#each filteredResumes as s}
              <li class:active={spawnForm.resume_uuid===s.uuid} on:click={() => pickResume(s)}>
                <div class="resume-head">
                  <code class="uuid">{s.uuid.slice(0,8)}</code>
                  <span class="resume-cwd">{s.cwd}</span>
                  <span class="resume-mod">{new Date(s.modified).toLocaleString()}</span>
                </div>
                {#if s.first_message}<div class="resume-msg">"{s.first_message}"</div>{/if}
              </li>
            {/each}
            {#if filteredResumes.length === 0}<li class="empty">No matching sessions.</li>{/if}
          </ul>
        {/if}

        <div class="row">
          <label class="grow">
            <span>Session name <em>(auto from folder if blank)</em></span>
            <input type="text" bind:value={spawnForm.name}
                   placeholder={spawnForm.cwd ? autoName(spawnForm.cwd) : 'e.g. iogrid-1, review-bot'} />
          </label>
          <label>
            <span>Agent</span>
            <select bind:value={spawnForm.agent}>
              {#each AGENTS as a}<option value={a}>{a}</option>{/each}
            </select>
          </label>
        </div>

        <div class="row">
          <label>
            <span>Team</span>
            <input type="text" bind:value={spawnForm.team} placeholder="default" />
          </label>
          <label>
            <span>Role</span>
            <select bind:value={spawnForm.role}>
              <option value="worker">worker</option>
              <option value="shepherd">Scrum Master (opt-in 4-eyes)</option>
            </select>
          </label>
          <label class="check" title="When ON (default): the agent is told it's running inside chepherd and given the chepherd MCP tools so it can spawn peers, talk to other agents via @target, and alert you. When OFF: vanilla agent, no chepherd awareness — pure single-session usage.">
            <input type="checkbox" bind:checked={spawnForm.use_default_prompt} />
            <span>
              Make the agent chepherd-aware
              <em class="check-hint">— knows about peers, can use MCP tools, observed by the Scrum Master (recommended)</em>
            </span>
          </label>
        </div>

        {#if spawnError}<div class="error">{spawnError}</div>{/if}
      </div>

      <footer class="modal-footer">
        <button class="ghost" on:click={() => showSpawn = false}>Cancel</button>
        <button class="primary" on:click={submitSpawn} disabled={spawnBusy} data-testid="spawn-submit">
          {spawnBusy ? 'Spawning…' : (spawnForm.mode==='resume' ? 'Resume session' : 'Spawn agent')}
        </button>
      </footer>
    </div>
  </div>
{/if}

<!-- Confirm dialog — replaces window.confirm/alert/prompt -->
{#if confirmDialog}
  <div class="modal-backdrop" on:click={() => confirmDialog = null} data-testid="confirm-backdrop">
    <div class="modal confirm" on:click|stopPropagation>
      <header class="modal-header">
        <h2>{confirmDialog.title}</h2>
        <button class="close" on:click={() => confirmDialog = null}>×</button>
      </header>
      <div class="modal-body">
        <p style="color:#ccc; line-height:1.5;">{confirmDialog.body}</p>
      </div>
      <footer class="modal-footer">
        <button class="ghost" on:click={() => confirmDialog = null} data-testid="confirm-cancel">Cancel</button>
        <button class={confirmDialog.danger ? 'danger' : 'primary'}
                on:click={async () => { const fn = confirmDialog.onConfirm; confirmDialog = null; if (fn) await fn(); }}
                data-testid="confirm-ok">
          {confirmDialog.confirmLabel || 'Confirm'}
        </button>
      </footer>
    </div>
  </div>
{/if}

<style>
  /* Theme tokens — :global(html[data-theme=...]) lets us flip everything
     from one source of truth on the html element. */
  :global(html[data-theme="dark"]) {
    --bg: #0a0a0a;
    --bg-elev: #111;
    --bg-input: #0a0a0a;
    --border: #1e1e1e;
    --border-strong: #2a2a2a;
    --fg: #f5f5f5;
    --fg-muted: #aaa;
    --fg-faint: #666;
    --accent: #ffa500;        /* chepherd orange */
    --accent-2: #87ceeb;      /* Scrum Master blue (#292 — legacy class name was .shepherd) */
    --danger: #ff6b6b;
    --select-bg: #1a2530;
    --select-border: #5f9ea0;
    --scrollbar-track: transparent;
    --scrollbar-thumb: #2a2a2a;
    --scrollbar-thumb-hover: #3a3a3a;
  }
  :global(html[data-theme="light"]) {
    --bg: #fafafa;
    --bg-elev: #ffffff;
    --bg-input: #ffffff;
    --border: #e5e7eb;
    --border-strong: #cbd5e1;
    --fg: #1a1a1a;
    --fg-muted: #555;
    --fg-faint: #888;
    --accent: #c97900;        /* darker orange for AA contrast on light */
    --accent-2: #2563eb;
    --danger: #c92020;
    --select-bg: #e0f2fe;
    --select-border: #2563eb;
    --scrollbar-track: transparent;
    --scrollbar-thumb: #cbd5e1;
    --scrollbar-thumb-hover: #94a3b8;
  }
  :global(html) { background: var(--bg); }
  :global(body) { background: var(--bg); color: var(--fg); }

  /* Modern scrollbars — replace the chunky default with a thin track
     that fades in on hover. Webkit + Firefox both supported. */
  :global(*) {
    scrollbar-width: thin;
    scrollbar-color: var(--scrollbar-thumb) var(--scrollbar-track);
  }
  :global(*::-webkit-scrollbar) { width: 12px; height: 12px; }
  :global(*::-webkit-scrollbar-track) { background: var(--scrollbar-track); }
  :global(*::-webkit-scrollbar-thumb) {
    background: var(--scrollbar-thumb);
    border-radius: 10px;
    border: 2px solid var(--bg);
    min-height: 40px;
  }
  :global(*::-webkit-scrollbar-thumb:hover) { background: var(--scrollbar-thumb-hover); }
  :global(*::-webkit-scrollbar-thumb:active) { background: var(--accent); }
  :global(*::-webkit-scrollbar-corner) { background: transparent; }

  /* xterm.js renders its own scrollable .xterm-viewport that dodges
     the global :global(*) selector due to specificity. Target it
     explicitly so the center pane scrollbar matches the rest. */
  :global(.xterm-viewport::-webkit-scrollbar) { width: 12px; }
  :global(.xterm-viewport::-webkit-scrollbar-track) { background: var(--scrollbar-track); }
  :global(.xterm-viewport::-webkit-scrollbar-thumb) {
    background: var(--scrollbar-thumb);
    border-radius: 10px;
    border: 2px solid var(--bg);
    min-height: 40px;
  }
  :global(.xterm-viewport::-webkit-scrollbar-thumb:hover) { background: var(--scrollbar-thumb-hover); }
  :global(.xterm-viewport::-webkit-scrollbar-thumb:active) { background: var(--accent); }
  :global(.xterm-viewport) { scrollbar-width: thin; scrollbar-color: var(--scrollbar-thumb) var(--scrollbar-track); }

  .dashboard { display: flex; flex-direction: column; height: 100vh; color: var(--fg); background: var(--bg); font-size: 14px; }

  .topbar { display: flex; align-items: center; gap: 1.2rem; padding: 0.6rem 1.2rem; background: var(--bg-elev); border-bottom: 1px solid var(--border); }
  .topbar .brand { font-size: 1.15rem; color: var(--accent); text-decoration: none; font-weight: 600; }
  .topbar .stats { flex: 1; color: var(--fg-muted); font-size: 0.9rem; }
  .topbar .warn { color: var(--danger); margin-left: 0.4rem; }
  .topbar .links { display: flex; gap: 1rem; }
  .topbar .links a { color: var(--fg-muted); text-decoration: none; font-size: 0.9rem; }
  .topbar .links a:hover { color: var(--accent); }
  button.primary { padding: 0.45rem 1rem; background: var(--accent); color: #000; border: none; border-radius: 6px; font-weight: 600; cursor: pointer; font-size: 0.9rem; }
  button.primary:hover { filter: brightness(1.12); }
  button.primary:disabled { opacity: 0.5; cursor: not-allowed; }
  button.secondary { padding: 0.45rem 0.9rem; background: var(--bg-elev); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; cursor: pointer; font-size: 0.9rem; }
  button.secondary:hover { filter: brightness(1.1); }
  button.danger { padding: 0.45rem 0.9rem; background: transparent; color: var(--danger); border: 1px solid var(--danger); border-radius: 6px; cursor: pointer; font-size: 0.9rem; opacity: 0.85; }
  button.danger:hover { opacity: 1; background: rgba(255,107,107,0.08); }
  button.ghost { padding: 0.45rem 1rem; background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 6px; cursor: pointer; font-size: 0.9rem; }
  button.icon-btn { background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 6px; width: 34px; height: 34px; cursor: pointer; font-size: 1.05rem; display: flex; align-items: center; justify-content: center; }
  button.icon-btn:hover { color: var(--accent); border-color: var(--accent); }

  .body { display: flex; flex: 1; min-height: 0; overflow: hidden; }

  /* Left pane */
  .left { width: 240px; min-width: 240px; background: var(--bg); border-right: 1px solid var(--border); padding: 0.9rem 1rem; overflow-y: auto; }
  .left h2 { font-size: 0.78rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.07em; margin: 0 0 0.4rem 0; font-weight: 600; }
  .left h2 .count { color: var(--fg-faint); font-weight: normal; }
  .session-list { list-style: none; padding: 0; margin: 0; }
  .session-list li { padding: 0.55rem 0.65rem; border-radius: 6px; cursor: pointer; margin-bottom: 0.25rem; border: 1px solid transparent; }
  .session-list li:hover { background: var(--bg-elev); border-color: var(--border); }
  .session-list li.selected { background: var(--select-bg); border-color: var(--select-border); }
  .session-list li.paused { opacity: 0.6; }
  .session-list li.empty { color: var(--fg-faint); font-size: 0.85rem; cursor: default; padding: 0.6rem 0.4rem; }
  .session-list li.empty:hover { background: transparent; border-color: transparent; }
  .row1 { display: flex; align-items: center; gap: 0.4rem; }
  .row1 .dot { font-size: 0.95rem; }
  .row1 .dot.worker { color: var(--accent-2); }
  .row1 .dot.scrummaster { color: var(--accent); }
  .row1 .name { font-weight: 600; flex: 1; }
  .row2 { font-size: 0.8rem; color: var(--fg-muted); margin-top: 0.2rem; padding-left: 1.3rem; }
  .badge { font-size: 0.7rem; padding: 0.1rem 0.4rem; border-radius: 3px; background: var(--border-strong); color: var(--fg-muted); }

  .inbox-h2 { display: flex; align-items: center; gap: 0.5rem; }
  .unread-badge { background: var(--accent); color: #000; border-radius: 9px; padding: 0.05rem 0.5rem; font-size: 0.72rem; font-weight: 700; }
  .link-btn { background: transparent; border: none; color: var(--accent-2); cursor: pointer; font-size: 0.74rem; padding: 0; text-decoration: underline; margin-left: auto; }
  .link-btn:hover { color: var(--accent); }
  .inbox { list-style: none; padding: 0; margin: 0; }
  .inbox li { padding: 0.5rem 0.55rem; font-size: 0.84rem; color: var(--fg-muted); border-left: 3px solid transparent; border-bottom: 1px solid var(--border); cursor: pointer; transition: background-color 0.15s; }
  .inbox li:hover { background: var(--bg-elev); }
  .inbox li.unread { border-left-color: var(--accent); color: var(--fg); background: rgba(255,165,0,0.04); }
  .inbox li.unread strong { color: var(--accent); }
  .inbox-meta { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 0.2rem; }
  .inbox-meta strong { font-size: 0.85rem; color: var(--accent-2); font-weight: 600; }
  .inbox-when { font-size: 0.72rem; color: var(--fg-faint); }
  .inbox-body { line-height: 1.35; }

  /* Center pane */
  .center { flex: 1; display: flex; flex-direction: column; background: var(--bg); min-width: 0; min-height: 0; }
  .center .title { display: flex; align-items: center; justify-content: space-between; gap: 0.8rem; padding: 0.5rem 1rem; background: var(--bg-elev); border-bottom: 1px solid var(--border); color: var(--fg); font-family: ui-monospace, monospace; font-size: 0.88rem; flex-shrink: 0; }
  .center .title-left { display: flex; align-items: center; min-width: 0; overflow: hidden; }
  .center .title-name { font-weight: 600; }
  .center .title .dot { color: var(--accent-2); margin-right: 0.4rem; }
  .center .title .dot.scrummaster { color: var(--accent); }
  .center .title .subtitle { color: var(--fg-muted); font-size: 0.82rem; margin-left: 0.5rem; white-space: nowrap; }
  .center .title-actions { display: flex; gap: 0.4rem; flex-shrink: 0; }
  .center .title-actions button { font-size: 0.82rem; padding: 0.32rem 0.7rem; }
  .center .term { flex: 1; padding: 0.4rem 0.5rem; min-height: 0; overflow: hidden; }
  .center .term :global(.xterm) { height: 100%; }
  .center .term :global(.xterm-viewport) { height: 100% !important; }

  /* Right pane */
  .right { width: 280px; min-width: 280px; background: var(--bg); border-left: 1px solid var(--border); padding: 0.9rem 0.9rem; overflow-y: auto; display: flex; flex-direction: column; gap: 0.75rem; }
  .right .card { background: var(--bg-elev); border: 1px solid var(--border); border-radius: 8px; padding: 0.75rem 0.9rem; }
  .right .card h3 { font-size: 0.74rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.07em; margin: 0 0 0.55rem 0; font-weight: 600; }
  .right .card .hint { color: var(--fg-faint); font-size: 0.85rem; margin: 0; line-height: 1.4; }
  .right dl { margin: 0; }
  .right dt { color: var(--fg-muted); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.04em; margin-top: 0.5rem; }
  .right dt:first-child { margin-top: 0; }
  .right dd { margin: 0.15rem 0 0 0; color: var(--fg); font-size: 0.9rem; word-break: break-word; }
  .right dd code { font-size: 0.85rem; color: var(--accent-2); }
  .right dd code.cwd { font-size: 0.78rem; word-break: break-all; color: var(--accent-2); }
  .role-pill { display: inline-block; padding: 0.05rem 0.5rem; border-radius: 9px; font-size: 0.74rem; background: var(--accent-2); color: #000; font-weight: 600; }
  .role-pill.scrummaster { background: var(--accent); color: #000; }

  /* Dense key:value grid — replaces flat <dl> with inline label-value rows.
     Each row is 2 columns: label (auto, capped) | value (1fr).
     Wrap multi-line cwd/uuid into the v cell only. */
  .kv { display: grid; grid-template-columns: minmax(60px, auto) 1fr; column-gap: 0.6rem; row-gap: 0.32rem; align-items: baseline; }
  .kv .k { color: var(--fg-muted); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.04em; }
  .kv .v { color: var(--fg); font-size: 0.88rem; word-break: break-word; min-width: 0; }
  .kv .v code { font-size: 0.82rem; color: var(--accent-2); }
  .kv .v code.cwd, .kv .v code.uuid { font-size: 0.78rem; word-break: break-all; }
  .kv .v.wrap { word-break: break-all; }
  .kv .v a { color: var(--accent); text-decoration: none; }
  .kv .v a:hover { text-decoration: underline; }

  .verdict { display: inline-block; padding: 0.04rem 0.45rem; border-radius: 4px; font-size: 0.72rem; font-weight: 600; }
  .verdict-silent { background: rgba(150,150,150,0.18); color: var(--fg-muted); }
  .verdict-praise { background: rgba(52,211,153,0.18); color: #34d399; }
  .verdict-coach  { background: rgba(255,165,0,0.18); color: var(--accent); }
  .verdict-intervene { background: rgba(255,107,107,0.18); color: var(--danger); }

  .status-exited { color: var(--fg-faint); }
  .last-msg { color: var(--fg-muted); font-size: 0.82rem; font-style: italic; margin: 0.5rem 0 0 0; line-height: 1.35; padding-left: 0.4rem; border-left: 2px solid var(--border-strong); }

  .score-note { font-size: 0.72rem; color: var(--fg-faint); margin: 0.55rem 0 0 0; font-style: italic; text-align: center; }

  /* Modal */
  .modal-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.65); display: flex; align-items: center; justify-content: center; z-index: 1000; backdrop-filter: blur(2px); }
  .modal { width: min(760px, 94vw); max-height: 92vh; background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 10px; display: flex; flex-direction: column; overflow: hidden; box-shadow: 0 12px 40px rgba(0,0,0,0.4); }
  .modal-header { display: flex; justify-content: space-between; align-items: center; padding: 0.95rem 1.4rem; border-bottom: 1px solid var(--border); }
  .modal-header h2 { margin: 0; color: var(--accent); font-size: 1.05rem; }
  .modal-header .close { background: transparent; color: var(--fg-muted); border: none; font-size: 1.65rem; cursor: pointer; line-height: 1; }
  .modal-header .close:hover { color: var(--fg); }
  .modal-body { padding: 1rem 1.4rem; overflow-y: auto; flex: 1; }
  .modal-body label { display: block; margin-top: 0.75rem; }
  .modal-body label.check { display: flex; align-items: center; gap: 0.5rem; margin-top: 1.7rem; }
  .modal-body label.check span { margin-bottom: 0; font-size: 0.88rem; color: var(--fg); text-transform: none; letter-spacing: normal; font-weight: normal; }
  .modal-body .check-hint { display: block; font-size: 0.78rem; color: var(--fg-faint); font-style: normal; margin-top: 0.15rem; }
  .modal-body label span { display: block; font-size: 0.78rem; color: var(--fg-muted); margin-bottom: 0.25rem; text-transform: uppercase; letter-spacing: 0.04em; }
  .modal-body label em { color: var(--fg-faint); font-style: normal; text-transform: none; }
  .modal-body input[type=text], .modal-body select { width: 100%; padding: 0.5rem 0.65rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; font-family: ui-monospace, monospace; font-size: 0.9rem; box-sizing: border-box; }
  .modal-body input[type=text]:focus, .modal-body select:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px rgba(255,165,0,0.18); }
  .modal-body .row { display: flex; gap: 0.6rem; }
  .modal-body .row label.grow { flex: 1; }
  .modal-footer { padding: 0.85rem 1.4rem; border-top: 1px solid var(--border); display: flex; justify-content: flex-end; gap: 0.6rem; }

  .mode-toggle { display: flex; gap: 0.25rem; background: var(--bg); border-radius: 6px; padding: 0.22rem; border: 1px solid var(--border-strong); margin-bottom: 0.5rem; }
  .mode-toggle button { flex: 1; padding: 0.45rem; background: transparent; color: var(--fg-muted); border: none; border-radius: 4px; cursor: pointer; font-size: 0.9rem; }
  .mode-toggle button.active { background: var(--accent); color: #000; font-weight: 600; }

  .autocomplete { position: relative; }
  .suggestions { position: absolute; top: calc(100% + 4px); left: 0; right: 0; background: var(--bg-elev); border: 1px solid var(--accent); border-radius: 6px; list-style: none; padding: 0.25rem; margin: 0; max-height: 280px; overflow-y: auto; z-index: 10; box-shadow: 0 8px 24px rgba(0,0,0,0.3); }
  .suggestions li { padding: 0.5rem 0.6rem; cursor: pointer; display: flex; align-items: center; gap: 0.5rem; border-radius: 4px; }
  .suggestions li:hover { background: var(--select-bg); }
  .suggestions li code { color: var(--accent-2); font-size: 0.85rem; flex: 1; }
  .suggestions li small { color: var(--fg-faint); font-size: 0.75rem; }

  .resume-list { list-style: none; padding: 0; margin: 0.5rem 0 0 0; max-height: 280px; overflow-y: auto; }
  .resume-list li { padding: 0.55rem 0.7rem; border: 1px solid var(--border-strong); border-radius: 6px; margin-bottom: 0.35rem; cursor: pointer; background: var(--bg-input); }
  .resume-list li:hover { border-color: var(--select-border); }
  .resume-list li.active { border-color: var(--accent); background: var(--select-bg); }
  .resume-list li.empty { color: var(--fg-faint); cursor: default; text-align: center; padding: 0.7rem; }
  .resume-head { display: flex; gap: 0.5rem; align-items: center; font-size: 0.8rem; }
  .resume-head .uuid { color: var(--accent-2); }
  .resume-head .resume-cwd { color: var(--fg); flex: 1; word-break: break-all; }
  .resume-head .resume-mod { color: var(--fg-muted); }
  .resume-msg { color: var(--fg-muted); font-size: 0.83rem; margin-top: 0.25rem; font-style: italic; }

  .error { margin-top: 0.7rem; padding: 0.5rem 0.8rem; background: rgba(255,107,107,0.1); border: 1px solid var(--danger); color: var(--danger); border-radius: 6px; font-size: 0.85rem; }
</style>

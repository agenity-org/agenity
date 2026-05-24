<!--
  chepherd v0.5 dashboard — 3-pane layout (sessions / xterm / details) +
  proper Spawn modal with folder picker + claude-session resume picker.
  Talks to the chepherd-v05 runtime via /api/v1/* + WS attach.
-->
<script>
  import { onMount } from 'svelte';

  let sessions = $state([]);
  let selectedName = $state(null);
  let selectedInfo = $state(null);
  let connected = $state(false);
  let inbox = $state([]);
  let showSpawn = $state(false);

  // Spawn form state
  let spawnForm = $state({
    name: '',
    agent: 'claude-code',
    tribe: 'default',
    role: 'worker',
    cwd: '',
    mode: 'fresh',          // 'fresh' | 'resume'
    resume_uuid: '',
  });
  let recentFolders = $state([]);
  let claudeSessions = $state([]);
  let spawnError = $state('');
  let spawnBusy = $state(false);

  const API = '/api/v1';
  const AGENTS = ['claude-code', 'qwen-code', 'aider', 'opencode', 'cursor-agent', 'little-coder', 'sovereign-shell'];

  async function refreshSessions() {
    try {
      const res = await fetch(`${API}/sessions`);
      const data = await res.json();
      sessions = data.sessions || [];
      connected = true;
      // Refresh selected session info if it still exists
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

  async function refreshRecentFolders() {
    try {
      const res = await fetch(`${API}/folders/recent`);
      const data = await res.json();
      recentFolders = data.folders || [];
    } catch { recentFolders = []; }
  }

  async function refreshClaudeSessions(cwd) {
    try {
      const url = cwd ? `${API}/claude-sessions?cwd=${encodeURIComponent(cwd)}` : `${API}/claude-sessions`;
      const res = await fetch(url);
      const data = await res.json();
      claudeSessions = data.sessions || [];
    } catch { claudeSessions = []; }
  }

  let term = null;
  let termContainer = null;
  let ws = null;

  async function attachTo(name) {
    if (selectedName === name) return;
    selectedName = name;
    selectedInfo = sessions.find(s => s.name === name) || null;
    if (ws) ws.close();
    if (term) term.dispose();

    const { Terminal } = await import('@xterm/xterm');
    const { FitAddon } = await import('@xterm/addon-fit');
    term = new Terminal({
      convertEol: true,
      fontFamily: 'ui-monospace, "JetBrains Mono", monospace',
      fontSize: 13,
      theme: { background: '#0a0a0a' },
      cursorBlink: true,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(termContainer);
    fit.fit();

    const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${wsProto}//${window.location.host}${API}/sessions/${name}/attach`);
    ws.binaryType = 'arraybuffer';
    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) {
        term.write(new Uint8Array(ev.data));
      } else {
        term.write(ev.data);
      }
    };
    term.onData((data) => {
      if (ws && ws.readyState === WebSocket.OPEN) ws.send(data);
    });
    window.addEventListener('resize', () => fit.fit());
  }

  function openSpawn() {
    spawnForm = { name: '', agent: 'claude-code', tribe: 'default', role: 'worker', cwd: '', mode: 'fresh', resume_uuid: '' };
    spawnError = '';
    showSpawn = true;
    refreshRecentFolders();
    refreshClaudeSessions(null);
  }

  function pickFolder(path) {
    spawnForm.cwd = path;
    if (spawnForm.mode === 'resume') refreshClaudeSessions(path);
  }

  function pickResume(s) {
    spawnForm.resume_uuid = s.uuid;
    spawnForm.cwd = s.cwd;
  }

  async function submitSpawn() {
    spawnError = '';
    if (!spawnForm.name) { spawnError = 'Name required'; return; }
    spawnBusy = true;
    const body = {
      name: spawnForm.name,
      agent: spawnForm.agent,
      tribe: spawnForm.tribe || 'default',
      role: spawnForm.role,
      cwd: spawnForm.cwd,
    };
    if (spawnForm.mode === 'resume' && spawnForm.resume_uuid) {
      body.resume_uuid = spawnForm.resume_uuid;
    }
    try {
      const res = await fetch(`${API}/sessions`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
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
    } catch (e) {
      spawnError = String(e);
    } finally {
      spawnBusy = false;
    }
  }

  async function pauseSession(paused) {
    if (!selectedName) return;
    await fetch(`${API}/sessions/${selectedName}/pause`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ paused }),
    });
    refreshSessions();
  }

  async function stopSession() {
    if (!selectedName) return;
    if (!confirm(`Stop session "${selectedName}"? The agent process will be terminated.`)) return;
    await fetch(`${API}/sessions/${selectedName}`, { method: 'DELETE' });
    if (term) term.dispose();
    if (ws) ws.close();
    selectedName = null;
    selectedInfo = null;
    refreshSessions();
  }

  function ageString(createdAt) {
    if (!createdAt) return '—';
    const ms = Date.now() - new Date(createdAt).getTime();
    const s = Math.floor(ms/1000);
    if (s < 60) return `${s}s`;
    const m = Math.floor(s/60);
    if (m < 60) return `${m}m`;
    const h = Math.floor(m/60);
    if (h < 24) return `${h}h ${m%60}m`;
    const d = Math.floor(h/24);
    return `${d}d ${h%24}h`;
  }

  onMount(() => {
    refreshSessions();
    refreshInbox();
    const interval = setInterval(() => { refreshSessions(); refreshInbox(); }, 2000);
    return () => clearInterval(interval);
  });
</script>

<div class="dashboard">
  <header class="dash-header">
    <h1>✻ chepherd</h1>
    <div class="stats">
      {sessions.length} session{sessions.length === 1 ? '' : 's'} ·
      {[...new Set(sessions.map(s => s.tribe))].length} tribe{[...new Set(sessions.map(s => s.tribe))].length === 1 ? '' : 's'}
      {#if !connected}<span class="warn">· runtime offline</span>{/if}
    </div>
    <div class="actions">
      <button class="primary" on:click={openSpawn}>+ spawn agent</button>
    </div>
  </header>

  <div class="body">
    <aside class="sidebar">
      <h2>Sessions</h2>
      <ul class="session-list">
        {#each sessions as s (s.id)}
          <li class:selected={selectedName === s.name} class:paused={s.paused} on:click={() => attachTo(s.name)}>
            <div class="row1">
              <span class="icon" class:shepherd={s.role==='shepherd'}>{s.role === 'shepherd' ? '✻' : '●'}</span>
              <span class="name">{s.name}</span>
              {#if s.paused}<span class="badge paused-badge">paused</span>{/if}
            </div>
            <div class="row2">
              <span class="agent">{s.agent}</span> · <span class="tribe">{s.tribe}</span> · <span class="age">{ageString(s.created_at)}</span>
            </div>
          </li>
        {/each}
        {#if sessions.length === 0}
          <li class="empty">No sessions. Click "+ spawn agent" to start one.</li>
        {/if}
      </ul>

      {#if inbox.length}
        <h2 style="margin-top: 2rem;">Human inbox ({inbox.length})</h2>
        <ul class="inbox">
          {#each inbox.slice(-5).reverse() as m}
            <li><strong>@{m.from}</strong> · {m.body}</li>
          {/each}
        </ul>
      {/if}
    </aside>

    <section class="center">
      <div class="title">
        {#if selectedName}
          <span class="dot" class:shepherd={selectedInfo?.role==='shepherd'}>●</span>
          {selectedName}
          <span class="subtitle">— live attach via WebSocket</span>
        {:else}
          <span class="subtitle">Pick a session ← or click "+ spawn agent" to create one</span>
        {/if}
      </div>
      <div class="term" bind:this={termContainer}></div>
    </section>

    <aside class="details">
      {#if selectedInfo}
        <h2>Session details</h2>
        <dl>
          <dt>Name</dt><dd><code>{selectedInfo.name}</code></dd>
          <dt>ID</dt><dd><code style="font-size:0.75rem;">{selectedInfo.id}</code></dd>
          <dt>Agent</dt><dd>{selectedInfo.agent}</dd>
          <dt>Role</dt><dd>{selectedInfo.role}</dd>
          <dt>Tribe</dt><dd>{selectedInfo.tribe}</dd>
          <dt>Cwd</dt><dd><code style="font-size:0.78rem;">{selectedInfo.cwd || '—'}</code></dd>
          <dt>Started</dt><dd>{ageString(selectedInfo.created_at)} ago</dd>
          <dt>Status</dt><dd>{selectedInfo.paused ? 'paused ⏸' : 'live'}</dd>
          {#if selectedInfo.shepherding && selectedInfo.shepherding.length}
            <dt>Watching</dt><dd>{selectedInfo.shepherding.join(', ')}</dd>
          {/if}
        </dl>

        <h2 style="margin-top:1.5rem;">Actions</h2>
        <div class="action-buttons">
          {#if selectedInfo.paused}
            <button class="secondary" on:click={() => pauseSession(false)}>▶ resume</button>
          {:else}
            <button class="secondary" on:click={() => pauseSession(true)}>⏸ pause</button>
          {/if}
          <button class="danger" on:click={stopSession}>⨯ stop</button>
        </div>
      {:else}
        <h2>Details</h2>
        <p style="color:#888;font-size:0.9rem;">Select a session on the left to see its details and act on it.</p>
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
          <button class:active={spawnForm.mode==='fresh'} on:click={() => spawnForm.mode='fresh'}>Fresh agent</button>
          <button class:active={spawnForm.mode==='resume'} on:click={() => { spawnForm.mode='resume'; refreshClaudeSessions(spawnForm.cwd); }}>Resume previous</button>
        </div>

        <label>
          <span>Session name <em>(unique @-address)</em></span>
          <input type="text" bind:value={spawnForm.name} placeholder="e.g. iogrid-1, eva, review-bot" />
        </label>

        <div class="row">
          <label class="grow">
            <span>Agent</span>
            <select bind:value={spawnForm.agent}>
              {#each AGENTS as a}<option value={a}>{a}</option>{/each}
            </select>
          </label>
          <label>
            <span>Tribe</span>
            <input type="text" bind:value={spawnForm.tribe} placeholder="default" />
          </label>
          <label>
            <span>Role</span>
            <select bind:value={spawnForm.role}>
              <option value="worker">worker</option>
              <option value="shepherd">shepherd</option>
            </select>
          </label>
        </div>

        <label>
          <span>Working directory</span>
          <input type="text" bind:value={spawnForm.cwd} placeholder="/home/openova/repos/yourproject" />
        </label>

        {#if recentFolders.length}
          <div class="recent">
            <div class="recent-label">Recent project folders <em>(click to use)</em></div>
            <div class="chip-list">
              {#each recentFolders.slice(0,10) as f}
                <button class="chip" class:active={spawnForm.cwd===f.path} on:click={() => pickFolder(f.path)}>
                  <code>{f.path}</code>
                  <small>{f.sessions} session{f.sessions===1?'':'s'}</small>
                </button>
              {/each}
            </div>
          </div>
        {/if}

        {#if spawnForm.mode === 'resume'}
          <div class="resume-section">
            <div class="recent-label">Resume which Claude session{spawnForm.cwd ? ` in ${spawnForm.cwd}` : ''}?</div>
            {#if claudeSessions.length}
              <ul class="resume-list">
                {#each claudeSessions.slice(0,20) as s}
                  <li class:active={spawnForm.resume_uuid===s.uuid} on:click={() => pickResume(s)}>
                    <div class="resume-head">
                      <code class="uuid">{s.uuid.slice(0,8)}</code>
                      <span class="resume-cwd">{s.cwd}</span>
                      <span class="resume-mod">{new Date(s.modified).toLocaleString()}</span>
                    </div>
                    {#if s.first_message}
                      <div class="resume-msg">"{s.first_message}"</div>
                    {/if}
                  </li>
                {/each}
              </ul>
            {:else}
              <p style="color:#888;">No Claude sessions found{spawnForm.cwd ? ' for this folder' : ''}.</p>
            {/if}
          </div>
        {/if}

        {#if spawnError}
          <div class="error">{spawnError}</div>
        {/if}
      </div>

      <footer class="modal-footer">
        <button class="ghost" on:click={() => showSpawn = false}>Cancel</button>
        <button class="primary" on:click={submitSpawn} disabled={spawnBusy}>
          {spawnBusy ? 'Spawning…' : (spawnForm.mode==='resume' ? 'Resume session' : 'Spawn agent')}
        </button>
      </footer>
    </div>
  </div>
{/if}

<style>
  .dashboard { display: flex; flex-direction: column; height: 100vh; color: #f5f5f5; font-family: ui-sans-serif, system-ui, sans-serif; background: #0a0a0a; }
  .dash-header { display: flex; align-items: center; padding: 0.75rem 1.5rem; background: #111; border-bottom: 1px solid #1e1e1e; }
  .dash-header h1 { font-size: 1.3rem; color: #ffa500; margin: 0 2rem 0 0; font-weight: 600; }
  .stats { flex: 1; color: #aaa; font-size: 0.9rem; }
  .warn { color: #ff6b6b; margin-left: 0.5rem; }
  button.primary { padding: 0.5rem 1rem; background: #ffa500; color: #000; border: none; border-radius: 4px; font-weight: 600; cursor: pointer; }
  button.primary:hover { background: #ffb733; }
  button.primary:disabled { opacity: 0.5; cursor: not-allowed; }
  button.secondary { padding: 0.45rem 0.9rem; background: #1a1a1a; color: #f5f5f5; border: 1px solid #2a2a2a; border-radius: 4px; cursor: pointer; }
  button.secondary:hover { background: #222; }
  button.danger { padding: 0.45rem 0.9rem; background: #2a1414; color: #ff6b6b; border: 1px solid #4a1f1f; border-radius: 4px; cursor: pointer; }
  button.danger:hover { background: #3a1818; }
  button.ghost { padding: 0.5rem 1rem; background: transparent; color: #aaa; border: 1px solid #2a2a2a; border-radius: 4px; cursor: pointer; }

  .body { display: flex; flex: 1; overflow: hidden; }

  /* Left pane */
  .sidebar { width: 280px; min-width: 280px; background: #0a0a0a; border-right: 1px solid #1e1e1e; padding: 1rem; overflow-y: auto; }
  .sidebar h2 { font-size: 0.78rem; color: #888; text-transform: uppercase; letter-spacing: 0.06em; margin: 0 0 0.5rem 0; font-weight: 600; }
  .session-list { list-style: none; padding: 0; margin: 0; }
  .session-list li { padding: 0.6rem 0.7rem; border-radius: 6px; cursor: pointer; margin-bottom: 0.25rem; border: 1px solid transparent; }
  .session-list li:hover { background: #151515; border-color: #222; }
  .session-list li.selected { background: #1a2530; border-color: #5f9ea0; }
  .session-list li.paused { opacity: 0.6; }
  .session-list li.empty { color: #666; font-size: 0.85rem; cursor: default; }
  .session-list li.empty:hover { background: transparent; border-color: transparent; }
  .row1 { display: flex; align-items: center; gap: 0.4rem; }
  .row1 .icon { color: #87ceeb; font-size: 0.9rem; }
  .row1 .icon.shepherd { color: #ffa500; }
  .row1 .name { font-weight: 600; flex: 1; }
  .row2 { font-size: 0.78rem; color: #888; margin-top: 0.2rem; padding-left: 1.3rem; }
  .badge { font-size: 0.7rem; padding: 0.1rem 0.4rem; border-radius: 3px; background: #333; color: #ccc; }
  .badge.paused-badge { background: #444; }

  .inbox { list-style: none; padding: 0; margin: 0; }
  .inbox li { padding: 0.5rem 0.4rem; font-size: 0.85rem; color: #ccc; border-bottom: 1px solid #1e1e1e; }
  .inbox li strong { color: #ffa500; }

  /* Center pane */
  .center { flex: 1; display: flex; flex-direction: column; background: #0a0a0a; min-width: 0; }
  .center .title { padding: 0.5rem 1rem; background: #111; border-bottom: 1px solid #1e1e1e; color: #f5f5f5; font-family: ui-monospace, monospace; font-size: 0.85rem; }
  .center .title .dot { color: #87ceeb; margin-right: 0.4rem; }
  .center .title .dot.shepherd { color: #ffa500; }
  .center .title .subtitle { color: #888; font-size: 0.8rem; margin-left: 0.5rem; }
  .center .term { flex: 1; padding: 0.5rem; min-height: 0; }

  /* Right pane */
  .details { width: 280px; min-width: 280px; background: #0a0a0a; border-left: 1px solid #1e1e1e; padding: 1rem; overflow-y: auto; }
  .details h2 { font-size: 0.78rem; color: #888; text-transform: uppercase; letter-spacing: 0.06em; margin: 0 0 0.6rem 0; font-weight: 600; }
  .details dl { margin: 0; }
  .details dt { color: #888; font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.04em; margin-top: 0.6rem; }
  .details dd { margin: 0.15rem 0 0 0; color: #f5f5f5; font-size: 0.9rem; word-break: break-all; }
  .details dd code { font-size: 0.85rem; color: #87ceeb; }
  .action-buttons { display: flex; flex-direction: column; gap: 0.4rem; }

  /* Modal */
  .modal-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.7); display: flex; align-items: center; justify-content: center; z-index: 1000; }
  .modal { width: min(720px, 92vw); max-height: 88vh; background: #111; border: 1px solid #2a2a2a; border-radius: 8px; display: flex; flex-direction: column; overflow: hidden; }
  .modal-header { display: flex; justify-content: space-between; align-items: center; padding: 1rem 1.5rem; border-bottom: 1px solid #1e1e1e; }
  .modal-header h2 { margin: 0; color: #ffa500; font-size: 1.1rem; }
  .modal-header .close { background: transparent; color: #888; border: none; font-size: 1.6rem; cursor: pointer; line-height: 1; }
  .modal-header .close:hover { color: #fff; }
  .modal-body { padding: 1.25rem 1.5rem; overflow-y: auto; flex: 1; }
  .modal-body label { display: block; margin-top: 0.9rem; }
  .modal-body label span { display: block; font-size: 0.78rem; color: #888; margin-bottom: 0.25rem; }
  .modal-body label em { color: #666; font-style: normal; }
  .modal-body input[type=text], .modal-body select { width: 100%; padding: 0.5rem 0.6rem; background: #0a0a0a; color: #f5f5f5; border: 1px solid #2a2a2a; border-radius: 4px; font-family: ui-monospace, monospace; font-size: 0.9rem; box-sizing: border-box; }
  .modal-body input[type=text]:focus, .modal-body select:focus { outline: none; border-color: #ffa500; }
  .modal-body .row { display: flex; gap: 0.6rem; }
  .modal-body .row label.grow { flex: 1; }
  .modal-footer { padding: 0.9rem 1.5rem; border-top: 1px solid #1e1e1e; display: flex; justify-content: flex-end; gap: 0.6rem; }

  .mode-toggle { display: flex; gap: 0.3rem; background: #0a0a0a; border-radius: 6px; padding: 0.25rem; border: 1px solid #2a2a2a; margin-bottom: 0.5rem; }
  .mode-toggle button { flex: 1; padding: 0.5rem; background: transparent; color: #888; border: none; border-radius: 4px; cursor: pointer; font-size: 0.9rem; }
  .mode-toggle button.active { background: #ffa500; color: #000; font-weight: 600; }

  .recent { margin-top: 1rem; }
  .recent-label { font-size: 0.78rem; color: #888; margin-bottom: 0.4rem; }
  .recent-label em { color: #666; font-style: normal; }
  .chip-list { display: flex; flex-wrap: wrap; gap: 0.35rem; }
  .chip { background: #0a0a0a; border: 1px solid #2a2a2a; border-radius: 4px; padding: 0.35rem 0.55rem; color: #ccc; font-size: 0.78rem; cursor: pointer; display: flex; flex-direction: column; align-items: flex-start; }
  .chip:hover { border-color: #ffa500; }
  .chip.active { border-color: #ffa500; background: #1f1810; }
  .chip code { color: #87ceeb; font-size: 0.78rem; }
  .chip small { color: #666; font-size: 0.7rem; margin-top: 0.15rem; }

  .resume-section { margin-top: 1rem; }
  .resume-list { list-style: none; padding: 0; margin: 0; max-height: 300px; overflow-y: auto; }
  .resume-list li { padding: 0.6rem 0.7rem; border: 1px solid #2a2a2a; border-radius: 5px; margin-bottom: 0.35rem; cursor: pointer; background: #0a0a0a; }
  .resume-list li:hover { border-color: #5f9ea0; }
  .resume-list li.active { border-color: #ffa500; background: #1f1810; }
  .resume-head { display: flex; gap: 0.6rem; align-items: center; font-size: 0.78rem; }
  .resume-head .uuid { color: #87ceeb; }
  .resume-head .resume-cwd { color: #ccc; flex: 1; word-break: break-all; }
  .resume-head .resume-mod { color: #888; }
  .resume-msg { color: #aaa; font-size: 0.82rem; margin-top: 0.3rem; font-style: italic; }

  .error { margin-top: 0.8rem; padding: 0.5rem 0.8rem; background: #2a1414; border: 1px solid #4a1f1f; color: #ff6b6b; border-radius: 4px; font-size: 0.85rem; }
</style>

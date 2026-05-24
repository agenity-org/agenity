<!--
  chepherd v0.5.1 dashboard. Issue #79 wave 2.
  - Single top bar: brand + stats + Docs/Download/GitHub + "+ spawn".
  - 3-pane body fills viewport vertically: sessions list / xterm / details.
  - Spawn modal: autocomplete folder input, auto-derived name, shepherd
    is opt-in (off by default), default system prompt opt-in.
  - Workers + Shepherds rendered as separate sections in the left list,
    so the operator can see who shepherds whom at a glance.
  - Scorecard panel in the right column (stub for now: chepherd needs an
    analyzer goroutine to populate G/V/F/E — filed as follow-up).
-->
<script>
  import { onMount } from 'svelte';

  let sessions = $state([]);
  let selectedName = $state(null);
  let selectedInfo = $state(null);
  let connected = $state(false);
  let inbox = $state([]);
  let showSpawn = $state(false);

  let spawnForm = $state({
    name: '',
    agent: 'claude-code',
    tribe: 'default',
    role: 'worker',
    cwd: '',
    mode: 'fresh',
    resume_uuid: '',
    use_default_prompt: false,
  });
  let folderQuery = $state('');
  let folderResults = $state([]);
  let allFolders = $state([]);
  let claudeSessions = $state([]);
  let claudeQuery = $state('');
  let spawnError = $state('');
  let spawnBusy = $state(false);

  const API = '/api/v1';
  const AGENTS = ['claude-code', 'qwen-code', 'aider', 'opencode', 'cursor-agent', 'little-coder', 'sovereign-shell'];

  // Worker/shepherd partition for the left pane.
  let workers = $derived(sessions.filter(s => s.role !== 'shepherd'));
  let shepherds = $derived(sessions.filter(s => s.role === 'shepherd'));

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

  async function loadAllFolders() {
    try {
      const res = await fetch(`${API}/folders/recent`);
      const data = await res.json();
      allFolders = data.folders || [];
      filterFolders();
    } catch {}
  }

  function filterFolders() {
    const q = folderQuery.trim().toLowerCase();
    if (!q) { folderResults = allFolders.slice(0, 8); return; }
    folderResults = allFolders.filter(f => f.path.toLowerCase().includes(q)).slice(0, 8);
  }

  async function refreshClaudeSessions(cwd) {
    try {
      const url = cwd ? `${API}/claude-sessions?cwd=${encodeURIComponent(cwd)}` : `${API}/claude-sessions`;
      const res = await fetch(url);
      const data = await res.json();
      claudeSessions = data.sessions || [];
    } catch { claudeSessions = []; }
  }

  // Auto-derive a unique session name from cwd basename if blank.
  function autoName(cwd) {
    const base = (cwd || '').split('/').filter(Boolean).pop() || 'agent';
    const slug = base.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/(^-|-$)/g, '');
    const existing = new Set(sessions.map(s => s.name));
    let candidate = slug;
    let n = 1;
    while (existing.has(candidate)) {
      n++;
      candidate = `${slug}-${n}`;
    }
    return candidate;
  }

  let term = null;
  let termContainer = null;
  let ws = null;
  let fitAddon = null;

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
    fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.open(termContainer);
    requestAnimationFrame(() => fitAddon.fit());

    const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${wsProto}//${window.location.host}${API}/sessions/${name}/attach`);
    ws.binaryType = 'arraybuffer';
    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) term.write(new Uint8Array(ev.data));
      else term.write(ev.data);
    };
    term.onData((d) => { if (ws && ws.readyState === WebSocket.OPEN) ws.send(d); });
  }

  function openSpawn() {
    spawnForm = { name: '', agent: 'claude-code', tribe: 'default', role: 'worker', cwd: '', mode: 'fresh', resume_uuid: '', use_default_prompt: false };
    folderQuery = '';
    claudeQuery = '';
    spawnError = '';
    showSpawn = true;
    loadAllFolders();
    refreshClaudeSessions(null);
  }

  function pickFolder(path) {
    spawnForm.cwd = path;
    folderQuery = path;
    folderResults = [];
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
    // Auto-name if blank
    if (!spawnForm.name && spawnForm.cwd) spawnForm.name = autoName(spawnForm.cwd);
    if (!spawnForm.name) { spawnError = 'Pick a folder so I can auto-name, or enter a name'; return; }
    spawnBusy = true;
    const body = {
      name: spawnForm.name,
      agent: spawnForm.agent,
      tribe: spawnForm.tribe || 'default',
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

  async function pauseSession(paused) {
    if (!selectedName) return;
    await fetch(`${API}/sessions/${selectedName}/pause`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ paused }),
    });
    refreshSessions();
  }

  async function stopSession() {
    if (!selectedName) return;
    if (!confirm(`Stop "${selectedName}"? The agent process will be terminated.`)) return;
    await fetch(`${API}/sessions/${selectedName}`, { method: 'DELETE' });
    if (term) term.dispose();
    if (ws) ws.close();
    selectedName = null;
    selectedInfo = null;
    refreshSessions();
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

  onMount(() => {
    refreshSessions(); refreshInbox();
    const interval = setInterval(() => { refreshSessions(); refreshInbox(); }, 2000);
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
      {shepherds.length} shepherd{shepherds.length === 1 ? '' : 's'}
      {#if !connected}<span class="warn">· runtime offline</span>{/if}
    </div>
    <nav class="links">
      <a href="/docs">Docs</a>
      <a href="/download">Download</a>
      <a href="https://github.com/chepherd/chepherd" target="_blank" rel="noopener">GitHub</a>
    </nav>
    <button class="primary" on:click={openSpawn}>+ spawn agent</button>
  </header>

  <div class="body">
    <!-- Left: Workers + Shepherds + Inbox -->
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
              <div class="row2">{s.agent} · {s.tribe} · {ageString(s.created_at)}</div>
            </li>
          {/each}
          {#if workers.length === 0}
            <li class="empty">No workers yet. Click "+ spawn agent" to start one.</li>
          {/if}
        </ul>
      </section>

      <section style="margin-top: 1.2rem;">
        <h2>Shepherds <span class="count">({shepherds.length})</span></h2>
        <ul class="session-list">
          {#each shepherds as s (s.id)}
            <li class:selected={selectedName === s.name} class:paused={s.paused} on:click={() => attachTo(s.name)}>
              <div class="row1">
                <span class="dot shepherd">✻</span>
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
          {#if shepherds.length === 0}
            <li class="empty">No shepherds. Spawn a shepherd to watch your workers (opt-in).</li>
          {/if}
        </ul>
      </section>

      {#if inbox.length}
        <section style="margin-top: 1.2rem;">
          <h2>Human inbox <span class="count">({inbox.length})</span></h2>
          <ul class="inbox">
            {#each inbox.slice(-5).reverse() as m}
              <li><strong>@{m.from}</strong> · {m.body}</li>
            {/each}
          </ul>
        </section>
      {/if}
    </aside>

    <!-- Center: xterm fills available height -->
    <section class="center">
      <div class="title">
        {#if selectedName}
          <span class="dot" class:shepherd={selectedInfo?.role==='shepherd'}>{selectedInfo?.role==='shepherd' ? '✻' : '●'}</span>
          {selectedName}
          <span class="subtitle">— live attach via WebSocket</span>
        {:else}
          <span class="subtitle">Pick a session ← or click "+ spawn agent" to create one</span>
        {/if}
      </div>
      <div class="term" bind:this={termContainer}></div>
    </section>

    <!-- Right: details + scorecard + actions -->
    <aside class="right">
      {#if selectedInfo}
        <h2>Session</h2>
        <dl>
          <dt>Name</dt><dd><code>{selectedInfo.name}</code></dd>
          <dt>Agent</dt><dd>{selectedInfo.agent}</dd>
          <dt>Role</dt><dd>{selectedInfo.role}</dd>
          <dt>Tribe</dt><dd>{selectedInfo.tribe}</dd>
          <dt>Cwd</dt><dd><code class="cwd">{selectedInfo.cwd || '—'}</code></dd>
          <dt>Started</dt><dd>{ageString(selectedInfo.created_at)} ago</dd>
          <dt>Status</dt><dd>{selectedInfo.paused ? 'paused ⏸' : 'live'}</dd>
          {#if selectedInfo.shepherding && selectedInfo.shepherding.length}
            <dt>Watching</dt><dd>{selectedInfo.shepherding.join(', ')}</dd>
          {/if}
        </dl>

        <h2 style="margin-top:1.3rem;">Scorecard</h2>
        <div class="scorecard">
          <div class="score-row"><span>G — goal clarity</span><span class="score-val">—</span></div>
          <div class="score-row"><span>V — velocity</span><span class="score-val">—</span></div>
          <div class="score-row"><span>F — focus</span><span class="score-val">—</span></div>
          <div class="score-row"><span>E — end-state proximity</span><span class="score-val">—</span></div>
          <p class="score-note">Analyzer goroutine not yet wired (follow-up).</p>
        </div>

        <h2 style="margin-top:1.3rem;">Actions</h2>
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
        <p style="color:#888;font-size:0.88rem;">Pick a session on the left to see details + actions.</p>
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
            <input type="text" bind:value={folderQuery} on:input={filterFolders}
                   on:focus={() => { if (allFolders.length===0) loadAllFolders(); filterFolders(); }}
                   placeholder="/home/openova/repos/yourproject" autocomplete="off" />
            {#if folderResults.length}
              <ul class="suggestions">
                {#each folderResults as f}
                  <li on:click={() => pickFolder(f.path)}>
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
            <span>Tribe</span>
            <input type="text" bind:value={spawnForm.tribe} placeholder="default" />
          </label>
          <label>
            <span>Role</span>
            <select bind:value={spawnForm.role}>
              <option value="worker">worker</option>
              <option value="shepherd">shepherd (opt-in 4-eyes)</option>
            </select>
          </label>
          <label class="check">
            <input type="checkbox" bind:checked={spawnForm.use_default_prompt} />
            <span>Use default {spawnForm.role} system prompt</span>
          </label>
        </div>

        {#if spawnError}<div class="error">{spawnError}</div>{/if}
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
  .dashboard { display: flex; flex-direction: column; height: 100vh; color: #f5f5f5; background: #0a0a0a; }

  .topbar { display: flex; align-items: center; gap: 1.5rem; padding: 0.65rem 1.2rem; background: #111; border-bottom: 1px solid #1e1e1e; }
  .topbar .brand { font-size: 1.15rem; color: #ffa500; text-decoration: none; font-weight: 600; }
  .topbar .stats { flex: 1; color: #aaa; font-size: 0.85rem; }
  .topbar .warn { color: #ff6b6b; margin-left: 0.4rem; }
  .topbar .links { display: flex; gap: 1.1rem; }
  .topbar .links a { color: #aaa; text-decoration: none; font-size: 0.88rem; }
  .topbar .links a:hover { color: #ffa500; }
  button.primary { padding: 0.45rem 1rem; background: #ffa500; color: #000; border: none; border-radius: 4px; font-weight: 600; cursor: pointer; font-size: 0.88rem; }
  button.primary:hover { background: #ffb733; }
  button.primary:disabled { opacity: 0.5; cursor: not-allowed; }
  button.secondary { padding: 0.45rem 0.9rem; background: #1a1a1a; color: #f5f5f5; border: 1px solid #2a2a2a; border-radius: 4px; cursor: pointer; font-size: 0.88rem; }
  button.secondary:hover { background: #222; }
  button.danger { padding: 0.45rem 0.9rem; background: #2a1414; color: #ff6b6b; border: 1px solid #4a1f1f; border-radius: 4px; cursor: pointer; font-size: 0.88rem; }
  button.ghost { padding: 0.45rem 1rem; background: transparent; color: #aaa; border: 1px solid #2a2a2a; border-radius: 4px; cursor: pointer; font-size: 0.88rem; }

  .body { display: flex; flex: 1; min-height: 0; overflow: hidden; }

  /* Left pane */
  .left { width: 280px; min-width: 280px; background: #0a0a0a; border-right: 1px solid #1e1e1e; padding: 0.9rem 1rem; overflow-y: auto; }
  .left h2 { font-size: 0.74rem; color: #888; text-transform: uppercase; letter-spacing: 0.07em; margin: 0 0 0.4rem 0; font-weight: 600; }
  .left h2 .count { color: #555; font-weight: normal; }
  .session-list { list-style: none; padding: 0; margin: 0; }
  .session-list li { padding: 0.55rem 0.65rem; border-radius: 6px; cursor: pointer; margin-bottom: 0.2rem; border: 1px solid transparent; }
  .session-list li:hover { background: #151515; border-color: #222; }
  .session-list li.selected { background: #1a2530; border-color: #5f9ea0; }
  .session-list li.paused { opacity: 0.6; }
  .session-list li.empty { color: #555; font-size: 0.8rem; cursor: default; padding: 0.6rem 0.4rem; }
  .session-list li.empty:hover { background: transparent; border-color: transparent; }
  .row1 { display: flex; align-items: center; gap: 0.4rem; }
  .row1 .dot { font-size: 0.9rem; }
  .row1 .dot.worker { color: #87ceeb; }
  .row1 .dot.shepherd { color: #ffa500; }
  .row1 .name { font-weight: 600; flex: 1; }
  .row2 { font-size: 0.75rem; color: #888; margin-top: 0.18rem; padding-left: 1.25rem; }
  .badge { font-size: 0.65rem; padding: 0.08rem 0.35rem; border-radius: 3px; background: #333; color: #ccc; }

  .inbox { list-style: none; padding: 0; margin: 0; }
  .inbox li { padding: 0.45rem 0.4rem; font-size: 0.8rem; color: #ccc; border-bottom: 1px solid #1e1e1e; }
  .inbox li strong { color: #ffa500; }

  /* Center pane */
  .center { flex: 1; display: flex; flex-direction: column; background: #0a0a0a; min-width: 0; min-height: 0; }
  .center .title { padding: 0.45rem 1rem; background: #111; border-bottom: 1px solid #1e1e1e; color: #f5f5f5; font-family: ui-monospace, monospace; font-size: 0.85rem; flex-shrink: 0; }
  .center .title .dot { color: #87ceeb; margin-right: 0.4rem; }
  .center .title .dot.shepherd { color: #ffa500; }
  .center .title .subtitle { color: #888; font-size: 0.78rem; margin-left: 0.5rem; }
  .center .term { flex: 1; padding: 0.4rem 0.5rem; min-height: 0; overflow: hidden; }
  .center .term :global(.xterm) { height: 100%; }
  .center .term :global(.xterm-viewport) { height: 100% !important; }

  /* Right pane */
  .right { width: 290px; min-width: 290px; background: #0a0a0a; border-left: 1px solid #1e1e1e; padding: 0.9rem 1rem; overflow-y: auto; }
  .right h2 { font-size: 0.74rem; color: #888; text-transform: uppercase; letter-spacing: 0.07em; margin: 0 0 0.5rem 0; font-weight: 600; }
  .right dl { margin: 0; }
  .right dt { color: #888; font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.04em; margin-top: 0.55rem; }
  .right dd { margin: 0.12rem 0 0 0; color: #f5f5f5; font-size: 0.88rem; word-break: break-word; }
  .right dd code { font-size: 0.82rem; color: #87ceeb; }
  .right dd code.cwd { font-size: 0.78rem; word-break: break-all; }
  .action-buttons { display: flex; flex-direction: column; gap: 0.35rem; }

  .scorecard { background: #0c0c0c; border: 1px solid #1e1e1e; border-radius: 6px; padding: 0.55rem 0.7rem; }
  .score-row { display: flex; justify-content: space-between; font-size: 0.82rem; color: #ccc; padding: 0.15rem 0; }
  .score-val { color: #ffa500; font-weight: 600; min-width: 1.8rem; text-align: right; }
  .score-note { font-size: 0.72rem; color: #666; margin: 0.4rem 0 0 0; font-style: italic; }

  /* Modal */
  .modal-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.75); display: flex; align-items: center; justify-content: center; z-index: 1000; }
  .modal { width: min(760px, 94vw); max-height: 92vh; background: #111; border: 1px solid #2a2a2a; border-radius: 8px; display: flex; flex-direction: column; overflow: hidden; }
  .modal-header { display: flex; justify-content: space-between; align-items: center; padding: 0.9rem 1.4rem; border-bottom: 1px solid #1e1e1e; }
  .modal-header h2 { margin: 0; color: #ffa500; font-size: 1.05rem; }
  .modal-header .close { background: transparent; color: #888; border: none; font-size: 1.6rem; cursor: pointer; line-height: 1; }
  .modal-body { padding: 1rem 1.4rem; overflow-y: auto; flex: 1; }
  .modal-body label { display: block; margin-top: 0.7rem; }
  .modal-body label.check { display: flex; align-items: center; gap: 0.5rem; margin-top: 1.65rem; }
  .modal-body label.check span { margin-bottom: 0; font-size: 0.85rem; color: #ccc; text-transform: none; }
  .modal-body label span { display: block; font-size: 0.74rem; color: #888; margin-bottom: 0.22rem; text-transform: uppercase; letter-spacing: 0.04em; }
  .modal-body label em { color: #666; font-style: normal; text-transform: none; }
  .modal-body input[type=text], .modal-body select { width: 100%; padding: 0.46rem 0.6rem; background: #0a0a0a; color: #f5f5f5; border: 1px solid #2a2a2a; border-radius: 4px; font-family: ui-monospace, monospace; font-size: 0.88rem; box-sizing: border-box; }
  .modal-body input[type=text]:focus, .modal-body select:focus { outline: none; border-color: #ffa500; }
  .modal-body .row { display: flex; gap: 0.55rem; }
  .modal-body .row label.grow { flex: 1; }
  .modal-footer { padding: 0.8rem 1.4rem; border-top: 1px solid #1e1e1e; display: flex; justify-content: flex-end; gap: 0.55rem; }

  .mode-toggle { display: flex; gap: 0.25rem; background: #0a0a0a; border-radius: 6px; padding: 0.22rem; border: 1px solid #2a2a2a; margin-bottom: 0.5rem; }
  .mode-toggle button { flex: 1; padding: 0.45rem; background: transparent; color: #888; border: none; border-radius: 4px; cursor: pointer; font-size: 0.88rem; }
  .mode-toggle button.active { background: #ffa500; color: #000; font-weight: 600; }

  .autocomplete { position: relative; }
  .suggestions { position: absolute; top: calc(100% + 2px); left: 0; right: 0; background: #0a0a0a; border: 1px solid #ffa500; border-radius: 4px; list-style: none; padding: 0; margin: 0; max-height: 240px; overflow-y: auto; z-index: 10; }
  .suggestions li { padding: 0.45rem 0.6rem; cursor: pointer; display: flex; align-items: center; gap: 0.5rem; }
  .suggestions li:hover { background: #1a2530; }
  .suggestions li code { color: #87ceeb; font-size: 0.82rem; flex: 1; }
  .suggestions li small { color: #666; font-size: 0.72rem; }

  .resume-list { list-style: none; padding: 0; margin: 0.5rem 0 0 0; max-height: 280px; overflow-y: auto; }
  .resume-list li { padding: 0.5rem 0.65rem; border: 1px solid #2a2a2a; border-radius: 5px; margin-bottom: 0.3rem; cursor: pointer; background: #0a0a0a; }
  .resume-list li:hover { border-color: #5f9ea0; }
  .resume-list li.active { border-color: #ffa500; background: #1f1810; }
  .resume-list li.empty { color: #666; cursor: default; text-align: center; padding: 0.7rem; }
  .resume-head { display: flex; gap: 0.5rem; align-items: center; font-size: 0.76rem; }
  .resume-head .uuid { color: #87ceeb; }
  .resume-head .resume-cwd { color: #ccc; flex: 1; word-break: break-all; }
  .resume-head .resume-mod { color: #888; }
  .resume-msg { color: #aaa; font-size: 0.8rem; margin-top: 0.25rem; font-style: italic; }

  .error { margin-top: 0.7rem; padding: 0.4rem 0.7rem; background: #2a1414; border: 1px solid #4a1f1f; color: #ff6b6b; border-radius: 4px; font-size: 0.83rem; }
</style>

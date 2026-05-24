<!--
  chepherd-rc-web dashboard. Connects to the local chepherd runtime via
  /api/v1/* + a WebSocket to /api/v1/sessions/{name}/attach. Renders the
  selected session's PTY output via xterm.js.

  This is the v0.5 minimum: session list + select + xterm.js-rendered
  live pane. v0.6 adds spawn modal, tribe view, scorecard side panel.
-->
<script>
  import { onMount } from 'svelte';

  let sessions = $state([]);
  let selectedName = $state(null);
  let connected = $state(false);
  let inbox = $state([]);

  const API = '/api/v1';

  async function refreshSessions() {
    try {
      const res = await fetch(`${API}/sessions`);
      const data = await res.json();
      sessions = data.sessions || [];
      connected = true;
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

  let term = null;
  let termContainer = null;
  let ws = null;

  async function attachTo(name) {
    if (selectedName === name) return;
    selectedName = name;
    if (ws) ws.close();
    if (term) term.dispose();

    const { Terminal } = await import('@xterm/xterm');
    const { FitAddon } = await import('@xterm/addon-fit');
    term = new Terminal({ convertEol: true, fontFamily: 'monospace', fontSize: 13, theme: { background: '#0a0a0a' } });
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

  async function spawnSession() {
    const name = prompt('Session name (e.g. iogrid-1):');
    if (!name) return;
    await fetch(`${API}/sessions`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, agent: 'claude-code', tribe: 'default', role: 'worker' }),
    });
    refreshSessions();
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
      <button on:click={spawnSession}>+ spawn</button>
    </div>
  </header>

  <div class="body">
    <aside class="sidebar">
      <h2>Sessions</h2>
      <ul>
        {#each sessions as s (s.id)}
          <li class={selectedName === s.name ? 'selected' : ''} on:click={() => attachTo(s.name)}>
            <span class="icon">{s.role === 'shepherd' ? '✻' : '●'}</span>
            <span class="name">{s.name}</span>
            <span class="tribe">{s.tribe}</span>
          </li>
        {/each}
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
      <div class="title">{selectedName ? `Live: ${selectedName}` : 'Pick a session →'}</div>
      <div class="term" bind:this={termContainer}></div>
    </section>
  </div>
</div>

<style>
  .dashboard { display: flex; flex-direction: column; height: 100vh; color: #f5f5f5; font-family: ui-sans-serif, system-ui, sans-serif; }
  .dash-header { display: flex; align-items: center; padding: 0.75rem 1.5rem; background: #111; border-bottom: 1px solid #1e1e1e; }
  .dash-header h1 { font-size: 1.3rem; color: #ffa500; margin-right: 2rem; }
  .stats { flex: 1; color: #aaa; font-size: 0.9rem; }
  .actions button { padding: 0.5rem 1rem; background: #ffa500; color: #000; border: none; border-radius: 4px; font-weight: 600; cursor: pointer; }
  .warn { color: #ff6b6b; margin-left: 0.5rem; }
  .body { display: flex; flex: 1; overflow: hidden; }
  .sidebar { width: 280px; background: #0a0a0a; border-right: 1px solid #1e1e1e; padding: 1rem; overflow-y: auto; }
  .sidebar h2 { font-size: 0.9rem; color: #888; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 0.5rem; }
  .sidebar ul { list-style: none; padding: 0; }
  .sidebar li { padding: 0.5rem; border-radius: 4px; cursor: pointer; display: flex; align-items: center; gap: 0.5rem; }
  .sidebar li:hover { background: #1a1a1a; }
  .sidebar li.selected { background: #5f9ea0; }
  .sidebar .icon { color: #87ceeb; }
  .sidebar .name { flex: 1; }
  .sidebar .tribe { color: #888; font-size: 0.8rem; }
  .sidebar .inbox li { padding: 0.4rem 0; font-size: 0.85rem; color: #ccc; border-bottom: 1px solid #1e1e1e; cursor: default; }
  .sidebar .inbox li:hover { background: transparent; }
  .center { flex: 1; display: flex; flex-direction: column; background: #0a0a0a; }
  .center .title { padding: 0.5rem 1rem; background: #111; border-bottom: 1px solid #1e1e1e; color: #ccc; font-family: monospace; }
  .center .term { flex: 1; padding: 0.5rem; }
</style>

<!--
  MissionSettings — full-screen settings (spec's "Settings Page"). Sections:
    Appearance — theme toggle (dark/light) + font scale (drives --ws-font)
    Accounts   — operator session info + sign out
    Mesh       — federation peers (GET /api/v1/peers, read-only)
    Developer  — A2A tasks (GET /api/v1/tasks) + recent global events
  Live data; no mockups. Theme + font changes apply immediately + persist.
-->
<script>
  import { onMount } from 'svelte';

  let { mode = 'dark', fontSize = 14, events = [], onToggleTheme, onFont, onSignout, onclose } = $props();

  const API = '/api/v1';
  let section = $state('appearance');
  let peers = $state([]);
  let tasks = $state([]);

  async function loadPeers() { try { const r = await fetch(`${API}/peers`); if (r.ok) { const j = await r.json(); peers = j.peers || []; } } catch {} }
  async function loadTasks() { try { const r = await fetch(`${API}/tasks`); if (r.ok) { const j = await r.json(); tasks = j.tasks || []; } } catch {} }
  onMount(() => { loadPeers(); loadTasks(); });

  const SECTIONS = [
    ['appearance', '🎨 Appearance'],
    ['accounts', '👤 Accounts'],
    ['mesh', '⇄ Mesh'],
    ['developer', '⚙ Developer'],
  ];
  function fmtTime(ts) { try { return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }); } catch { return ''; } }
</script>

<div class="overlay" role="dialog" aria-modal="true">
  <div class="sheet">
    <aside class="nav">
      <div class="nav-h">SETTINGS</div>
      {#each SECTIONS as [id, label]}
        <button class:active={section === id} onclick={() => (section = id)}>{label}</button>
      {/each}
      <div class="nav-sp"></div>
      <button class="close" onclick={() => onclose?.()}>✕ Close</button>
    </aside>
    <main class="body">
      {#if section === 'appearance'}
        <h2>Appearance</h2>
        <div class="row">
          <div class="row-l"><span class="rl-t">Theme</span><span class="rl-s">Mission control runs deep-dark by default; daylight ops variant available.</span></div>
          <button class="toggle" onclick={() => onToggleTheme?.()}>
            <span class="seg" class:on={mode === 'dark'}>🌙 Dark</span><span class="seg" class:on={mode === 'light'}>☀ Light</span>
          </button>
        </div>
        <div class="row">
          <div class="row-l"><span class="rl-t">Font scale</span><span class="rl-s">Scales every pane + terminal (--ws-font).</span></div>
          <div class="stepper">
            <button onclick={() => onFont?.(-1)}>A−</button>
            <span class="fz">{fontSize}px</span>
            <button onclick={() => onFont?.(+1)}>A+</button>
          </div>
        </div>
      {:else if section === 'accounts'}
        <h2>Accounts</h2>
        <p class="muted">Signed in as <strong>operator</strong> (bootstrap token in localStorage).</p>
        <div class="row">
          <div class="row-l"><span class="rl-t">Session</span><span class="rl-s">Clear the stored token and return to the login screen.</span></div>
          <button class="danger" onclick={() => onSignout?.()}>⎋ Sign out</button>
        </div>
      {:else if section === 'mesh'}
        <h2>Federation mesh</h2>
        {#if !peers.length}
          <p class="muted">No hub-discovered peers.</p>
        {:else}
          <table class="tbl">
            <thead><tr><th>Peer</th><th>SID</th><th>Synced</th><th>Card URL</th></tr></thead>
            <tbody>
              {#each peers as p}
                <tr><td>{p.name || '—'}</td><td class="mono">{(p.sid || '').slice(0, 10)}</td><td>{fmtTime(p.syncedAt)}</td><td class="mono trunc">{p.card?.url || '—'}</td></tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {:else if section === 'developer'}
        <h2>Developer</h2>
        <h3>A2A tasks</h3>
        {#if !tasks.length}<p class="muted">No tasks.</p>{:else}
          <table class="tbl">
            <thead><tr><th>ID</th><th>Method</th><th>State</th><th>Updated</th></tr></thead>
            <tbody>
              {#each tasks.slice(0, 30) as t}
                <tr><td class="mono">{(t.id || '').slice(0, 8)}</td><td>{t.method}</td><td><span class="badge {t.state}">{t.state}</span></td><td>{fmtTime(t.updatedAt)}</td></tr>
              {/each}
            </tbody>
          </table>
        {/if}
        <h3>Recent events</h3>
        <ul class="ev">
          {#each (events || []).slice(-30).reverse() as e}
            <li><span class="ev-t">{fmtTime(e.at || e.created_at || e.ts)}</span><span class="ev-k">{e.kind || e.type || e.event || 'event'}</span><span class="ev-b">{(e.body || e.msg || e.message || '').slice(0, 110)}</span></li>
          {/each}
          {#if !(events || []).length}<li class="muted">No events captured yet.</li>{/if}
        </ul>
      {/if}
    </main>
  </div>
</div>

<style>
  .overlay { position: fixed; inset: 0; z-index: 210; background: color-mix(in srgb, var(--m-bg) 80%, transparent); backdrop-filter: blur(3px); display: flex; align-items: center; justify-content: center; padding: 1.5rem; }
  .sheet { display: flex; width: 100%; max-width: 60rem; height: 80vh; background: var(--m-panel); border: 1px solid var(--m-border-strong); border-radius: 12px; overflow: hidden; box-shadow: 0 30px 70px -20px var(--m-shadow); color: var(--m-fg); }
  .nav { width: 13rem; flex: 0 0 auto; background: var(--m-panel-2); border-right: 1px solid var(--m-border); display: flex; flex-direction: column; padding: 0.7rem; gap: 2px; }
  .nav-h { font-size: 0.62rem; letter-spacing: 0.16em; color: var(--m-fg-faint); font-weight: 700; padding: 0.3rem 0.5rem 0.6rem; }
  .nav button { text-align: left; background: transparent; border: 0; border-radius: 6px; color: var(--m-fg-dim); font: inherit; font-size: 0.82rem; padding: 0.5rem 0.6rem; cursor: pointer; }
  .nav button:hover { background: var(--m-panel-3); color: var(--m-fg); }
  .nav button.active { background: var(--m-select); color: var(--m-accent-2); }
  .nav-sp { flex: 1; }
  .nav .close { color: var(--m-fg-faint); }
  .body { flex: 1; overflow-y: auto; padding: 1.2rem 1.6rem; }
  .body::-webkit-scrollbar { width: 9px; }
  .body::-webkit-scrollbar-thumb { background: var(--m-scroll); border-radius: 5px; }
  h2 { font-size: 1.05rem; margin: 0 0 1rem; }
  h3 { font-size: 0.8rem; margin: 1.3rem 0 0.5rem; color: var(--m-fg-dim); text-transform: uppercase; letter-spacing: 0.06em; }
  .muted { color: var(--m-fg-faint); font-size: 0.84rem; }
  .row { display: flex; align-items: center; justify-content: space-between; gap: 1rem; padding: 0.8rem 0; border-bottom: 1px solid var(--m-border); }
  .row-l { display: flex; flex-direction: column; gap: 0.15rem; }
  .rl-t { font-size: 0.86rem; font-weight: 600; }
  .rl-s { font-size: 0.74rem; color: var(--m-fg-faint); }
  .toggle { display: flex; gap: 2px; background: var(--m-panel-3); border: 1px solid var(--m-border-strong); border-radius: 7px; padding: 2px; cursor: pointer; }
  .toggle .seg { font-size: 0.78rem; padding: 0.35rem 0.7rem; border-radius: 5px; color: var(--m-fg-faint); }
  .toggle .seg.on { background: var(--m-accent-2); color: var(--m-bg); font-weight: 700; }
  .stepper { display: flex; align-items: center; gap: 0.5rem; }
  .stepper button { background: var(--m-panel-3); color: var(--m-fg); border: 1px solid var(--m-border-strong); border-radius: 5px; width: 2rem; height: 1.9rem; cursor: pointer; font: inherit; }
  .stepper button:hover { border-color: var(--m-accent-2); color: var(--m-accent-2); }
  .fz { font-family: ui-monospace, monospace; font-size: 0.82rem; min-width: 3rem; text-align: center; }
  .danger { background: transparent; color: var(--m-danger); border: 1px solid var(--m-danger); border-radius: 6px; padding: 0.4rem 0.9rem; font: inherit; font-size: 0.8rem; cursor: pointer; }
  .danger:hover { background: color-mix(in srgb, var(--m-danger) 14%, transparent); }
  .tbl { width: 100%; border-collapse: collapse; font-size: 0.76rem; }
  .tbl th { text-align: left; color: var(--m-fg-faint); font-weight: 600; padding: 0.35rem 0.5rem; border-bottom: 1px solid var(--m-border); font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.05em; }
  .tbl td { padding: 0.35rem 0.5rem; border-bottom: 1px solid var(--m-border); color: var(--m-fg-dim); }
  .tbl .mono { font-family: ui-monospace, monospace; }
  .tbl .trunc { max-width: 16rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .badge { font-size: 0.66rem; padding: 0.08rem 0.4rem; border-radius: 3px; background: var(--m-panel-3); color: var(--m-fg-dim); }
  .badge.completed { color: var(--m-ok); }
  .badge.failed { color: var(--m-danger); }
  .badge.working { color: var(--m-accent-2); }
  .ev { list-style: none; display: flex; flex-direction: column; gap: 0.25rem; }
  .ev li { display: grid; grid-template-columns: auto auto 1fr; gap: 0.5rem; font-size: 0.72rem; align-items: baseline; }
  .ev-t { font-family: ui-monospace, monospace; color: var(--m-fg-faint); }
  .ev-k { color: var(--m-accent-2); font-weight: 600; }
  .ev-b { color: var(--m-fg-dim); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
</style>

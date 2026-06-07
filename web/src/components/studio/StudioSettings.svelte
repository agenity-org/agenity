<!--
  StudioSettings — full settings surface for the studio dashboard, rendered
  as a side-panel section (Activity Bar ▸ Settings). Sections:
    Appearance  — theme toggle (dark/light) + font size (drives --ws-font)
    Accounts    — vault entries from GET /api/v1/accounts (read-only list)
    Roles       — role catalog from GET /api/v1/roles (read-only)
    Mesh        — federation peers from GET /api/v1/peers
    Tasks       — A2A task inbox from GET /api/v1/tasks
    Developer   — recent events count + endpoints reference
  Theme + font controls are LIVE (the founder-mandated requirement #7).
-->
<script>
  import { onMount } from 'svelte';

  let {
    theme = 'dark', fontSize = 14, events = [],
    onToggleTheme = () => {}, onFontSize = () => {},
  } = $props();

  let section = $state('appearance');
  let accounts = $state([]);
  let roles = $state([]);
  let peers = $state([]);
  let tasks = $state([]);
  let loaded = $state({});

  const API = '/api/v1';
  async function load(name) {
    if (loaded[name]) return;
    loaded = { ...loaded, [name]: true };
    try {
      if (name === 'accounts') { const d = await (await fetch(`${API}/accounts`)).json(); accounts = d.accounts || d.entries || []; }
      if (name === 'roles')    { const d = await (await fetch(`${API}/roles`)).json();    roles = d.roles || []; }
      if (name === 'mesh')     { const d = await (await fetch(`${API}/peers`)).json();    peers = d.peers || []; }
      if (name === 'tasks')    { const d = await (await fetch(`${API}/tasks`)).json();    tasks = d.tasks || []; }
    } catch {}
  }
  function go(s) { section = s; load(s); }
  onMount(() => { load('accounts'); });

  const SECTIONS = [
    { id: 'appearance', label: 'Appearance', glyph: '🎨' },
    { id: 'accounts', label: 'Accounts', glyph: '🔑' },
    { id: 'roles', label: 'Roles & Skills', glyph: '🎭' },
    { id: 'mesh', label: 'Mesh', glyph: '⇄' },
    { id: 'tasks', label: 'A2A Tasks', glyph: '✉' },
    { id: 'dev', label: 'Developer', glyph: '⚙' },
  ];
</script>

<div class="settings">
  <nav class="sec-nav">
    {#each SECTIONS as s}
      <button class:active={section === s.id} onclick={() => go(s.id)}>
        <span class="sg">{s.glyph}</span>{s.label}
      </button>
    {/each}
  </nav>

  <div class="sec-body">
    {#if section === 'appearance'}
      <h3>Appearance</h3>
      <div class="field">
        <label class="flabel">Theme</label>
        <div class="seg">
          <button class:on={theme === 'dark'} onclick={() => { if (theme !== 'dark') onToggleTheme(); }}>🌙 Dark</button>
          <button class:on={theme === 'light'} onclick={() => { if (theme !== 'light') onToggleTheme(); }}>☀ Light</button>
        </div>
        <p class="hint">Both themes are fully designed. Choice persists (localStorage) and respects your OS preference on first load.</p>
      </div>
      <div class="field">
        <label class="flabel">Font size <span class="fval">{fontSize}px</span></label>
        <div class="fontctl">
          <button onclick={() => onFontSize(fontSize - 1)} aria-label="Smaller">A−</button>
          <input type="range" min="9" max="22" value={fontSize} oninput={(e) => onFontSize(+e.currentTarget.value)} />
          <button onclick={() => onFontSize(fontSize + 1)} aria-label="Larger">A+</button>
        </div>
        <p class="hint">Scales every pane uniformly via the --ws-font CSS variable; terminals re-fit + send SIGWINCH.</p>
      </div>

    {:else if section === 'accounts'}
      <h3>Accounts <span class="cnt">{accounts.length}</span></h3>
      {#if accounts.length}
        <ul class="list">
          {#each accounts as a}
            <li><span class="lk">{a.name || a.id || a.label}</span><span class="lm">{a.kind || a.type || a.provider || ''}</span></li>
          {/each}
        </ul>
      {:else}
        <p class="empty">No vault entries, or the endpoint isn't exposed in this build.</p>
      {/if}

    {:else if section === 'roles'}
      <h3>Roles &amp; Skills <span class="cnt">{roles.length}</span></h3>
      {#if roles.length}
        <ul class="list">
          {#each roles as r}
            <li>
              <span class="lk">{r.name || r.id}</span>
              {#if r.default_skills?.length}<span class="lm">{r.default_skills.length} skills</span>{/if}
            </li>
          {/each}
        </ul>
      {:else}
        <p class="empty">No roles returned by the API.</p>
      {/if}

    {:else if section === 'mesh'}
      <h3>Mesh peers <span class="cnt">{peers.length}</span></h3>
      {#if peers.length}
        <ul class="list">
          {#each peers as p}
            <li>
              <span class="lk">⇄ {p.name || p.sid}</span>
              {#if p.card?.url}<a class="lm link" href={p.card.url} target="_blank" rel="noopener">{p.card.url}</a>{/if}
            </li>
          {/each}
        </ul>
      {:else}
        <p class="empty">No federation peers discovered.</p>
      {/if}

    {:else if section === 'tasks'}
      <h3>A2A tasks <span class="cnt">{tasks.length}</span></h3>
      {#if tasks.length}
        <ul class="list">
          {#each tasks as t}
            <li>
              <span class="lk mono">{(t.id || '').slice(0, 10)}</span>
              <span class="lm">{t.method} · <em>{t.state}</em></span>
            </li>
          {/each}
        </ul>
      {:else}
        <p class="empty">No A2A tasks in the inbox.</p>
      {/if}

    {:else if section === 'dev'}
      <h3>Developer</h3>
      <dl class="kv">
        <dt>Events in memory</dt><dd>{events.length}</dd>
        <dt>Event stream</dt><dd class="mono">/api/v1/events/stream</dd>
        <dt>Sessions</dt><dd class="mono">/api/v1/sessions</dd>
        <dt>Transcript</dt><dd class="mono">/api/v1/transcript?teams=all</dd>
        <dt>Attach (WS)</dt><dd class="mono">/api-v08/v1/sessions/&lcub;name&rcub;/attach</dd>
      </dl>
      <p class="hint">The studio dashboard polls sessions/teams/memberships/inbox/events every 2.5s and streams live events + per-team transcript ticks.</p>
    {/if}
  </div>
</div>

<style>
  .settings { height: 100%; display: flex; flex-direction: column; min-height: 0; }
  .sec-nav { display: flex; flex-wrap: wrap; gap: 0.2rem; padding: 0.5rem; border-bottom: 1px solid var(--st-border); }
  .sec-nav button { display: flex; align-items: center; gap: 0.3rem; background: transparent; border: 1px solid transparent;
    border-radius: 6px; color: var(--st-fg-muted); cursor: pointer; font: inherit; font-size: 0.74rem; padding: 0.25rem 0.45rem; }
  .sec-nav button:hover { background: var(--st-hover); color: var(--st-fg); }
  .sec-nav button.active { background: var(--st-sel-bg); color: var(--st-fg); border-color: var(--st-border); }
  .sg { font-size: 0.85rem; }
  .sec-body { flex: 1; overflow-y: auto; padding: 0.9rem; }
  h3 { font-size: 0.95rem; margin: 0 0 0.9rem; display: flex; align-items: center; gap: 0.5rem; }
  .cnt { font-size: 0.7rem; background: var(--st-chip); border-radius: 999px; padding: 0.05rem 0.45rem; color: var(--st-fg-muted); }
  .field { margin-bottom: 1.3rem; }
  .flabel { display: block; font-size: 0.78rem; color: var(--st-fg-muted); margin-bottom: 0.45rem; }
  .fval { color: var(--st-accent); font-family: ui-monospace, monospace; }
  .seg { display: inline-flex; border: 1px solid var(--st-border); border-radius: 8px; overflow: hidden; }
  .seg button { background: var(--st-chip); border: 0; color: var(--st-fg-muted); cursor: pointer; font: inherit; font-size: 0.82rem; padding: 0.4rem 0.9rem; }
  .seg button.on { background: var(--st-accent); color: #0a0a0a; font-weight: 600; }
  .fontctl { display: flex; align-items: center; gap: 0.6rem; }
  .fontctl button { background: var(--st-chip); border: 1px solid var(--st-border); border-radius: 6px; color: var(--st-fg);
    cursor: pointer; font: inherit; font-weight: 600; padding: 0.3rem 0.6rem; }
  .fontctl button:hover { border-color: var(--st-accent); }
  .fontctl input[type=range] { flex: 1; accent-color: var(--st-accent); }
  .hint { color: var(--st-fg-faint); font-size: 0.76rem; line-height: 1.5; margin: 0.5rem 0 0; }
  .list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 0.3rem; }
  .list li { display: flex; align-items: center; justify-content: space-between; gap: 0.6rem;
    background: var(--st-chip); border: 1px solid var(--st-border); border-radius: 7px; padding: 0.45rem 0.65rem; }
  .lk { font-size: 0.84rem; font-weight: 500; }
  .lm { font-size: 0.74rem; color: var(--st-fg-muted); }
  .lm em { color: var(--st-accent); font-style: normal; }
  .link { color: var(--st-accent-2); text-decoration: none; }
  .link:hover { text-decoration: underline; }
  .empty { color: var(--st-fg-faint); font-size: 0.82rem; }
  .mono { font-family: ui-monospace, monospace; }
  .kv { display: grid; grid-template-columns: max-content 1fr; gap: 0.4rem 1rem; margin: 0; }
  .kv dt { color: var(--st-fg-muted); font-size: 0.78rem; }
  .kv dd { margin: 0; font-size: 0.78rem; }
</style>

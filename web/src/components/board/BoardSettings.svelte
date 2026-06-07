<!--
  BoardSettings — full-viewport settings sheet for the "board" dashboard.

  Sections:
    Appearance — theme toggle (dark/light) + font size, persisted.
    Accounts   — GET /api/v1/claude-tokens (Claude accounts, read view).
    Mesh       — GET /api/v1/peers (federation peers, read view).
    Developer  — live event stream from props + GET /api/v1/tasks (A2A inbox).

  Theme + font are owned by the parent (Dashboardboard) via callbacks so
  the live xterm panes re-theme without a reload.
-->
<script>
  import { onMount } from 'svelte';

  let {
    theme = 'dark',
    fontSize = 14,
    events = [],
    ontheme = () => {},
    onfont = () => {},
    onclose = () => {},
  } = $props();

  const API = '/api/v1';
  let section = $state('appearance');
  let accounts = $state([]);
  let peers = $state([]);
  let tasks = $state([]);
  let loadedExtras = $state(false);

  async function loadExtras() {
    if (loadedExtras) return;
    loadedExtras = true;
    try { const r = await fetch(`${API}/claude-tokens`); if (r.ok) { const j = await r.json(); accounts = j.tokens || j.accounts || []; } } catch {}
    try { const r = await fetch(`${API}/peers`); if (r.ok) { const j = await r.json(); peers = j.peers || []; } } catch {}
    try { const r = await fetch(`${API}/tasks`); if (r.ok) { const j = await r.json(); tasks = j.tasks || []; } } catch {}
  }
  onMount(loadExtras);

  const SECTIONS = [
    { id: 'appearance', label: 'Appearance', icon: '🎨' },
    { id: 'accounts', label: 'Accounts', icon: '🔑' },
    { id: 'mesh', label: 'Mesh', icon: '⇄' },
    { id: 'developer', label: 'Developer', icon: '⚙' },
  ];

  let recentEvents = $derived([...(events || [])].slice(-60).reverse());
</script>

<div class="ov" role="dialog" aria-modal="true">
  <div class="panel">
    <header class="p-head">
      <h2>Settings</h2>
      <button class="x" onclick={onclose} aria-label="close settings">✕</button>
    </header>
    <div class="p-body">
      <nav class="p-nav">
        {#each SECTIONS as s}
          <button class:active={section === s.id} onclick={() => section = s.id}>
            <span class="nico">{s.icon}</span>{s.label}
          </button>
        {/each}
      </nav>

      <div class="p-main">
        {#if section === 'appearance'}
          <h3>Appearance</h3>
          <div class="field">
            <span class="flabel">Theme</span>
            <div class="theme-toggle" role="group" aria-label="theme">
              <button class:on={theme === 'light'} onclick={() => ontheme('light')}>☀ Light</button>
              <button class:on={theme === 'dark'} onclick={() => ontheme('dark')}>🌙 Dark</button>
            </div>
          </div>
          <div class="field">
            <span class="flabel">Font size</span>
            <div class="font-ctl">
              <button onclick={() => onfont(fontSize - 1)} aria-label="smaller">A−</button>
              <span class="font-num">{fontSize}px</span>
              <button onclick={() => onfont(fontSize + 1)} aria-label="larger">A+</button>
            </div>
          </div>
          <p class="note">Theme + font apply live to every card and terminal pane, and persist in this browser.</p>

        {:else if section === 'accounts'}
          <h3>Claude accounts</h3>
          {#if !accounts.length}
            <p class="note">No Claude accounts connected (or the endpoint isn't exposed). Connect one via the spawn wizard's account stage.</p>
          {:else}
            <ul class="rows">
              {#each accounts as a}
                <li><span class="r-main">{a.label || a.name || a.id}</span>{#if a.email}<span class="r-sub">{a.email}</span>{/if}</li>
              {/each}
            </ul>
          {/if}

        {:else if section === 'mesh'}
          <h3>Federation mesh</h3>
          {#if !peers.length}
            <p class="note">No hub-discovered peers. This daemon is running standalone or no peers have synced.</p>
          {:else}
            <ul class="rows">
              {#each peers as p}
                <li>
                  <span class="r-main">⇄ {p.name || p.sid}</span>
                  {#if p.card?.url}<span class="r-sub">{p.card.url}</span>{/if}
                  {#if p.syncedAt}<span class="r-tag">synced {new Date(p.syncedAt).toLocaleTimeString()}</span>{/if}
                </li>
              {/each}
            </ul>
          {/if}

        {:else if section === 'developer'}
          <h3>A2A tasks</h3>
          {#if !tasks.length}
            <p class="note">No A2A tasks recorded.</p>
          {:else}
            <ul class="rows">
              {#each tasks.slice(0, 20) as t}
                <li><span class="r-main mono">{t.method}</span><span class="r-tag state-{t.state}">{t.state}</span>{#if t.updatedAt}<span class="r-sub">{new Date(t.updatedAt).toLocaleTimeString()}</span>{/if}</li>
              {/each}
            </ul>
          {/if}
          <h3 style="margin-top:1.2rem">Event stream</h3>
          {#if !recentEvents.length}
            <p class="note">No events yet.</p>
          {:else}
            <ul class="ev-list">
              {#each recentEvents as e, i (i)}
                <li><span class="ev-kind">{e.kind || e.type || 'event'}</span><span class="ev-body mono">{e.body || e.message || e.summary || JSON.stringify(e).slice(0, 120)}</span></li>
              {/each}
            </ul>
          {/if}
        {/if}
      </div>
    </div>
  </div>
</div>

<style>
  .ov { position: fixed; inset: 0; z-index: 280; background: var(--board-bg); display: flex; }
  .panel { display: flex; flex-direction: column; flex: 1; min-height: 0; }
  .p-head {
    display: flex; align-items: center; padding: 0.9rem 1.4rem;
    border-bottom: 1px solid var(--board-border); flex: 0 0 auto;
  }
  .p-head h2 { font-size: 1.1rem; margin: 0; flex: 1; color: var(--board-fg); }
  .x { background: transparent; border: 1px solid var(--board-border-strong); color: var(--board-fg-muted); border-radius: 8px; width: 32px; height: 32px; cursor: pointer; }
  .x:hover { color: var(--board-fg); background: var(--board-hover); }

  .p-body { flex: 1; min-height: 0; display: flex; }
  .p-nav { flex: 0 0 200px; border-right: 1px solid var(--board-border); padding: 0.8rem; display: flex; flex-direction: column; gap: 0.2rem; }
  .p-nav button {
    display: flex; align-items: center; gap: 0.55rem; text-align: left;
    background: transparent; border: 0; color: var(--board-fg-muted);
    border-radius: 8px; padding: 0.5rem 0.65rem; font-size: 0.86rem; cursor: pointer;
  }
  .p-nav button:hover { background: var(--board-hover); color: var(--board-fg); }
  .p-nav button.active { background: var(--board-accent-bg); color: var(--board-accent); font-weight: 600; }
  .nico { width: 1.1rem; text-align: center; }

  .p-main { flex: 1; min-width: 0; overflow-y: auto; padding: 1.2rem 1.6rem; }
  .p-main h3 { font-size: 0.95rem; color: var(--board-fg); margin: 0 0 0.9rem; }

  .field { display: flex; align-items: center; gap: 1rem; margin-bottom: 1.1rem; }
  .flabel { width: 110px; font-size: 0.82rem; color: var(--board-fg-muted); }

  .theme-toggle, .font-ctl { display: inline-flex; gap: 0.3rem; }
  .theme-toggle button, .font-ctl button {
    background: var(--board-surface); border: 1px solid var(--board-border-strong); color: var(--board-fg-muted);
    border-radius: 8px; padding: 0.4rem 0.8rem; font-size: 0.82rem; cursor: pointer;
  }
  .theme-toggle button.on { background: var(--board-accent-bg); border-color: var(--board-accent); color: var(--board-accent); font-weight: 600; }
  .theme-toggle button:hover:not(.on), .font-ctl button:hover { background: var(--board-hover); color: var(--board-fg); }
  .font-num { align-self: center; font-family: ui-monospace, monospace; font-size: 0.82rem; color: var(--board-fg); min-width: 42px; text-align: center; }

  .note { font-size: 0.8rem; color: var(--board-fg-faint); line-height: 1.5; max-width: 48ch; }

  .rows { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 0.4rem; }
  .rows li {
    display: flex; align-items: center; gap: 0.7rem; flex-wrap: wrap;
    background: var(--board-surface); border: 1px solid var(--board-border);
    border-radius: 8px; padding: 0.55rem 0.75rem;
  }
  .r-main { font-size: 0.85rem; color: var(--board-fg); font-weight: 550; }
  .r-sub { font-size: 0.74rem; color: var(--board-fg-faint); }
  .r-tag { font-size: 0.68rem; color: var(--board-fg-muted); background: var(--board-chip-bg); border-radius: 5px; padding: 0.06rem 0.4rem; }
  .r-tag.state-completed { color: var(--board-ok); }
  .r-tag.state-failed { color: var(--board-danger); }
  .mono { font-family: ui-monospace, monospace; }

  .ev-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 0.25rem; }
  .ev-list li { display: flex; gap: 0.6rem; font-size: 0.76rem; padding: 0.25rem 0; border-bottom: 1px solid var(--board-border); }
  .ev-kind { flex: 0 0 9rem; color: var(--board-accent); font-weight: 600; }
  .ev-body { flex: 1; color: var(--board-fg-muted); word-break: break-word; }
</style>

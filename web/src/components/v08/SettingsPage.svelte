<!--
  SettingsPage — the ⚙ Settings IA (#693, UX-3 of #690).

  A routed full-viewport page (hash deep-link: #settings/<section>) —
  NOT a floating pane — re-hosting the configure-mode widgets that used
  to live in the workspace grid:

    accounts   → WidgetAccounts      (Claude accounts + git providers)
    roles      → WidgetRoleMatrix + WidgetAgentSkills
    team       → WidgetCanon         (team canon docs)
    mesh       → read-only hub/peer status (from /api/v1/peers + healthz)
    developer  → global events stream + raw MCP log

  Existing widget components are RE-HOSTED, not rewritten — their flows
  (save a provider token, flip a role skill, view canon) work unchanged.
-->
<script>
  import WidgetAccounts from './widgets/WidgetAccounts.svelte';
  import WidgetRoleMatrix from './widgets/WidgetRoleMatrix.svelte';
  import WidgetAgentSkills from './widgets/WidgetAgentSkills.svelte';
  import WidgetCanon from './widgets/WidgetCanon.svelte';
  import WidgetEvents from './widgets/WidgetEvents.svelte';
  import WidgetMCPLog from './widgets/WidgetMCPLog.svelte';

  let { teams = [], events = [], onclose = () => {} } = $props();

  const INTROS = {
    appearance: 'Theme, font size and density for this browser.',
    accounts: 'Claude accounts and git providers chepherd injects into agents at spawn.',
    roles: 'Which skills each role brings; tune defaults for every future spawn.',
    team: 'Team charters (canon) that agents read as shared ground truth.',
    mesh: 'This daemon\'s hub connection and the peers discovered through it.',
    developer: 'Raw event and MCP streams — diagnostic surfaces, not daily tools.',
  };
  const SECTIONS = [
    { id: 'appearance', label: '🎨 Appearance' },
    { id: 'accounts',  label: '⚓ Accounts & Providers' },
    { id: 'roles',     label: '🎮 Roles & Skills' },
    { id: 'team',      label: '📜 Team' },
    { id: 'mesh',      label: '⇄ Mesh' },
    { id: 'developer', label: '🔧 Developer' },
  ];

  // Deep-link: #settings/<section>. Read on mount; write on switch.
  function sectionFromHash() {
    const m = (typeof location !== 'undefined' ? location.hash : '').match(/^#settings\/?([a-z]*)/);
    return m && SECTIONS.some(s => s.id === m[1]) ? m[1] : 'accounts';
  }
  let section = $state(sectionFromHash());
  // #693 review (b) — keep section in sync with Back/Forward; close when
  // the hash leaves #settings entirely.
  $effect(() => {
    const onHash = () => {
      if (!location.hash.startsWith('#settings')) { onclose(); return; }
      section = sectionFromHash();
    };
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  });
  function switchSection(id) {
    section = id;
    try { location.hash = '#settings/' + id; } catch {}
  }
  function close() {
    try { if (location.hash.startsWith('#settings')) location.hash = ''; } catch {}
    onclose();
  }

  // Mesh section data (read-only status).
  let peers = $state([]);
  let health = $state(null);
  $effect(() => {
    if (section !== 'mesh') return;
    let stop = false;
    (async () => {
      try {
        const [pr, hr] = await Promise.all([
          fetch('/api/v1/peers'), fetch('/healthz'),
        ]);
        if (stop) return;
        if (pr.ok) peers = (await pr.json()).peers || [];
        if (hr.ok) health = await hr.json();
      } catch {}
    })();
    return () => { stop = true; };
  });
  function syncedAgo(p) {
    const t = p?.syncedAt;
    if (!t) return '—';
    const s = Math.max(0, Math.round((Date.now() - new Date(t).getTime()) / 1000));
    return s < 60 ? `${s}s ago` : `${Math.floor(s / 60)}m ago`;
  }
</script>

<div class="settings-page" data-testid="settings-page">
  <header class="sp-head">
    <h1>⚙ Settings</h1>
    <button class="sp-close" onclick={close} title="back to workspace" data-testid="settings-close">×</button>
  </header>
  <div class="sp-body">
    <nav class="sp-nav" data-testid="settings-nav">
      {#each SECTIONS as s}
        <button class:active={section === s.id} aria-current={section === s.id ? 'page' : undefined} onclick={() => switchSection(s.id)} data-testid={'settings-nav-' + s.id}>{s.label}</button>
      {/each}
    </nav>
    <main class="sp-main" data-testid={'settings-section-' + section}>
      <p class="sp-intro">{INTROS[section]}</p>
      {#if section === 'appearance'}
        <h2>Theme</h2>
        <button class="appearance-btn" onclick={() => window.dispatchEvent(new CustomEvent('chepherd-toggle-theme'))}>toggle light / dark</button>
        <h2>Font size</h2>
        <div class="appearance-row">
          <button class="appearance-btn" onclick={() => window.dispatchEvent(new CustomEvent('chepherd-font-delta', { detail: -1 }))}>A-</button>
          <button class="appearance-btn" onclick={() => window.dispatchEvent(new CustomEvent('chepherd-font-delta', { detail: 1 }))}>A+</button>
          <span class="muted">applies to all widgets</span>
        </div>
      {:else if section === 'accounts'}
        <WidgetAccounts />
      {:else if section === 'roles'}
        <h2>Role × skill matrix</h2>
        <WidgetRoleMatrix />
        <h2>Skill library</h2>
        <WidgetAgentSkills agent={null} />
      {:else if section === 'team'}
        <WidgetCanon agent={null} {teams} />
      {:else if section === 'mesh'}
        <h2>Hub mesh</h2>
        <dl class="mesh-status">
          <dt>hub</dt><dd data-testid="mesh-hub-url">{health?.federation?.hub_url || '— (start daemon with --hub-url / CHEPHERD_HUB_URL)'}</dd>
          <dt>org</dt><dd>{health?.federation?.org_id || '—'}</dd>
          <dt>peers</dt><dd>{peers.length}</dd>
        </dl>
        <ul class="mesh-peers">
          {#each peers as p (p.sid)}
            <li><span class="glyph">⇄</span> <strong>{p.name || p.sid}</strong> <span class="muted">{p.card?.url?.startsWith?.('hub://') ? 'via hub mesh' : (p.card?.url || '')} · synced {syncedAgo(p)}</span></li>
          {/each}
          {#if !peers.length}<li class="muted">No peers discovered.</li>{/if}
        </ul>
      {:else}
        <h2>Global event stream</h2>
        <div class="dev-pane"><WidgetEvents {events} /></div>
        <h2>Raw MCP log</h2>
        <div class="dev-pane"><WidgetMCPLog {events} /></div>
      {/if}
    </main>
  </div>
</div>

<style>
  .settings-page { position: fixed; inset: 0; z-index: 60; background: var(--bg, #111); display: flex; flex-direction: column; }
  .sp-head { display: flex; align-items: center; gap: 1rem; padding: 0.6rem 1.1rem; border-bottom: 1px solid var(--border, #2a2a2a); }
  .sp-head h1 { font-size: 1rem; margin: 0; }
  .sp-close { margin-left: auto; background: none; border: 1px solid var(--border, #2a2a2a); border-radius: 5px; color: var(--fg, #ddd); font-size: 1rem; width: 1.8rem; height: 1.8rem; cursor: pointer; }
  .sp-body { display: flex; flex: 1; min-height: 0; }
  .sp-nav { display: flex; flex-direction: column; gap: 0.1rem; padding: 0.8rem 0.5rem; min-width: 200px; border-right: 1px solid var(--border, #2a2a2a); }
  .sp-nav button { text-align: left; background: none; border: none; border-radius: 5px; color: var(--fg-muted, #aaa); padding: 0.42rem 0.65rem; cursor: pointer; font-size: 0.88rem; }
  .sp-nav button.active { color: var(--fg, #eee); background: var(--bg-elevated, #1d1d1d); }
  .sp-main { flex: 1; overflow: auto; padding: 1.1rem 2rem; min-width: 0; }
  /* #709.S1.5 — settings-grade form layout: constrained content column
     so re-hosted widgets stop floating in whitespace. */
  .sp-main > :global(*) { max-width: 760px; }
  .sp-intro { color: var(--fg-muted, #999); font-size: 0.85rem; margin: 0 0 1rem; }
  .sp-main h2 { font-size: 0.85rem; text-transform: uppercase; letter-spacing: 0.05em; color: var(--fg-muted, #888); margin: 1.1rem 0 0.5rem; }
  .sp-main h2:first-child { margin-top: 0; }
  .mesh-status { display: grid; grid-template-columns: max-content 1fr; gap: 0.25rem 0.9rem; font-size: 0.88rem; }
  .mesh-status dt { color: var(--fg-muted, #888); }
  .mesh-peers { list-style: none; padding: 0; margin: 0.8rem 0 0; font-size: 0.88rem; }
  .mesh-peers .glyph { color: var(--accent-2, #87ceeb); }
  .muted { color: var(--fg-muted, #888); }
  .dev-pane { max-height: 40vh; overflow: auto; border: 1px solid var(--border, #2a2a2a); border-radius: 6px; }
  .appearance-btn { background: var(--bg-elevated, #1d1d1d); border: 1px solid var(--border, #2a2a2a); border-radius: 5px; color: var(--fg, #ddd); padding: 0.3rem 0.7rem; cursor: pointer; margin-right: 0.4rem; }
  .appearance-row { display: flex; align-items: center; gap: 0.4rem; }
</style>

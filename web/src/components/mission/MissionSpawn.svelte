<!--
  MissionSpawn — spawn view (the spec's spawn flow). Two real paths off the
  data layer:
    - Single agent: POST /api/v1/sessions {name, role, team, cwd, agent}
    - Team template: GET /api/v1/templates → POST /api/v1/templates/{name}/apply
  Modal overlay; on success calls onLaunched (root refreshes) + closes.
-->
<script>
  import { onMount } from 'svelte';

  let { teams = [], defaultCwd = '', onclose, onLaunched } = $props();

  const API = '/api/v1';
  let tab = $state('single'); // single | template
  let templates = $state([]);
  let busy = $state(false);
  let err = $state('');
  let okMsg = $state('');

  // single-agent form
  let name = $state('');
  let role = $state('worker');
  let team = $state('');
  let cwd = $state(defaultCwd || '');
  const ROLES = ['worker', 'reviewer', 'architect', 'tech-lead', 'qa', 'shepherd'];

  // template form
  let selectedTemplate = $state('');
  let tplTeam = $state('');

  const teamNames = $derived((teams || []).map(t => t.name || t));

  async function loadTemplates() {
    try { const r = await fetch(`${API}/templates`); if (r.ok) { const j = await r.json(); templates = j.templates || []; } } catch {}
  }
  onMount(loadTemplates);

  async function spawnSingle() {
    if (!name.trim()) { err = 'name is required'; return; }
    busy = true; err = ''; okMsg = '';
    try {
      const r = await fetch(`${API}/sessions`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: name.trim(), role, team: team.trim() || undefined, cwd: cwd.trim() || undefined }),
      });
      const txt = await r.text();
      if (!r.ok) { let m = `HTTP ${r.status}`; try { m = JSON.parse(txt).error || m; } catch { m = txt.slice(0, 120) || m; } err = m; }
      else { okMsg = `spawned ${name.trim()}`; onLaunched?.(); setTimeout(() => onclose?.(), 600); }
    } catch (e) { err = String(e); }
    busy = false;
  }

  async function applyTemplate() {
    if (!selectedTemplate) { err = 'pick a template'; return; }
    busy = true; err = ''; okMsg = '';
    try {
      const r = await fetch(`${API}/templates/${encodeURIComponent(selectedTemplate)}/apply`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ team: tplTeam.trim() || undefined, cwd: cwd.trim() || undefined }),
      });
      const txt = await r.text();
      if (!r.ok) { let m = `HTTP ${r.status}`; try { m = JSON.parse(txt).error || m; } catch { m = txt.slice(0, 120) || m; } err = m; }
      else { okMsg = `applied ${selectedTemplate}`; onLaunched?.(); setTimeout(() => onclose?.(), 600); }
    } catch (e) { err = String(e); }
    busy = false;
  }
</script>

<div class="overlay" role="dialog" aria-modal="true" onclick={(e) => { if (e.target === e.currentTarget) onclose?.(); }}>
  <div class="panel">
    <header class="ph">
      <span class="pt">◈ SPAWN</span>
      <button class="x" onclick={() => onclose?.()} aria-label="Close">✕</button>
    </header>
    <div class="segtabs">
      <button class:active={tab === 'single'} onclick={() => (tab = 'single')}>Single agent</button>
      <button class:active={tab === 'template'} onclick={() => (tab = 'template')}>Team template</button>
    </div>

    {#if tab === 'single'}
      <div class="form">
        <label>Name<input bind:value={name} placeholder="e.g. worker-1" autocomplete="off" /></label>
        <label>Role
          <select bind:value={role}>{#each ROLES as r}<option value={r}>{r}</option>{/each}</select>
        </label>
        <label>Team
          <input bind:value={team} placeholder="team name (optional)" list="m-teams" autocomplete="off" />
          <datalist id="m-teams">{#each teamNames as t}<option value={t}></option>{/each}</datalist>
        </label>
        <label>Working dir<input bind:value={cwd} placeholder="/home/openova/repos/…" autocomplete="off" /></label>
      </div>
      <footer class="pf">
        {#if err}<span class="err">{err}</span>{/if}
        {#if okMsg}<span class="ok">{okMsg}</span>{/if}
        <button class="go" disabled={busy} onclick={spawnSingle}>{busy ? 'Launching…' : 'Launch agent'}</button>
      </footer>
    {:else}
      <div class="form">
        <label>Template
          <select bind:value={selectedTemplate}>
            <option value="">pick a template…</option>
            {#each templates as t}<option value={t.name}>{t.name}{t.blurb ? ' — ' + t.blurb : ''}</option>{/each}
          </select>
        </label>
        {#if !templates.length}<p class="hint">No templates registered on this daemon. Drop catalog YAML and reopen.</p>{/if}
        <label>Team name<input bind:value={tplTeam} placeholder="new team name (optional)" autocomplete="off" /></label>
        <label>Working dir<input bind:value={cwd} placeholder="/home/openova/repos/…" autocomplete="off" /></label>
      </div>
      <footer class="pf">
        {#if err}<span class="err">{err}</span>{/if}
        {#if okMsg}<span class="ok">{okMsg}</span>{/if}
        <button class="go" disabled={busy || !selectedTemplate} onclick={applyTemplate}>{busy ? 'Applying…' : 'Apply template'}</button>
      </footer>
    {/if}
  </div>
</div>

<style>
  .overlay { position: fixed; inset: 0; z-index: 200; background: color-mix(in srgb, var(--m-bg) 72%, transparent); backdrop-filter: blur(3px); display: flex; align-items: center; justify-content: center; padding: 1rem; }
  .panel { width: 100%; max-width: 30rem; background: var(--m-panel); border: 1px solid var(--m-border-strong); border-radius: 10px; box-shadow: 0 24px 60px -16px var(--m-shadow); overflow: hidden; color: var(--m-fg); }
  .ph { display: flex; align-items: center; justify-content: space-between; padding: 0.7rem 0.9rem; background: var(--m-panel-2); border-bottom: 1px solid var(--m-border); }
  .pt { font-size: 0.72rem; letter-spacing: 0.16em; font-weight: 700; color: var(--m-accent); }
  .x { background: transparent; border: 0; color: var(--m-fg-faint); font-size: 0.9rem; cursor: pointer; }
  .x:hover { color: var(--m-danger); }
  .segtabs { display: flex; gap: 2px; padding: 0.6rem 0.9rem 0; }
  .segtabs button { flex: 1; background: var(--m-panel-3); color: var(--m-fg-dim); border: 1px solid var(--m-border); border-bottom: 0; border-radius: 6px 6px 0 0; padding: 0.45rem; font: inherit; font-size: 0.76rem; cursor: pointer; }
  .segtabs button.active { background: var(--m-panel); color: var(--m-accent-2); border-color: var(--m-border-strong); }
  .form { display: flex; flex-direction: column; gap: 0.6rem; padding: 0.9rem; }
  .form label { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.7rem; color: var(--m-fg-faint); text-transform: uppercase; letter-spacing: 0.06em; }
  .form input, .form select { background: var(--m-bg); color: var(--m-fg); border: 1px solid var(--m-border-strong); border-radius: 5px; padding: 0.45rem 0.55rem; font: inherit; font-size: 0.84rem; text-transform: none; letter-spacing: normal; }
  .form input:focus, .form select:focus { outline: none; border-color: var(--m-accent-2); }
  .hint { font-size: 0.72rem; color: var(--m-fg-faint); margin: 0; }
  .pf { display: flex; align-items: center; gap: 0.6rem; padding: 0.3rem 0.9rem 0.9rem; }
  .err { color: var(--m-danger); font-size: 0.74rem; flex: 1; }
  .ok { color: var(--m-ok); font-size: 0.74rem; flex: 1; }
  .go { margin-left: auto; background: var(--m-accent); color: var(--m-bg); border: 0; border-radius: 6px; padding: 0.5rem 1.1rem; font-weight: 700; font-size: 0.82rem; cursor: pointer; }
  .go:disabled { opacity: 0.5; cursor: not-allowed; }
</style>

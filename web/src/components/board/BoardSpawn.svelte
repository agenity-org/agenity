<!--
  BoardSpawn — launch a new agent onto the board.

  Posts the real spawn body to POST /api/v1/sessions (mirrors
  v09/Stage5Launch.svelte's body: {name, agent, team, role, cwd}).
  Offers team templates too (GET /api/v1/templates →
  POST /api/v1/templates/{name}/apply {team, cwd}).
-->
<script>
  import { onMount } from 'svelte';

  let { teams = [], onclose = () => {}, onspawned = () => {} } = $props();

  const API = '/api/v1';

  let mode = $state('single');     // 'single' | 'template'
  let name = $state('');
  let agent = $state('claude-code');
  let role = $state('worker');
  let team = $state('');
  let cwd = $state('/home/chepherd/repos');
  let templates = $state([]);
  let pickedTemplate = $state('');
  let busy = $state(false);
  let err = $state('');
  let ok = $state('');

  const ROLES = ['worker', 'tech-lead', 'architect', 'reviewer', 'qa', 'shepherd'];
  const AGENTS = ['claude-code', 'codex', 'gemini'];

  let teamNames = $derived((teams || []).map(t => t.name || t).filter(Boolean));

  async function loadTemplates() {
    try { const r = await fetch(`${API}/templates`); const j = await r.json(); templates = j.templates || []; } catch {}
  }
  onMount(loadTemplates);

  async function spawnSingle() {
    if (!name.trim()) { err = 'name is required'; return; }
    busy = true; err = ''; ok = '';
    try {
      const r = await fetch(`${API}/sessions`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: name.trim(),
          agent,
          role,
          team: team.trim() || undefined,
          cwd: cwd.trim() || '/home/chepherd/repos',
          role_id: role,
        }),
      });
      if (!r.ok) { err = await r.text().catch(() => '') || `HTTP ${r.status}`; return; }
      ok = `Spawned ${name.trim()}`;
      onspawned();
      setTimeout(onclose, 600);
    } catch (e) { err = e?.message || 'spawn failed'; }
    finally { busy = false; }
  }

  async function applyTemplate() {
    if (!pickedTemplate) { err = 'pick a template'; return; }
    busy = true; err = ''; ok = '';
    try {
      const r = await fetch(`${API}/templates/${encodeURIComponent(pickedTemplate)}/apply`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ team: team.trim() || undefined, cwd: cwd.trim() || '/home/chepherd/repos' }),
      });
      if (!r.ok) { err = await r.text().catch(() => '') || `HTTP ${r.status}`; return; }
      ok = `Applied template ${pickedTemplate}`;
      onspawned();
      setTimeout(onclose, 700);
    } catch (e) { err = e?.message || 'apply failed'; }
    finally { busy = false; }
  }
</script>

<div class="ov" role="dialog" aria-modal="true" onclick={onclose}>
  <div class="sheet" onclick={(e) => e.stopPropagation()}>
    <header class="sh-head">
      <h2>Launch agent</h2>
      <button class="x" onclick={onclose} aria-label="close">✕</button>
    </header>

    <div class="tabs">
      <button class:active={mode === 'single'} onclick={() => mode = 'single'}>Single agent</button>
      <button class:active={mode === 'template'} onclick={() => mode = 'template'}>Team template</button>
    </div>

    {#if mode === 'single'}
      <div class="form">
        <label>Name<input bind:value={name} placeholder="e.g. alpha" autocomplete="off" /></label>
        <div class="two">
          <label>Agent
            <select bind:value={agent}>{#each AGENTS as a}<option value={a}>{a}</option>{/each}</select>
          </label>
          <label>Role
            <select bind:value={role}>{#each ROLES as r}<option value={r}>{r}</option>{/each}</select>
          </label>
        </div>
        <label>Team
          <input bind:value={team} placeholder="existing or new team name" list="board-teams" autocomplete="off" />
          <datalist id="board-teams">{#each teamNames as t}<option value={t}></option>{/each}</datalist>
        </label>
        <label>Working directory<input bind:value={cwd} placeholder="/home/chepherd/repos/owner/repo" autocomplete="off" /></label>
      </div>
    {:else}
      <div class="form">
        <label>Template
          <select bind:value={pickedTemplate}>
            <option value="">— pick a team shape —</option>
            {#each templates as t}<option value={t.name}>{t.name}{t.description ? ' — ' + t.description : ''}</option>{/each}
          </select>
        </label>
        {#if !templates.length}<p class="hint">No templates available from the daemon. Use Single agent instead.</p>{/if}
        <label>Team name<input bind:value={team} placeholder="new team name" autocomplete="off" /></label>
        <label>Working directory<input bind:value={cwd} placeholder="/home/chepherd/repos/owner/repo" autocomplete="off" /></label>
      </div>
    {/if}

    {#if err}<div class="msg err">{err}</div>{/if}
    {#if ok}<div class="msg ok">{ok}</div>{/if}

    <footer class="sh-foot">
      <button class="ghost" onclick={onclose}>Cancel</button>
      {#if mode === 'single'}
        <button class="go" onclick={spawnSingle} disabled={busy || !name.trim()}>{busy ? 'Launching…' : 'Launch'}</button>
      {:else}
        <button class="go" onclick={applyTemplate} disabled={busy || !pickedTemplate}>{busy ? 'Applying…' : 'Apply template'}</button>
      {/if}
    </footer>
  </div>
</div>

<style>
  .ov {
    position: fixed; inset: 0; z-index: 300; display: flex; align-items: center; justify-content: center;
    background: var(--board-scrim); padding: 1rem;
  }
  .sheet {
    width: min(520px, 96vw); max-height: 90vh; overflow-y: auto;
    background: var(--board-surface); border: 1px solid var(--board-border-strong);
    border-radius: 14px; box-shadow: 0 24px 60px var(--board-shadow);
  }
  .sh-head { display: flex; align-items: center; padding: 1rem 1.2rem 0.6rem; }
  .sh-head h2 { font-size: 1.05rem; margin: 0; flex: 1; color: var(--board-fg); }
  .x { background: transparent; border: 0; color: var(--board-fg-muted); font-size: 1rem; cursor: pointer; }
  .x:hover { color: var(--board-fg); }

  .tabs { display: flex; gap: 0.4rem; padding: 0 1.2rem 0.4rem; }
  .tabs button {
    background: transparent; border: 1px solid var(--board-border); color: var(--board-fg-muted);
    border-radius: 999px; padding: 0.3rem 0.85rem; font-size: 0.8rem; cursor: pointer;
  }
  .tabs button.active { background: var(--board-accent-bg); border-color: var(--board-accent); color: var(--board-accent); }

  .form { display: flex; flex-direction: column; gap: 0.7rem; padding: 0.8rem 1.2rem; }
  .two { display: flex; gap: 0.7rem; }
  .two label { flex: 1; }
  label { display: flex; flex-direction: column; gap: 0.28rem; font-size: 0.76rem; color: var(--board-fg-muted); }
  input, select {
    background: var(--board-input); color: var(--board-fg);
    border: 1px solid var(--board-border-strong); border-radius: 8px;
    padding: 0.45rem 0.55rem; font-size: 0.85rem; font-family: inherit;
  }
  input:focus, select:focus { outline: none; border-color: var(--board-accent); }
  .hint { font-size: 0.74rem; color: var(--board-fg-faint); margin: 0; }

  .msg { margin: 0 1.2rem; padding: 0.5rem 0.7rem; border-radius: 8px; font-size: 0.8rem; word-break: break-word; }
  .msg.err { color: var(--board-danger); background: var(--board-danger-bg); border: 1px solid var(--board-danger); }
  .msg.ok { color: var(--board-ok); background: var(--board-ok-bg); border: 1px solid var(--board-ok); }

  .sh-foot { display: flex; justify-content: flex-end; gap: 0.6rem; padding: 0.9rem 1.2rem 1.2rem; }
  .ghost { background: transparent; border: 1px solid var(--board-border-strong); color: var(--board-fg); border-radius: 8px; padding: 0.45rem 0.95rem; cursor: pointer; font-size: 0.84rem; }
  .ghost:hover { background: var(--board-hover); }
  .go { background: var(--board-accent); color: var(--board-accent-fg); border: 0; border-radius: 8px; padding: 0.45rem 1.1rem; font-weight: 650; font-size: 0.84rem; cursor: pointer; }
  .go:disabled { opacity: 0.5; cursor: not-allowed; }
  .go:hover:not(:disabled) { filter: brightness(1.08); }
</style>

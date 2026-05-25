<script>
  import { onMount } from 'svelte';
  let { onClose, onApplied } = $props();
  const API = '/api-v06/v1';
  let templates = $state([]);
  let selected = $state(null);
  let team = $state('');
  let topology = $state('');               // hub | mesh | custom — '' means use template default
  let cwd = $state('/home/openova/repos/chepherd');
  let busy = $state(false);
  let error = $state('');
  let forkName = $state('');
  let forkingBusy = $state(false);

  async function loadTemplates() {
    try {
      const r = await fetch(`${API}/templates`);
      const data = await r.json();
      templates = data.templates || [];
      if (templates.length && !selected) selected = templates[0].name;
    } catch (e) { error = String(e); }
  }
  onMount(loadTemplates);
  async function apply() {
    if (!selected) return;
    busy = true; error = '';
    try {
      const r = await fetch(`${API}/templates/${selected}/apply`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ team: team || selected, cwd, topology }),
      });
      if (!r.ok) { const e = await r.json().catch(()=>({})); error = e.error || `HTTP ${r.status}`; }
      else { onApplied?.(); onClose?.(); }
    } catch (e) { error = String(e); }
    busy = false;
  }
  async function fork() {
    if (!selected) return;
    const name = (forkName || (selected + '-fork')).trim();
    forkingBusy = true; error = '';
    try {
      const r = await fetch(`${API}/templates/${selected}/fork`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ new_name: name }),
      });
      if (!r.ok) { const e = await r.json().catch(()=>({})); error = e.error || `HTTP ${r.status}`; }
      else { forkName = ''; await loadTemplates(); selected = name; }
    } catch (e) { error = String(e); }
    forkingBusy = false;
  }
</script>

<div class="backdrop" on:click={onClose}>
  <div class="modal" on:click|stopPropagation>
    <header><h2>📦 install a team template</h2><button on:click={onClose}>×</button></header>
    <div class="body">
      <div class="picker">
        <ul>
          {#each templates as t}
            <li class:active={selected === t.name} on:click={() => (selected = t.name)}>
              <strong>{t.name}</strong> <small>· {t.topology} · {t.members} members</small>
              <p>{t.description}</p>
            </li>
          {/each}
        </ul>
      </div>
      <div class="row">
        <label>Team name (defaults to template name) <input bind:value={team} placeholder={selected || ''} /></label>
        <label>Topology <select bind:value={topology}>
          <option value="">(template default)</option>
          <option value="hub">hub (shepherd in the middle)</option>
          <option value="mesh">mesh (peer-to-peer)</option>
          <option value="custom">custom</option>
        </select></label>
      </div>
      <label>Working directory <input bind:value={cwd} /></label>

      <details class="fork">
        <summary>🍴 Fork this template (copy it under your own name → edit YAML on disk → re-apply)</summary>
        <div class="row">
          <label>New template name <input bind:value={forkName} placeholder="{selected}-fork" /></label>
          <button class="ghost" on:click={fork} disabled={forkingBusy || !selected}>{forkingBusy ? 'Forking…' : 'Fork'}</button>
        </div>
        <p class="hint">Forked YAML lands at <code>~/.local/state/chepherd-v06/catalog/&lt;name&gt;.yaml</code>; edit it in your IDE, the template list refreshes on next picker open.</p>
      </details>

      {#if error}<div class="error">{error}</div>{/if}
    </div>
    <footer>
      <button class="ghost" on:click={onClose}>Cancel</button>
      <button class="primary" on:click={apply} disabled={busy || !selected}>{busy ? 'Applying…' : 'Apply template'}</button>
    </footer>
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.65); display: flex; align-items: center; justify-content: center; z-index: 1000; backdrop-filter: blur(2px); }
  .modal { width: min(760px, 94vw); max-height: 88vh; background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 10px; display: flex; flex-direction: column; }
  header { padding: 0.85rem 1.2rem; border-bottom: 1px solid var(--border); display: flex; justify-content: space-between; }
  h2 { margin: 0; color: var(--accent); font-size: 1.05rem; }
  header button { background: transparent; color: var(--fg-muted); border: none; font-size: 1.5rem; cursor: pointer; }
  .body { padding: 1rem 1.2rem; overflow-y: auto; }
  .picker ul { list-style: none; padding: 0; margin: 0 0 0.8rem 0; max-height: 380px; overflow-y: auto; }
  .picker li { padding: 0.6rem 0.7rem; border: 1px solid var(--border); border-radius: 6px; margin-bottom: 0.35rem; cursor: pointer; }
  .picker li:hover { border-color: var(--select-border); background: var(--bg); }
  .picker li.active { border-color: var(--accent); background: rgba(255,165,0,0.06); }
  .picker li small { color: var(--fg-muted); }
  .picker li p { color: var(--fg-muted); font-size: 0.78rem; margin: 0.25rem 0 0 0; white-space: pre-line; }
  label { display: block; margin-top: 0.7rem; color: var(--fg-muted); font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.04em; }
  input { width: 100%; padding: 0.45rem 0.6rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; font-family: ui-monospace, monospace; font-size: 0.88rem; margin-top: 0.2rem; }
  .error { margin-top: 0.7rem; padding: 0.45rem 0.7rem; background: rgba(255,107,107,0.1); border: 1px solid var(--danger); color: var(--danger); border-radius: 6px; font-size: 0.84rem; }
  footer { padding: 0.85rem 1.2rem; border-top: 1px solid var(--border); display: flex; justify-content: flex-end; gap: 0.6rem; }
  .primary { background: var(--accent); color: #000; border: none; border-radius: 6px; padding: 0.45rem 1rem; font-weight: 600; cursor: pointer; }
  .ghost { background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.45rem 1rem; cursor: pointer; }
  .row { display: flex; gap: 0.6rem; align-items: flex-end; }
  .row label { flex: 1; }
  .row button { white-space: nowrap; }
  select { width: 100%; padding: 0.45rem 0.6rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; font-family: ui-monospace, monospace; font-size: 0.88rem; margin-top: 0.2rem; cursor: pointer; }
  details.fork { margin-top: 1rem; border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.55rem 0.7rem; background: var(--bg); }
  details.fork summary { cursor: pointer; color: var(--fg-muted); font-size: 0.8rem; user-select: none; }
  details.fork .hint { color: var(--fg-muted); font-size: 0.75rem; margin: 0.5rem 0 0 0; }
</style>

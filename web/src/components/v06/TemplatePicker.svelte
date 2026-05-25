<script>
  import { onMount } from 'svelte';
  let { onClose, onApplied } = $props();
  const API = '/api-v06/v1';
  let templates = $state([]);
  let selected = $state(null);
  let team = $state('');
  let cwd = $state('/home/openova/repos/chepherd');
  let busy = $state(false);
  let error = $state('');
  onMount(async () => {
    try {
      const r = await fetch(`${API}/templates`);
      const data = await r.json();
      templates = data.templates || [];
      if (templates.length) selected = templates[0].name;
    } catch (e) { error = String(e); }
  });
  async function apply() {
    if (!selected) return;
    busy = true; error = '';
    try {
      const r = await fetch(`${API}/templates/${selected}/apply`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ team: team || selected, cwd }),
      });
      if (!r.ok) { const e = await r.json().catch(()=>({})); error = e.error || `HTTP ${r.status}`; }
      else { onApplied?.(); onClose?.(); }
    } catch (e) { error = String(e); }
    busy = false;
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
      <label>Team name (defaults to template name) <input bind:value={team} placeholder={selected || ''} /></label>
      <label>Working directory <input bind:value={cwd} /></label>
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
</style>

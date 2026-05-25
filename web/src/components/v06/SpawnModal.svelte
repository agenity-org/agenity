<!--
  SpawnModal — spawn a new agent + (optionally) attach to a team. v0.6 spawn modal
  carries the team+role pickers as part of the form because v0.6 promotes them to
  first-class instead of just a field on the agent.
-->
<script>
  let { onClose, onSpawned } = $props();

  let name = $state('');
  let agent = $state('claude-code');
  let team = $state('default');
  let role = $state('worker');
  let cwd = $state('');
  let useDefaultPrompt = $state(true);
  let error = $state('');
  let busy = $state(false);

  const AGENTS = ['claude-code', 'qwen-code', 'aider', 'opencode', 'sovereign-shell'];
  const ROLES = ['worker', 'shepherd', 'reviewer', 'reviewer-discipline', 'reviewer-architect', 'tester', 'architect'];

  function autoName(c) {
    if (!c) return '';
    const base = c.split('/').filter(Boolean).pop() || 'agent';
    return base.toLowerCase().replace(/[^a-z0-9]+/g, '-');
  }

  async function submit() {
    error = '';
    if (!name && cwd) name = autoName(cwd);
    if (!name) { error = 'name required'; return; }
    busy = true;
    try {
      const r = await fetch('/api/v1/sessions', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, agent, team, role, cwd, use_default_prompt: useDefaultPrompt }),
      });
      if (!r.ok) { const e = await r.json().catch(()=>({})); error = e.error || `HTTP ${r.status}`; }
      else { onSpawned?.(); onClose?.(); }
    } catch (e) { error = String(e); }
    busy = false;
  }
</script>

<div class="backdrop" on:click={onClose}>
  <div class="modal" on:click|stopPropagation>
    <header><h2>+ spawn agent</h2><button on:click={onClose}>×</button></header>
    <div class="body">
      <label>Working directory <input bind:value={cwd} placeholder="/home/openova/repos/chepherd" /></label>
      <div class="row">
        <label>Name (auto from folder if blank) <input bind:value={name} placeholder={autoName(cwd)} /></label>
        <label>Agent <select bind:value={agent}>{#each AGENTS as a}<option value={a}>{a}</option>{/each}</select></label>
      </div>
      <div class="row">
        <label>Team <input bind:value={team} placeholder="default" /></label>
        <label>Role <select bind:value={role}>{#each ROLES as r}<option value={r}>{r}</option>{/each}</select></label>
      </div>
      <label class="check"><input type="checkbox" bind:checked={useDefaultPrompt} /> Make the agent chepherd-aware (recommended)</label>
      {#if error}<div class="error">{error}</div>{/if}
    </div>
    <footer>
      <button class="ghost" on:click={onClose}>Cancel</button>
      <button class="primary" on:click={submit} disabled={busy}>{busy ? 'Spawning…' : 'Spawn'}</button>
    </footer>
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.65); display: flex; align-items: center; justify-content: center; z-index: 1000; backdrop-filter: blur(2px); }
  .modal { width: min(640px, 92vw); background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 10px; display: flex; flex-direction: column; max-height: 88vh; }
  header { padding: 0.85rem 1.2rem; border-bottom: 1px solid var(--border); display: flex; justify-content: space-between; }
  h2 { margin: 0; color: var(--accent); font-size: 1.05rem; }
  header button { background: transparent; color: var(--fg-muted); border: none; font-size: 1.5rem; cursor: pointer; }
  .body { padding: 1rem 1.2rem; overflow-y: auto; }
  label { display: block; margin-top: 0.7rem; color: var(--fg-muted); font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.04em; }
  label.check { display: flex; align-items: center; gap: 0.5rem; text-transform: none; letter-spacing: normal; color: var(--fg); font-size: 0.88rem; margin-top: 1.5rem; }
  input[type=text], input:not([type]), select { width: 100%; padding: 0.45rem 0.6rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; font-family: ui-monospace, monospace; font-size: 0.88rem; margin-top: 0.2rem; }
  .row { display: flex; gap: 0.6rem; }
  .row label { flex: 1; }
  .error { margin-top: 0.7rem; padding: 0.45rem 0.7rem; background: rgba(255,107,107,0.1); border: 1px solid var(--danger); color: var(--danger); border-radius: 6px; font-size: 0.84rem; }
  footer { padding: 0.85rem 1.2rem; border-top: 1px solid var(--border); display: flex; justify-content: flex-end; gap: 0.6rem; }
  .primary { background: var(--accent); color: #000; border: none; border-radius: 6px; padding: 0.45rem 1rem; font-weight: 600; cursor: pointer; }
  .ghost { background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.45rem 1rem; cursor: pointer; }
</style>

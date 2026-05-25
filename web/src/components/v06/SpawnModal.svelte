<!--
  SpawnModal — spawn a new agent + (optionally) attach to a team. v0.6 spawn modal
  carries the team+role pickers as part of the form because v0.6 promotes them to
  first-class instead of just a field on the agent.
-->
<script>
  import { onMount } from 'svelte';
  let { onClose, onSpawned } = $props();
  const API = '/api-v06/v1';

  let name = $state('');
  let agent = $state('claude-code');
  let team = $state('default');
  let role = $state('worker');
  let cwd = $state('');
  let mode = $state('fresh'); // 'fresh' | 'resume'
  let resumeUuid = $state('');
  let useDefaultPrompt = $state(true);
  let error = $state('');
  let busy = $state(false);

  // Resume picker state (ported from v0.5)
  let claudeSessions = $state([]);
  let claudeQuery = $state('');
  let recentFolders = $state([]);

  const AGENTS = ['claude-code', 'qwen-code', 'aider', 'opencode', 'sovereign-shell'];
  const ROLES = ['worker', 'shepherd', 'reviewer', 'reviewer-discipline', 'reviewer-architect', 'tester', 'architect'];

  function autoName(c) {
    if (!c) return '';
    const base = c.split('/').filter(Boolean).pop() || 'agent';
    return base.toLowerCase().replace(/[^a-z0-9]+/g, '-');
  }

  async function loadResumeOptions(forCwd) {
    try {
      const url = forCwd ? `${API}/claude-sessions?cwd=${encodeURIComponent(forCwd)}` : `${API}/claude-sessions`;
      const r = await fetch(url);
      const data = await r.json();
      claudeSessions = data.sessions || [];
    } catch { claudeSessions = []; }
  }

  async function loadRecentFolders() {
    try {
      const r = await fetch(`${API}/folders/recent`);
      const data = await r.json();
      recentFolders = data.folders || [];
    } catch {}
  }

  let filteredResumes = $derived.by(() => {
    const q = claudeQuery.trim().toLowerCase();
    if (!q) return claudeSessions.slice(0, 30);
    return claudeSessions.filter(s =>
      (s.cwd || '').toLowerCase().includes(q) ||
      (s.first_message || '').toLowerCase().includes(q) ||
      (s.uuid || '').toLowerCase().includes(q)
    ).slice(0, 30);
  });

  function pickResume(s) {
    resumeUuid = s.uuid;
    cwd = s.cwd;
    if (!name) name = autoName(s.cwd);
  }
  function pickFolder(f) {
    cwd = f.path;
    if (!name) name = autoName(f.path);
    if (mode === 'resume') loadResumeOptions(f.path);
  }

  onMount(() => {
    loadRecentFolders();
    loadResumeOptions(null);
  });

  async function submit() {
    error = '';
    if (!name && cwd) name = autoName(cwd);
    if (!name) { error = 'name required'; return; }
    busy = true;
    try {
      const body = { name, agent, team, role, cwd, use_default_prompt: useDefaultPrompt };
      if (mode === 'resume' && resumeUuid) body.resume_uuid = resumeUuid;
      const r = await fetch(`${API}/sessions`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
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
      <div class="mode-toggle">
        <button class:active={mode==='fresh'} on:click={() => mode='fresh'}>Fresh session</button>
        <button class:active={mode==='resume'} on:click={() => { mode='resume'; loadResumeOptions(cwd); }}>Resume previous Claude session</button>
      </div>

      <label>Working directory <input bind:value={cwd} on:input={() => { if (mode==='resume') loadResumeOptions(cwd); }} placeholder="/home/openova/repos/chepherd" /></label>

      {#if recentFolders.length}
        <div class="folder-chips">
          {#each recentFolders.slice(0,8) as f}
            <button class="chip" class:active={cwd===f.path} on:click={() => pickFolder(f)}>
              <code>{f.path}</code><small>{f.sessions}</small>
            </button>
          {/each}
        </div>
      {/if}

      {#if mode === 'resume'}
        <label>Filter sessions <input bind:value={claudeQuery} placeholder="filter by uuid / cwd / first message..." /></label>
        <ul class="resume-list">
          {#each filteredResumes as s}
            <li class:active={resumeUuid === s.uuid} on:click={() => pickResume(s)}>
              <div class="resume-head">
                <code class="uuid">{s.uuid.slice(0,8)}</code>
                <span class="resume-cwd">{s.cwd}</span>
                <span class="resume-mod">{new Date(s.modified).toLocaleString()}</span>
              </div>
              {#if s.first_message}<div class="resume-msg">"{s.first_message}"</div>{/if}
            </li>
          {/each}
          {#if !filteredResumes.length}<li class="empty">No matching sessions.</li>{/if}
        </ul>
      {/if}

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
  .mode-toggle { display: flex; gap: 0.25rem; background: var(--bg); border-radius: 6px; padding: 0.22rem; border: 1px solid var(--border-strong); }
  .mode-toggle button { flex: 1; padding: 0.45rem; background: transparent; color: var(--fg-muted); border: none; border-radius: 4px; cursor: pointer; font-size: 0.88rem; }
  .mode-toggle button.active { background: var(--accent); color: #000; font-weight: 600; }
  .folder-chips { display: flex; flex-wrap: wrap; gap: 0.3rem; margin-top: 0.5rem; }
  .chip { background: var(--bg); border: 1px solid var(--border-strong); border-radius: 4px; padding: 0.3rem 0.5rem; cursor: pointer; font-size: 0.75rem; color: var(--fg-muted); display: flex; align-items: center; gap: 0.35rem; }
  .chip:hover { border-color: var(--accent); }
  .chip.active { border-color: var(--accent); background: rgba(255,165,0,0.06); }
  .chip code { color: var(--accent-2); font-size: 0.75rem; }
  .chip small { color: var(--fg-faint); font-size: 0.68rem; }
  .resume-list { list-style: none; padding: 0; margin: 0.5rem 0 0 0; max-height: 260px; overflow-y: auto; }
  .resume-list li { padding: 0.55rem 0.7rem; border: 1px solid var(--border-strong); border-radius: 6px; margin-bottom: 0.35rem; cursor: pointer; background: var(--bg-input); }
  .resume-list li:hover { border-color: var(--select-border); }
  .resume-list li.active { border-color: var(--accent); background: var(--select-bg); }
  .resume-list li.empty { color: var(--fg-faint); cursor: default; text-align: center; padding: 0.7rem; }
  .resume-head { display: flex; gap: 0.5rem; align-items: center; font-size: 0.78rem; }
  .resume-head .uuid { color: var(--accent-2); }
  .resume-head .resume-cwd { color: var(--fg); flex: 1; word-break: break-all; }
  .resume-head .resume-mod { color: var(--fg-muted); }
  .resume-msg { color: var(--fg-muted); font-size: 0.8rem; margin-top: 0.25rem; font-style: italic; }
</style>

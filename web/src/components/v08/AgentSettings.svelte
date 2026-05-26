<!--
  AgentSettings — single modal that consolidates everything operator
  needs to tune one running agent. Tabs:
    1. Prompt   — view + send refined working-instructions
    2. Skills   — context budget / model tier / discipline / velocity / $-cap
    3. Canon    — read + edit the team's CLAUDE.md (shared with team)
    4. Membership — current team, change team, change role
    5. Actions  — Pause / Resume / Restart / Stop
-->
<script>
  import { onMount } from 'svelte';
  let { agent, teams, onClose } = $props();
  const API = '/api-v08/v1';
  let tab = $state('prompt');

  // Prompt state
  let promptDraft = $state('');
  let promptDirty = $state(false);
  let promptSaving = $state(false);
  let promptErr = $state('');

  // Skills state
  let skills = $state({ context_budget: 0, model_tier: '', discipline_weight: 0, velocity_expect: '', token_budget_usd: 0 });
  let skillsSaving = $state(false);

  // Canon state
  let canonBody = $state('');
  let canonDraft = $state('');
  let canonEditing = $state(false);

  // Membership state — list ALL memberships for this agent, not just the primary team.
  let memberships = $state([]);
  let addTeam = $state('');
  let addRole = $state('worker');
  let renaming = $state(false);
  let renameDraft = $state(agent?.name || '');

  onMount(() => {
    promptDraft = agent?.system_prompt || '';
    renameDraft = agent?.name || '';
    const s = agent?.stat_sheet || {};
    skills = {
      context_budget: s.context_budget || 0,
      model_tier: s.model_tier || '',
      discipline_weight: s.discipline_weight || 0,
      velocity_expect: s.velocity_expect || '',
      token_budget_usd: s.token_budget_usd || 0,
    };
    loadCanon();
    loadMemberships();
  });
  async function loadMemberships() {
    try {
      const r = await fetch(`${API}/memberships?agent=${encodeURIComponent(agent.name)}`);
      const d = await r.json();
      memberships = d.memberships || [];
    } catch {}
  }
  async function addMembership() {
    if (!addTeam) return;
    await fetch(`${API}/memberships`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ agent: agent.name, team: addTeam, role: addRole }),
    });
    addTeam = ''; addRole = 'worker';
    await loadMemberships();
  }
  async function removeMembership(team) {
    await fetch(`${API}/memberships?agent=${encodeURIComponent(agent.name)}&team=${encodeURIComponent(team)}`, { method: 'DELETE' });
    await loadMemberships();
  }
  async function changeRole(team, newRole) {
    await fetch(`${API}/memberships`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ agent: agent.name, team, role: newRole }),
    });
    await loadMemberships();
  }
  async function doRename() {
    if (!renameDraft || renameDraft === agent.name) return;
    renaming = true;
    const r = await fetch(`${API}/sessions/${agent.name}/rename`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ new_name: renameDraft }),
    });
    renaming = false;
    if (r.ok) onClose?.();
  }

  async function loadCanon() {
    if (!agent?.team) return;
    try {
      const r = await fetch(`${API}/teams/${agent.team}/canon`);
      const d = await r.json();
      canonBody = d.body || '';
      canonDraft = d.body || '';
    } catch {}
  }

  async function savePrompt() {
    promptSaving = true; promptErr = '';
    try {
      const r = await fetch(`${API}/sessions/${agent.name}/poke-prompt`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: promptDraft }),
      });
      if (!r.ok) { const e = await r.json().catch(()=>({})); promptErr = e.error || `HTTP ${r.status}`; }
      else { promptDirty = false; }
    } catch (e) { promptErr = String(e); }
    promptSaving = false;
  }

  async function saveSkills() {
    skillsSaving = true;
    const patch = {};
    if (skills.context_budget) patch.context_budget = +skills.context_budget;
    if (skills.model_tier) patch.model_tier = skills.model_tier;
    if (skills.discipline_weight) patch.discipline_weight = +skills.discipline_weight;
    if (skills.velocity_expect) patch.velocity_expect = skills.velocity_expect;
    if (skills.token_budget_usd) patch.token_budget_usd = +skills.token_budget_usd;
    await fetch(`${API}/sessions/${agent.name}/stat-sheet`, {
      method: 'PATCH', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(patch),
    });
    skillsSaving = false;
  }

  async function saveCanon() {
    await fetch(`${API}/teams/${agent.team}/canon`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ body: canonDraft }),
    });
    canonBody = canonDraft;
    canonEditing = false;
  }

  async function changeTeam() {
    changingTeam = true;
    // leave current team
    if (agent.team) {
      await fetch(`${API}/memberships?agent=${encodeURIComponent(agent.name)}&team=${encodeURIComponent(agent.team)}`, { method: 'DELETE' });
    }
    await fetch(`${API}/memberships`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ agent: agent.name, team: pickTeam, role: pickRole }),
    });
    changingTeam = false;
    onClose?.();
  }

  async function action(act) {
    let url, method, body;
    switch (act) {
      case 'pause':   url = `${API}/sessions/${agent.name}/pause`;   method='POST'; body = JSON.stringify({ paused: true }); break;
      case 'unpause': url = `${API}/sessions/${agent.name}/pause`;   method='POST'; body = JSON.stringify({ paused: false }); break;
      case 'restart': url = `${API}/sessions/${agent.name}/restart`; method='POST'; body = null; break;
      case 'stop':    url = `${API}/sessions/${agent.name}`;         method='DELETE'; body = null; break;
    }
    await fetch(url, { method, headers: body ? { 'Content-Type': 'application/json' } : {}, body });
    if (act === 'stop') onClose?.();
  }
</script>

<div class="backdrop" on:click={onClose}>
  <div class="modal" on:click|stopPropagation>
    <header>
      <h2>{agent?.name} <small>· {agent?.role} · team {agent?.team}</small></h2>
      <button on:click={onClose}>×</button>
    </header>
    <nav class="tabs">
      <button class:active={tab==='prompt'} on:click={() => tab='prompt'}>✏ Prompt</button>
      <button class:active={tab==='skills'} on:click={() => tab='skills'}>🎮 Skills</button>
      <button class:active={tab==='canon'} on:click={() => tab='canon'}>📜 Canon</button>
      <button class:active={tab==='membership'} on:click={() => tab='membership'}>🧩 Membership</button>
      <button class:active={tab==='actions'} on:click={() => tab='actions'}>⚙ Actions</button>
    </nav>
    <div class="body">
      {#if tab === 'prompt'}
        <p class="hint">Edit the working instructions. Save sends a fresh user message "Your updated working instructions from the operator: …" — Claude picks it up without restart.</p>
        <textarea bind:value={promptDraft} on:input={() => (promptDirty = true)} rows="16"></textarea>
        {#if promptErr}<div class="err">{promptErr}</div>{/if}
        <div class="row"><button class="primary" on:click={savePrompt} disabled={promptSaving || !promptDirty}>{promptSaving ? 'Sending…' : 'Send to agent'}</button></div>
      {:else if tab === 'skills'}
        <p class="hint">Per-agent stat sheet — defaults shipped per role; override only what you want. Save patches the runtime.</p>
        <div class="grid">
          <label>Context budget<input type="number" min="0" step="10000" bind:value={skills.context_budget} /></label>
          <label>Model tier
            <select bind:value={skills.model_tier}>
              <option value="">(role default)</option>
              <option value="haiku">haiku</option>
              <option value="sonnet">sonnet</option>
              <option value="opus">opus</option>
              <option value="qwen">qwen</option>
            </select>
          </label>
          <label>Discipline weight<input type="number" min="0" max="3" step="0.1" bind:value={skills.discipline_weight} /></label>
          <label>Velocity expect
            <select bind:value={skills.velocity_expect}>
              <option value="">(role default)</option>
              <option value="low">low</option>
              <option value="medium">medium</option>
              <option value="high">high</option>
            </select>
          </label>
          <label>Token budget $/session<input type="number" min="0" step="0.5" bind:value={skills.token_budget_usd} /></label>
        </div>
        <div class="row"><button class="primary" on:click={saveSkills} disabled={skillsSaving}>{skillsSaving ? 'Saving…' : 'Save'}</button></div>
      {:else if tab === 'canon'}
        <p class="hint">Team CLAUDE.md — every member reads this each tick (shepherd via <code>read_canon</code> MCP). Shared across the whole team.</p>
        {#if canonEditing}
          <textarea bind:value={canonDraft} rows="18"></textarea>
          <div class="row">
            <button class="ghost" on:click={() => { canonDraft = canonBody; canonEditing = false; }}>Cancel</button>
            <button class="primary" on:click={saveCanon}>Save</button>
          </div>
        {:else}
          <pre class="body">{canonBody || '(no canon yet — click Edit to create one)'}</pre>
          <div class="row"><button class="ghost" on:click={() => (canonEditing = true)}>Edit</button></div>
        {/if}
      {:else if tab === 'membership'}
        <p class="hint">Memberships connect this agent to one or more teams, each with its own role. Add, remove, or change roles below.</p>
        <table class="mem-table">
          <thead><tr><th>Team</th><th>Role</th><th></th></tr></thead>
          <tbody>
            {#each memberships as m (m.team_name)}
              <tr>
                <td>{m.team_name}</td>
                <td>
                  <select value={m.role} on:change={(e) => changeRole(m.team_name, e.target.value)}>
                    {#each ['worker', 'shepherd', 'reviewer', 'tester', 'architect'] as r}<option value={r}>{r}</option>{/each}
                  </select>
                </td>
                <td><button class="danger" on:click={() => removeMembership(m.team_name)}>✕ Remove</button></td>
              </tr>
            {/each}
            {#if !memberships.length}<tr><td colspan="3" class="empty">No memberships yet — add one below.</td></tr>{/if}
          </tbody>
        </table>
        <div class="add-row">
          <select bind:value={addTeam}>
            <option value="">Pick team…</option>
            {#each (teams || []).filter(t => !memberships.find(m => m.team_name === t.name)) as t}<option value={t.name}>{t.name}</option>{/each}
          </select>
          <select bind:value={addRole}>
            {#each ['worker', 'shepherd', 'reviewer', 'tester', 'architect'] as r}<option value={r}>{r}</option>{/each}
          </select>
          <button class="primary" on:click={addMembership} disabled={!addTeam}>+ Add membership</button>
        </div>
      {:else if tab === 'actions'}
        <p class="hint">Lifecycle actions on this agent. Rename: change the @-address operator-side. Restart: reboot preserving cwd / role / prompt / stat sheet. Stop: tear down.</p>
        <label>Rename agent <input bind:value={renameDraft} placeholder={agent?.name} /></label>
        <div class="row"><button class="primary" on:click={doRename} disabled={renaming || !renameDraft || renameDraft === agent?.name}>{renaming ? 'Renaming…' : 'Rename'}</button></div>
        <hr style="margin: 1rem 0; border: none; border-top: 1px solid var(--border);" />
        <div class="actions">
          <button on:click={() => action('pause')}>⏸ Pause</button>
          <button on:click={() => action('unpause')}>▶ Resume</button>
          <button on:click={() => action('restart')}>↻ Restart</button>
          <button class="danger" on:click={() => action('stop')}>■ Stop</button>
        </div>
      {/if}
    </div>
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.65); display: flex; align-items: center; justify-content: center; z-index: 1000; backdrop-filter: blur(2px); }
  .modal { width: min(820px, 96vw); max-height: 94vh; background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 10px; display: flex; flex-direction: column; }
  header { display: flex; align-items: center; padding: 0.85rem 1.2rem; border-bottom: 1px solid var(--border); gap: 0.6rem; }
  h2 { margin: 0; color: var(--accent); flex: 1; }
  h2 small { color: var(--fg-muted); font-weight: normal; }
  header > button { background: transparent; color: var(--fg-muted); border: none; font-size: 1.5rem; cursor: pointer; }
  nav.tabs { display: flex; gap: 0.2rem; padding: 0.4rem 0.7rem; background: var(--bg); border-bottom: 1px solid var(--border); }
  nav.tabs button { padding: 0.4rem 0.85rem; background: transparent; color: var(--fg-muted); border: none; border-radius: 6px; cursor: pointer; }
  nav.tabs button.active { background: var(--bg-elev); color: var(--accent); }
  .body { padding: 1rem 1.2rem; overflow-y: auto; flex: 1; min-height: 280px; }
  .hint { color: var(--fg-muted); margin: 0 0 0.7rem 0; }
  textarea { width: 100%; padding: 0.55rem 0.7rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; font-family: ui-monospace, monospace; resize: vertical; box-sizing: border-box; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0.4rem 0.7rem; }
  label { display: block; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.04em; }
  input, select { width: 100%; padding: 0.4rem 0.55rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 4px; font-family: ui-monospace, monospace; margin-top: 0.15rem; box-sizing: border-box; }
  pre.body { background: var(--bg-input); padding: 0.7rem; border-radius: 6px; margin: 0; overflow: auto; white-space: pre-wrap; word-break: break-word; max-height: 50vh; }
  .row { margin-top: 0.9rem; display: flex; gap: 0.55rem; justify-content: flex-end; }
  .actions { display: grid; grid-template-columns: 1fr 1fr; gap: 0.6rem; }
  .actions button { padding: 0.7rem 1rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; cursor: pointer; }
  .actions button.danger { color: var(--danger); border-color: var(--danger); }
  .mem-table { width: 100%; border-collapse: collapse; margin-bottom: 0.8rem; }
  .mem-table th { text-align: left; padding: 0.4rem 0.5rem; color: var(--fg-muted); border-bottom: 1px solid var(--border); }
  .mem-table td { padding: 0.4rem 0.5rem; border-bottom: 1px solid var(--border); }
  .mem-table td.empty { color: var(--fg-faint); text-align: center; padding: 1rem; }
  .mem-table button.danger { background: transparent; color: var(--danger); border: 1px solid var(--danger); border-radius: 4px; padding: 0.25rem 0.55rem; cursor: pointer; }
  .add-row { display: flex; gap: 0.5rem; align-items: center; }
  .add-row select { flex: 1; }
  button.primary { background: var(--accent); color: #000; border: none; border-radius: 6px; padding: 0.45rem 1rem; font-weight: 600; cursor: pointer; }
  button.primary:disabled { opacity: 0.4; cursor: not-allowed; }
  button.ghost { background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.45rem 0.95rem; cursor: pointer; }
  .err { color: var(--danger); margin-top: 0.6rem; }
</style>

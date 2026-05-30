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
  let globalMD = $state('');
  let globalMDOpen = $state(false);

  // Skills state
  let skills = $state({ context_budget: 0, model_tier: '', discipline_weight: 0, velocity_expect: '', token_budget_usd: 0 });
  let skillsSaving = $state(false);
  let modelApplying = $state(false);

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

  // Esc closes the modal. Operator request 2026-05-29.
  $effect(() => {
    function onKey(e) { if (e.key === 'Escape') onClose?.(); }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });


  // Vault / Accounts state
  let vaultCreds = $state([]);
  let vaultProviders = $state([]);
  let vaultAdding = $state(false);
  let vaultForm = $state({ provider: 'anthropic-api', label: '', env_var: '', value: '' });
  let vaultSaving = $state(false);

  // #415 P0 — Per-session AgentCard. Pulls from #404 P0.1 endpoint so
  // the Skills tab shows the actual chepherd skills + role capabilities
  // the agent ships with, not just the stat sheet. Operator's complaint
  // 2026-05-31: "🎮 Skills tab shows completely irrelevant content"
  // because pre-#415 it ONLY showed stat sheet (context budget / model
  // tier / etc). Now shows skills + capabilities ABOVE the stat sheet.
  let agentCard = $state(null);
  async function loadAgentCard() {
    if (!agent?.name) return;
    try {
      const r = await fetch(`${API}/sessions/${encodeURIComponent(agent.name)}/agent-card`);
      if (r.ok) {
        agentCard = await r.json();
      }
    } catch {}
  }

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
    loadGlobalMD();
    loadVault();
    loadAgentCard();
  });

  async function loadGlobalMD() {
    try {
      const r = await fetch(`${API}/runtime/global-md`);
      const d = await r.json();
      globalMD = d.body || '';
    } catch {}
  }
  async function loadVault() {
    try {
      const [cr, pr] = await Promise.all([
        fetch(`${API}/vault`).then(r => r.ok ? r.json() : []),
        fetch(`${API}/vault/providers`).then(r => r.ok ? r.json() : []),
      ]);
      vaultCreds = Array.isArray(cr) ? cr : [];
      vaultProviders = Array.isArray(pr) ? pr : [];
    } catch {}
  }
  async function saveVaultCred() {
    if (!vaultForm.provider || !vaultForm.value) return;
    vaultSaving = true;
    try {
      const r = await fetch(`${API}/vault`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(vaultForm),
      });
      if (r.ok) {
        vaultForm = { provider: 'anthropic-api', label: '', env_var: '', value: '' };
        vaultAdding = false;
        await loadVault();
      }
    } finally { vaultSaving = false; }
  }
  async function deleteVaultCred(id) {
    await fetch(`${API}/vault/${encodeURIComponent(id)}`, { method: 'DELETE' });
    await loadVault();
  }
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

  const MODEL_IDS = {
    haiku:  'claude-haiku-4-5-20251001',
    sonnet: 'claude-sonnet-4-6',
    opus:   'claude-opus-4-7',
    qwen:   'qwen-coder',  // non-claude; just show notice
  };
  async function applyModel() {
    if (!skills.model_tier) return;
    const modelId = MODEL_IDS[skills.model_tier] || skills.model_tier;
    if (skills.model_tier === 'qwen') {
      alert('Model change for qwen-code requires a restart. Save + restart from the right-click menu.');
      return;
    }
    modelApplying = true;
    await fetch(`${API}/sessions/${agent.name}/send`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ body: `/model ${modelId}` }),
    });
    modelApplying = false;
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
      <button class:active={tab==='actions'} on:click={() => tab='actions'}>✎ Rename</button>
      <button class:active={tab==='accounts'} on:click={() => tab='accounts'}>🔑 Accounts</button>
    </nav>
    <div class="body">
      {#if tab === 'prompt'}
        <div class="source-stack">
          <!-- Source 1: Global ~/.claude/CLAUDE.md -->
          <details class="source-block" bind:open={globalMDOpen}>
            <summary class="source-head">
              <span class="source-label">① Global <code>~/.claude/CLAUDE.md</code></span>
              <span class="source-badge ro">read-only</span>
            </summary>
            <pre class="source-body">{globalMD || '(empty)'}</pre>
          </details>
          <!-- Source 2: Team canon -->
          <div class="source-block">
            <div class="source-head">
              <span class="source-label">② Team canon <code>{agent?.team || '—'}/CLAUDE.md</code></span>
              <span class="source-badge ro">read-only · edit in Canon tab</span>
            </div>
            <pre class="source-body">{canonBody || '(no team canon yet)'}</pre>
          </div>
          <!-- Source 3: Agent system prompt (editable) -->
          <div class="source-block">
            <div class="source-head">
              <span class="source-label">③ Agent system prompt</span>
              <span class="source-badge">editable · sends without restart</span>
            </div>
            <textarea bind:value={promptDraft} on:input={() => (promptDirty = true)} rows="10"></textarea>
            {#if promptErr}<div class="err">{promptErr}</div>{/if}
            <div class="row"><button class="primary" on:click={savePrompt} disabled={promptSaving || !promptDirty}>{promptSaving ? 'Sending…' : 'Send to agent'}</button></div>
          </div>
        </div>
      {:else if tab === 'skills'}
        <!-- #415 P0 — chepherd skills + role capabilities from the
             per-session AgentCard (#404 P0.1 endpoint). Pre-#415 this
             tab only showed the stat sheet, which operator flagged as
             "completely irrelevant" because the actual skill content
             is the .claude/skills/*/SKILL.md set + the role-derived
             capability mapping. -->
        <section class="skills-section">
          <h3 class="section-head">Chepherd skills shipped with this agent</h3>
          <p class="hint">Skills are recipes the agent invokes via claude-code's <code>/skills</code> command. Read at spawn time from <code>~/.claude/skills/&lt;name&gt;/SKILL.md</code>.</p>
          {#if agentCard?.skills?.length}
            <ul class="skill-list">
              {#each agentCard.skills as skill}
                <li><code>{skill}</code></li>
              {/each}
            </ul>
          {:else}
            <p class="empty">No skills loaded — agent-card endpoint returned no skills, or the agent isn't running.</p>
          {/if}
        </section>
        <section class="skills-section">
          <h3 class="section-head">Role capabilities</h3>
          <p class="hint">Capabilities advertised by this agent's role (<code>{agentCard?.role || agent?.role || '—'}</code>) — peer agents read these from <code>chepherd.get_peer_card</code> to know what to expect when interacting.</p>
          {#if agentCard?.capabilities?.length}
            <ul class="capability-list">
              {#each agentCard.capabilities as cap}
                <li><code>{cap}</code></li>
              {/each}
            </ul>
          {:else}
            <p class="empty">No capabilities — role may be unknown to the capability map. <code>general-purpose</code> is the default fallback.</p>
          {/if}
        </section>
        <section class="skills-section">
          <h3 class="section-head">Stat sheet · discipline matrix</h3>
          <p class="hint">Per-agent stat sheet — defaults shipped per role; override only what you want. Save patches the runtime.</p>
        <div class="settings-grid">
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
        <div class="row">
          <button class="ghost" on:click={applyModel} disabled={!skills.model_tier || modelApplying} title="Send /model command to agent PTY — takes effect immediately for claude-code">{modelApplying ? 'Applying…' : '↪ Apply model now'}</button>
          <button class="primary" on:click={saveSkills} disabled={skillsSaving}>{skillsSaving ? 'Saving…' : 'Save'}</button>
        </div>
        </section>
      {:else if tab === 'canon'}
        <p class="hint">Team CLAUDE.md — every member reads this each tick (chepherd via <code>read_canon</code> MCP). Shared across the whole team.</p>
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
                    {#each ['worker', 'orchestrator', 'shepherd', 'reviewer', 'tester', 'architect'] as r}<option value={r}>{r}</option>{/each}
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
            {#each ['worker', 'orchestrator', 'shepherd', 'reviewer', 'tester', 'architect'] as r}<option value={r}>{r}</option>{/each}
          </select>
          <button class="primary" on:click={addMembership} disabled={!addTeam}>+ Add membership</button>
        </div>
      {:else if tab === 'actions'}
        <p class="hint">Change the agent's @-address (name). The session, cwd, role, and history remain unchanged. Pause / Resume / Restart / Stop are in the right-click menu on the session list.</p>
        <label>New name <input bind:value={renameDraft} placeholder={agent?.name} /></label>
        <div class="row"><button class="primary" on:click={doRename} disabled={renaming || !renameDraft || renameDraft === agent?.name}>{renaming ? 'Renaming…' : 'Rename'}</button></div>
      {:else if tab === 'accounts'}
        <p class="hint">Encrypted credential vault — values are AES-256-GCM encrypted at rest, injected as env vars into agents at spawn. No values are displayed after save.</p>
        {#if vaultCreds.length}
        <table class="vault-table">
          <thead><tr><th>Provider</th><th>Label</th><th>Env var</th><th>Added</th><th></th></tr></thead>
          <tbody>
            {#each vaultCreds as c (c.id)}
            <tr>
              <td><span class="vault-provider">{c.provider_label || c.provider}</span></td>
              <td>{c.label || '—'}</td>
              <td><code>{c.env_var}</code></td>
              <td class="muted">{new Date(c.created_at).toLocaleDateString()}</td>
              <td><button class="danger" on:click={() => deleteVaultCred(c.id)}>✕</button></td>
            </tr>
            {/each}
          </tbody>
        </table>
        {:else}
        <p class="hint muted">No credentials stored yet.</p>
        {/if}
        {#if vaultAdding}
        <div class="vault-form">
          <label>Provider
            <select bind:value={vaultForm.provider} on:change={() => {
              const p = vaultProviders.find(x => x.id === vaultForm.provider);
              if (p && !vaultForm.env_var) vaultForm.env_var = p.default_env;
            }}>
              {#each vaultProviders as p}<option value={p.id}>{p.label}</option>{/each}
            </select>
          </label>
          <label>Label (optional) <input bind:value={vaultForm.label} placeholder="e.g. work claude max" /></label>
          <label>Env var <input bind:value={vaultForm.env_var} placeholder="e.g. ANTHROPIC_API_KEY" /></label>
          <label>Value (secret) <input type="password" bind:value={vaultForm.value} placeholder="sk-ant-..." /></label>
          <div class="row">
            <button class="ghost" on:click={() => { vaultAdding = false; vaultForm = { provider: 'anthropic-api', label: '', env_var: '', value: '' }; }}>Cancel</button>
            <button class="primary" on:click={saveVaultCred} disabled={vaultSaving || !vaultForm.value}>{vaultSaving ? 'Saving…' : 'Save'}</button>
          </div>
        </div>
        {:else}
        <div class="row"><button class="ghost" on:click={() => vaultAdding = true}>+ Add credential</button></div>
        {/if}
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
  .settings-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0.4rem 0.7rem; }
  /* #415 P0 — Skills tab sections (skills + capabilities + stat sheet) */
  .skills-section { margin-bottom: 1.1rem; }
  .skills-section + .skills-section { border-top: 1px solid var(--border); padding-top: 0.9rem; }
  .section-head { font-size: 0.85rem; font-weight: 600; color: var(--accent); margin: 0 0 0.25rem 0; text-transform: uppercase; letter-spacing: 0.04em; }
  .skill-list, .capability-list { list-style: none; padding: 0; margin: 0.3rem 0 0; display: flex; flex-wrap: wrap; gap: 0.35rem; }
  .skill-list li, .capability-list li { background: var(--bg-elev); border: 1px solid var(--border); border-radius: 4px; padding: 0.18rem 0.5rem; font-size: 0.82rem; }
  .skill-list code, .capability-list code { background: transparent; padding: 0; color: var(--fg); }
  .empty { color: var(--fg-faint); font-size: 0.82rem; font-style: italic; }
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
  .source-stack { display: flex; flex-direction: column; gap: 0.8rem; }
  .source-block { border: 1px solid var(--border); border-radius: 6px; overflow: hidden; }
  .source-head { display: flex; align-items: center; gap: 0.5rem; padding: 0.45rem 0.7rem; background: var(--bg); cursor: default; list-style: none; }
  .source-head::-webkit-details-marker { display: none; }
  details.source-block > summary.source-head { cursor: pointer; }
  .source-label { flex: 1; font-size: 0.78rem; color: var(--fg-muted); font-weight: 600; }
  .source-badge { font-size: 0.68rem; color: var(--fg-faint); background: var(--bg-input); border-radius: 999px; padding: 0.1rem 0.45rem; }
  .source-badge.ro { color: var(--fg-faint); }
  .source-body { background: var(--bg-input); padding: 0.55rem 0.7rem; margin: 0; font-family: ui-monospace, monospace; font-size: 0.74rem; white-space: pre-wrap; word-break: break-word; max-height: 160px; overflow-y: auto; color: var(--fg-muted); }
  .source-block textarea { border: none; border-top: 1px solid var(--border); border-radius: 0; margin: 0; }
  .source-block .row { padding: 0 0.7rem 0.7rem; }
  .add-row select { flex: 1; }
  button.primary { background: var(--accent); color: #000; border: none; border-radius: 6px; padding: 0.45rem 1rem; font-weight: 600; cursor: pointer; }
  button.primary:disabled { opacity: 0.4; cursor: not-allowed; }
  button.ghost { background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.45rem 0.95rem; cursor: pointer; }
  .err { color: var(--danger); margin-top: 0.6rem; }
  .vault-table { width: 100%; border-collapse: collapse; margin-bottom: 0.8rem; }
  .vault-table th { text-align: left; padding: 0.4rem 0.5rem; color: var(--fg-muted); border-bottom: 1px solid var(--border); font-size: 0.78rem; }
  .vault-table td { padding: 0.4rem 0.5rem; border-bottom: 1px solid var(--border); font-size: 0.83rem; }
  .vault-table td.muted { color: var(--fg-muted); font-size: 0.75rem; }
  .vault-table button.danger { background: transparent; color: var(--danger); border: 1px solid var(--danger); border-radius: 4px; padding: 0.2rem 0.5rem; cursor: pointer; font-size: 0.78rem; }
  .vault-provider { font-size: 0.8rem; background: var(--bg-input); border-radius: 4px; padding: 0.1rem 0.4rem; }
  .vault-form { display: flex; flex-direction: column; gap: 0.6rem; padding: 0.8rem; background: var(--bg-input); border: 1px solid var(--border); border-radius: 6px; margin-top: 0.6rem; }
  .vault-form label { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.82rem; color: var(--fg-muted); }
  .vault-form input, .vault-form select { width: 100%; box-sizing: border-box; }
  .muted { color: var(--fg-muted); }
</style>

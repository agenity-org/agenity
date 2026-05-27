<!--
  Stage3Agents — v0.9.1 SpawnWizard Stage 3 (#179 architect 2026-05-28 FINAL+).

  Per-agent model is now Role + N owned Skills (not the v0.9.0 single
  primary_skill + alt_skills):

    agents[i] = {
      label, role_id, owned_skills[], owned_skills_scope{}, account_id,
      agent_type, account_class
    }

  Backward-compat: hydrates legacy v0.9.0 template slots (primary_skill,
  alt_skills) into role_id + owned_skills, so the wizard works against
  /api/v1/team-templates whether the server returns v0.9.0 or v0.9.1
  shape.

  Coverage panel: shows how many of the 10 LEAN skills are owned
  somewhere on the team. 10/10 = ✓ full coverage. <10 = ⚠ partial
  with the uncovered list shown. Launch is always enabled — the panel
  is informational; the operator may intentionally ship a team that
  doesn't own every practice.

  Props:
    template:    the Stage-1 selected Template (with slots[])
    agents:      $bindable — operator's current roster (becomes the
                 launch payload).
    teamName:    $bindable — defaults to template-derived slug
-->
<script>
  let {
    template = null,
    agents = $bindable([]),
    teamName = $bindable(''),
  } = $props();

  let allSkills = $state([]);
  let allRoles = $state([]);
  let vault = $state([]);
  let existingAgents = $state([]);
  let pickerOpenForSlot = $state(-1);   // -1 = closed; ≥0 = swap role on agent i
  let skillPickerForAgent = $state(-1); // -1 = closed; ≥0 = add owned skill to agent i
  let addPickerOpen = $state(false);

  // #200 Bug 7: per-agent AgentType picker (default claude-code).
  // The agent type drives which CLI binary spawns inside the pod.
  // Account-class on the Account dropdown follows the agent type
  // (anthropic for claude-code; openai for codex; etc.).
  const AGENT_TYPES = [
    { id: 'claude-code', label: 'claude-code (default)' },
    { id: 'codex',       label: 'codex' },
    { id: 'aider',       label: 'aider' },
    { id: 'opencode',    label: 'opencode' },
    { id: 'gemini-cli',  label: 'gemini-cli' },
    { id: 'goose',       label: 'goose' },
  ];

  async function loadSkills() {
    try {
      const r = await fetch('/api-v08/v1/skills');
      if (r.ok) allSkills = (await r.json()).skills || [];
    } catch {}
  }
  async function loadRoles() {
    try {
      const r = await fetch('/api-v08/v1/roles');
      if (r.ok) {
        const j = await r.json();
        // /api/v1/roles returns a bare array per roles_v194.go.
        allRoles = Array.isArray(j) ? j : (j.roles || []);
      }
    } catch {}
  }
  async function loadVault() {
    try {
      const r = await fetch('/api-v08/v1/vault');
      if (r.ok) vault = (await r.json()).creds || [];
    } catch {}
  }
  async function loadAgents() {
    try {
      const r = await fetch('/api-v08/v1/agents');
      if (r.ok) existingAgents = (await r.json()).agents || [];
    } catch {}
  }

  $effect(() => { loadSkills(); loadRoles(); loadVault(); loadAgents(); });

  // Hydrate roster from the picked template's slots. Handles BOTH
  // shapes — v0.9.1 (role_id + owned_skills + owned_skills_scope) and
  // legacy v0.9.0 (primary_skill + alt_skills).
  $effect(() => {
    if (!template || agents.length > 0) return;
    if (!template.slots || template.slots.length === 0) {
      return; // Custom — empty roster, operator adds via + Add agent
    }
    if (!teamName) {
      teamName = (template.name || '').toLowerCase().replace(/\s+/g, '-');
    }
    agents = template.slots.map(s => ({
      label: s.label,
      role_id: s.role_id || s.primary_skill || 'generalist',
      owned_skills: s.owned_skills || (s.primary_skill ? [s.primary_skill] : []),
      owned_skills_scope: s.owned_skills_scope || {},
      agent_type: s.agent_type_default || 'claude-code',
      account_id: '',
      account_class: s.account_class_default || 'anthropic',
    }));
  });

  function resumeMatch(label) {
    if (!teamName) return null;
    return existingAgents.find(a => a.label === label) || null;
  }

  function skillByID(id) { return allSkills.find(s => s.id === id) || null; }
  function roleByID(id)  { return allRoles.find(r => r.id === id) || null; }

  function setRole(i, roleID) {
    const role = roleByID(roleID);
    // Setting the role refreshes default owned_skills from the role's
    // DefaultSkills, but ONLY if the agent's current owned_skills is
    // empty — never overwrite operator's manual chip picks.
    const next = { ...agents[i], role_id: roleID };
    if (!next.owned_skills?.length && role?.default_skills?.length) {
      next.owned_skills = [...role.default_skills];
    }
    agents[i] = next;
  }
  function addOwnedSkill(i, skillID) {
    if (agents[i].owned_skills.includes(skillID)) return;
    agents[i] = { ...agents[i], owned_skills: [...agents[i].owned_skills, skillID] };
  }
  function removeOwnedSkill(i, skillID) {
    agents[i] = { ...agents[i], owned_skills: agents[i].owned_skills.filter(s => s !== skillID) };
  }
  function setAccount(i, accID) {
    agents[i] = { ...agents[i], account_id: accID };
  }
  function removeAgent(i) {
    agents = agents.filter((_, idx) => idx !== i);
  }

  function addAgent(roleID) {
    const role = roleByID(roleID);
    const label = (role ? role.id : 'agent') + '-' + (agents.length + 1);
    agents = [...agents, {
      label,
      role_id: roleID,
      owned_skills: role?.default_skills ? [...role.default_skills] : [],
      owned_skills_scope: {},
      agent_type: 'claude-code',
      account_id: '',
      account_class: 'anthropic',
    }];
    addPickerOpen = false;
  }

  function vaultForAgent(a) {
    const cls = a?.account_class || 'anthropic';
    return vault.filter(v => !v.provider || v.provider === cls);
  }

  // Coverage panel — count which of the APPLICABLE LEAN skills are
  // owned by anyone on the team. Per architect's #200 Bug 3 spec:
  // Solo (1 agent) excludes team_only skills (team-orchestration +
  // process-coaching) → 8/8 ✓; Pair+ counts all 10 → up to 10/10.
  // Cyan check at full coverage, amber warn below.
  const coverage = $derived.by(() => {
    const owned = new Set();
    for (const a of agents) {
      for (const sk of a.owned_skills || []) owned.add(sk);
    }
    const teamSize = agents.length;
    const builtins = allSkills.filter(s => s.read_only);
    const applicable = builtins.filter(s => !(s.team_only && teamSize < 2));
    const total = applicable.length || 10;
    const covered = applicable.filter(s => owned.has(s.id));
    const missing = applicable.filter(s => !owned.has(s.id));
    return {
      total,
      coveredCount: covered.length,
      missing,
      ok: covered.length === total,
    };
  });
</script>

<div class="stage3">
  <h2>Who's on the team?</h2>
  <p class="lead">Each agent has ONE role + N owned skills. Re-using a label resumes that agent's prior PVC.</p>

  <label class="field">
    <span>Team name</span>
    <input type="text" bind:value={teamName} placeholder="my-team" />
  </label>

  {#if allSkills.length > 0}
    <section class="coverage" class:warn={!coverage.ok}>
      <header>
        <span class="cov-icon" aria-hidden="true">{coverage.ok ? '✓' : '⚠'}</span>
        <span class="cov-text">
          <strong>{coverage.coveredCount}/{coverage.total}</strong> LEAN skills covered
          {#if !coverage.ok} — your team will not own:{/if}
        </span>
      </header>
      {#if !coverage.ok && coverage.missing.length}
        <ul class="cov-missing">
          {#each coverage.missing as m}
            <li>{m.name}</li>
          {/each}
        </ul>
        <p class="cov-hint">This is OK — Launch stays enabled. Add a chip below if you want full coverage.</p>
      {/if}
    </section>
  {/if}

  {#if (!template || (template.slots && template.slots.length === 0)) && agents.length === 0}
    <div class="empty-state">
      <p>This is a Custom team — build your roster.</p>
      <button class="primary" onclick={() => addPickerOpen = true}>+ Add agent</button>
    </div>
  {/if}

  <ul class="agents">
    {#each agents as a, i}
      {@const role = roleByID(a.role_id)}
      {@const resume = resumeMatch(a.label)}
      <li class="card">
        <header>
          <span class="a-label">{a.label}</span>
          <span class="a-state">
            {#if resume}↻ resumes{:else}★ fresh{/if}
          </span>
          <button type="button" class="x" onclick={() => removeAgent(i)} aria-label="remove">×</button>
        </header>

        <label class="row">
          <span class="row-label">Agent</span>
          <select
            onchange={(e) => {
              const v = e.target.value;
              agents[i] = {
                ...agents[i],
                agent_type: v,
                account_class: (v === 'codex') ? 'openai' : 'anthropic',
                account_id: '',
              };
            }}
          >
            {#each AGENT_TYPES as at}
              <option value={at.id} selected={a.agent_type === at.id}>{at.label}</option>
            {/each}
          </select>
        </label>

        <div class="row">
          <div class="row-label">Role</div>
          <div class="chips">
            <button type="button" class="chip role" onclick={() => pickerOpenForSlot = i}>
              {role ? role.name : (a.role_id || 'pick a role')}
            </button>
          </div>
        </div>

        <div class="row">
          <div class="row-label">Skills</div>
          <div class="chips">
            {#each a.owned_skills as sid}
              {@const s = skillByID(sid)}
              {#if s}
                <span class="chip">
                  {s.name}
                  {#if a.owned_skills_scope?.[sid]}
                    <em>({a.owned_skills_scope[sid]})</em>
                  {/if}
                  <button type="button" class="chip-x" onclick={() => removeOwnedSkill(i, sid)} aria-label="remove skill">×</button>
                </span>
              {/if}
            {/each}
            <button type="button" class="chip add" onclick={() => skillPickerForAgent = i}>+ skill</button>
          </div>
        </div>

        <label class="row">
          <span class="row-label">Account</span>
          <select onchange={(e) => setAccount(i, e.target.value)}>
            <option value="">(default — newest matching {a.account_class})</option>
            {#each vaultForAgent(a) as v}
              <option value={v.id} selected={v.id === a.account_id}>⚓ {v.label || v.id}</option>
            {/each}
          </select>
        </label>

        {#if resume}
          <p class="resume-hint">Will re-attach this agent's PVC ({a.label}) from a prior session.</p>
        {:else}
          <p class="resume-hint">New UUID + new PVC will be provisioned on launch.</p>
        {/if}
      </li>
    {/each}
  </ul>

  {#if template && template.slots && template.slots.length > 0}
    <p class="footer-hint">To change the team shape, go back to Step 1.</p>
  {/if}

  <!-- Role picker -->
  {#if pickerOpenForSlot >= 0}
    <div class="modal-bg" onclick={() => pickerOpenForSlot = -1}>
      <div class="modal" onclick={(e) => e.stopPropagation()}>
        <header>
          <h3>Pick a role</h3>
          <button class="x" onclick={() => pickerOpenForSlot = -1} aria-label="close">×</button>
        </header>
        <ul class="skill-list">
          {#each allRoles as r}
            <li>
              <button
                type="button"
                class="skill-row"
                onclick={() => {
                  setRole(pickerOpenForSlot, r.id);
                  pickerOpenForSlot = -1;
                }}
              >
                <span class="sk-name">{r.name} <em class="cat">{r.category}</em></span>
                <span class="sk-desc">{r.description}</span>
              </button>
            </li>
          {/each}
        </ul>
      </div>
    </div>
  {/if}

  <!-- Owned-skill picker -->
  {#if skillPickerForAgent >= 0}
    <div class="modal-bg" onclick={() => skillPickerForAgent = -1}>
      <div class="modal" onclick={(e) => e.stopPropagation()}>
        <header>
          <h3>Add a skill</h3>
          <button class="x" onclick={() => skillPickerForAgent = -1} aria-label="close">×</button>
        </header>
        <ul class="skill-list">
          {#each allSkills as s}
            <li>
              <button
                type="button"
                class="skill-row"
                onclick={() => {
                  addOwnedSkill(skillPickerForAgent, s.id);
                  skillPickerForAgent = -1;
                }}
              >
                <span class="sk-name">{s.name}</span>
                <span class="sk-desc">{s.description}</span>
              </button>
            </li>
          {/each}
        </ul>
      </div>
    </div>
  {/if}

  <!-- Add-agent picker (Custom path) — picks a role -->
  {#if addPickerOpen}
    <div class="modal-bg" onclick={() => addPickerOpen = false}>
      <div class="modal" onclick={(e) => e.stopPropagation()}>
        <header>
          <h3>Pick a role for the new agent</h3>
          <button class="x" onclick={() => addPickerOpen = false} aria-label="close">×</button>
        </header>
        <ul class="skill-list">
          {#each allRoles as r}
            <li>
              <button type="button" class="skill-row" onclick={() => addAgent(r.id)}>
                <span class="sk-name">{r.name} <em class="cat">{r.category}</em></span>
                <span class="sk-desc">{r.description}</span>
              </button>
            </li>
          {/each}
        </ul>
      </div>
    </div>
  {/if}
</div>

<style>
  .stage3 { padding: 1.25rem; }
  h2 { font-size: 1.15rem; margin: 0 0 0.35rem 0; }
  .lead { color: var(--fg-muted, #888); margin: 0 0 1.2rem 0; font-size: 0.9rem; }
  .field { display: flex; align-items: center; gap: 0.65rem; margin-bottom: 1rem; }
  .field > span { color: var(--fg-muted, #888); font-size: 0.85rem; }
  .field input { flex: 1; padding: 0.4rem 0.55rem; border-radius: 4px; border: 1px solid var(--border, #2a2a2a); background: var(--bg, #0a0a0a); color: var(--fg, #f5f5f5); font: inherit; }

  /* Coverage panel — sits above the agent list */
  .coverage {
    padding: 0.55rem 0.85rem; border-radius: 6px;
    background: rgba(135,206,235,0.06); border: 1px solid rgba(135,206,235,0.18);
    margin: 0 0 0.9rem 0;
  }
  .coverage.warn {
    background: rgba(255, 193, 7, 0.06); border-color: rgba(255, 193, 7, 0.28);
  }
  .coverage header { display: flex; align-items: center; gap: 0.5rem; }
  .cov-icon { font-size: 1rem; color: var(--accent-2, #87ceeb); }
  .coverage.warn .cov-icon { color: #f7b500; }
  .cov-text { font-size: 0.85rem; color: var(--fg, #f5f5f5); }
  .cov-missing { list-style: none; padding: 0; margin: 0.35rem 0 0 0; display: flex; flex-wrap: wrap; gap: 0.32rem; }
  .cov-missing li {
    font-size: 0.74rem; padding: 0.05rem 0.45rem; border-radius: 3px;
    background: rgba(255,193,7,0.12); border: 1px solid rgba(255,193,7,0.3);
    color: #f7b500;
  }
  .cov-hint { margin: 0.35rem 0 0 0; font-size: 0.74rem; color: var(--fg-muted, #aaa); font-style: italic; }

  .empty-state { text-align: center; padding: 1.5rem 1rem; background: var(--bg-elevated, #1a1a1a); border: 1px dashed var(--border, #2a2a2a); border-radius: 8px; margin-bottom: 1rem; }
  .empty-state p { color: var(--fg-muted, #888); margin: 0 0 0.6rem 0; }
  .primary { background: var(--accent-2, #87ceeb); border: 0; color: #0a0a0a; padding: 0.45rem 0.95rem; border-radius: 4px; cursor: pointer; font-weight: 600; }

  /* #200 Bug 4: 3-per-row compact card grid (not full-width rows).
     Squad (8 agents) renders as 3+3+2 in a 3-col layout. */
  .agents {
    list-style: none; padding: 0; margin: 0;
    display: grid; grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 0.55rem;
  }
  .card {
    background: var(--bg-elevated, #1a1a1a); border: 1px solid var(--border, #2a2a2a);
    border-radius: 6px; padding: 0.5rem 0.65rem; margin: 0;
    display: flex; flex-direction: column; gap: 0.3rem;
    font-size: 0.78rem; min-width: 0;
  }
  .card header { display: flex; align-items: center; gap: 0.4rem; margin: 0 0 0.15rem 0; }
  .a-label { font-weight: 600; font-size: 0.82rem; }
  .a-state { font-size: 0.78rem; color: var(--fg-muted, #aaa); margin-left: 0.3rem; }
  .x { margin-left: auto; background: transparent; border: 0; color: var(--fg-muted, #888); cursor: pointer; font-size: 1.05rem; padding: 0 0.3rem; }
  .x:hover { color: var(--danger, #e74c3c); }

  /* #200 Bug 4: compact rows for 3-per-row grid */
  .row { display: flex; align-items: flex-start; gap: 0.4rem; margin: 0; min-width: 0; }
  .row-label { color: var(--fg-muted, #888); font-size: 0.7rem; min-width: 44px; padding-top: 0.2rem; text-transform: uppercase; letter-spacing: 0.03em; }
  .row select { flex: 1; min-width: 0; padding: 0.18rem 0.32rem; border-radius: 3px; border: 1px solid var(--border, #2a2a2a); background: var(--bg, #0a0a0a); color: var(--fg, #f5f5f5); font: inherit; font-size: 0.74rem; }
  .chips { display: flex; flex-wrap: wrap; gap: 0.22rem; flex: 1; min-width: 0; }
  .chip { display: inline-flex; align-items: center; gap: 0.22rem; background: var(--bg, #0a0a0a); border: 1px solid var(--border, #2a2a2a); color: var(--fg, #f5f5f5); padding: 0.12rem 0.4rem; border-radius: 999px; font-size: 0.72rem; cursor: pointer; font: inherit; }
  .chip.role { background: rgba(135,206,235,0.18); border-color: var(--accent-2, #87ceeb); color: var(--accent-2, #87ceeb); font-weight: 600; }
  .chip.add { color: var(--accent-2, #87ceeb); border-style: dashed; }
  .chip em { color: var(--fg-muted, #aaa); font-style: italic; font-size: 0.62rem; }
  .chip-x { background: transparent; border: 0; color: inherit; cursor: pointer; padding: 0; font: inherit; opacity: 0.7; }
  .chip-x:hover { opacity: 1; }
  .resume-hint { color: var(--fg-faint, #666); font-size: 0.68rem; margin: 0.15rem 0 0 0; font-style: italic; }

  .footer-hint { color: var(--fg-muted, #888); font-size: 0.82rem; text-align: center; margin: 0.7rem 0 0 0; font-style: italic; }

  /* Role / skill picker modal */
  .modal-bg { position: fixed; inset: 0; background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 200; }
  .modal { background: #0a0a0a; border: 1px solid #2a2a2a; border-radius: 10px; width: 520px; max-width: 92vw; max-height: 80vh; display: flex; flex-direction: column; }
  .modal header { display: flex; align-items: center; padding: 0.6rem 1rem; border-bottom: 1px solid #2a2a2a; }
  .modal header h3 { flex: 1; margin: 0; font-size: 0.95rem; }
  .skill-list { list-style: none; padding: 0; margin: 0; overflow-y: auto; }
  .skill-row { display: flex; flex-direction: column; align-items: flex-start; width: 100%; text-align: left; background: transparent; border: 0; border-bottom: 1px solid #2a2a2a; padding: 0.55rem 1rem; cursor: pointer; font: inherit; color: var(--fg, #f5f5f5); }
  .skill-row:hover { background: rgba(135,206,235,0.06); }
  .sk-name { font-weight: 600; font-size: 0.92rem; }
  .sk-name .cat { font-size: 0.72rem; color: var(--fg-muted, #888); font-style: italic; font-weight: 400; margin-left: 0.4rem; }
  .sk-desc { color: var(--fg-muted, #888); font-size: 0.8rem; margin-top: 0.18rem; }
</style>

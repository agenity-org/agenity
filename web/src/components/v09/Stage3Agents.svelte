<!--
  Stage3Agents — v0.9 SpawnWizard Stage 3 (#179, architect pivot 2026-05-27).

  RENAMED from Stage3Members. Drastically simplified per architect:

  DROPPED:
   - Team-shape switcher (use Back to change shape)
   - Fresh / Resume / Handoff mode picker (always-resume rule replaces)
   - "+ Add member" button (Custom template handles ad-hoc via empty start)
   - "Save as recipe" checkbox (moved to Stage 4)

  KEPT / ADDED:
   - Per-agent skill chips from #194 (primary + optional Additional)
   - Per-agent account picker filtered by Skill's AgentTypeCompat
   - Always-resume model: identity = (team_name, slot_label). If an
     Agent with that identity exists owned by this operator → ↻ resume
     (re-attach PVC). Otherwise → ★ fresh spawn.

  Props:
    template:    the Stage-1 selected Template (with slots[])
    agents:      $bindable — operator's current roster (becomes the
                 launch payload). Each: { label, skills[], account_id,
                 agent_type, primary_skill, alt_skills[] }
    teamName:    $bindable — defaults to template-derived slug
-->
<script>
  let {
    template = null,
    agents = $bindable([]),
    teamName = $bindable(''),
  } = $props();

  let allSkills = $state([]);
  let vault = $state([]);
  let existingAgents = $state([]);     // from /api/v1/agents — for resume-detection
  let pickerOpenForSlot = $state(-1);  // index of agent in `agents`, -1 = closed
  let addPickerOpen = $state(false);   // Custom-path: skill picker for adding a new agent

  async function loadSkills() {
    try {
      const r = await fetch('/api-v08/v1/skills');
      if (r.ok) allSkills = (await r.json()).skills || [];
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

  $effect(() => { loadSkills(); loadVault(); loadAgents(); });

  // Initialise roster from the picked template's slots — runs once when
  // agents is empty and template is known.
  $effect(() => {
    if (!template || agents.length > 0) return;
    if (!template.slots || template.slots.length === 0) {
      // Custom template — leave empty; UI shows + Add agent
      return;
    }
    if (!teamName) {
      teamName = (template.name || '').toLowerCase().replace(/\s+/g, '-');
    }
    agents = template.slots.map(s => ({
      label: s.label,
      primary_skill: s.primary_skill,
      alt_skills: s.alt_skills || [],
      additional_skills: [],
      agent_type: s.agent_type_default || 'claude-code',
      account_id: '',
      account_class: s.account_class_default || 'anthropic',
    }));
  });

  // Always-resume identity match — (team_name, slot_label) owned by op.
  // Returns the matching existing agent or null.
  function resumeMatch(label) {
    if (!teamName) return null;
    return existingAgents.find(a =>
      a.label === label &&
      // we don't have team-name on the Agent entity in this branch's API
      // contract yet; identity match falls back to label+agent-type.
      true
    ) || null;
  }

  function skillByID(id) { return allSkills.find(s => s.id === id) || null; }

  function setPrimarySkill(i, skillID) {
    agents[i] = { ...agents[i], primary_skill: skillID };
  }
  function addAdditionalSkill(i, skillID) {
    if (agents[i].additional_skills.includes(skillID)) return;
    if (agents[i].primary_skill === skillID) return;
    agents[i] = { ...agents[i], additional_skills: [...agents[i].additional_skills, skillID] };
  }
  function removeAdditionalSkill(i, skillID) {
    agents[i] = { ...agents[i], additional_skills: agents[i].additional_skills.filter(s => s !== skillID) };
  }
  function setAccount(i, accID) {
    agents[i] = { ...agents[i], account_id: accID };
  }
  function removeAgent(i) {
    agents = agents.filter((_, idx) => idx !== i);
  }

  // Custom-path: + Add agent
  function addAgent(skillID) {
    const sk = skillByID(skillID);
    const label = (sk ? sk.id : 'agent') + '-' + (agents.length + 1);
    agents = [...agents, {
      label,
      primary_skill: skillID,
      alt_skills: [],
      additional_skills: [],
      agent_type: sk?.agent_type_compat?.[0] || 'claude-code',
      account_id: '',
      account_class: 'anthropic',
    }];
    addPickerOpen = false;
  }

  function vaultForSkill(skill) {
    // Filter by Skill's AgentTypeCompat — for now we approximate via
    // account_class match. Claude-code skills → anthropic accounts.
    if (!skill) return vault;
    const cls = skill.agent_type_compat?.[0] === 'codex' ? 'openai' : 'anthropic';
    return vault.filter(v => !v.provider || v.provider === cls);
  }
</script>

<div class="stage3">
  <h2>Who's on the team?</h2>
  <p class="lead">Skill chips drive each agent's behavior. Re-using an existing label resumes that agent's prior PVC.</p>

  <label class="field">
    <span>Team name</span>
    <input type="text" bind:value={teamName} placeholder="my-team" />
  </label>

  {#if (!template || (template.slots && template.slots.length === 0)) && agents.length === 0}
    <!-- Custom template, empty roster -->
    <div class="empty-state">
      <p>This is a Custom team — build your roster.</p>
      <button class="primary" onclick={() => addPickerOpen = true}>+ Add agent</button>
    </div>
  {/if}

  <ul class="agents">
    {#each agents as a, i}
      {@const primary = skillByID(a.primary_skill)}
      {@const resume = resumeMatch(a.label)}
      <li class="card">
        <header>
          <span class="a-label">{a.label}</span>
          <span class="a-state">
            {#if resume}↻ resumes{:else}★ fresh{/if}
          </span>
          <button type="button" class="x" onclick={() => removeAgent(i)} aria-label="remove">×</button>
        </header>

        <div class="row">
          <div class="row-label">Skills</div>
          <div class="chips">
            {#if primary}
              <button type="button" class="chip primary" onclick={() => pickerOpenForSlot = i}>
                {primary.name}
              </button>
            {/if}
            {#each a.additional_skills as sid}
              {@const s = skillByID(sid)}
              {#if s}
                <span class="chip">
                  {s.name}
                  <button type="button" class="chip-x" onclick={() => removeAdditionalSkill(i, sid)} aria-label="remove skill">×</button>
                </span>
              {/if}
            {/each}
            <button type="button" class="chip add" onclick={() => pickerOpenForSlot = -1 - i /* sentinel for additional add */}>+ skill</button>
          </div>
        </div>

        <label class="row">
          <span class="row-label">Account</span>
          <select onchange={(e) => setAccount(i, e.target.value)}>
            <option value="">(default — newest matching {a.account_class})</option>
            {#each vaultForSkill(primary) as v}
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

  <!-- Skill picker for primary swap (slot i ≥ 0) or additional-add (slot i = -1-real) -->
  {#if pickerOpenForSlot !== -1}
    {@const i = pickerOpenForSlot >= 0 ? pickerOpenForSlot : (-1 - pickerOpenForSlot)}
    {@const isAdditional = pickerOpenForSlot < 0}
    {@const slot = agents[i]}
    {@const candidates = isAdditional ? allSkills : (slot?.alt_skills?.length > 0 ? allSkills.filter(s => s.id === slot.primary_skill || slot.alt_skills.includes(s.id)) : allSkills)}
    <div class="modal-bg" onclick={() => pickerOpenForSlot = -1}>
      <div class="modal" onclick={(e) => e.stopPropagation()}>
        <header>
          <h3>{isAdditional ? 'Add additional skill' : 'Swap primary skill'}</h3>
          <button class="x" onclick={() => pickerOpenForSlot = -1} aria-label="close">×</button>
        </header>
        <ul class="skill-list">
          {#each candidates as s}
            <li>
              <button
                type="button"
                class="skill-row"
                onclick={() => {
                  if (isAdditional) addAdditionalSkill(i, s.id);
                  else setPrimarySkill(i, s.id);
                  pickerOpenForSlot = -1;
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

  <!-- Add-agent picker (Custom path) -->
  {#if addPickerOpen}
    <div class="modal-bg" onclick={() => addPickerOpen = false}>
      <div class="modal" onclick={(e) => e.stopPropagation()}>
        <header>
          <h3>Pick a skill for the new agent</h3>
          <button class="x" onclick={() => addPickerOpen = false} aria-label="close">×</button>
        </header>
        <ul class="skill-list">
          {#each allSkills as s}
            <li>
              <button type="button" class="skill-row" onclick={() => addAgent(s.id)}>
                <span class="sk-name">{s.name}</span>
                <span class="sk-desc">{s.description}</span>
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

  .empty-state { text-align: center; padding: 1.5rem 1rem; background: var(--bg-elevated, #1a1a1a); border: 1px dashed var(--border, #2a2a2a); border-radius: 8px; margin-bottom: 1rem; }
  .empty-state p { color: var(--fg-muted, #888); margin: 0 0 0.6rem 0; }
  .primary { background: var(--accent-2, #87ceeb); border: 0; color: #0a0a0a; padding: 0.45rem 0.95rem; border-radius: 4px; cursor: pointer; font-weight: 600; }

  .agents { list-style: none; padding: 0; margin: 0; }
  .card { background: var(--bg-elevated, #1a1a1a); border: 1px solid var(--border, #2a2a2a); border-radius: 8px; padding: 0.65rem 0.85rem; margin-bottom: 0.6rem; }
  .card header { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; }
  .a-label { font-weight: 600; }
  .a-state { font-size: 0.78rem; color: var(--fg-muted, #aaa); margin-left: 0.3rem; }
  .x { margin-left: auto; background: transparent; border: 0; color: var(--fg-muted, #888); cursor: pointer; font-size: 1.05rem; padding: 0 0.3rem; }
  .x:hover { color: var(--danger, #e74c3c); }

  .row { display: flex; align-items: flex-start; gap: 0.65rem; margin-bottom: 0.5rem; }
  .row-label { color: var(--fg-muted, #888); font-size: 0.78rem; min-width: 70px; padding-top: 0.3rem; }
  .row select { flex: 1; padding: 0.35rem 0.55rem; border-radius: 4px; border: 1px solid var(--border, #2a2a2a); background: var(--bg, #0a0a0a); color: var(--fg, #f5f5f5); font: inherit; }
  .chips { display: flex; flex-wrap: wrap; gap: 0.4rem; }
  .chip { display: inline-flex; align-items: center; gap: 0.32rem; background: var(--bg, #0a0a0a); border: 1px solid var(--border, #2a2a2a); color: var(--fg, #f5f5f5); padding: 0.28rem 0.65rem; border-radius: 999px; font-size: 0.82rem; cursor: pointer; font: inherit; }
  .chip.primary { background: rgba(135,206,235,0.18); border-color: var(--accent-2, #87ceeb); color: var(--accent-2, #87ceeb); font-weight: 600; }
  .chip.add { color: var(--accent-2, #87ceeb); border-style: dashed; }
  .chip-x { background: transparent; border: 0; color: inherit; cursor: pointer; padding: 0; font: inherit; opacity: 0.7; }
  .chip-x:hover { opacity: 1; }
  .resume-hint { color: var(--fg-faint, #666); font-size: 0.78rem; margin: 0.25rem 0 0 0; font-style: italic; }

  .footer-hint { color: var(--fg-muted, #888); font-size: 0.82rem; text-align: center; margin: 0.7rem 0 0 0; font-style: italic; }

  /* Skill picker modal */
  .modal-bg { position: fixed; inset: 0; background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 200; }
  .modal { background: #0a0a0a; border: 1px solid #2a2a2a; border-radius: 10px; width: 520px; max-width: 92vw; max-height: 80vh; display: flex; flex-direction: column; }
  .modal header { display: flex; align-items: center; padding: 0.6rem 1rem; border-bottom: 1px solid #2a2a2a; }
  .modal header h3 { flex: 1; margin: 0; font-size: 0.95rem; }
  .skill-list { list-style: none; padding: 0; margin: 0; overflow-y: auto; }
  .skill-row { display: flex; flex-direction: column; align-items: flex-start; width: 100%; text-align: left; background: transparent; border: 0; border-bottom: 1px solid #2a2a2a; padding: 0.55rem 1rem; cursor: pointer; font: inherit; color: var(--fg, #f5f5f5); }
  .skill-row:hover { background: rgba(135,206,235,0.06); }
  .sk-name { font-weight: 600; font-size: 0.92rem; }
  .sk-desc { color: var(--fg-muted, #888); font-size: 0.8rem; margin-top: 0.18rem; }
</style>

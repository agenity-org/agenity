<!--
  Stage 3 — Skills. Per-team role × skill matrix override.

  Operator-confirmed 2026-05-29: replaces the old roster-with-chips view.
  Shows the matrix INSIDE the wizard, but edits do NOT mutate the global
  role catalog. Each cell click adds/removes the skill from this team's
  per-spawn override map. At Launch, owned_skills per agent =
  skillOverrides[role_id] ?? role.default_skills.

  Columns: ONLY the distinct role_ids actually present in the team's
  slots (Solo = 1 col, Pair = 2 cols, Squad = 8 cols). Avoids the
  full-catalog confusion.

  Cells: ✓ (in this team's defaults) or · (not). Click to toggle.

  Hydrates initial state from the role's current default_skills the
  first time the matrix loads for each role — that way the operator
  sees a meaningful starting point even before any per-team edits.

  Props:
    template       — Stage 1's pick (carries slots → roles)
    agents         — $bindable; hydrated from template slots if empty
    teamName       — $bindable
    skillOverrides — $bindable; map of roleID → string[] of skill IDs
-->
<script>
  let {
    template = null,
    agents = $bindable([]),
    teamName = $bindable(''),
    skillOverrides = $bindable({}),
  } = $props();

  const API = '/api/v1';

  let allSkills = $state([]);
  let allRoles = $state([]);
  let loading = $state(true);
  let savingCell = $state({});  // key = `${roleID}:${skillID}`

  async function load() {
    loading = true;
    try {
      const [rRes, sRes] = await Promise.all([
        fetch(`${API}/roles`),
        fetch(`${API}/skills`),
      ]);
      const rJ = rRes.ok ? await rRes.json() : [];
      const sJ = sRes.ok ? await sRes.json() : { skills: [] };
      allRoles = Array.isArray(rJ) ? rJ : (rJ.roles || []);
      const ss = Array.isArray(sJ) ? sJ : (sJ.skills || []);
      allSkills = ss.filter(s => s.read_only)
        .sort((a,b) => (a.sort_order ?? 999) - (b.sort_order ?? 999));
    } catch {}
    loading = false;
  }

  // Hydrate roster from template slots (same as before).
  $effect(() => {
    if (!template || agents.length > 0) return;
    if (!template.slots || template.slots.length === 0) return;
    if (!teamName) {
      teamName = (template.name || '').toLowerCase().replace(/\s+/g, '-');
    }
    const _labelCounts = {};
    agents = template.slots.map(s => {
      // Ensure UNIQUE labels: duplicate slots (e.g. two "opencode" workers)
      // must not collide. The label keys per-agent account overrides AND
      // becomes the spawned agent's @-name — colliding labels meant picking
      // an account for one duplicate set it for both (operator-reported
      // 2026-06-19), and would clash at spawn. Suffix repeats: foo, foo-2, foo-3.
      const base = s.label || s.role_id || 'agent';
      _labelCounts[base] = (_labelCounts[base] || 0) + 1;
      const label = _labelCounts[base] === 1 ? base : `${base}-${_labelCounts[base]}`;
      return {
        label,
        role_id: s.role_id || s.primary_skill || 'generalist',
        owned_skills: s.owned_skills || (s.primary_skill ? [s.primary_skill] : []),
        owned_skills_scope: s.owned_skills_scope || {},
        agent_type: s.agent_type_default || 'claude-code',
        account_id: '',
        account_class: s.account_class_default || 'anthropic',
      };
    });
  });

  $effect(() => { load(); });

  // Hydrate skillOverrides with role.default_skills the first time
  // matrix renders for that role (so the operator sees meaningful
  // starting state, not an empty matrix).
  $effect(() => {
    if (allRoles.length === 0 || agents.length === 0) return;
    const rolesInTeam = new Set(agents.map(a => a.role_id).filter(Boolean));
    const updates = {};
    let anyAdded = false;
    for (const roleID of rolesInTeam) {
      if (skillOverrides[roleID] !== undefined) continue;
      const role = allRoles.find(r => r.id === roleID);
      if (role) {
        updates[roleID] = [...(role.default_skills || [])];
        anyAdded = true;
      }
    }
    if (anyAdded) skillOverrides = { ...skillOverrides, ...updates };
  });

  function roleByID(id) { return allRoles.find(r => r.id === id) || null; }

  // Columns: distinct roleIDs actually in the team (preserves slot order).
  const teamRoles = $derived.by(() => {
    const seen = new Set();
    const out = [];
    for (const a of agents) {
      if (a.role_id && !seen.has(a.role_id)) {
        seen.add(a.role_id);
        const r = roleByID(a.role_id);
        if (r) out.push({ id: a.role_id, role: r, count: agents.filter(x => x.role_id === a.role_id).length });
      }
    }
    return out;
  });

  function isOn(roleID, skillID) {
    return Array.isArray(skillOverrides[roleID]) && skillOverrides[roleID].includes(skillID);
  }

  function toggle(roleID, skillID) {
    const cur = skillOverrides[roleID] || [];
    const next = cur.includes(skillID)
      ? cur.filter(s => s !== skillID)
      : [...cur, skillID];
    skillOverrides = { ...skillOverrides, [roleID]: next };
  }
</script>

<div class="stage3">
  <header class="head">
    <div class="title">
      <h2>Who's bringing what?</h2>
      <p class="lead">Click a cell to flip a skill for this team. Edits don't touch global role defaults — tune those via the <strong>🎮 roles</strong> dashboard pane.</p>
    </div>
    <label class="field">
      <span>Team name</span>
      <input type="text" bind:value={teamName} placeholder="my-team" />
    </label>
  </header>

  {#if loading || allSkills.length === 0 || teamRoles.length === 0}
    <p class="hint">Loading matrix…</p>
  {:else}
    <div class="grid-wrap">
      <table class="skills-grid">
        <thead>
          <tr>
            <th class="skill-th">skill</th>
            {#each teamRoles as col (col.id)}
              <th class="role-th" title={`${col.role.name}${col.count > 1 ? ` × ${col.count}` : ''}`}>
                {col.role.name}{#if col.count > 1}<span class="cnt">×{col.count}</span>{/if}
              </th>
            {/each}
          </tr>
        </thead>
        <tbody>
          {#each allSkills as sk (sk.id)}
            <tr>
              <th class="skill-th" title={sk.description}>{sk.name}</th>
              {#each teamRoles as col (col.id)}
                {@const on = isOn(col.id, sk.id)}
                <td
                  class="cell"
                  class:on
                  onclick={() => toggle(col.id, sk.id)}
                  title={`${col.role.name} → ${sk.name} (${on ? 'in this team' : 'click to add'})`}
                >{on ? '✓' : '·'}</td>
              {/each}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .stage3 { padding: 1.1rem 1.25rem; }
  /* Two-column header: title text on the left, compact "Team name" input on the right. */
  .head { display: flex; align-items: flex-start; gap: 1.2rem; margin-bottom: 0.85rem; }
  .head .title { flex: 1; min-width: 0; }
  h2 { font-size: 1.15rem; margin: 0 0 0.35rem 0; }
  .lead { color: var(--fg-muted, #888); margin: 0; font-size: 0.84rem; line-height: 1.5; }
  .lead strong { color: var(--accent-2, #87ceeb); }
  .field {
    flex: 0 0 14rem; display: flex; flex-direction: column; gap: 0.25rem;
    font-size: 0.78rem; color: var(--fg-muted, #888);
  }
  .field input { padding: 0.4rem 0.55rem; border-radius: 4px; border: 1px solid var(--border, #2a2a2a); background: var(--bg, #0a0a0a); color: var(--fg, #f5f5f5); font: inherit; }
  .grid-wrap { overflow-x: auto; padding: 0.2rem 0; }
  /* #226 — was `.grid`; collided with the global `.grid { display: grid;
     grid-template-columns: repeat(3,1fr) }` rule in web/src/styles/global.css.
     The unscoped global made our <table> a 3-column grid; thead + tbody
     landed in separate columns side-by-side (header on left, body on
     right). Renamed to .skills-grid to keep this <table>'s default
     `display: table` semantics. Other components also use the unscoped
     `.grid` name — defensive sweep tracked as #228. */
  /* #236 — `width: 100%` + `table-layout: fixed` so the role columns
     share the available width equally. Without `table-layout: fixed`
     browsers use the auto algorithm which sizes columns to content +
     leaves whitespace on the right. The skill-th column gets an
     explicit `width: 14rem`; the role-th + .cell columns use auto so
     `table-layout: fixed` distributes `100% - 14rem` equally among
     them. .grid-wrap's `overflow-x: auto` still kicks in when the
     accumulated min-widths exceed the container (mobile / 5+ roles). */
  .skills-grid { border-collapse: separate; border-spacing: 0; font-size: 0.84rem; width: 100%; table-layout: fixed; }
  .skills-grid thead th { color: var(--fg-muted, #aaa); font-weight: 600; padding: 0.4rem 0.55rem; text-align: center; border-bottom: 1px solid var(--border, #2a2a2a); }
  /* #251 — `white-space: nowrap` alone lets long skill names visually
     leak across the column boundary even though the cell's measured
     width stays at 14rem. Adding `overflow: hidden` clips the bleed
     + `text-overflow: ellipsis` gives the operator a readable
     truncation marker. The full skill name is still accessible via
     the existing title={sk.description} tooltip (line 156). The
     thead's "skill" header cell shares the same class but is short
     enough to never trip the ellipsis. */
  .skills-grid .skill-th { text-align: left; color: var(--fg, #f5f5f5); padding-right: 0.7rem; font-weight: 500; width: 14rem; max-width: 14rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .skills-grid .role-th { color: var(--accent-2, #87ceeb); min-width: 5rem; }
  .skills-grid .cnt { color: var(--fg-muted, #888); margin-left: 0.18rem; font-size: 0.72rem; }
  .cell {
    text-align: center; cursor: pointer; user-select: none;
    padding: 0.32rem 0.55rem; min-width: 5rem;
    color: var(--fg-muted, #777); font-weight: 600;
    border-bottom: 1px solid rgba(255,255,255,0.03);
    transition: background 80ms, color 80ms;
  }
  .cell:hover { background: rgba(135, 206, 235, 0.08); color: var(--fg, #f5f5f5); }
  .cell.on { color: var(--accent-2, #87ceeb); background: rgba(135,206,235,0.10); }
  .cell.on:hover { background: rgba(135, 206, 235, 0.18); }
  .skills-grid tbody tr:hover { background: rgba(255,255,255,0.02); }
  .hint { color: var(--fg-muted, #888); font-size: 0.85rem; }
</style>

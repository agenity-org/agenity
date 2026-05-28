<!--
  WidgetRoleMatrix — 🎮 roles widget.

  10×12 grid (rows = LEAN skills, cols = role catalog).
  Each cell is a clickable toggle showing whether that skill is in
  the role's default_skills list. Saves on every click via
  PUT /api/v1/roles/{id} with {default_skills: [...]} — backend's
  route detects default_skills-only patches and routes them to
  SetDefaultSkills (which is allowed on builtin roles; preserves
  the role's identity ReadOnly).

  Result: every spawn of role X gets exactly the chips operator
  pinned to that role in this matrix.

  Filed in response to operator's 2026-05-28 Stage-3-complexity
  pushback. Replaces per-spawn skill chip picking; Stage 3 now
  just shows roster.
-->
<script>
  const API = '/api-v08/v1';

  let roles = $state([]);
  let skills = $state([]);
  let loading = $state(true);
  let error = $state('');
  // Per-cell saving state: key = `${roleID}:${skillID}`.
  let saving = $state({});

  async function load() {
    loading = true; error = '';
    try {
      const [rRes, sRes] = await Promise.all([
        fetch(`${API}/roles`),
        fetch(`${API}/skills`),
      ]);
      if (!rRes.ok) throw new Error('roles: HTTP ' + rRes.status);
      if (!sRes.ok) throw new Error('skills: HTTP ' + sRes.status);
      const rJ = await rRes.json();
      const sJ = await sRes.json();
      const rs = Array.isArray(rJ) ? rJ : (Array.isArray(rJ.roles) ? rJ.roles : []);
      const ss = Array.isArray(sJ) ? sJ : (Array.isArray(sJ.skills) ? sJ.skills : []);
      // Show builtins first, by sort_order. Skills: same.
      roles = [...rs].sort((a,b) => (a.sort_order ?? 999) - (b.sort_order ?? 999));
      skills = [...ss].filter(s => s.read_only)
        .sort((a,b) => (a.sort_order ?? 999) - (b.sort_order ?? 999));
    } catch (e) {
      error = String(e?.message || e);
    } finally {
      loading = false;
    }
  }

  function isOn(role, skillID) {
    return Array.isArray(role.default_skills) && role.default_skills.includes(skillID);
  }

  async function toggleCell(role, skillID) {
    const key = `${role.id}:${skillID}`;
    if (saving[key]) return;
    saving = { ...saving, [key]: true };
    const next = isOn(role, skillID)
      ? (role.default_skills || []).filter(s => s !== skillID)
      : [...(role.default_skills || []), skillID];
    try {
      const r = await fetch(`${API}/roles/${encodeURIComponent(role.id)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ default_skills: next }),
      });
      if (!r.ok) {
        const t = await r.text();
        error = `save ${role.id}: ${t.trim() || 'HTTP ' + r.status}`;
        return;
      }
      // Local update so the cell flips instantly without re-fetch.
      const i = roles.findIndex(x => x.id === role.id);
      if (i >= 0) {
        roles[i] = { ...roles[i], default_skills: next };
        roles = [...roles];
      }
    } catch (e) {
      error = `save ${role.id}: ${e?.message || e}`;
    } finally {
      const { [key]: _, ...rest } = saving;
      saving = rest;
    }
  }

  // Compact role label — short name fits the column header.
  function roleAbbrev(r) {
    const map = {
      'product-owner':       'PO',
      'architect':           'Ar',
      'tech-lead':           'TL',
      'scrum-master':        'SM',
      'generalist':          'Gen',
      'full-stack-developer':'FS',
      'frontend-developer':  'FE',
      'backend-developer':   'BE',
      'devops-sre':          'Ops',
      'qa-engineer':         'QA',
      'security-engineer':   'Sec',
      'code-reviewer':       'CR',
    };
    return map[r.id] || r.name?.slice(0,3) || '?';
  }

  // Category dividers for header — categorical grouping in the
  // wireframe (Leader / Method / Engineering / Quality).
  const CATEGORY_ORDER = ['leadership', 'methodology', 'engineering', 'operations', 'quality'];
  function groupedRoles() {
    const buckets = {};
    for (const r of roles) {
      const c = r.category || 'other';
      if (!buckets[c]) buckets[c] = [];
      buckets[c].push(r);
    }
    return CATEGORY_ORDER.filter(c => buckets[c]?.length > 0).map(c => ({ category: c, roles: buckets[c] }));
  }

  $effect(() => { load(); });
</script>

<div class="matrix">
  <header class="row top">
    <h4>roles × skills</h4>
    <span class="legend">● in defaults · click to toggle</span>
    <button type="button" class="link" onclick={load} title="reload">↻</button>
  </header>

  {#if loading}
    <p class="hint">Loading…</p>
  {:else if error}
    <p class="err">⚠ {error}</p>
    <button class="link" onclick={load}>Try again</button>
  {:else if roles.length === 0 || skills.length === 0}
    <p class="hint">No roles or skills loaded.</p>
  {:else}
    {@const groups = groupedRoles()}
    <div class="grid-wrap">
      <table class="grid">
        <thead>
          <tr>
            <th class="skill-th"></th>
            {#each groups as g, gi}
              <th class="cat-h" colspan={g.roles.length}>{g.category}</th>
              {#if gi < groups.length - 1}<th class="divider-th"></th>{/if}
            {/each}
          </tr>
          <tr>
            <th class="skill-th">skill</th>
            {#each groups as g, gi}
              {#each g.roles as r}
                <th class="role-th" title={r.name}>{roleAbbrev(r)}</th>
              {/each}
              {#if gi < groups.length - 1}<th class="divider-th"></th>{/if}
            {/each}
          </tr>
        </thead>
        <tbody>
          {#each skills as sk}
            <tr>
              <th class="skill-th" title={sk.description}>{sk.name}</th>
              {#each groups as g, gi}
                {#each g.roles as r}
                  {@const on = isOn(r, sk.id)}
                  {@const key = `${r.id}:${sk.id}`}
                  <td
                    class="cell"
                    class:on={on}
                    class:saving={saving[key]}
                    onclick={() => toggleCell(r, sk.id)}
                    title="{r.name} → {sk.name} ({on ? 'in defaults' : 'click to add'})"
                  >{on ? '●' : '·'}</td>
                {/each}
                {#if gi < groups.length - 1}<td class="divider-td">│</td>{/if}
              {/each}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="note">
      Changes save immediately to <code>PUT /api/v1/roles/&#x7B;id&#x7D;</code>.
      Every future spawn of that role uses the new chips.
      Pair-conditional clause on Code Reviewer is in PrimaryPrompt — it overlays at spawn time when team size = 2.
    </p>
  {/if}
</div>

<style>
  .matrix { padding: 0.7rem 0.85rem; height: 100%; overflow-y: auto; overflow-x: auto; background: var(--bg); }
  .row.top { display: flex; align-items: center; gap: 0.5rem; margin: 0 0 0.55rem 0; flex-wrap: wrap; }
  h4 { margin: 0; font-size: 0.74rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; }
  .legend { color: var(--fg-muted); font-size: 0.74rem; flex: 1; }
  .link { background: transparent; border: 0; color: var(--accent-2, #87ceeb); cursor: pointer; font: inherit; font-size: 0.86rem; padding: 0; }
  .link:hover { text-decoration: underline; }
  .grid-wrap { overflow-x: auto; }
  .grid { border-collapse: separate; border-spacing: 0; font-size: 0.78rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .grid thead th { color: var(--fg-muted); font-weight: 600; padding: 0.25rem 0.4rem; text-align: center; }
  .grid .cat-h { color: var(--fg, #f5f5f5); font-size: 0.7rem; text-transform: lowercase; letter-spacing: 0.04em; border-bottom: 1px solid var(--border, #2a2a2a); }
  .grid .role-th { color: var(--accent-2, #87ceeb); min-width: 2.2rem; }
  .grid .skill-th { text-align: left; color: var(--fg, #f5f5f5); padding-right: 0.6rem; font-weight: 500; max-width: 12rem; white-space: nowrap; }
  .grid .divider-th, .grid .divider-td { width: 0.45rem; color: var(--border, #2a2a2a); padding: 0; text-align: center; }
  .cell {
    text-align: center; cursor: pointer; user-select: none;
    padding: 0.25rem 0.4rem; min-width: 2.2rem;
    color: var(--fg-muted, #888); font-weight: 600;
    transition: background 80ms, color 80ms;
  }
  .cell:hover { background: rgba(135, 206, 235, 0.08); color: var(--fg, #f5f5f5); }
  .cell.on { color: var(--accent-2, #87ceeb); background: rgba(135,206,235,0.10); }
  .cell.on:hover { background: rgba(135, 206, 235, 0.18); }
  .cell.saving { opacity: 0.5; cursor: progress; }
  .grid tbody tr:hover { background: rgba(255,255,255,0.02); }
  .note { color: var(--fg-muted, #888); font-size: 0.74rem; margin-top: 0.7rem; line-height: 1.5; }
  .note code { color: var(--accent-2, #87ceeb); font-size: 0.74rem; }
  .hint { color: var(--fg-muted, #888); font-size: 0.84rem; padding: 0.4rem 0; }
  .err { color: var(--danger, #e74c3c); font-size: 0.84rem; margin: 0.4rem 0; }
</style>

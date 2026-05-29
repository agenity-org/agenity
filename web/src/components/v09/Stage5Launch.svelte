<!--
  Stage5Launch — v0.9.1 final spawn step.

  Consumes the 5-stage selection shape from SpawnWizardV9:
    {
      template, repo, members[], teamName,
      skillOverrides:           { roleID → string[] },
      typeAccounts:             { agentType → vaultEntryID },
      agentAccountOverrides:    { agentLabel → vaultEntryID },
    }

  Per-agent resolution at Launch time:
    accountID(a)  = agentAccountOverrides[a.label]
                    || typeAccounts[a.agent_type || 'claude-code']
                    || ''
    ownedSkills(a)= skillOverrides[a.role_id]
                    || a.owned_skills
                    || [a.primary_skill]

  Operator-locked policy 2026-05-29:
    Launch is BLOCKED if any agent has no resolvable account_id.
    No auto-pick fallback; the wizard's `canAdvance` at Stage 4
    already enforces this — this stage just guards the POST too.
-->
<script>
  import PreflightChecks from './PreflightChecks.svelte';

  let { selection, saveAsRecipe = $bindable(false), onlaunch } = $props();

  let preflight = $state({ ready: false, anyFail: false });
  let launching = $state(false);
  let launchError = $state('');
  let skillCache = $state({});
  let roleCache = $state({});

  async function loadSkillNames() {
    try {
      const r = await fetch('/api-v08/v1/skills');
      if (!r.ok) return;
      const j = await r.json();
      const m = {};
      for (const s of (j.skills || [])) m[s.id] = s;
      skillCache = m;
    } catch {}
  }
  async function loadRoleNames() {
    try {
      const r = await fetch('/api-v08/v1/roles');
      if (!r.ok) return;
      const j = await r.json();
      const m = {};
      const list = Array.isArray(j) ? j : (j.roles || []);
      for (const role of list) m[role.id] = role;
      roleCache = m;
    } catch {}
  }
  $effect(() => { loadSkillNames(); loadRoleNames(); });

  function skillName(id) { return id ? (skillCache[id]?.name || id) : '—'; }
  function roleName(id)  { return id ? (roleCache[id]?.name || id) : '—'; }

  function memberRole(m) { return m.role_id || m.primary_skill || ''; }

  // Skills: Stage 3 per-team override wins; otherwise use the
  // member's owned_skills (Stage 1 template default).
  function ownedSkillsFor(m) {
    const rID = memberRole(m);
    const override = selection?.skillOverrides?.[rID];
    if (Array.isArray(override) && override.length >= 0) return override;
    if (m.owned_skills && m.owned_skills.length) return m.owned_skills;
    if (m.additional_skills && m.additional_skills.length) return m.additional_skills;
    if (m.primary_skill) return [m.primary_skill];
    return [];
  }
  function memberScope(m, skillID) { return m.owned_skills_scope?.[skillID] || ''; }

  // Account resolution: per-agent override > per-type default.
  function accountFor(m) {
    const ov = selection?.agentAccountOverrides?.[m.label];
    if (ov) return ov;
    const t = m.agent_type || 'claude-code';
    return selection?.typeAccounts?.[t] || '';
  }

  // Account chips roll-up — group members by resolved accountID.
  const reused = $derived.by(() => {
    const counts = new Map();
    for (const m of (selection?.members || [])) {
      const k = accountFor(m) || `(unset-${m.agent_type || 'claude-code'})`;
      counts.set(k, (counts.get(k) || 0) + 1);
    }
    return [...counts.entries()].map(([k, n]) => ({ account: k, count: n }));
  });

  // Coverage informational — same logic as before but with the
  // Stage-3 override applied.
  const coverage = $derived.by(() => {
    const owned = new Set();
    for (const m of (selection?.members || [])) {
      for (const sk of ownedSkillsFor(m)) owned.add(sk);
    }
    const teamSize = (selection?.members || []).length;
    const builtins = Object.values(skillCache).filter(s => s.read_only);
    const applicable = builtins.filter(s => !(s.team_only && teamSize < 2));
    const total = applicable.length || 10;
    const covered = applicable.filter(s => owned.has(s.id));
    const missing = applicable.filter(s => !owned.has(s.id));
    return {
      total,
      coveredCount: covered.length,
      missing,
      ok: covered.length === total && total > 0,
    };
  });

  // Gate Launch on every agent having a resolvable account_id —
  // belt-and-braces alongside Stage 4 canAdvance.
  const allAccountsResolved = $derived.by(() =>
    (selection?.members || []).every(m => !!accountFor(m))
  );

  async function launch() {
    if (!allAccountsResolved) {
      launchError = 'Every agent needs an account selected. Go back to Accounts.';
      return;
    }
    launching = true;
    launchError = '';
    try {
      const members = selection?.members || [];
      const skResp = await fetch('/api-v08/v1/skills');
      const skJson = skResp.ok ? await skResp.json() : { skills: [] };
      const skByID = {};
      for (const s of (skJson.skills || [])) skByID[s.id] = s;

      for (const m of members) {
        const ownedSkills = ownedSkillsFor(m);
        const firstSkill = ownedSkills.length ? skByID[ownedSkills[0]] || {} : {};
        const accountID = accountFor(m);
        const body = {
          name: m.label,
          agent: m.agent_type || 'claude-code',
          team: selection?.teamName,
          role: 'worker',
          cwd: '/home/chepherd/repos',
          role_id: memberRole(m),
          owned_skills: ownedSkills,
          owned_skills_scope: m.owned_skills_scope || {},
          account_id: accountID,
          claude_token_id: accountID,
          system_prompt: firstSkill.prompt_override || firstSkill.org_override_body || '',
          stat_sheet: firstSkill.stat_sheet || undefined,
        };
        const r = await fetch('/api-v08/v1/sessions', {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (!r.ok) {
          const t = await r.text();
          throw new Error('spawn ' + m.label + ': ' + (t.trim() || 'HTTP ' + r.status));
        }
      }
      onlaunch?.();
    } catch (e) {
      launchError = String(e.message || e);
    } finally {
      launching = false;
    }
  }
</script>

<div class="stage5">
  <h2>Ready to spawn</h2>

  <dl class="summary">
    <dt>Shape</dt><dd>{selection?.template?.name || '—'}</dd>
    <dt>Repo</dt><dd>{selection?.repo?.full_name || '—'} <span class="kind">({selection?.repo?.kind || '—'})</span></dd>
    <dt>Team</dt><dd>{selection?.teamName || '—'}</dd>
    <dt>Agents</dt><dd>{(selection?.members || []).length}</dd>
  </dl>

  <ul class="members">
    {#each selection?.members || [] as m}
      <li>
        <span class="m-label">{m.label}</span>
        <span class="m-role">{roleName(memberRole(m))}</span>
        {#each ownedSkillsFor(m) as sid}
          <span class="m-skill">
            {skillName(sid)}{#if memberScope(m, sid)} <em>({memberScope(m, sid)})</em>{/if}
          </span>
        {/each}
        {#if accountFor(m)}
          <span class="m-account">⚓ {accountFor(m)}</span>
        {:else}
          <span class="m-account miss">⚠ no account</span>
        {/if}
      </li>
    {/each}
  </ul>

  {#if reused.length > 0}
    <div class="reused">
      <span class="reused-lbl">Accounts:</span>
      {#each reused as r}
        <span class="reused-chip">⚓ {r.account} (×{r.count})</span>
      {/each}
    </div>
  {/if}

  {#if Object.keys(skillCache).length > 0 && !coverage.ok}
    <div class="warn">
      <span class="warn-icon">⚠</span>
      <span class="warn-text">
        <strong>{coverage.coveredCount}/{coverage.total}</strong> LEAN skills covered.
        Missing: {coverage.missing.map(m => m.name).join(', ')}.
        <em>Informational — Launch is still enabled.</em>
      </span>
    </div>
  {/if}

  <PreflightChecks selection={selection} onstate={(s) => preflight = s} />

  <label class="recipe">
    <input type="checkbox" bind:checked={saveAsRecipe} />
    Save this team as a recipe
  </label>

  {#if launchError}
    <p class="err">⚠ {launchError}</p>
  {/if}

  <button
    type="button"
    class="launch"
    disabled={preflight.anyFail || launching || !allAccountsResolved}
    onclick={launch}
    title={!allAccountsResolved ? 'Every agent needs an account selected — back to Accounts' : ''}
  >
    {launching ? 'Launching…' : '⚡ Launch'}
  </button>
</div>

<style>
  .stage5 { padding: 1.25rem; }
  h2 { font-size: 1.15rem; margin: 0 0 1rem 0; }

  .summary { display: grid; grid-template-columns: 100px 1fr; gap: 0.4rem 0.85rem; margin: 0 0 0.85rem 0; font-size: 0.9rem; }
  .summary dt { color: var(--fg-muted, #888); font-weight: 500; }
  .summary dd { margin: 0; color: var(--fg, #f5f5f5); }
  .kind { color: var(--fg-muted, #888); font-size: 0.82rem; }

  .members { list-style: none; padding: 0; margin: 0 0 0.85rem 0; }
  .members li { display: flex; align-items: center; flex-wrap: wrap; gap: 0.4rem; padding: 0.18rem 0; font-size: 0.85rem; }
  .m-label { font-weight: 600; min-width: 80px; }
  .m-role { font-size: 0.78rem; padding: 0.04rem 0.5rem; border-radius: 999px; background: rgba(135, 206, 235, 0.18); color: #87ceeb; font-weight: 600; }
  .m-skill { font-size: 0.72rem; padding: 0.04rem 0.4rem; border-radius: 999px; background: var(--bg, #0a0a0a); border: 1px solid var(--border, #2a2a2a); color: var(--fg-muted, #aaa); }
  .m-skill em { color: var(--fg-faint, #777); font-style: italic; font-size: 0.68rem; }
  .m-account { color: var(--fg-muted, #888); font-size: 0.82rem; margin-left: auto; }
  .m-account.miss { color: #e74c3c; }

  .reused { background: var(--bg, #0a0a0a); border: 1px solid var(--border, #2a2a2a); border-radius: 4px; padding: 0.4rem 0.65rem; margin-bottom: 0.85rem; font-size: 0.82rem; display: flex; flex-wrap: wrap; gap: 0.4rem; align-items: center; }
  .reused-lbl { color: var(--fg-muted, #888); }
  .reused-chip { color: var(--accent-2, #87ceeb); }

  .warn {
    background: rgba(255, 193, 7, 0.08); border: 1px solid rgba(255, 193, 7, 0.3);
    border-radius: 5px; padding: 0.4rem 0.7rem; margin: 0 0 0.85rem 0;
    display: flex; gap: 0.5rem; align-items: flex-start; font-size: 0.82rem;
  }
  .warn-icon { color: #f7b500; flex-shrink: 0; }
  .warn-text { color: var(--fg, #f5f5f5); line-height: 1.4; }
  .warn-text em { color: var(--fg-muted, #aaa); font-style: italic; font-size: 0.78rem; }

  .recipe { display: inline-flex; align-items: center; gap: 0.4rem; margin: 0.85rem 0; color: var(--fg-muted, #aaa); font-size: 0.88rem; }
  .launch {
    display: block; width: 100%;
    background: var(--accent-2, #87ceeb); color: #0a0a0a;
    border: 0; border-radius: 6px;
    padding: 0.65rem; font-size: 1rem; font-weight: 700;
    cursor: pointer; margin-top: 0.55rem;
  }
  .launch:disabled { opacity: 0.45; cursor: not-allowed; }
  .err { color: var(--danger, #e74c3c); font-size: 0.85rem; margin: 0.5rem 0; }
</style>

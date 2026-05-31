<!--
  Stage5Launch — v0.9.2 final spawn step (REDESIGNED #252).

  Consumes the 5-stage selection shape from SpawnWizardV9 (unchanged):
    {
      template, repo, members[], teamName,
      skillOverrides:           { roleID → string[] },
      agentTypeOverrides:       { agentLabel → agent type slug },
      agentModelOverrides:      { agentLabel → model name },
      typeAccounts:             { agentType → vaultEntryID },
      agentAccountOverrides:    { agentLabel → vaultEntryID },
    }

  Per-agent resolution at Launch time (unchanged from v0.9.1):
    accountID(a)  = agentAccountOverrides[a.label]
                    || typeAccounts[a.agent_type || 'claude-code']
                    || ''
    ownedSkills(a)= skillOverrides[a.role_id]
                    || a.owned_skills
                    || [a.primary_skill]

  Operator-locked policy 2026-05-29 (unchanged):
    Launch is BLOCKED if any agent has no resolvable account_id.

  Architect-greenlit redesign 2026-05-30 (#252):
    1. Primary CTA lives in the OUTER wizard footer via `controller`
       $bindable — parent reads `controller.canLaunch` / `controller.label`
       / `controller.launching` and calls `controller.launch()`.
    2. Plain `<table>` summary (Label · Role · Type · Model · Account)
       replaces the per-row chip-wrap. Role cell carries a tooltip
       listing the agent's owned skills.
    3. Account roll-up in the team header — shown ONCE when all agents
       share an account; per-row column otherwise.
    4. Pre-flight collapsed to a one-liner with `<details>` expander.
    5. Per-agent inline progress during launch: queued → spawning →
       ready / failed. Sequential awaits (per-v0.9.2 architect call).
    6. Partial-failure: failed rows show inline error + per-row Retry
       button; heading-level "Retry N failed agents" too.
-->
<script>
  import PreflightChecks from './PreflightChecks.svelte';

  let {
    selection,
    saveAsRecipe = $bindable(false),
    onlaunch,
    controller = $bindable({
      label: 'Launch',
      canLaunch: false,
      launching: false,
      launch: () => {},
    }),
  } = $props();

  let preflight = $state({ ready: false, anyFail: false });
  let launchError = $state('');
  let skillCache = $state({});
  let roleCache = $state({});

  // Per-agent result tracking. Map of agentLabel → {status, durationMs, error}.
  // status ∈ 'queued' | 'spawning' | 'ready' | 'failed'
  // null = pre-launch (not yet attempted)
  let launchResults = $state(null);
  let launchingNow = $state(false);

  async function loadSkillNames() {
    try {
      const r = await fetch('/api/v1/skills');
      if (!r.ok) return;
      const j = await r.json();
      const m = {};
      for (const s of (j.skills || [])) m[s.id] = s;
      skillCache = m;
    } catch {}
  }
  async function loadRoleNames() {
    try {
      const r = await fetch('/api/v1/roles');
      if (!r.ok) return;
      const j = await r.json();
      const m = {};
      const list = Array.isArray(j) ? j : (j.roles || []);
      for (const role of list) m[role.id] = role;
      roleCache = m;
    } catch {}
  }
  // #274 — pre-Launch name-collision check. Cross-reference each
  // proposed agent label against /api/v1/sessions to surface
  // conflicts INLINE before the operator hits Launch, instead of
  // watching M/N agents fail with `runtime.Spawn: name "X" already
  // in use`. Walker hit this on a Squad-while-scrum scenario: 4/8
  // agents share names with the live scrum team → 4 spawn errors
  // with no upfront warning. Auto-suffixing (`scrum-master-2`) is
  // proposed as the operator-friendly default; manual rename per row
  // is the escape hatch.
  let liveAgentNames = $state(new Set());
  async function loadLiveAgentNames() {
    try {
      const r = await fetch('/api/v1/sessions');
      if (!r.ok) return;
      const j = await r.json();
      const list = Array.isArray(j?.sessions) ? j.sessions
                 : Array.isArray(j) ? j : [];
      liveAgentNames = new Set(list.map(s => s.name).filter(Boolean));
    } catch {}
  }
  $effect(() => { loadSkillNames(); loadRoleNames(); loadLiveAgentNames(); });

  // Map of proposed-label → effective-label (auto-suffix on collision).
  // Operator can override by editing the label inline; for v0.9.2 we
  // auto-suffix with a numeric counter and surface the rename in the
  // table.
  let labelOverrides = $state({});
  function effectiveLabel(m) {
    if (labelOverrides[m.label]) return labelOverrides[m.label];
    if (!liveAgentNames.has(m.label)) return m.label;
    // Find the lowest -N suffix that doesn't collide.
    let i = 2;
    while (liveAgentNames.has(m.label + '-' + i)) i++;
    return m.label + '-' + i;
  }
  // List of (originalLabel, effectiveLabel) for agents whose name was
  // auto-suffixed — drives the inline warning banner.
  const collisions = $derived.by(() => {
    const out = [];
    for (const m of (selection?.members || [])) {
      const eff = effectiveLabel(m);
      if (eff !== m.label) out.push({ original: m.label, effective: eff });
    }
    return out;
  });

  function skillName(id) { return id ? (skillCache[id]?.name || id) : '—'; }
  function roleName(id)  { return id ? (roleCache[id]?.name || id) : '—'; }
  function memberRole(m) { return m.role_id || m.primary_skill || ''; }

  function ownedSkillsFor(m) {
    const rID = memberRole(m);
    const override = selection?.skillOverrides?.[rID];
    if (Array.isArray(override) && override.length >= 0) return override;
    if (m.owned_skills && m.owned_skills.length) return m.owned_skills;
    if (m.additional_skills && m.additional_skills.length) return m.additional_skills;
    if (m.primary_skill) return [m.primary_skill];
    return [];
  }

  function resolvedAgentType(m) {
    return selection?.agentTypeOverrides?.[m.label] || m.agent_type || 'claude-code';
  }

  function resolvedModel(m) {
    return selection?.agentModelOverrides?.[m.label] || '';
  }

  function accountFor(m) {
    const ov = selection?.agentAccountOverrides?.[m.label];
    if (ov) return ov;
    return selection?.typeAccounts?.[resolvedAgentType(m)] || '';
  }

  function shortAccount(id) {
    if (!id) return '—';
    if (id.length <= 18) return id;
    return id.slice(0, 14) + '…' + id.slice(-4);
  }

  // Team-header account roll-up: when ALL agents share the same
  // account, show it ONCE in the team header. Otherwise return null
  // and the per-row Account column will surface the variation.
  const sharedAccount = $derived.by(() => {
    const accounts = new Set();
    for (const m of (selection?.members || [])) {
      const a = accountFor(m);
      if (a) accounts.add(a);
    }
    return accounts.size === 1 ? [...accounts][0] : null;
  });

  // Coverage informational — same logic as before.
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

  const allAccountsResolved = $derived.by(() =>
    (selection?.members || []).every(m => !!accountFor(m))
  );

  // Per-agent owned-skill list for tooltip on Role cell.
  function roleTooltip(m) {
    const skills = ownedSkillsFor(m).map(skillName);
    if (!skills.length) return roleName(memberRole(m));
    return `${roleName(memberRole(m))} — owns: ${skills.join(', ')}`;
  }

  // The status tally used by the heading + outer footer label.
  const tally = $derived.by(() => {
    if (!launchResults) return null;
    let ready = 0, failed = 0, spawning = 0, queued = 0;
    const labels = Object.keys(launchResults);
    for (const l of labels) {
      const s = launchResults[l].status;
      if (s === 'ready') ready++;
      else if (s === 'failed') failed++;
      else if (s === 'spawning') spawning++;
      else queued++;
    }
    return { total: labels.length, ready, failed, spawning, queued, done: spawning + queued === 0 };
  });

  const headingLabel = $derived.by(() => {
    if (!launchResults) return `Ready to spawn ${(selection?.members || []).length} agents`;
    if (!tally.done) return `⚡ Spawning — ${tally.ready} of ${tally.total} ready`;
    if (tally.failed > 0) return `⚠ ${tally.failed} of ${tally.total} agents failed`;
    return `✓ All ${tally.total} agents spawned`;
  });

  // Best-effort clean-up of a backend error payload. The runtime
  // returns plain strings sometimes + `{"error": "..."}` JSON other
  // times. The status cell is too narrow to render raw JSON, so we
  // unwrap `error` / `message` / `detail` keys, fall back to the
  // first stringy field, and finally use the raw text. Strips a
  // trailing line-break + collapses internal whitespace runs.
  function cleanError(raw) {
    if (!raw) return 'unknown error';
    let s = String(raw).trim();
    try {
      const j = JSON.parse(s);
      if (typeof j === 'string') return j;
      if (j && typeof j === 'object') {
        const pick = j.error || j.message || j.detail || j.msg
          || Object.values(j).find(v => typeof v === 'string');
        if (pick) s = String(pick);
      }
    } catch {}
    return s.replace(/\s+/g, ' ').slice(0, 160);
  }

  // POST one agent → resolve to { ok, error }.
  async function spawnOne(m) {
    try {
      const skResp = await fetch('/api/v1/skills');
      const skJson = skResp.ok ? await skResp.json() : { skills: [] };
      const skByID = {};
      for (const s of (skJson.skills || [])) skByID[s.id] = s;
      const ownedSkills = ownedSkillsFor(m);
      const firstSkill = ownedSkills.length ? skByID[ownedSkills[0]] || {} : {};
      const accountID = accountFor(m);
      const resolvedAgent = resolvedAgentType(m);
      const resolvedMod = resolvedModel(m);
      const statSheet = (firstSkill.stat_sheet || resolvedMod)
        ? { ...(firstSkill.stat_sheet || {}), ...(resolvedMod ? { model_tier: resolvedMod } : {}) }
        : undefined;
      const body = {
        // #274 — use the collision-resolved effective label so an
        // operator launching Squad while a scrum team is alive auto-
        // suffixes the duplicate names instead of getting M/N
        // `runtime.Spawn: name "X" already in use` errors.
        name: effectiveLabel(m),
        agent: resolvedAgent,
        team: selection?.teamName,
        role: 'worker',
        // #596 — thread Stage 2 Repo selection through to backend cwd.
        // Was hardcoded '/home/chepherd/repos' (container-only path)
        // which (per #594) breaks host-direct deploy: cwd doesn't
        // exist → os/exec fork ENOENT → misleading 'fork/exec podman:
        // no such file' error.
        //
        // Now: prefer operator's Stage 2 Repo selection (full_name
        // → /home/chepherd/repos/<full_name>) so Stage 5 spawns into
        // the actual repo dir. Backend (#594 fix) translates
        // /home/chepherd/* → host $HOME equivalent + pre-validates
        // cwd exists with actionable error if missing.
        //
        // selection.repo shape: { kind, full_name?, clone_url?, ... }
        // — full_name is the canonical identifier across builtin +
        // external providers (see Stage2Repo.svelte:17 doc).
        cwd: selection?.repo?.full_name
          ? `/home/chepherd/repos/${selection.repo.full_name}`
          : '/home/chepherd/repos',
        // Pass provider_id when present so backend resolveProviderCwd
        // can do the smarter clone-path resolution for external/embedded
        // providers. Fallback (empty) keeps the existing $HOME default.
        provider_id: selection?.repo?.provider_id || selection?.repo?.token_id || '',
        role_id: memberRole(m),
        owned_skills: ownedSkills,
        owned_skills_scope: m.owned_skills_scope || {},
        account_id: accountID,
        claude_token_id: accountID,
        system_prompt: firstSkill.prompt_override || firstSkill.org_override_body || '',
        stat_sheet: statSheet,
      };
      const r = await fetch('/api/v1/sessions', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) {
        const t = await r.text();
        return { ok: false, error: cleanError(t || 'HTTP ' + r.status) };
      }
      return { ok: true };
    } catch (e) {
      return { ok: false, error: cleanError(e.message || e) };
    }
  }

  async function runBatch(labelsToSpawn) {
    if (!allAccountsResolved) {
      launchError = 'Every agent needs an account selected. Go back to Accounts.';
      return;
    }
    launchingNow = true;
    launchError = '';
    const members = (selection?.members || []).filter(m => labelsToSpawn.includes(m.label));

    // Initialise / merge results map (preserve prior 'ready' rows on retry).
    const prior = launchResults || {};
    const next = { ...prior };
    for (const m of members) {
      next[m.label] = { status: 'queued', durationMs: 0, error: '' };
    }
    // Any agents NOT in this batch but already in prior keep their state.
    for (const m of (selection?.members || [])) {
      if (!next[m.label]) next[m.label] = prior[m.label] || { status: 'queued', durationMs: 0, error: '' };
    }
    launchResults = next;

    for (const m of members) {
      const before = Date.now();
      launchResults = { ...launchResults, [m.label]: { ...launchResults[m.label], status: 'spawning' } };
      const res = await spawnOne(m);
      const elapsed = Date.now() - before;
      launchResults = {
        ...launchResults,
        [m.label]: res.ok
          ? { status: 'ready', durationMs: elapsed, error: '' }
          : { status: 'failed', durationMs: elapsed, error: res.error },
      };
    }
    launchingNow = false;

    // All done + zero failures → call onlaunch.
    const failedCount = Object.values(launchResults).filter(r => r.status === 'failed').length;
    if (failedCount === 0) onlaunch?.();
  }

  async function launch() {
    const allLabels = (selection?.members || []).map(m => m.label);
    return runBatch(allLabels);
  }

  async function retryFailed() {
    if (!launchResults) return;
    const failedLabels = Object.entries(launchResults)
      .filter(([, r]) => r.status === 'failed')
      .map(([l]) => l);
    return runBatch(failedLabels);
  }

  async function retryOne(label) {
    return runBatch([label]);
  }

  // Drive the outer wizard footer's primary CTA via the controller
  // $bindable. SpawnWizardV9 reads {label, canLaunch, launching} for
  // the button and invokes controller.launch() on click.
  $effect(() => {
    const failedCount = launchResults
      ? Object.values(launchResults).filter(r => r.status === 'failed').length
      : 0;
    const launchedAtLeastOne = !!launchResults;
    controller = {
      label: launchingNow
        ? `⟳ ${tally?.ready || 0} / ${tally?.total || 0}`
        : (launchedAtLeastOne
            ? (failedCount > 0 ? `Retry ${failedCount} failed` : '✓ Done')
            : `⚡ Launch ${(selection?.members || []).length} agents`),
      canLaunch: !preflight.anyFail && allAccountsResolved && !launchingNow
                  && (launchedAtLeastOne ? failedCount > 0 : true),
      launching: launchingNow,
      launch: launchedAtLeastOne ? retryFailed : launch,
    };
  });
</script>

<div class="stage6">
  <h2>{headingLabel}</h2>

  <div class="team-header">
    <div class="th-left">
      <div class="th-team">{selection?.teamName || '—'}</div>
      <div class="th-meta">
        {selection?.template?.name || '—'} ·
        {selection?.repo?.full_name || '—'}
        <span class="th-kind">({selection?.repo?.kind || '—'})</span>
      </div>
    </div>
    {#if sharedAccount}
      <div class="th-account" title={sharedAccount}>
        <span class="th-account-lbl">Account · all {(selection?.members || []).length} agents</span>
        <span class="th-account-id">⚓ {shortAccount(sharedAccount)}</span>
      </div>
    {/if}
  </div>

  {#if collisions.length > 0 && !launchResults}
    <!-- #274 — pre-Launch name-collision banner. Listing the auto-
         suffixed renames inline so the operator sees the resolution
         BEFORE clicking Launch + can intervene if they want to abort
         vs accept the -N suffixes. -->
    <div class="collision-banner" role="status">
      <span class="cb-icon">⚠</span>
      <div class="cb-text">
        <strong>{collisions.length} agent {collisions.length === 1 ? 'name is' : 'names are'} already in use.</strong>
        Auto-suffixing to avoid the
        <code>runtime.Spawn: name "X" already in use</code> error:
        <ul class="cb-list">
          {#each collisions as c}
            <li><code>{c.original}</code> → <code>{c.effective}</code></li>
          {/each}
        </ul>
        <em>Stop the existing agent(s) first if you want the original names back.</em>
      </div>
    </div>
  {/if}

  <table class="agents-table" class:launching={launchingNow || !!launchResults}>
    <thead>
      <tr>
        <th class="col-label">Label</th>
        <th class="col-role">Role</th>
        <th class="col-type">Type</th>
        <th class="col-model">Model</th>
        {#if !sharedAccount && !launchResults}<th class="col-account">Account</th>{/if}
        {#if launchResults}<th class="col-status">Status</th>{/if}
      </tr>
    </thead>
    <tbody>
      {#each selection?.members || [] as m (m.label)}
        {@const r = launchResults?.[m.label]}
        <tr class:row-ready={r?.status === 'ready'}
            class:row-spawning={r?.status === 'spawning'}
            class:row-failed={r?.status === 'failed'}
            class:row-queued={r?.status === 'queued'}>
          <td class="col-label">
            {#if effectiveLabel(m) !== m.label}
              <span class="lbl-renamed" title={`Auto-suffixed: original "${m.label}" is already taken`}>{effectiveLabel(m)}</span>
              <small class="lbl-was">was {m.label}</small>
            {:else}
              {m.label}
            {/if}
          </td>
          <td class="col-role" title={roleTooltip(m)}>{roleName(memberRole(m))}</td>
          <td class="col-type">{resolvedAgentType(m)}</td>
          <td class="col-model">{resolvedModel(m) || '—'}</td>
          {#if !sharedAccount && !launchResults}
            {#if accountFor(m)}
              <td class="col-account" title={accountFor(m)}>⚓ {shortAccount(accountFor(m))}</td>
            {:else}
              <td class="col-account miss">⚠ no account</td>
            {/if}
          {/if}
          {#if launchResults}
            <td class="col-status">
              {#if r?.status === 'ready'}
                <span class="status-ready">✓ ready <em>({(r.durationMs / 1000).toFixed(1)}s)</em></span>
              {:else if r?.status === 'spawning'}
                <span class="status-spawning">⟳ spawning…</span>
              {:else if r?.status === 'failed'}
                <span class="status-failed" title={r.error}>⚠ {r.error}</span>
                <button class="row-retry" type="button" onclick={() => retryOne(m.label)} disabled={launchingNow}>Retry</button>
              {:else}
                <span class="status-queued">· queued</span>
              {/if}
            </td>
          {/if}
        </tr>
      {/each}
    </tbody>
  </table>

  {#if Object.keys(skillCache).length > 0 && !coverage.ok && !launchResults}
    <div class="coverage-note">
      <span class="coverage-icon">⚠</span>
      <span class="coverage-text">
        <strong>{coverage.coveredCount}/{coverage.total}</strong> LEAN skills covered.
        Missing: {coverage.missing.map(m => m.name).join(', ')}.
        <em>Informational — Launch is still enabled.</em>
      </span>
    </div>
  {/if}

  {#if !launchResults}
    <details class="preflight-details">
      <summary>
        <span class="pf-summary-label">Pre-flight</span>
        <span class="pf-summary-state">
          {#if preflight.anyFail}⚠ checks failed{:else if preflight.ready}✓ checks passed{:else}⟳ running…{/if}
        </span>
      </summary>
      <PreflightChecks selection={selection} onstate={(s) => preflight = s} />
    </details>

    <label class="recipe">
      <input type="checkbox" bind:checked={saveAsRecipe} />
      Save this team as a recipe
    </label>
  {/if}

  {#if launchError}
    <p class="err">⚠ {launchError}</p>
  {/if}

  {#if !launchResults}
    <!--
      Preflight checks run silently when collapsed; mount the component
      detached so onstate fires for the controller's canLaunch gate.
      The visible component above is inside <details> — it only renders
      when expanded — so we mount a hidden instance here for the side
      effect when collapsed.
    -->
    <div class="preflight-hidden" aria-hidden="true">
      <PreflightChecks selection={selection} onstate={(s) => preflight = s} />
    </div>
  {/if}
</div>

<style>
  .stage6 { padding: 1.1rem 1.25rem; }
  h2 { font-size: 1.1rem; margin: 0 0 0.85rem 0; font-weight: 600; }

  /* Team header — single-row identity bar with team name + repo on
     the left and an account roll-up on the right when all agents
     share. Replaces the old <dl class="summary"> + .reused split. */
  /* #274 — name-collision banner. Inline, non-blocking — operator
     can still Launch + the renames have already happened. Stops short
     of a modal because the resolution (-N suffix) is operator-friendly
     and the alternative (stop existing agents) is operator's call. */
  .collision-banner {
    display: flex; gap: 0.7rem; align-items: flex-start;
    padding: 0.6rem 0.85rem;
    background: rgba(255, 193, 7, 0.08);
    border: 1px solid rgba(255, 193, 7, 0.35);
    border-radius: 5px;
    margin-bottom: 0.9rem;
    font-size: 0.82rem;
  }
  .cb-icon { color: #f7b500; font-size: 1rem; flex-shrink: 0; }
  .cb-text { color: var(--fg, #f5f5f5); line-height: 1.45; }
  .cb-text strong { color: #f7b500; }
  .cb-text code {
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.78rem;
    padding: 0 0.25rem;
    background: rgba(255, 255, 255, 0.04);
    border-radius: 3px;
  }
  .cb-list {
    margin: 0.35rem 0 0.35rem 1.2rem;
    padding: 0;
  }
  .cb-list li { padding: 0.08rem 0; }
  .cb-text em { color: var(--fg-muted, #aaa); font-style: italic; font-size: 0.78rem; }
  .lbl-renamed { color: #f7b500; font-weight: 600; }
  .lbl-was { display: block; color: var(--fg-faint, #777); font-size: 0.7rem; margin-top: 0.1rem; }

  .team-header {
    display: flex; align-items: center; gap: 1rem;
    padding: 0.7rem 0.85rem;
    background: rgba(135, 206, 235, 0.04);
    border: 1px solid var(--border, #2a2a2a);
    border-radius: 6px;
    margin-bottom: 0.9rem;
  }
  .th-left { flex: 1; min-width: 0; }
  .th-team { font-weight: 600; font-size: 0.95rem; color: var(--fg, #f5f5f5); }
  .th-meta { font-size: 0.78rem; color: var(--fg-muted, #888); margin-top: 0.15rem; }
  .th-kind { color: var(--fg-faint, #777); }
  .th-account {
    text-align: right; flex-shrink: 0;
    font-size: 0.78rem;
  }
  .th-account-lbl { color: var(--fg-muted, #888); }
  .th-account-id { display: block; color: var(--accent-2, #87ceeb); font-weight: 500; margin-top: 0.15rem; font-family: ui-monospace, SFMono-Regular, monospace; }

  /* Agent summary table — uniform 32px rows, 4 (or 5/6) columns, no
     chip-wrap. The Role cell carries a title= tooltip listing the
     agent's owned skills so the operator can still inspect skills
     without the visual noise. */
  .agents-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.85rem;
    table-layout: fixed;
    margin-bottom: 0.85rem;
  }
  .agents-table thead th {
    text-align: left;
    color: var(--fg-muted, #888);
    font-weight: 500;
    font-size: 0.74rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    padding: 0.4rem 0.55rem 0.3rem 0.55rem;
    border-bottom: 1px solid var(--border, #2a2a2a);
  }
  .agents-table tbody td {
    padding: 0.42rem 0.55rem;
    border-bottom: 1px solid rgba(255, 255, 255, 0.03);
    color: var(--fg, #f5f5f5);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    vertical-align: middle;
  }
  /* Status cell needs to wrap a multi-line error + show the inline
     Retry button without clipping. Override the global cell rule. */
  .agents-table tbody td.col-status {
    white-space: normal;
    overflow: visible;
    text-overflow: clip;
    line-height: 1.35;
  }
  .agents-table tbody td.col-status .status-failed {
    display: inline-block;
    max-width: 70%;
    word-break: break-word;
  }
  .agents-table tbody tr:hover { background: rgba(255, 255, 255, 0.02); }
  .col-label { width: 18%; font-weight: 600; }
  .col-role  { width: 26%; color: var(--accent-2, #87ceeb); }
  .col-type  { width: 16%; color: var(--fg-muted, #aaa); font-family: ui-monospace, SFMono-Regular, monospace; font-size: 0.78rem; }
  .col-model { width: 18%; color: var(--fg-muted, #aaa); font-family: ui-monospace, SFMono-Regular, monospace; font-size: 0.78rem; }
  .col-account { width: 22%; font-family: ui-monospace, SFMono-Regular, monospace; font-size: 0.78rem; color: var(--fg-muted, #aaa); }
  .col-account.miss { color: var(--danger, #e74c3c); }
  .col-status { width: 30%; }

  /* Per-row launch state colors. Spawning/ready/failed/queued. */
  tr.row-ready    .col-status .status-ready    { color: var(--success, #4ade80); }
  tr.row-ready    .col-status .status-ready em { color: var(--fg-muted, #888); font-style: italic; font-size: 0.75rem; }
  tr.row-spawning .col-status .status-spawning { color: var(--accent-2, #87ceeb); }
  tr.row-failed   { background: rgba(231, 76, 60, 0.08); }
  tr.row-failed   .col-status .status-failed   { color: var(--danger, #e74c3c); font-size: 0.78rem; }
  tr.row-queued   .col-status .status-queued   { color: var(--fg-faint, #666); }
  .row-retry {
    margin-left: 0.5rem;
    padding: 0.18rem 0.6rem;
    background: rgba(135, 206, 235, 0.18);
    color: var(--accent-2, #87ceeb);
    border: 1px solid var(--accent-2, #87ceeb);
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.74rem;
    font: inherit;
    font-weight: 500;
  }
  .row-retry:hover { background: rgba(135, 206, 235, 0.28); }
  .row-retry:disabled { opacity: 0.45; cursor: not-allowed; }

  /* Coverage notice — kept but compact + muted relative to the agents
     table which now carries the primary information density. */
  .coverage-note {
    background: rgba(255, 193, 7, 0.06);
    border: 1px solid rgba(255, 193, 7, 0.25);
    border-radius: 5px; padding: 0.4rem 0.65rem;
    margin: 0 0 0.85rem 0;
    display: flex; gap: 0.5rem; align-items: flex-start;
    font-size: 0.78rem;
  }
  .coverage-icon { color: #f7b500; flex-shrink: 0; }
  .coverage-text { color: var(--fg, #f5f5f5); line-height: 1.4; }
  .coverage-text em { color: var(--fg-muted, #aaa); font-style: italic; font-size: 0.74rem; }

  /* Pre-flight collapsed by default — one-liner summary that operators
     can expand for details. Replaces the always-on 75px panel. */
  .preflight-details {
    border: 1px solid var(--border, #2a2a2a);
    border-radius: 5px;
    padding: 0.4rem 0.65rem;
    margin-bottom: 0.85rem;
  }
  .preflight-details summary {
    cursor: pointer; font-size: 0.82rem;
    display: flex; align-items: center; gap: 0.85rem;
    list-style: none;
  }
  .preflight-details summary::-webkit-details-marker { display: none; }
  .preflight-details summary::before {
    content: '▸'; color: var(--fg-muted, #888);
    font-size: 0.7rem; transition: transform 100ms;
  }
  .preflight-details[open] summary::before { transform: rotate(90deg); }
  .pf-summary-label { font-weight: 500; color: var(--fg-muted, #888); }
  .pf-summary-state { color: var(--fg, #f5f5f5); }

  .recipe {
    display: inline-flex; align-items: center; gap: 0.4rem;
    color: var(--fg-muted, #aaa); font-size: 0.85rem;
    margin-top: 0.2rem;
  }

  .err { color: var(--danger, #e74c3c); font-size: 0.85rem; margin: 0.5rem 0; }

  /* Hidden mount used to drive preflight side-effects when the
     <details> panel is collapsed. aria-hidden + visually hidden. */
  .preflight-hidden {
    position: absolute;
    width: 1px; height: 1px;
    margin: -1px; padding: 0; border: 0;
    overflow: hidden; clip: rect(0 0 0 0);
    pointer-events: none;
  }
</style>

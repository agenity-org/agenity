<!--
  Stage4Launch — v0.9 SpawnWizard Stage 4 (#180, architect pivot 2026-05-27).

  Slim launch summary + pre-flight panel + Launch button. Crucially:
  NO first-message field — first message belongs in the agent's
  terminal AFTER spawn.

  Post-pivot data shape per #179: members[] entries have
  primary_skill + additional_skills (no mode field — always-resume
  rule decides Fresh vs Resume by label match).

  Props:
    selection: { template, repo, members, teamName }
    saveAsRecipe: $bindable — toggle
    onlaunch():    callback when Launch fires
-->
<script>
  import PreflightChecks from './PreflightChecks.svelte';

  let { selection, saveAsRecipe = $bindable(false), onlaunch } = $props();

  let preflight = $state({ ready: false, anyFail: false });
  let launching = $state(false);
  let launchError = $state('');
  let skillCache = $state({}); // id → {name, icon}

  // Resolve skill names from #194 for the per-agent badge.
  async function loadSkillNames() {
    try {
      const r = await fetch('/api-v08/v1/skills');
      if (!r.ok) return;
      const j = await r.json();
      const m = {};
      for (const s of (j.skills || [])) {
        m[s.id] = { name: s.name, icon: s.icon };
      }
      skillCache = m;
    } catch {}
  }
  $effect(() => { loadSkillNames(); });

  function skillName(id) {
    if (!id) return '—';
    return skillCache[id]?.name || id;
  }

  // Reused-credentials roll-up: count occurrences of each account ref.
  const reused = $derived.by(() => {
    const counts = new Map();
    for (const m of selection?.members || []) {
      const k = m.account_id || `default-${m.account_class || 'unknown'}`;
      counts.set(k, (counts.get(k) || 0) + 1);
    }
    return [...counts.entries()].map(([k, n]) => ({ account: k, count: n }));
  });

  // Launch — issues one POST /api/v1/sessions per member. The Skill
  // Library (#194) supplies system prompt + default tools + stat sheet
  // for each agent based on its primary_skill. Always-resume identity
  // match (#179) is decided server-side via runtime.Spawn() looking up
  // an existing Agent record by (label, team, operator) — when the
  // record exists, the same UUID + PVC is reused.
  async function launch() {
    launching = true;
    launchError = '';
    try {
      const members = selection?.members || [];
      // Pre-flight: pull skill prompts so we send them with each spawn.
      const skResp = await fetch('/api-v08/v1/skills');
      const skJson = skResp.ok ? await skResp.json() : { skills: [] };
      const skByID = {};
      for (const s of (skJson.skills || [])) skByID[s.id] = s;

      for (const m of members) {
        const primary = skByID[m.primary_skill] || {};
        const body = {
          name: m.label,
          agent: m.agent_type || 'claude-code',
          team: selection?.teamName,
          role: 'worker',
          cwd: '/home/chepherd/repos',
          system_prompt: primary.prompt_override || '',
          stat_sheet: primary.stat_sheet || undefined,
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

<div class="stage4">
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
        <span class="m-skill">{skillName(m.primary_skill)}</span>
        {#if m.additional_skills && m.additional_skills.length > 0}
          {#each m.additional_skills as sid}
            <span class="m-extra-skill">+ {skillName(sid)}</span>
          {/each}
        {/if}
        {#if m.account_id}
          <span class="m-account">⚓ {m.account_id}</span>
        {/if}
      </li>
    {/each}
  </ul>

  {#if reused.length > 0}
    <div class="reused">
      <span class="reused-lbl">Reused:</span>
      {#each reused as r}
        <span class="reused-chip">⚓ {r.account} (×{r.count})</span>
      {/each}
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
    disabled={!preflight.ready || preflight.anyFail || launching}
    onclick={launch}
  >
    {launching ? 'Launching…' : '⚡ Launch'}
  </button>
</div>

<style>
  .stage4 { padding: 1.25rem; }
  h2 { font-size: 1.15rem; margin: 0 0 1rem 0; }

  .summary { display: grid; grid-template-columns: 100px 1fr; gap: 0.4rem 0.85rem; margin: 0 0 0.85rem 0; font-size: 0.9rem; }
  .summary dt { color: var(--fg-muted, #888); font-weight: 500; }
  .summary dd { margin: 0; color: var(--fg, #f5f5f5); }
  .kind { color: var(--fg-muted, #888); font-size: 0.82rem; }

  .members { list-style: none; padding: 0; margin: 0 0 0.85rem 0; }
  .members li { display: flex; align-items: center; gap: 0.5rem; padding: 0.18rem 0; font-size: 0.85rem; }
  .m-label { font-weight: 600; min-width: 80px; }
  .m-skill { font-size: 0.78rem; padding: 0.04rem 0.5rem; border-radius: 999px; background: rgba(135, 206, 235, 0.18); color: #87ceeb; font-weight: 600; }
  .m-extra-skill { font-size: 0.72rem; padding: 0.04rem 0.4rem; border-radius: 999px; background: var(--bg, #0a0a0a); border: 1px solid var(--border, #2a2a2a); color: var(--fg-muted, #aaa); }
  .m-account { color: var(--fg-muted, #888); font-size: 0.82rem; }

  .reused { background: var(--bg, #0a0a0a); border: 1px solid var(--border, #2a2a2a); border-radius: 4px; padding: 0.4rem 0.65rem; margin-bottom: 0.85rem; font-size: 0.82rem; display: flex; flex-wrap: wrap; gap: 0.4rem; align-items: center; }
  .reused-lbl { color: var(--fg-muted, #888); }
  .reused-chip { color: var(--accent-2, #87ceeb); }

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

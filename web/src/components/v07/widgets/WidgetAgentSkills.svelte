<!--
  WidgetAgentSkills — read + edit the operator-set stat sheet ("character
  sheet") for the selected agent: context budget, model tier, discipline
  weight, velocity expectation, $-cap. Edit → Save patches the runtime
  via PATCH .../stat-sheet (per-field merge).
-->
<script>
  let { agent } = $props();
  const API = '/api-v07/v1';
  let editing = $state(false);
  let draft = $state({ context_budget: 0, model_tier: '', discipline_weight: 0, velocity_expect: '', token_budget_usd: 0 });
  let saving = $state(false);
  let err = $state('');

  function startEdit() {
    const s = agent?.stat_sheet || {};
    draft = {
      context_budget: s.context_budget || 0,
      model_tier: s.model_tier || '',
      discipline_weight: s.discipline_weight || 0,
      velocity_expect: s.velocity_expect || '',
      token_budget_usd: s.token_budget_usd || 0,
    };
    editing = true;
    err = '';
  }
  async function save() {
    if (!agent?.name) return;
    saving = true; err = '';
    const patch = {};
    if (draft.context_budget) patch.context_budget = +draft.context_budget;
    if (draft.model_tier) patch.model_tier = draft.model_tier;
    if (draft.discipline_weight) patch.discipline_weight = +draft.discipline_weight;
    if (draft.velocity_expect) patch.velocity_expect = draft.velocity_expect;
    if (draft.token_budget_usd) patch.token_budget_usd = +draft.token_budget_usd;
    try {
      const r = await fetch(`${API}/sessions/${agent.name}/stat-sheet`, {
        method: 'PATCH', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(patch),
      });
      if (!r.ok) { const e = await r.json().catch(()=>({})); err = e.error || `HTTP ${r.status}`; }
      else { editing = false; }
    } catch (e) { err = String(e); }
    saving = false;
  }
</script>

<div class="wrap">
  <header>
    <h4>Skills</h4>
    {#if agent}
      <small>{agent.name} · {agent.role}</small>
      {#if !editing}
        <button class="ghost" on:click={startEdit}>Edit</button>
      {:else}
        <button class="ghost" on:click={() => editing = false}>Cancel</button>
        <button class="primary" on:click={save} disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
      {/if}
    {/if}
  </header>
  {#if !agent}
    <p class="empty">No agent selected.</p>
  {:else if editing}
    <div class="skills-grid">
      <label>Context budget (tokens)<input type="number" min="0" step="10000" bind:value={draft.context_budget} /></label>
      <label>Model tier
        <select bind:value={draft.model_tier}>
          <option value="">(leave alone)</option>
          <option value="haiku">haiku</option>
          <option value="sonnet">sonnet</option>
          <option value="opus">opus</option>
          <option value="qwen">qwen</option>
        </select>
      </label>
      <label>Discipline weight<input type="number" min="0" max="3" step="0.1" bind:value={draft.discipline_weight} /></label>
      <label>Velocity expect
        <select bind:value={draft.velocity_expect}>
          <option value="">(leave alone)</option>
          <option value="low">low</option>
          <option value="medium">medium</option>
          <option value="high">high</option>
        </select>
      </label>
      <label>Token budget USD<input type="number" min="0" step="0.5" bind:value={draft.token_budget_usd} /></label>
    </div>
    {#if err}<div class="err">{err}</div>{/if}
  {:else}
    <dl class="kv">
      <dt>Context budget</dt><dd>{agent?.stat_sheet?.context_budget?.toLocaleString() ?? '—'} tokens</dd>
      <dt>Model tier</dt><dd>{agent?.stat_sheet?.model_tier ?? '—'}</dd>
      <dt>Discipline weight</dt><dd>{agent?.stat_sheet?.discipline_weight ?? '—'}</dd>
      <dt>Velocity expect</dt><dd>{agent?.stat_sheet?.velocity_expect ?? '—'}</dd>
      <dt>Token $-cap</dt><dd>{agent?.stat_sheet?.token_budget_usd ? `$${agent.stat_sheet.token_budget_usd}` : '— (no cap)'}</dd>
      <dt>Tool allowlist</dt><dd>{agent?.stat_sheet?.tool_allowlist?.length ? agent.stat_sheet.tool_allowlist.join(', ') : '— (all)'}</dd>
    </dl>
  {/if}
</div>

<style>
  .wrap { display: flex; flex-direction: column; height: 100%; overflow: hidden; }
  header { display: flex; align-items: center; gap: 0.5rem; padding: 0.4rem 0.55rem; border-bottom: 1px solid var(--border); }
  h4 { margin: 0; color: var(--accent); font-size: 0.82rem; }
  small { color: var(--fg-muted); font-size: 0.72rem; flex: 1; }
  button.ghost { background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 4px; padding: 0.18rem 0.55rem; font-size: 0.72rem; cursor: pointer; }
  button.primary { background: var(--accent); color: #000; border: none; border-radius: 4px; padding: 0.2rem 0.65rem; font-size: 0.72rem; font-weight: 600; cursor: pointer; }
  .kv { display: grid; grid-template-columns: auto 1fr; gap: 0.2rem 0.7rem; padding: 0.5rem 0.7rem; margin: 0; font-size: 0.75rem; }
  .kv dt { color: var(--fg-muted); }
  .kv dd { margin: 0; color: var(--fg); font-family: ui-monospace, monospace; }
  .skills-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0.4rem 0.65rem; padding: 0.5rem 0.7rem; }
  .skills-grid label { display: block; color: var(--fg-muted); font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.04em; }
  .skills-grid input, .skills-grid select { width: 100%; padding: 0.3rem 0.45rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 4px; font-family: ui-monospace, monospace; font-size: 0.78rem; margin-top: 0.15rem; }
  .empty { color: var(--fg-muted); padding: 0.6rem 0.7rem; }
  .err { color: var(--danger); padding: 0.35rem 0.55rem; font-size: 0.72rem; }
</style>

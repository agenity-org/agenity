<!--
  Stage4Launch — v0.9 SpawnWizard Stage 4 (#180).

  Slim launch summary + pre-flight panel + Launch button. Crucially:
  NO first-message field — first message belongs in the agent's
  terminal AFTER spawn (per ticket spec).

  Props:
    selection: { template, repo, members, teamName }
    saveAsRecipe: $bindable — toggle (also exposed on Stage 3; mirror here)
    onlaunch():    callback when Launch is clicked
-->
<script>
  import PreflightChecks from './PreflightChecks.svelte';

  let { selection, saveAsRecipe = $bindable(false), onlaunch } = $props();

  let preflight = $state({ ready: false, anyFail: false });
  let launching = $state(false);
  let launchError = $state('');

  // Reused-credentials roll-up: count occurrences of each account ref.
  const reused = $derived.by(() => {
    const counts = new Map();
    for (const m of selection?.members || []) {
      const k = m.account_id || `default-${m.account_class || 'unknown'}`;
      counts.set(k, (counts.get(k) || 0) + 1);
    }
    return [...counts.entries()].map(([k, n]) => ({ account: k, count: n }));
  });

  async function launch() {
    launching = true;
    launchError = '';
    try {
      const body = {
        team: selection?.teamName,
        template: selection?.template?.id,
        repo: selection?.repo,
        members: selection?.members,
        save_as_recipe: saveAsRecipe,
      };
      // The v0.9 unified workspace endpoint lands with #178 backend.
      // For now we POST to a known v0.8 endpoint as fallback so the
      // preview lights up; the wizard's parent will wire the real
      // path when assembled.
      const r = await fetch('/api-v08/v1/workspaces', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) {
        const t = await r.text();
        throw new Error(t.trim() || 'HTTP ' + r.status);
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
        <span class="m-mode m-mode-{m.mode}">{m.mode}</span>
        {#if m.mode === 'fresh' && m.account_id}
          <span class="m-account">⚓ {m.account_id}</span>
        {:else if m.mode === 'resume' && m.agent_id}
          <span class="m-account">↻ {m.agent_id.slice(0,8)}…</span>
        {:else if m.mode === 'handoff' && m.agent_id}
          <span class="m-account">⇄ from {m.agent_id.slice(0,8)}…</span>
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
  .m-mode { font-size: 0.72rem; padding: 0.04rem 0.4rem; border-radius: 3px; }
  .m-mode-fresh { background: rgba(135, 206, 235, 0.18); color: #87ceeb; }
  .m-mode-resume { background: rgba(95, 215, 95, 0.18); color: #5fd75f; }
  .m-mode-handoff { background: rgba(255, 165, 0, 0.18); color: #ffa500; }
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

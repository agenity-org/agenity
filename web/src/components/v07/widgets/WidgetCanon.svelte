<!--
  WidgetCanon — view + edit the per-team CLAUDE.md (canon). Shepherds
  re-read this each tick (chepherd.read_canon MCP tool), so editing here
  changes their next-tick context. Team is picked from a dropdown
  populated by /api/v1/teams.
-->
<script>
  import { onMount } from 'svelte';
  let { agent, teams } = $props();
  const API = '/api-v07/v1';

  let teamName = $state('');
  let body = $state('');
  let editing = $state(false);
  let draft = $state('');
  let loading = $state(false);
  let saving = $state(false);
  let err = $state('');

  // Auto-pick the selected agent's team if we have one.
  $effect(() => {
    if (!teamName && agent?.team) teamName = agent.team;
    if (!teamName && teams?.length) teamName = teams[0].name;
  });
  $effect(() => { if (teamName) loadCanon(); });

  async function loadCanon() {
    if (!teamName) return;
    loading = true; err = '';
    try {
      const r = await fetch(`${API}/teams/${teamName}/canon`);
      if (!r.ok) { err = `HTTP ${r.status}`; body = ''; loading = false; return; }
      const data = await r.json();
      body = data.body || '';
    } catch (e) { err = String(e); }
    loading = false;
  }
  function startEdit() { draft = body; editing = true; err = ''; }
  async function save() {
    saving = true; err = '';
    try {
      const r = await fetch(`${API}/teams/${teamName}/canon`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ body: draft }),
      });
      if (!r.ok) { const e = await r.json().catch(()=>({})); err = e.error || `HTTP ${r.status}`; }
      else { body = draft; editing = false; }
    } catch (e) { err = String(e); }
    saving = false;
  }
</script>

<div class="wrap">
  <header>
    <h4>Canon</h4>
    <select bind:value={teamName}>
      {#each (teams || []) as t}<option value={t.name}>{t.name}</option>{/each}
    </select>
    {#if !editing}
      <button class="ghost" on:click={startEdit} disabled={loading || !teamName}>Edit</button>
    {:else}
      <button class="ghost" on:click={() => editing = false}>Cancel</button>
      <button class="primary" on:click={save} disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
    {/if}
  </header>
  {#if !teamName}
    <p class="empty">No team selected.</p>
  {:else if editing}
    <textarea class="area" rows="20" bind:value={draft}></textarea>
    {#if err}<div class="err">{err}</div>{/if}
    <p class="hint">Edits are picked up by shepherds via <code>chepherd.read_canon</code> on their next tick (no restart needed).</p>
  {:else if loading}
    <p class="empty">Loading…</p>
  {:else if err}
    <div class="err">{err}</div>
  {:else if !body}
    <p class="empty">No canon file yet for team <code>{teamName}</code>. Click Edit to create one.</p>
  {:else}
    <pre class="body">{body}</pre>
  {/if}
</div>

<style>
  .wrap { display: flex; flex-direction: column; height: 100%; overflow: hidden; }
  header { display: flex; align-items: center; gap: 0.4rem; padding: 0.4rem 0.55rem; border-bottom: 1px solid var(--border); }
  h4 { margin: 0; color: var(--accent); font-size: 0.82rem; flex: 0; }
  select { background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 4px; padding: 0.15rem 0.4rem; font-size: 0.75rem; flex: 1; }
  button.ghost { background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 4px; padding: 0.18rem 0.55rem; font-size: 0.72rem; cursor: pointer; }
  button.primary { background: var(--accent); color: #000; border: none; border-radius: 4px; padding: 0.2rem 0.65rem; font-size: 0.72rem; font-weight: 600; cursor: pointer; }
  .body { flex: 1; margin: 0; padding: 0.55rem 0.7rem; overflow: auto; white-space: pre-wrap; word-break: break-word; font-family: ui-monospace, monospace; font-size: 0.72rem; color: var(--fg); }
  .area { flex: 1; margin: 0.4rem 0.55rem; padding: 0.5rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 4px; font-family: ui-monospace, monospace; font-size: 0.72rem; resize: none; }
  .empty { color: var(--fg-muted); padding: 0.6rem 0.7rem; font-size: 0.78rem; }
  .err { color: var(--danger); padding: 0.35rem 0.55rem; font-size: 0.72rem; }
  .hint { color: var(--fg-muted); font-size: 0.7rem; padding: 0.3rem 0.55rem; font-style: italic; }
</style>

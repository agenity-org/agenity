<!--
  WidgetAgentPrompt — show the selected agent's effective system prompt
  (the role default OR an operator override at spawn time). Edit→Save
  pokes a refined prompt into the running PTY via POST .../poke-prompt.
-->
<script>
  let { agent } = $props();
  const API = '/api-v07/v1';
  let editing = $state(false);
  let draft = $state('');
  let saving = $state(false);
  let err = $state('');

  function effective() {
    return agent?.system_prompt || '(role default — start editing to override)';
  }

  function startEdit() {
    draft = agent?.system_prompt || '';
    editing = true;
    err = '';
  }
  async function save() {
    if (!agent?.name) return;
    saving = true; err = '';
    try {
      const r = await fetch(`${API}/sessions/${agent.name}/poke-prompt`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: draft }),
      });
      if (!r.ok) { const e = await r.json().catch(()=>({})); err = e.error || `HTTP ${r.status}`; }
      else { editing = false; }
    } catch (e) { err = String(e); }
    saving = false;
  }
</script>

<div class="wrap">
  <header>
    <h4>Prompt</h4>
    {#if agent}
      <small>{agent.name} · {agent.role}</small>
      {#if !editing}
        <button class="ghost" on:click={startEdit}>Edit</button>
      {:else}
        <button class="ghost" on:click={() => editing = false}>Cancel</button>
        <button class="primary" on:click={save} disabled={saving}>{saving ? 'Sending…' : 'Send to agent'}</button>
      {/if}
    {/if}
  </header>
  {#if !agent}
    <p class="empty">No agent selected.</p>
  {:else if editing}
    <textarea class="area" rows="14" bind:value={draft}></textarea>
    {#if err}<div class="err">{err}</div>{/if}
    <p class="hint">Pressing "Send to agent" writes a fresh user-message saying "Your updated working instructions from the operator: …" so the live agent picks up the new direction without restart.</p>
  {:else}
    <pre class="body">{effective()}</pre>
  {/if}
</div>

<style>
  .wrap { display: flex; flex-direction: column; height: 100%; overflow: hidden; }
  header { display: flex; align-items: center; gap: 0.5rem; padding: 0.4rem 0.55rem; border-bottom: 1px solid var(--border); }
  h4 { margin: 0; color: var(--accent); font-size: 0.82rem; }
  small { color: var(--fg-muted); font-size: 0.72rem; flex: 1; }
  button.ghost { background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 4px; padding: 0.18rem 0.55rem; font-size: 0.72rem; cursor: pointer; }
  button.primary { background: var(--accent); color: #000; border: none; border-radius: 4px; padding: 0.2rem 0.65rem; font-size: 0.72rem; font-weight: 600; cursor: pointer; }
  .body { flex: 1; margin: 0; padding: 0.55rem 0.7rem; overflow: auto; white-space: pre-wrap; word-break: break-word; font-family: ui-monospace, monospace; font-size: 0.74rem; color: var(--fg); }
  .area { flex: 1; margin: 0.4rem 0.55rem; padding: 0.5rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 4px; font-family: ui-monospace, monospace; font-size: 0.74rem; resize: none; }
  .empty { color: var(--fg-muted); padding: 0.6rem 0.7rem; }
  .err { color: var(--danger); padding: 0.35rem 0.55rem; font-size: 0.72rem; }
  .hint { color: var(--fg-muted); font-size: 0.7rem; padding: 0.3rem 0.55rem; font-style: italic; }
</style>

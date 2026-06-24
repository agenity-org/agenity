<!--
  WidgetAgentIdentity — static "who is this agent" card.
  Independently addable (#127 R5 redo task #53). Sister widget:
  WidgetAgentRuntime renders the dynamic counters.
-->
<script>
  import { onMount } from 'svelte';
  let { agent } = $props();
  const API = '/api-v08/v1';
  let claudeStatus = $state(null);
  let claudeProfile = $state(null);

  onMount(async () => {
    try { const r = await fetch(`${API}/runtime/claude-status`); claudeStatus = await r.json(); } catch {}
    try { const r = await fetch(`${API}/runtime/claude-profile`); claudeProfile = await r.json(); } catch {}
  });

  function ghRepoShort(url) {
    if (!url) return null;
    const m = url.match(/[:/]([^/:]+\/[^/.]+)(\.git)?$/);
    return m ? m[1] : url;
  }
  function modelLabel() {
    if (!agent?.model) return '—';
    if (agent.context_size === 1_000_000 && !agent.model.includes('[1m]')) return `${agent.model}[1m]`;
    return agent.model;
  }
</script>

<div class="wrap">
  {#if !agent}
    <p class="empty">No agent selected.</p>
  {:else}
    <section class="card">
      <div class="card-head">
        <span class="card-title">Identity</span>
        <span class="agent-name">{agent.name}</span>
      </div>
      <dl>
        <dt>agent</dt><dd>{agent.agent}</dd>
        <dt>role</dt><dd>{agent.role}</dd>
        <dt>team</dt><dd>{agent.team || '—'}</dd>
        <dt>repo</dt><dd>{#if agent.github_url}<a href={agent.github_url} target="_blank" title={agent.github_url}>{ghRepoShort(agent.github_url)} ↗</a>{:else}—{/if}</dd>
        <dt>branch</dt><dd>{agent.branch || '—'}</dd>
        {#if agent.container_runtime}<dt>runtime</dt><dd class="mono">{agent.container_runtime}</dd>{/if}
        {#if agent.trust_band}<dt>trust</dt><dd class="mono">{agent.trust_band}</dd>{/if}
        <dt>login</dt><dd>{claudeStatus?.login_method ?? '—'}</dd>
        <dt>account</dt><dd title={claudeProfile?.email || 'profile API requires elevated OAuth scope'}>{claudeProfile?.email || claudeStatus?.subscription || '—'}</dd>
        <dt>model</dt><dd class="mono">{modelLabel()}</dd>
      </dl>
    </section>
  {/if}
</div>

<style>
  .wrap { display: flex; flex-direction: column; height: 100%; overflow-y: auto; }
  .empty { color: var(--fg-muted); padding: 0.6rem 0.7rem; }
  .card { background: var(--bg-elev); }
  .card-head { display: flex; align-items: center; gap: 0.5rem; padding: 0.38rem 0.7rem; border-bottom: 1px solid var(--border); }
  .card-title { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.07em; color: var(--accent); font-weight: 600; }
  .agent-name { color: var(--fg-muted); font-size: 0.82rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
  dl { display: grid; grid-template-columns: minmax(90px, max-content) 1fr; gap: 0.08rem 0.6rem; padding: 0.35rem 0.7rem; margin: 0; }
  dt { color: var(--fg-muted); white-space: nowrap; font-size: 0.82rem; line-height: 1.6; }
  dd { margin: 0; color: var(--fg); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; font-size: 0.82rem; line-height: 1.6; }
  .mono { font-family: ui-monospace, monospace; }
  a { color: var(--accent-2); text-decoration: none; }
  a:hover { text-decoration: underline; }
</style>

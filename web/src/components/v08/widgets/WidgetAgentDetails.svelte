<!--
  WidgetAgentDetails — flat single-block KV view. No sub-sections,
  no extra spacing dividers. Every value gets ellipsis on overflow.
-->
<script>
  import { onMount } from 'svelte';
  let { agent } = $props();
  const API = '/api-v08/v1';
  let claudeStatus = $state(null);
  let claudeProfile = $state(null);

  onMount(async () => {
    try {
      const r = await fetch(`${API}/runtime/claude-status`);
      claudeStatus = await r.json();
    } catch {}
    try {
      const r = await fetch(`${API}/runtime/claude-profile`);
      claudeProfile = await r.json();
    } catch {}
  });

  function ageString(at) {
    if (!at) return '—';
    const s = Math.floor((Date.now() - new Date(at).getTime()) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s/60)}m ago`;
    if (s < 86400) return `${Math.floor(s/3600)}h ago`;
    return `${Math.floor(s/86400)}d ago`;
  }
  function idleString(idleSecs) {
    if (idleSecs == null) return '—';
    if (idleSecs < 1) return '0s';
    if (idleSecs < 60) return `${idleSecs.toFixed(0)}s`;
    if (idleSecs < 3600) return `${(idleSecs/60).toFixed(1)}m`;
    return `${(idleSecs/3600).toFixed(1)}h`;
  }
  function bytesString(n) {
    if (n == null) return '—';
    if (n < 1024) return `${n} B`;
    if (n < 1024*1024) return `${(n/1024).toFixed(1)} KiB`;
    return `${(n/1024/1024).toFixed(2)} MiB`;
  }
  function statusChip() {
    if (!agent) return ['—', 'muted'];
    if (agent.exited) return [`exited (${agent.exit_code ?? 0})`, 'danger'];
    if (agent.paused) return ['paused', 'warn'];
    return ['live', 'ok'];
  }
  function ghRepoShort(url) {
    if (!url) return null;
    const m = url.match(/[:/]([^/:]+\/[^/.]+)(\.git)?$/);
    return m ? m[1] : url;
  }
  function ctxPct() {
    if (!agent?.context_size || !agent?.context_tokens) return null;
    return Math.min(100, (agent.context_tokens / agent.context_size) * 100);
  }
  function modelLabel() {
    if (!agent?.model) return '—';
    if (agent.context_size === 1_000_000 && !agent.model.includes('[1m]')) return `${agent.model}[1m]`;
    return agent.model;
  }

  let [statusText, statusKind] = $derived.by(statusChip);
  let ctx = $derived(ctxPct());
</script>

<div class="wrap">
  <header>
    <h4>Agent Details</h4>
    {#if agent}<small>{agent.name}</small>{/if}
  </header>
  {#if !agent}
    <p class="empty">No agent selected.</p>
  {:else}
    <dl>
      <dt>status</dt><dd><span class="chip {statusKind}">● {statusText}</span></dd>
      <dt>started</dt><dd>{ageString(agent.created_at)}</dd>
      <dt>role</dt><dd>{agent.role}</dd>
      <dt>team</dt><dd>{agent.team}</dd>
      <dt>cwd</dt><dd class="mono" title={agent.cwd}>{agent.cwd || '—'}</dd>
      <dt>repo</dt><dd>{#if agent.github_url}<a href={agent.github_url} target="_blank" title={agent.github_url}>{ghRepoShort(agent.github_url)} ↗</a>{:else}—{/if}</dd>
      <dt>branch</dt><dd>{agent.branch || '—'}</dd>
      <dt>agent</dt><dd>{agent.agent}</dd>
      {#if agent.container_runtime}<dt>runtime</dt><dd class="mono">{agent.container_runtime}</dd>{/if}
      {#if agent.trust_band}<dt>trust band</dt><dd class="mono">{agent.trust_band}</dd>{/if}
      <dt>login</dt><dd>{claudeStatus?.login_method ?? '—'}</dd>
      <dt>email</dt><dd title="Anthropic profile API didn't return this — likely requires elevated OAuth scope">{claudeProfile?.email || '—'}</dd>
      <dt>subscription</dt><dd>{claudeStatus?.subscription ?? '—'}</dd>
      <dt>model</dt><dd class="mono">{modelLabel()}</dd>
      <dt>pid</dt><dd class="mono">{agent.pid ?? '—'}</dd>
      <dt>uuid</dt><dd class="mono" title={agent.id}>{agent.id || '—'}</dd>
      {#if agent.claude_uuid}<dt>claude session</dt><dd class="mono" title={agent.claude_uuid}>{agent.claude_uuid}</dd>{/if}
      <dt>bytes 5m</dt><dd>{bytesString(agent.bytes_5m)}</dd>
      <dt>total</dt><dd>{bytesString(agent.total_bytes)}</dd>
      <dt>idle</dt><dd>{idleString(agent.idle_seconds)}</dd>
      <dt>context size</dt><dd>{agent.context_size ? agent.context_size.toLocaleString() + ' tokens' : '—'}</dd>
      <dt>context used</dt><dd>
        {#if ctx != null}
          <span class="ctx-bar"><span class="ctx-fill" style="width:{ctx}%"></span></span>
          <span class="ctx-num">{ctx.toFixed(1)}%</span>
        {:else}—{/if}
      </dd>
      <dt>session limit</dt><dd class="muted" title="Anthropic's per-session quota — not yet exposed via API">—</dd>
      <dt>weekly limit</dt><dd class="muted" title="Anthropic's weekly rate-limit quota — not yet exposed via API">—</dd>
    </dl>
  {/if}
</div>

<style>
  .wrap { display: flex; flex-direction: column; height: 100%; overflow: hidden; }
  header { display: flex; align-items: center; gap: 0.5rem; padding: 0.4rem 0.55rem; border-bottom: 1px solid var(--border); }
  h4 { margin: 0; color: var(--accent); }
  small { color: var(--fg-muted); flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  dl { display: grid; grid-template-columns: minmax(110px, max-content) 1fr; gap: 0.1rem 0.7rem; padding: 0.4rem 0.7rem; margin: 0; overflow-y: auto; }
  dt { color: var(--fg-muted); white-space: nowrap; }
  dd { margin: 0; color: var(--fg); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .mono { font-family: ui-monospace, monospace; }
  .muted { color: var(--fg-faint); }
  .chip { display: inline-block; padding: 0.05rem 0.45rem; border-radius: 999px; font-weight: 600; }
  .chip.ok { background: rgba(80, 200, 120, 0.15); color: #5cd57f; }
  .chip.warn { background: rgba(255, 165, 0, 0.15); color: #ffaa55; }
  .chip.danger { background: rgba(255, 107, 107, 0.18); color: #ff6b6b; }
  .chip.muted { background: rgba(150, 150, 150, 0.15); color: var(--fg-muted); }
  .ctx-bar { display: inline-block; width: 80px; height: 6px; background: var(--bg-input); border: 1px solid var(--border); border-radius: 3px; vertical-align: middle; overflow: hidden; margin-right: 0.4rem; }
  .ctx-fill { display: block; height: 100%; background: linear-gradient(90deg, var(--accent-2), var(--accent)); }
  .ctx-num { font-family: ui-monospace, monospace; }
  a { color: var(--accent-2); text-decoration: none; }
  a:hover { text-decoration: underline; }
  .empty { color: var(--fg-muted); padding: 0.6rem 0.7rem; }
</style>

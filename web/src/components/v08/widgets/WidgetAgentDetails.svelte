<!--
  WidgetAgentDetails — two cards:
    1. Identity (static): agent, role, team, repo/branch, account info, model
    2. Runtime (dynamic): status, uptime, pid, throughput, context, limits
  Fields removed per operator feedback: cwd, uuid, claude-session.
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
  // Session limit + weekly limit come from claudeStatus when available.
  // Anthropic doesn't expose these in the credentials JSON directly, so
  // we surface the rate_tier which encodes the tier bucket instead.
  function limitHint() {
    if (!claudeStatus?.logged_in) return null;
    const tier = claudeStatus?.rate_tier;
    const sub = claudeStatus?.subscription;
    if (!tier && !sub) return null;
    return `${sub ?? ''}${tier ? ' · tier ' + tier : ''}`;
  }

  let [statusText, statusKind] = $derived.by(statusChip);
  let ctx = $derived(ctxPct());
</script>

<div class="wrap">
  {#if !agent}
    <p class="empty">No agent selected.</p>
  {:else}
    <!-- Card 1: Identity — static fields that don't change while running -->
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

    <!-- Card 2: Runtime — dynamic fields that change during execution -->
    <section class="card">
      <div class="card-head">
        <span class="card-title">Runtime</span>
        <span class="chip {statusKind}">● {statusText}</span>
      </div>
      <dl>
        <dt>started</dt><dd>{ageString(agent.created_at)}</dd>
        <dt>pid</dt><dd class="mono">{agent.pid ?? '—'}</dd>
        <dt>bytes 5m</dt><dd>{bytesString(agent.bytes_5m)}</dd>
        <dt>total</dt><dd>{bytesString(agent.total_bytes)}</dd>
        <dt>idle</dt><dd>{idleString(agent.idle_seconds)}</dd>
        <dt>context</dt><dd>
          {#if ctx != null}
            <span class="ctx-bar"><span class="ctx-fill" style="width:{ctx}%"></span></span>
            <span class="ctx-num">{ctx.toFixed(1)}%</span>
          {:else}—{/if}
        </dd>
        <dt>ctx size</dt><dd>{agent.context_size ? agent.context_size.toLocaleString() + ' tk' : '—'}</dd>
        <dt>session limit</dt><dd class="muted" title="Anthropic per-session quota — not exposed via credentials API">{limitHint() ?? '—'}</dd>
        <dt>weekly limit</dt><dd class="muted" title="Anthropic weekly rate-limit quota">{limitHint() ? 'see account portal' : '—'}</dd>
      </dl>
    </section>
  {/if}
</div>

<style>
  .wrap { display: flex; flex-direction: column; height: 100%; overflow-y: auto; gap: 1px; }
  .empty { color: var(--fg-muted); padding: 0.6rem 0.7rem; }

  .card { background: var(--bg-elev); border-bottom: 1px solid var(--border); }
  .card-head { display: flex; align-items: center; gap: 0.5rem; padding: 0.38rem 0.7rem; border-bottom: 1px solid var(--border); }
  .card-title { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.07em; color: var(--accent); font-weight: 600; }
  .agent-name { color: var(--fg-muted); font-size: 0.82rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }

  dl { display: grid; grid-template-columns: minmax(90px, max-content) 1fr; gap: 0.08rem 0.6rem; padding: 0.35rem 0.7rem; margin: 0; }
  dt { color: var(--fg-muted); white-space: nowrap; font-size: 0.82rem; line-height: 1.6; }
  dd { margin: 0; color: var(--fg); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; font-size: 0.82rem; line-height: 1.6; }
  .mono { font-family: ui-monospace, monospace; }
  .muted { color: var(--fg-faint); }

  .chip { display: inline-block; padding: 0.05rem 0.45rem; border-radius: 999px; font-weight: 600; font-size: 0.75rem; }
  .chip.ok { background: rgba(80, 200, 120, 0.15); color: #5cd57f; }
  .chip.warn { background: rgba(255, 165, 0, 0.15); color: #ffaa55; }
  .chip.danger { background: rgba(255, 107, 107, 0.18); color: #ff6b6b; }
  .chip.muted { background: rgba(150, 150, 150, 0.15); color: var(--fg-muted); }

  .ctx-bar { display: inline-block; width: 64px; height: 6px; background: var(--bg-input); border: 1px solid var(--border); border-radius: 3px; vertical-align: middle; overflow: hidden; margin-right: 0.35rem; }
  .ctx-fill { display: block; height: 100%; background: linear-gradient(90deg, var(--accent-2), var(--accent)); }
  .ctx-num { font-family: ui-monospace, monospace; }

  a { color: var(--accent-2); text-decoration: none; }
  a:hover { text-decoration: underline; }
</style>

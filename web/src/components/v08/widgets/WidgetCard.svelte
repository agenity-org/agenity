<!--
  WidgetCard — renders one of (identity|location|process) cards for the
  selected agent. Reuses the dense kv grid layout from v0.5.
-->
<script>
  let { kind, selectedAgent, sessions, memberships = [] } = $props();
  let info = $derived(sessions?.find(s => s.name === selectedAgent));
  let myMems = $derived(memberships.filter(m => m.agent_name === selectedAgent));
  function ageString(createdAt) {
    if (!createdAt) return '—';
    const s = Math.floor((Date.now() - new Date(createdAt).getTime()) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s/60)}m ago`;
    return `${Math.floor(s/3600)}h ago`;
  }
  function fmtBytes(n) {
    if (n == null) return '—';
    if (n < 1024) return n + ' B';
    if (n < 1024*1024) return (n/1024).toFixed(1) + ' KiB';
    return (n/1024/1024).toFixed(2) + ' MiB';
  }
  function fmtSec(s) {
    if (s == null) return '—';
    if (s < 60) return Math.floor(s) + 's';
    if (s < 3600) return Math.floor(s/60) + 'm';
    return Math.floor(s/3600) + 'h';
  }
</script>

<div class="card">
  {#if !info}
    <p class="hint">No agent selected.</p>
  {:else if kind === 'identity'}
    <h4>Identity</h4>
    <div class="kv">
      <span class="k">name</span><span class="v"><code>{info.name}</code></span>
      <span class="k">agent</span><span class="v">{info.agent}</span>
      <span class="k">role</span><span class="v">{info.role}</span>
      <span class="k">team</span><span class="v">{info.team || '—'}</span>
      {#if myMems.length > 1}
        <span class="k">also in</span><span class="v">{myMems.filter(m => m.team_name !== info.team).map(m => `${m.team_name} (${m.role})`).join(', ')}</span>
      {/if}
    </div>
  {:else if kind === 'location'}
    <h4>Location</h4>
    <div class="kv">
      <span class="k">cwd</span><span class="v wrap"><code>{info.cwd || '—'}</code></span>
      {#if info.github_url}<span class="k">repo</span><span class="v wrap"><a href={info.github_url} target="_blank">{info.github_url.replace('https://github.com/','')} ↗</a></span>{/if}
      {#if info.branch}<span class="k">branch</span><span class="v"><code>{info.branch}</code></span>{/if}
      <span class="k">started</span><span class="v">{ageString(info.created_at)}</span>
      <span class="k">status</span><span class="v">{info.exited ? `⨯ exited (${info.exit_code})` : info.paused ? '⏸ paused' : info.bytes_5m > 0 ? '● live' : '○ idle'}</span>
    </div>
  {:else if kind === 'process'}
    <h4>Process</h4>
    <div class="kv">
      <span class="k">pid</span><span class="v"><code>{info.pid || '—'}</code></span>
      <span class="k">uuid</span><span class="v wrap"><code class="uuid">{info.id}</code></span>
      <span class="k">bytes 5m</span><span class="v">{fmtBytes(info.bytes_5m)}</span>
      <span class="k">total</span><span class="v">{fmtBytes(info.total_bytes)}</span>
      <span class="k">idle</span><span class="v">{fmtSec(info.idle_seconds)}</span>
    </div>
  {/if}
</div>

<style>
  .card { padding: 0.75rem 0.85rem; height: 100%; overflow-y: auto; background: var(--bg); }
  h4 { font-size: 0.82rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.05em; margin: 0 0 0.55rem 0; font-weight: 600; }
  .kv { display: grid; grid-template-columns: minmax(60px, auto) 1fr; column-gap: 0.6rem; row-gap: 0.32rem; align-items: baseline; }
  .kv .k { color: var(--fg-muted); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.04em; }
  .kv .v { color: var(--fg); font-size: 0.86rem; word-break: break-word; }
  .kv .v code { color: var(--accent-2); font-size: 0.82rem; }
  .kv .v code.uuid { font-size: 0.74rem; word-break: break-all; }
  .kv .v.wrap { word-break: break-all; }
  .kv .v a { color: var(--accent); text-decoration: none; }
  .kv .v a:hover { text-decoration: underline; }
  .hint { color: var(--fg-faint); font-size: 0.85rem; padding: 0.5rem 0; }
</style>

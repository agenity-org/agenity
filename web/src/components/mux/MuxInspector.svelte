<!--
  MuxInspector — the focused-agent detail widget for the mux dashboard.
  Renders real session data (identity, role, scorecard geomean, runtime,
  shepherd verdict, github/branch) for the workspace-level selectedAgent.
  Pure read view; all data comes from the polled /api/v1/sessions wire.
-->
<script>
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let { sessions = [], memberships = [], selectedAgent = null, selectAgent = () => {} } = $props();

  let agent = $derived((sessions || []).find((s) => s.name === selectedAgent) || null);
  let ident = $derived(agent ? agentIdentity(agent) : null);
  let teamsOf = $derived(
    (memberships || []).filter((m) => m.agent_name === selectedAgent).map((m) => m.team_name)
  );

  // Scorecard dims: G(oal) V(elocity) F(idelity) E(fficiency) D(iscipline).
  const DIMS = [
    ['G', 'goal'],
    ['V', 'velocity'],
    ['F', 'fidelity'],
    ['E', 'efficiency'],
    ['D', 'discipline'],
  ];
  function geomean(sc) {
    if (!sc) return null;
    const vals = DIMS.map(([k]) => sc[k]).filter((v) => typeof v === 'number' && v > 0);
    if (!vals.length) return null;
    const prod = vals.reduce((a, b) => a * b, 1);
    return Math.pow(prod, 1 / vals.length);
  }
  function fmtBytes(n) {
    if (!n && n !== 0) return '—';
    if (n < 1024) return n + ' B';
    if (n < 1048576) return (n / 1024).toFixed(1) + ' KB';
    return (n / 1048576).toFixed(1) + ' MB';
  }
  function relAge(at) {
    if (!at) return '—';
    const s = Math.floor((Date.now() - new Date(at).getTime()) / 1000);
    if (s < 60) return `${s}s`;
    if (s < 3600) return `${Math.floor(s / 60)}m`;
    if (s < 86400) return `${Math.floor(s / 3600)}h`;
    return `${Math.floor(s / 86400)}d`;
  }
</script>

<div class="insp">
  {#if !agent}
    <div class="empty">No agent focused. Click an agent in the rail or a terminal pane.</div>
  {:else}
    {@const gm = geomean(agent.scorecard)}
    <header class="hd">
      <span class="big-ic" style="color:{ident.color}">{ident.icon}</span>
      <div class="hd-txt">
        <div class="nm" style="color:{ident.color}">{agent.name}</div>
        <div class="rl">{agent.role || 'agent'}{#if teamsOf.length} · {teamsOf.join(', ')}{/if}</div>
      </div>
      <span class="state {agent.exited ? 'dead' : agent.paused ? 'paused' : 'live'}">
        {agent.exited ? 'exited' : agent.paused ? 'paused' : 'live'}
      </span>
    </header>

    {#if agent.scorecard}
      <section class="card">
        <div class="card-h">Scorecard {#if gm != null}<span class="gm">{gm.toFixed(1)}</span>{/if}</div>
        <div class="bars">
          {#each DIMS as [k, lbl]}
            {@const v = agent.scorecard[k]}
            <div class="bar-row" title={lbl}>
              <span class="bar-k">{k}</span>
              <span class="bar-track"><span class="bar-fill" style="width:{typeof v === 'number' ? (v / 10) * 100 : 0}%; background:{ident.color}"></span></span>
              <span class="bar-v">{typeof v === 'number' ? v.toFixed(1) : '—'}</span>
            </div>
          {/each}
        </div>
      </section>
    {/if}

    {#if agent.last_verdict}
      <section class="card">
        <div class="card-h">Shepherd verdict</div>
        <div class="verdict v-{(agent.last_verdict || '').toLowerCase()}">{agent.last_verdict}</div>
        {#if agent.last_verdict_msg}<div class="vmsg">{agent.last_verdict_msg}</div>{/if}
        {#if agent.last_verdict_at}<div class="vmeta">{relAge(agent.last_verdict_at)} ago{#if agent.intervention_count} · {agent.intervention_count} interventions{/if}</div>{/if}
      </section>
    {/if}

    <section class="card">
      <div class="card-h">Runtime</div>
      <dl class="kv">
        <dt>uptime</dt><dd>{relAge(agent.created_at)}</dd>
        <dt>idle</dt><dd>{agent.idle_seconds != null ? agent.idle_seconds + 's' : '—'}</dd>
        <dt>traffic 5m</dt><dd>{fmtBytes(agent.bytes_5m)}</dd>
        <dt>total</dt><dd>{fmtBytes(agent.total_bytes)}</dd>
        {#if agent.pid}<dt>pid</dt><dd>{agent.pid}</dd>{/if}
        {#if agent.cwd}<dt>cwd</dt><dd class="mono ellip" title={agent.cwd}>{agent.cwd}</dd>{/if}
        {#if agent.branch}<dt>branch</dt><dd class="mono">{agent.branch}</dd>{/if}
      </dl>
      {#if agent.github_url}
        <a class="gh" href={agent.github_url} target="_blank" rel="noopener">open repo ↗</a>
      {/if}
    </section>
  {/if}
</div>

<style>
  .insp { height: 100%; overflow-y: auto; padding: 0.6rem 0.7rem; font-family: var(--mux-mono); }
  .empty { color: var(--mux-fg-muted); font-size: 0.8rem; padding: 1rem 0.5rem; line-height: 1.5; }
  .hd { display: flex; align-items: center; gap: 0.55rem; margin-bottom: 0.7rem; }
  .big-ic { font-size: 1.5rem; line-height: 1; }
  .hd-txt { flex: 1; min-width: 0; }
  .nm { font-weight: 700; font-size: 0.95rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .rl { color: var(--mux-fg-muted); font-size: 0.72rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .state { font-size: 0.66rem; font-weight: 700; padding: 0.1rem 0.45rem; border-radius: 999px; text-transform: uppercase; letter-spacing: 0.04em; }
  .state.live { color: var(--mux-ok); background: var(--mux-ok-soft); }
  .state.paused { color: var(--mux-warn); background: var(--mux-warn-soft); }
  .state.dead { color: var(--mux-fg-faint); background: var(--mux-hover); }

  .card { background: var(--mux-bar); border: 1px solid var(--mux-border); border-radius: 7px; padding: 0.55rem 0.65rem; margin-bottom: 0.55rem; }
  .card-h { font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--mux-fg-muted); margin-bottom: 0.45rem; display: flex; align-items: center; gap: 0.5rem; }
  .gm { margin-left: auto; color: var(--mux-accent); font-weight: 700; font-size: 0.92rem; letter-spacing: 0; }

  .bars { display: flex; flex-direction: column; gap: 0.3rem; }
  .bar-row { display: flex; align-items: center; gap: 0.5rem; }
  .bar-k { width: 1rem; color: var(--mux-fg-muted); font-size: 0.72rem; }
  .bar-track { flex: 1; height: 6px; background: var(--mux-bg); border-radius: 3px; overflow: hidden; }
  .bar-fill { display: block; height: 100%; border-radius: 3px; transition: width 0.3s; }
  .bar-v { width: 1.8rem; text-align: right; color: var(--mux-fg); font-size: 0.72rem; }

  .verdict { font-weight: 700; font-size: 0.82rem; }
  .verdict.pass { color: var(--mux-ok); }
  .verdict.fail { color: var(--mux-danger); }
  .verdict.hold, .verdict.warn { color: var(--mux-warn); }
  .vmsg { color: var(--mux-fg); font-size: 0.76rem; margin-top: 0.25rem; line-height: 1.4; }
  .vmeta { color: var(--mux-fg-faint); font-size: 0.68rem; margin-top: 0.25rem; }

  .kv { display: grid; grid-template-columns: max-content 1fr; gap: 0.2rem 0.7rem; margin: 0; }
  .kv dt { color: var(--mux-fg-muted); font-size: 0.72rem; }
  .kv dd { color: var(--mux-fg); font-size: 0.72rem; margin: 0; text-align: right; }
  .kv dd.mono { font-family: var(--mux-mono); }
  .kv dd.ellip { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 12rem; justify-self: end; }
  .gh { display: inline-block; margin-top: 0.5rem; color: var(--mux-accent); font-size: 0.74rem; text-decoration: none; }
  .gh:hover { text-decoration: underline; }
</style>

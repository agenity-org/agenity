<!--
  StudioInspector — identity + scorecard + runtime + last-verdict detail for
  the focused agent. Pure read-render over the polled `sessions` list (no own
  fetch); mirrors the inspector region of the v08 shell.
-->
<script>
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let { sessions = [], memberships = [], selectedAgent = null } = $props();

  let agent = $derived((sessions || []).find(s => s.name === selectedAgent) || null);
  let id = $derived(agent ? agentIdentity(agent) : null);
  let teamsOf = $derived(
    (memberships || []).filter(m => m.agent_name === selectedAgent)
  );

  function ago(sec) {
    if (sec == null) return '—';
    if (sec < 60) return sec + 's';
    if (sec < 3600) return Math.floor(sec / 60) + 'm';
    return Math.floor(sec / 3600) + 'h';
  }
  function bytes(n) {
    if (n == null) return '—';
    if (n < 1024) return n + ' B';
    if (n < 1048576) return (n / 1024).toFixed(1) + ' KB';
    return (n / 1048576).toFixed(1) + ' MB';
  }
  // geometric mean of the 5 scorecard dims (G,V,F,E,D)
  function geomean(sc) {
    if (!sc) return null;
    const dims = ['G', 'V', 'F', 'E', 'D'].map(k => sc[k]).filter(v => typeof v === 'number' && v > 0);
    if (!dims.length) return null;
    const prod = dims.reduce((a, b) => a * b, 1);
    return Math.pow(prod, 1 / dims.length);
  }
  function scoreColor(v) {
    if (v == null) return 'var(--st-fg-faint)';
    if (v >= 4) return 'var(--st-ok)';
    if (v >= 3) return 'var(--st-accent)';
    return 'var(--st-danger)';
  }
  function statusOf(s) {
    if (!s) return { label: '—', cls: '' };
    if (s.exited) return { label: 'exited' + (s.exit_code != null ? ' (' + s.exit_code + ')' : ''), cls: 'exited' };
    if (s.paused) return { label: 'paused', cls: 'paused' };
    if (s.live) return { label: 'live', cls: 'live' };
    return { label: 'idle', cls: '' };
  }
</script>

<div class="insp">
  {#if !agent}
    <div class="insp-empty">Select an agent in the Explorer to inspect it.</div>
  {:else}
    {@const st = statusOf(agent)}
    {@const gm = geomean(agent.scorecard)}
    <header class="insp-head" style="--ac:{id.color}">
      <span class="badge" style="background:{id.color}">{id.icon}</span>
      <div class="who">
        <div class="name">{agent.name}</div>
        <div class="role">{agent.role || 'agent'}{#if agent.team} · {agent.team}{/if}</div>
      </div>
      <span class="st-pill {st.cls}">{st.label}</span>
    </header>

    <section class="insp-sec">
      <h4>Scorecard</h4>
      {#if agent.scorecard}
        <div class="scards">
          {#each ['G','V','F','E','D'] as k}
            <div class="scard">
              <span class="sk">{k}</span>
              <span class="sv" style="color:{scoreColor(agent.scorecard[k])}">{agent.scorecard[k] ?? '—'}</span>
            </div>
          {/each}
        </div>
        {#if gm != null}
          <div class="geomean">geomean <strong style="color:{scoreColor(gm)}">{gm.toFixed(2)}</strong></div>
        {/if}
      {:else}
        <div class="muted">No scorecard yet.</div>
      {/if}
    </section>

    {#if agent.last_verdict}
      <section class="insp-sec">
        <h4>Last verdict</h4>
        <div class="verdict">
          <span class="vlabel">{agent.last_verdict}</span>
          {#if agent.intervention_count}<span class="vcount">{agent.intervention_count} intervention{agent.intervention_count === 1 ? '' : 's'}</span>{/if}
        </div>
        {#if agent.last_verdict_msg}<p class="vmsg">{agent.last_verdict_msg}</p>{/if}
      </section>
    {/if}

    <section class="insp-sec">
      <h4>Runtime</h4>
      <dl class="kv">
        <dt>Status</dt><dd>{st.label}</dd>
        <dt>PID</dt><dd>{agent.pid ?? '—'}</dd>
        <dt>Idle</dt><dd>{ago(agent.idle_seconds)}</dd>
        <dt>Throughput 5m</dt><dd>{bytes(agent.bytes_5m)}</dd>
        <dt>Total out</dt><dd>{bytes(agent.total_bytes)}</dd>
        <dt>CWD</dt><dd class="ellip" title={agent.cwd}>{agent.cwd || '—'}</dd>
        {#if agent.branch}<dt>Branch</dt><dd class="ellip" title={agent.branch}>{agent.branch}</dd>{/if}
      </dl>
    </section>

    {#if teamsOf.length}
      <section class="insp-sec">
        <h4>Memberships</h4>
        <div class="memb">
          {#each teamsOf as m}
            <span class="mchip">{m.team_name} · <em>{m.role}</em></span>
          {/each}
        </div>
      </section>
    {/if}

    {#if agent.github_url}
      <section class="insp-sec">
        <a class="ghlink" href={agent.github_url} target="_blank" rel="noopener">Open repository ↗</a>
      </section>
    {/if}
  {/if}
</div>

<style>
  .insp { height: 100%; overflow-y: auto; padding: 0.8rem; color: var(--st-fg); font-size: 0.84rem; }
  .insp-empty { color: var(--st-fg-faint); padding: 1.5rem 0.5rem; text-align: center; font-size: 0.85rem; }
  .insp-head { display: flex; align-items: center; gap: 0.6rem; padding-bottom: 0.7rem; border-bottom: 1px solid var(--st-border); }
  .badge { width: 1.8rem; height: 1.8rem; border-radius: 7px; display: grid; place-items: center; color: #0a0a0a; font-size: 1rem; flex-shrink: 0; }
  .who { flex: 1; min-width: 0; }
  .name { font-weight: 700; font-size: 0.92rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .role { color: var(--st-fg-muted); font-size: 0.76rem; }
  .st-pill { font-size: 0.68rem; padding: 0.12rem 0.45rem; border-radius: 999px; background: var(--st-chip); color: var(--st-fg-muted); border: 1px solid var(--st-border); }
  .st-pill.live { color: var(--st-ok); border-color: var(--st-ok); }
  .st-pill.paused { color: var(--st-accent); border-color: var(--st-accent); }
  .st-pill.exited { color: var(--st-danger); border-color: var(--st-danger); }
  .insp-sec { padding: 0.8rem 0; border-bottom: 1px solid var(--st-border); }
  .insp-sec h4 { font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--st-fg-muted); margin: 0 0 0.5rem; }
  .muted { color: var(--st-fg-faint); font-size: 0.8rem; }
  .scards { display: grid; grid-template-columns: repeat(5, 1fr); gap: 0.4rem; }
  .scard { display: flex; flex-direction: column; align-items: center; gap: 0.15rem; background: var(--st-chip); border: 1px solid var(--st-border); border-radius: 7px; padding: 0.45rem 0; }
  .sk { font-size: 0.64rem; color: var(--st-fg-muted); }
  .sv { font-size: 1.05rem; font-weight: 700; font-family: ui-monospace, monospace; }
  .geomean { margin-top: 0.5rem; font-size: 0.78rem; color: var(--st-fg-muted); }
  .verdict { display: flex; align-items: center; gap: 0.5rem; }
  .vlabel { font-weight: 600; }
  .vcount { font-size: 0.72rem; color: var(--st-fg-muted); }
  .vmsg { margin: 0.4rem 0 0; color: var(--st-fg-muted); font-size: 0.8rem; line-height: 1.45; }
  .kv { display: grid; grid-template-columns: max-content 1fr; gap: 0.3rem 0.8rem; margin: 0; }
  .kv dt { color: var(--st-fg-muted); font-size: 0.76rem; }
  .kv dd { margin: 0; font-family: ui-monospace, monospace; font-size: 0.78rem; }
  .ellip { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 100%; }
  .memb { display: flex; flex-wrap: wrap; gap: 0.35rem; }
  .mchip { font-size: 0.74rem; background: var(--st-chip); border: 1px solid var(--st-border); border-radius: 6px; padding: 0.15rem 0.45rem; }
  .mchip em { color: var(--st-fg-muted); font-style: normal; }
  .ghlink { color: var(--st-accent-2); text-decoration: none; font-size: 0.82rem; font-weight: 600; }
  .ghlink:hover { text-decoration: underline; }
</style>

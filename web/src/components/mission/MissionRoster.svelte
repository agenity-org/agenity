<!--
  MissionRoster — team-grouped agent roster (the spec's "Rail / Sessions
  List" view). Real data from the /api/v1/sessions + /teams + /memberships
  layer (passed down as props from the root). Each agent renders in its
  identity color + role icon (agentIdentity). Clicking a row rebinds the
  focused terminal pane (onSelectAgent). Shows live/paused/exited status,
  idle, throughput, and scorecard geomean.

  Used in two places: the fixed left RAIL, and as a 'kanban' pane widget
  (compact=true) inside the center grid.
-->
<script>
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let {
    sessions = [], teams = [], memberships = [],
    selectedAgent = null, onSelectAgent, compact = false,
  } = $props();

  let filter = $state('');

  // Group agents by team using memberships, falling back to session.team.
  const grouped = $derived.by(() => {
    const byAgent = new Map();
    for (const m of memberships) {
      if (!byAgent.has(m.agent_name)) byAgent.set(m.agent_name, m.team_name);
    }
    const q = filter.trim().toLowerCase();
    const rows = sessions.filter(s => !q || s.name.toLowerCase().includes(q) || (s.role || '').toLowerCase().includes(q));
    const groups = new Map();
    for (const s of rows) {
      const team = byAgent.get(s.name) || s.team || '(unassigned)';
      if (!groups.has(team)) groups.set(team, []);
      groups.get(team).push(s);
    }
    // stable sort: team name, then agent name
    return [...groups.entries()]
      .sort((a, b) => a[0].localeCompare(b[0]))
      .map(([team, list]) => [team, list.sort((a, b) => a.name.localeCompare(b.name))]);
  });

  function statusOf(s) {
    if (s.exited) return 'dead';
    if (s.paused) return 'paused';
    if (s.live === false) return 'dead';
    return 'live';
  }
  function geomean(sc) {
    if (!sc) return null;
    const vals = ['G', 'V', 'F', 'E', 'D'].map(k => sc[k]).filter(v => typeof v === 'number' && v > 0);
    if (!vals.length) return null;
    const prod = vals.reduce((a, b) => a * b, 1);
    return Math.round(Math.pow(prod, 1 / vals.length) * 10) / 10;
  }
  function idleFmt(sec) {
    if (sec == null) return '';
    if (sec < 60) return sec + 's';
    if (sec < 3600) return Math.floor(sec / 60) + 'm';
    return Math.floor(sec / 3600) + 'h';
  }
  function bytesFmt(n) {
    if (!n) return '0';
    if (n < 1024) return n + 'B';
    if (n < 1048576) return (n / 1024).toFixed(0) + 'K';
    return (n / 1048576).toFixed(1) + 'M';
  }
</script>

<div class="roster" class:compact>
  <div class="r-head">
    <span class="r-title">ROSTER</span>
    <span class="r-count">{sessions.filter(s => !s.exited).length}/{sessions.length}</span>
  </div>
  <div class="r-filter">
    <input placeholder="filter agents…" bind:value={filter} />
  </div>
  <div class="r-list">
    {#each grouped as [team, list]}
      <div class="team-row">
        <span class="team-name">{team}</span>
        <span class="team-n">{list.length}</span>
      </div>
      {#each list as s (s.name)}
        {@const ident = agentIdentity(s)}
        {@const st = statusOf(s)}
        {@const gm = geomean(s.scorecard)}
        <button
          class="agent"
          class:sel={s.name === selectedAgent}
          onclick={() => onSelectAgent?.(s.name)}
          style="--id-color:{ident.color}"
          title={`${s.name} · ${s.role || 'agent'} · ${st}`}
        >
          <span class="status-dot {st}"></span>
          <span class="ic" style="color:{ident.color}">{ident.icon}</span>
          <span class="meta">
            <span class="nm">{s.name}</span>
            <span class="sub">
              <span class="role">{s.role || 'agent'}</span>
              {#if st === 'live'}<span class="bytes">{bytesFmt(s.bytes_5m)}/5m</span>{/if}
              {#if s.idle_seconds != null && st === 'live'}<span class="idle">⏱{idleFmt(s.idle_seconds)}</span>{/if}
            </span>
          </span>
          {#if gm != null}
            <span class="score" class:good={gm >= 7} class:mid={gm >= 4 && gm < 7} class:bad={gm < 4}>{gm}</span>
          {/if}
        </button>
      {/each}
    {/each}
    {#if !grouped.length}
      <p class="empty">No agents. Use “+ SPAWN” to launch one.</p>
    {/if}
  </div>
</div>

<style>
  .roster { display: flex; flex-direction: column; height: 100%; min-height: 0; background: var(--m-panel); color: var(--m-fg); }
  .roster.compact { border: 0; }
  .r-head {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.5rem 0.7rem; border-bottom: 1px solid var(--m-border);
  }
  .r-title { font-size: 0.66rem; letter-spacing: 0.16em; color: var(--m-fg-faint); font-weight: 700; }
  .r-count { font-family: ui-monospace, monospace; font-size: 0.72rem; color: var(--m-accent-2); }
  .r-filter { padding: 0.4rem 0.55rem; border-bottom: 1px solid var(--m-border); }
  .r-filter input {
    width: 100%; box-sizing: border-box; background: var(--m-bg); color: var(--m-fg);
    border: 1px solid var(--m-border-strong); border-radius: 5px; padding: 0.32rem 0.5rem;
    font: inherit; font-size: 0.76rem;
  }
  .r-filter input::placeholder { color: var(--m-fg-faint); }
  .r-list { flex: 1; overflow-y: auto; padding: 0.3rem; }
  .r-list::-webkit-scrollbar { width: 9px; }
  .r-list::-webkit-scrollbar-thumb { background: var(--m-scroll); border-radius: 5px; }
  .r-list::-webkit-scrollbar-thumb:hover { background: var(--m-scroll-hover); }

  .team-row {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.4rem 0.5rem 0.2rem; margin-top: 0.3rem;
  }
  .team-name { font-size: 0.64rem; letter-spacing: 0.1em; text-transform: uppercase; color: var(--m-fg-dim); font-weight: 700; }
  .team-n { font-size: 0.62rem; color: var(--m-fg-faint); font-family: ui-monospace, monospace; }

  .agent {
    display: flex; align-items: center; gap: 0.5rem; width: 100%;
    background: transparent; border: 1px solid transparent; border-radius: 6px;
    padding: 0.4rem 0.5rem; cursor: pointer; text-align: left; color: var(--m-fg);
    border-left: 3px solid transparent;
  }
  .agent:hover { background: var(--m-panel-3); }
  .agent.sel { background: var(--m-select); border-left-color: var(--id-color); }
  .status-dot { width: 8px; height: 8px; border-radius: 50%; flex: 0 0 auto; }
  .status-dot.live { background: var(--m-live); box-shadow: 0 0 6px -1px var(--m-live); }
  .status-dot.paused { background: var(--m-paused); }
  .status-dot.dead { background: var(--m-dead); }
  .ic { font-size: 0.95rem; flex: 0 0 auto; width: 1.1rem; text-align: center; }
  .meta { display: flex; flex-direction: column; gap: 1px; min-width: 0; flex: 1; }
  .nm { font-size: 0.8rem; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-family: ui-monospace, monospace; }
  .sub { display: flex; align-items: center; gap: 0.4rem; font-size: 0.64rem; color: var(--m-fg-faint); }
  .role { text-transform: uppercase; letter-spacing: 0.05em; }
  .bytes, .idle { font-family: ui-monospace, monospace; }
  .score {
    font-family: ui-monospace, monospace; font-size: 0.72rem; font-weight: 700;
    padding: 0.05rem 0.35rem; border-radius: 4px; flex: 0 0 auto;
    background: var(--m-panel-3); color: var(--m-fg-dim);
  }
  .score.good { color: var(--m-ok); }
  .score.mid { color: var(--m-warn); }
  .score.bad { color: var(--m-danger); }
  .empty { color: var(--m-fg-faint); font-size: 0.78rem; padding: 1rem 0.7rem; }
</style>

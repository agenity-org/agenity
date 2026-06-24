<script>
  let { sessions, selectedAgent, selectAgent } = $props();
  let sortKey = $state('score');
  let sortDir = $state(-1);
  function geomean(sc) {
    if (!sc) return null;
    const vs = [sc.G, sc.V, sc.F, sc.E, sc.D].filter(v => v != null && v > 0);
    if (!vs.length) return null;
    let p = 1; for (const v of vs) p *= v;
    return Math.pow(p, 1/vs.length);
  }
  function ageSec(ts) { return ts ? (Date.now() - new Date(ts).getTime()) / 1000 : 0; }
  function setSort(k) {
    if (sortKey === k) sortDir = -sortDir;
    else { sortKey = k; sortDir = -1; }
  }
  let rows = $derived.by(() => {
    const out = (sessions || []).map(s => ({
      name: s.name,
      role: s.role,
      team: s.team || '—',
      score: geomean(s.scorecard),
      status: s.exited ? 'exited' : s.paused ? 'paused' : s.bytes_5m > 0 ? 'live' : 'idle',
      age: ageSec(s.created_at),
      bytes_5m: s.bytes_5m || 0,
      verdict: s.last_verdict || '—',
    }));
    out.sort((a, b) => {
      let av = a[sortKey], bv = b[sortKey];
      if (av == null) av = -Infinity;
      if (bv == null) bv = -Infinity;
      if (typeof av === 'string') return av.localeCompare(bv) * sortDir;
      return (av - bv) * sortDir;
    });
    return out;
  });
  function fmtAge(s) {
    if (s < 60) return Math.floor(s) + 's';
    if (s < 3600) return Math.floor(s/60) + 'm';
    return Math.floor(s/3600) + 'h';
  }
</script>

<div class="board">
  <table>
    <thead>
      <tr>
        <th on:click={() => setSort('name')}>Name</th>
        <th on:click={() => setSort('role')}>Role</th>
        <th on:click={() => setSort('team')}>Team</th>
        <th on:click={() => setSort('score')} class="num">Score</th>
        <th on:click={() => setSort('status')}>Status</th>
        <th on:click={() => setSort('age')} class="num">Age</th>
        <th on:click={() => setSort('verdict')}>Verdict</th>
        <th on:click={() => setSort('bytes_5m')} class="num">Bytes/5m</th>
      </tr>
    </thead>
    <tbody>
      {#each rows as r (r.name)}
        <tr class:selected={selectedAgent === r.name} on:click={() => selectAgent(r.name)}>
          <td><span class="dot" class:shep={r.role === 'shepherd'}>{r.role === 'shepherd' ? '✻' : '●'}</span> {r.name}</td>
          <td>{r.role}</td>
          <td>{r.team}</td>
          <td class="num">{r.score != null ? r.score.toFixed(1) : '—'}</td>
          <td>{r.status}</td>
          <td class="num">{fmtAge(r.age)}</td>
          <td>{r.verdict}</td>
          <td class="num">{r.bytes_5m}</td>
        </tr>
      {/each}
      {#if !rows.length}
        <tr><td colspan="8" class="empty">No agents. Spawn one via "+ spawn".</td></tr>
      {/if}
    </tbody>
  </table>
</div>

<style>
  .board { overflow: auto; height: 100%; background: var(--bg); padding: 0.4rem; }
  table { width: 100%; border-collapse: collapse; font-size: 0.84rem; }
  th { background: var(--bg-elev); color: var(--fg-muted); text-align: left; padding: 0.35rem 0.5rem; cursor: pointer; user-select: none; font-size: 0.74rem; text-transform: uppercase; letter-spacing: 0.04em; border-bottom: 1px solid var(--border); position: sticky; top: 0; }
  th:hover { color: var(--accent); }
  th.num, td.num { text-align: right; font-variant-numeric: tabular-nums; }
  tr { cursor: pointer; }
  tr:hover { background: var(--bg-elev); }
  tr.selected { background: var(--select-bg); }
  td { padding: 0.32rem 0.5rem; border-bottom: 1px solid var(--border); }
  .dot { color: var(--accent-2); }
  .dot.shep { color: var(--accent); }
  .empty { text-align: center; color: var(--fg-faint); padding: 1rem; }
</style>

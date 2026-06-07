<!--
  StudioExplorer — the collapsible session tree (VS Code "Explorer" idiom).
  Groups live + exited agents by team, each row colored + icon'd via
  agentIdentity. Clicking a row re-binds the focused terminal pane to that
  agent (pane switching) and focuses it in the Inspector. Per-row status dot
  + idle/throughput micro-stats. Read-only over the polled lists.
-->
<script>
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let {
    sessions = [], teams = [], memberships = [],
    selectedAgent = null, onSelect = () => {}, onNewTab = () => {},
  } = $props();

  let query = $state('');
  let collapsed = $state({}); // teamName -> bool

  // Build team -> [session] groups. Sessions carry `team`; fall back to
  // memberships, then an "ungrouped" bucket. Stable sort by name.
  let groups = $derived.by(() => {
    const q = query.trim().toLowerCase();
    const byName = new Map((sessions || []).map(s => [s.name, s]));
    const teamOf = new Map();
    for (const m of (memberships || [])) {
      if (!teamOf.has(m.agent_name)) teamOf.set(m.agent_name, m.team_name);
    }
    const buckets = new Map();
    for (const s of (sessions || [])) {
      if (q && !s.name.toLowerCase().includes(q) && !String(s.role || '').toLowerCase().includes(q)) continue;
      const team = s.team || teamOf.get(s.name) || '(ungrouped)';
      if (!buckets.has(team)) buckets.set(team, []);
      buckets.get(team).push(s);
    }
    // ensure declared teams appear even when empty (so operator sees structure)
    for (const t of (teams || [])) {
      const nm = t.name || t;
      if (!buckets.has(nm) && !q) buckets.set(nm, []);
    }
    const out = [...buckets.entries()].map(([team, list]) => ({
      team,
      list: list.sort((a, b) => a.name.localeCompare(b.name)),
    }));
    out.sort((a, b) => a.team.localeCompare(b.team));
    return out;
  });

  function statusCls(s) {
    if (s.exited) return 'exited';
    if (s.paused) return 'paused';
    if (s.live) return 'live';
    return '';
  }
  function ago(sec) {
    if (sec == null) return '';
    if (sec < 60) return sec + 's';
    if (sec < 3600) return Math.floor(sec / 60) + 'm';
    return Math.floor(sec / 3600) + 'h';
  }
  function geomean(sc) {
    if (!sc) return null;
    const dims = ['G', 'V', 'F', 'E', 'D'].map(k => sc[k]).filter(v => typeof v === 'number' && v > 0);
    if (!dims.length) return null;
    return Math.pow(dims.reduce((a, b) => a * b, 1), 1 / dims.length);
  }
  function rowClick(e, s) {
    if (e.altKey || e.button === 1) onNewTab(s.name);
    else onSelect(s.name);
  }
</script>

<div class="explorer">
  <div class="ex-search">
    <input placeholder="Search agents…" bind:value={query} aria-label="Search agents" />
  </div>
  <div class="ex-tree">
    {#if !groups.length}
      <div class="ex-empty">No agents yet.<br />Use ⊕ Spawn to create one.</div>
    {/if}
    {#each groups as g (g.team)}
      <div class="team">
        <button class="team-head" onclick={() => (collapsed = { ...collapsed, [g.team]: !collapsed[g.team] })}>
          <span class="caret" class:open={!collapsed[g.team]}>▸</span>
          <span class="tname">{g.team}</span>
          <span class="tcount">{g.list.length}</span>
        </button>
        {#if !collapsed[g.team]}
          <div class="rows">
            {#each g.list as s (s.name)}
              {@const id = agentIdentity(s)}
              {@const gm = geomean(s.scorecard)}
              <button
                class="row"
                class:sel={s.name === selectedAgent}
                class:dim={s.exited}
                onclick={(e) => rowClick(e, s)}
                onauxclick={(e) => { if (e.button === 1) { e.preventDefault(); onNewTab(s.name); } }}
                title={`${s.name} — ${s.role || 'agent'} (Alt-click to open in a new tab)`}
              >
                <span class="dot {statusCls(s)}"></span>
                <span class="ricon" style="color:{id.color}">{id.icon}</span>
                <span class="rname">{s.name}</span>
                {#if gm != null}<span class="rscore" class:lo={gm < 3}>{gm.toFixed(1)}</span>{/if}
                {#if !s.exited && s.idle_seconds != null}<span class="ridle">{ago(s.idle_seconds)}</span>{/if}
              </button>
            {/each}
            {#if !g.list.length}<div class="row-empty">no members</div>{/if}
          </div>
        {/if}
      </div>
    {/each}
  </div>
</div>

<style>
  .explorer { height: 100%; display: flex; flex-direction: column; min-height: 0; }
  .ex-search { padding: 0.5rem 0.6rem; border-bottom: 1px solid var(--st-border); }
  .ex-search input { width: 100%; box-sizing: border-box; background: var(--st-chip);
    border: 1px solid var(--st-border); border-radius: 6px; color: var(--st-fg);
    padding: 0.35rem 0.55rem; font: inherit; font-size: 0.8rem; }
  .ex-search input:focus { outline: none; border-color: var(--st-accent); }
  .ex-tree { flex: 1; overflow-y: auto; padding: 0.3rem 0; }
  .ex-empty { color: var(--st-fg-faint); text-align: center; padding: 1.5rem 1rem; font-size: 0.82rem; line-height: 1.6; }
  .team { margin-bottom: 0.1rem; }
  .team-head { display: flex; align-items: center; gap: 0.4rem; width: 100%; background: transparent;
    border: 0; color: var(--st-fg-muted); cursor: pointer; padding: 0.3rem 0.6rem; font: inherit;
    font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.05em; }
  .team-head:hover { color: var(--st-fg); }
  .caret { display: inline-block; transition: transform 0.12s; font-size: 0.7rem; }
  .caret.open { transform: rotate(90deg); }
  .tname { flex: 1; text-align: left; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tcount { background: var(--st-chip); border-radius: 999px; padding: 0 0.4rem; font-size: 0.68rem; }
  .rows { display: flex; flex-direction: column; }
  .row { display: flex; align-items: center; gap: 0.45rem; width: 100%; background: transparent;
    border: 0; border-left: 2px solid transparent; color: var(--st-fg); cursor: pointer;
    padding: 0.32rem 0.6rem 0.32rem 1.2rem; font: inherit; font-size: 0.82rem; text-align: left; }
  .row:hover { background: var(--st-hover); }
  .row.sel { background: var(--st-sel-bg); border-left-color: var(--st-accent); }
  .row.dim { opacity: 0.5; }
  .dot { width: 0.55rem; height: 0.55rem; border-radius: 50%; flex-shrink: 0; background: var(--st-fg-faint); }
  .dot.live { background: var(--st-ok); box-shadow: 0 0 0 3px color-mix(in srgb, var(--st-ok) 22%, transparent); }
  .dot.paused { background: var(--st-accent); }
  .dot.exited { background: var(--st-danger); }
  .ricon { flex-shrink: 0; width: 1.1rem; text-align: center; }
  .rname { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .rscore { font-family: ui-monospace, monospace; font-size: 0.72rem; color: var(--st-ok); }
  .rscore.lo { color: var(--st-danger); }
  .ridle { font-size: 0.7rem; color: var(--st-fg-faint); font-family: ui-monospace, monospace; }
  .row-empty { color: var(--st-fg-faint); font-size: 0.74rem; padding: 0.25rem 0.6rem 0.25rem 1.2rem; }
</style>

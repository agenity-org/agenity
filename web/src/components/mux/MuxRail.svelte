<!--
  MuxRail — the left session rail for the mux dashboard. Team-grouped
  agent roster from the live /api/v1/sessions + /memberships wire, each
  agent in its identity color + role icon. Click = focus that agent in
  the active terminal pane (view change). Alt/middle-click = open it in a
  NEW pane (split). Tmux "window list" feel: compact monospace rows.
-->
<script>
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let {
    sessions = [],
    teams = [],
    memberships = [],
    selectedAgent = null,
    selectAgent = () => {},
    openInNewPane = () => {},
    spawn = () => {},
  } = $props();

  let filter = $state('');

  // Build team → [sessions] groups; agents with no membership land in "(unassigned)".
  let groups = $derived.by(() => {
    const teamOf = new Map();
    for (const m of memberships || []) {
      if (!teamOf.has(m.agent_name)) teamOf.set(m.agent_name, m.team_name);
    }
    const buckets = new Map();
    const q = filter.trim().toLowerCase();
    for (const s of sessions || []) {
      if (q && !(s.name.toLowerCase().includes(q) || (s.role || '').toLowerCase().includes(q))) continue;
      const t = teamOf.get(s.name) || '(unassigned)';
      if (!buckets.has(t)) buckets.set(t, []);
      buckets.get(t).push(s);
    }
    const order = [...buckets.keys()].sort((a, b) => {
      if (a === '(unassigned)') return 1;
      if (b === '(unassigned)') return -1;
      return a.localeCompare(b);
    });
    return order.map((t) => ({
      team: t,
      members: buckets.get(t).sort((a, b) =>
        (a.created_at || '').localeCompare(b.created_at || '') || a.name.localeCompare(b.name)
      ),
    }));
  });

  let liveCount = $derived((sessions || []).filter((s) => !s.exited && s.live !== false).length);

  function rowClick(e, name) {
    if (e.altKey || e.metaKey || e.button === 1) { openInNewPane(name); return; }
    selectAgent(name);
  }
  function relAge(at) {
    if (!at) return '';
    const s = Math.floor((Date.now() - new Date(at).getTime()) / 1000);
    if (s < 60) return `${s}s`;
    if (s < 3600) return `${Math.floor(s / 60)}m`;
    if (s < 86400) return `${Math.floor(s / 3600)}h`;
    return `${Math.floor(s / 86400)}d`;
  }
</script>

<aside class="rail">
  <header class="rail-hd">
    <span class="rh-title">windows</span>
    <span class="rh-count">{liveCount}/{(sessions || []).length}</span>
  </header>
  <div class="rail-filter">
    <input type="text" placeholder="filter agents…" bind:value={filter} spellcheck="false" autocomplete="off" />
  </div>
  <div class="rail-body">
    {#each groups as g (g.team)}
      <div class="grp">
        <div class="grp-h"><span class="grp-bullet"></span>{g.team}<span class="grp-n">{g.members.length}</span></div>
        {#each g.members as s (s.name)}
          {@const id = agentIdentity(s)}
          {@const sel = s.name === selectedAgent}
          <button
            class="row {sel ? 'sel' : ''} {s.exited ? 'exited' : ''}"
            onmousedown={(e) => rowClick(e, s.name)}
            title="click: focus in active pane · Alt/middle-click: open in new pane"
          >
            <span class="row-rail" style="background:{id.color}; opacity:{sel ? 1 : 0.35}"></span>
            <span class="row-ic" style="color:{id.color}">{id.icon}</span>
            <span class="row-nm">{s.name}</span>
            <span class="row-meta">
              {#if s.paused}<span class="pz" title="paused">⏸</span>{/if}
              {#if s.intervention_count}<span class="iv" title="{s.intervention_count} interventions">!{s.intervention_count}</span>{/if}
              <span class="row-age">{relAge(s.created_at)}</span>
              <span class="row-dot {!s.exited && s.live !== false ? 'live' : 'dead'}"></span>
            </span>
          </button>
        {/each}
      </div>
    {/each}
    {#if !groups.length}
      <div class="rail-empty">
        {#if filter.trim()}No agents match “{filter}”.{:else}No agents running.{/if}
      </div>
    {/if}
  </div>
  <footer class="rail-ft">
    <button class="spawn" onclick={() => spawn()}>+ spawn agent</button>
  </footer>
</aside>

<style>
  .rail { display: flex; flex-direction: column; height: 100%; background: var(--mux-bar); border-right: 1px solid var(--mux-border); font-family: var(--mux-mono); }
  .rail-hd { display: flex; align-items: center; padding: 0.55rem 0.7rem 0.4rem; }
  .rh-title { font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.1em; color: var(--mux-fg-muted); font-weight: 700; }
  .rh-count { margin-left: auto; font-size: 0.7rem; color: var(--mux-ok); }
  .rail-filter { padding: 0 0.55rem 0.5rem; }
  .rail-filter input { width: 100%; box-sizing: border-box; background: var(--mux-bg); border: 1px solid var(--mux-border); border-radius: 5px; color: var(--mux-fg); font-family: var(--mux-mono); font-size: 0.76rem; padding: 0.3rem 0.5rem; }
  .rail-filter input:focus { outline: none; border-color: var(--mux-accent); }
  .rail-body { flex: 1; overflow-y: auto; padding: 0 0.35rem; }

  .grp { margin-bottom: 0.5rem; }
  .grp-h { display: flex; align-items: center; gap: 0.4rem; padding: 0.3rem 0.4rem 0.2rem; font-size: 0.66rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--mux-fg-muted); font-weight: 700; }
  .grp-bullet { width: 5px; height: 5px; border-radius: 50%; background: var(--mux-accent); opacity: 0.7; }
  .grp-n { margin-left: auto; color: var(--mux-fg-faint); font-weight: 600; }

  .row {
    display: flex; align-items: center; gap: 0.45rem; width: 100%;
    background: transparent; border: none; border-radius: 5px;
    padding: 0.3rem 0.4rem 0.3rem 0.15rem; cursor: pointer; position: relative;
    color: var(--mux-fg); font-family: var(--mux-mono); font-size: 0.78rem; text-align: left;
  }
  .row:hover { background: var(--mux-hover); }
  .row.sel { background: var(--mux-sel); }
  .row.exited { opacity: 0.5; }
  .row-rail { width: 3px; align-self: stretch; border-radius: 2px; flex: 0 0 auto; }
  .row-ic { width: 1.05rem; text-align: center; flex: 0 0 auto; }
  .row-nm { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
  .row.sel .row-nm { color: var(--mux-fg); }
  .row-meta { display: inline-flex; align-items: center; gap: 0.3rem; flex: 0 0 auto; }
  .pz { color: var(--mux-warn); font-size: 0.66rem; }
  .iv { color: var(--mux-danger); font-size: 0.62rem; font-weight: 700; }
  .row-age { color: var(--mux-fg-faint); font-size: 0.64rem; }
  .row-dot { width: 6px; height: 6px; border-radius: 50%; }
  .row-dot.live { background: var(--mux-ok); }
  .row-dot.dead { background: var(--mux-fg-faint); }
  .rail-empty { color: var(--mux-fg-muted); font-size: 0.76rem; padding: 1rem 0.6rem; line-height: 1.5; }

  .rail-ft { padding: 0.5rem 0.55rem; border-top: 1px solid var(--mux-border); }
  .spawn { width: 100%; background: var(--mux-accent-soft); color: var(--mux-accent); border: 1px solid var(--mux-accent); border-radius: 6px; padding: 0.4rem; cursor: pointer; font-family: var(--mux-mono); font-size: 0.78rem; font-weight: 600; }
  .spawn:hover { background: var(--mux-accent); color: var(--mux-on-accent); }
</style>

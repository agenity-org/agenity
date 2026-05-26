<!--
  WidgetSessionList — left-pane-style tree of teams → memberships → agents.
  Click an agent to select. Group by team with shepherd icon distinct.
-->
<script>
  let { sessions, teams, memberships, selectedAgent, selectAgent } = $props();
  const API = '/api-v08/v1';
  let ctxMenu = $state(null); // { x, y, agent, currentTeam, currentRole }

  function openTeamSettings(t, mems) {
    window.dispatchEvent(new CustomEvent('chepherd-open-team-settings', { detail: { team: t, members: mems } }));
  }

  function openContext(ev, agent, currentTeam, currentRole) {
    ev.preventDefault();
    ctxMenu = { x: ev.clientX, y: ev.clientY, agent, currentTeam, currentRole };
  }
  function closeContext() { ctxMenu = null; }
  async function moveAgent(toTeam) {
    if (!ctxMenu) return;
    const { agent, currentTeam, currentRole } = ctxMenu;
    if (toTeam === currentTeam) { closeContext(); return; }
    if (currentTeam && currentTeam !== 'Unaffiliated') {
      await fetch(`${API}/memberships?agent=${encodeURIComponent(agent)}&team=${encodeURIComponent(currentTeam)}`, { method: 'DELETE' });
    }
    await fetch(`${API}/memberships`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ agent, team: toTeam, role: currentRole || 'worker' }),
    });
    closeContext();
  }
  async function changeRole(toRole) {
    if (!ctxMenu) return;
    const { agent, currentTeam } = ctxMenu;
    await fetch(`${API}/memberships`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ agent, team: currentTeam, role: toRole }),
    });
    closeContext();
  }

  // Build tree: per-team sub-list of memberships, plus an Unaffiliated bucket
  let tree = $derived.by(() => {
    const byTeam = new Map();
    for (const m of (memberships || [])) {
      const list = byTeam.get(m.team_name) || [];
      list.push(m);
      byTeam.set(m.team_name, list);
    }
    const out = [];
    for (const t of (teams || [])) {
      out.push({ team: t, members: (byTeam.get(t.name) || []) });
    }
    // Unaffiliated: agents with no membership
    const memberAgents = new Set((memberships || []).map(m => m.agent_name));
    const unaffiliated = (sessions || []).filter(s => !memberAgents.has(s.name));
    if (unaffiliated.length) out.push({ team: { name: 'Unaffiliated', topology: 'mesh' }, members: unaffiliated.map(s => ({ agent_name: s.name, role: s.role })) });
    return out;
  });

  function agentByName(name) {
    return (sessions || []).find(s => s.name === name);
  }
  function geomean(sc) {
    if (!sc) return null;
    const vs = [sc.G, sc.V, sc.F, sc.E, sc.D].filter(v => v != null && v > 0);
    if (!vs.length) return null;
    let p = 1;
    for (const v of vs) p *= v;
    return Math.pow(p, 1 / vs.length);
  }
  function ageString(createdAt) {
    if (!createdAt) return '—';
    const s = Math.floor((Date.now() - new Date(createdAt).getTime()) / 1000);
    if (s < 60) return `${s}s`;
    if (s < 3600) return `${Math.floor(s/60)}m`;
    return `${Math.floor(s/3600)}h`;
  }
</script>

<div class="list">
  {#each tree as group (group.team.name)}
    <section class="team">
      <h3 class="team-head" on:click={() => openTeamSettings(group.team, group.members)} title="open team settings">
        <span class="team-name">{group.team.name}</span>
        <span class="team-meta">· {group.team.topology} · {group.members.length}</span>
      </h3>
      <ul>
        {#each group.members as m (m.agent_name)}
          {@const agent = agentByName(m.agent_name)}
          {#if agent}
            {@const score = geomean(agent.scorecard)}
            <li class:selected={selectedAgent === agent.name}
                on:click={() => selectAgent(agent.name)}
                on:contextmenu={(ev) => openContext(ev, agent.name, group.team.name, m.role)}
                title="right-click for membership actions">
              <span class="icon" class:shepherd={m.role === 'shepherd'}>{m.role === 'shepherd' ? '✻' : '●'}</span>
              <span class="name">{agent.name}</span>
              {#if score != null}<span class="score">{score.toFixed(1)}</span>{/if}
              <span class="age">{ageString(agent.created_at)}</span>
            </li>
          {/if}
        {/each}
      </ul>
    </section>
  {/each}
  {#if !tree.length}
    <p class="empty">No agents yet. Use "+ spawn" or "📦 templates".</p>
  {/if}
</div>

{#if ctxMenu}
  <div class="ctx-backdrop" on:click={closeContext}>
    <div class="ctx-menu" style="left: {ctxMenu.x}px; top: {ctxMenu.y}px;" on:click|stopPropagation>
      <div class="ctx-head">{ctxMenu.agent} <small>· {ctxMenu.currentTeam} / {ctxMenu.currentRole}</small></div>
      <button on:click={() => { window.dispatchEvent(new CustomEvent('chepherd-open-agent-settings', { detail: { agentName: ctxMenu.agent } })); closeContext(); }}>⚙ Settings…</button>
      <button on:click={async () => { await fetch(`${API}/sessions/${ctxMenu.agent}/pause`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ paused: true }) }); closeContext(); }}>⏸ Pause</button>
      <button on:click={async () => { await fetch(`${API}/sessions/${ctxMenu.agent}/pause`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ paused: false }) }); closeContext(); }}>▶ Resume</button>
      <button on:click={async () => { await fetch(`${API}/sessions/${ctxMenu.agent}/restart`, { method: 'POST' }); closeContext(); }}>↻ Restart</button>
      <button class="danger" on:click={async () => { await fetch(`${API}/sessions/${ctxMenu.agent}`, { method: 'DELETE' }); closeContext(); }}>■ Stop</button>
    </div>
  </div>
{/if}

<style>
  .list { padding: 0.5rem 0.5rem; height: 100%; overflow-y: auto; background: var(--bg); }
  .team { margin-bottom: 0.8rem; }
  .team h3 { font-size: 0.82rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.05em; margin: 0 0 0.3rem 0.2rem; font-weight: 600; }
  .team-head { cursor: pointer; }
  .team-head:hover { color: var(--accent); }
  .team-meta { color: var(--fg-faint); font-weight: normal; text-transform: none; letter-spacing: 0; }
  .team ul { list-style: none; padding: 0; margin: 0; }
  .team li { display: flex; align-items: center; gap: 0.45rem; padding: 0.4rem 0.5rem; border-radius: 4px; cursor: pointer; font-size: 0.85rem; border: 1px solid transparent; }
  .team li:hover { background: var(--bg-elev); border-color: var(--border); }
  .team li.selected { background: var(--select-bg); border-color: var(--select-border); }
  .team li .icon { color: var(--accent-2); }
  .team li .icon.shepherd { color: var(--accent); }
  .team li .name { flex: 1; font-weight: 500; }
  .team li .score { background: var(--accent); color: #000; padding: 0.05rem 0.4rem; border-radius: 8px; font-size: 0.72rem; font-weight: 600; }
  .team li .age { color: var(--fg-faint); font-size: 0.74rem; }
  .empty { color: var(--fg-faint); font-size: 0.82rem; padding: 1rem; text-align: center; }
  .ctx-backdrop { position: fixed; inset: 0; z-index: 999; }
  .ctx-menu { position: fixed; background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.35rem; min-width: 200px; box-shadow: 0 6px 20px rgba(0,0,0,0.5); display: flex; flex-direction: column; gap: 0.05rem; }
  .ctx-head { padding: 0.3rem 0.5rem; color: var(--accent); font-weight: 600; font-size: 0.82rem; border-bottom: 1px solid var(--border); margin-bottom: 0.25rem; }
  .ctx-head small { color: var(--fg-muted); font-weight: normal; }
  .ctx-section { padding: 0.25rem 0.5rem; color: var(--fg-muted); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.06em; }
  .ctx-menu button { padding: 0.35rem 0.6rem; background: transparent; color: var(--fg); border: none; border-radius: 4px; cursor: pointer; text-align: left; font-size: 0.82rem; }
  .ctx-menu button:hover { background: var(--bg); color: var(--accent); }
  .ctx-menu button small { color: var(--fg-muted); }
</style>

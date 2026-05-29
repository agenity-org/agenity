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

  // #247 — deterministic stable sort: teams ASC by name, members ASC
  // by agent_name within each team, unaffiliated bucket ASC by name
  // last. Without this the rows reshuffle on every poll-refresh as
  // /api/v1/teams + /api/v1/memberships return in insertion order
  // (no implicit sort guarantee). Operator gets visual jitter every
  // 2.5s + can't reliably Shift+click ranges that have just rearranged.
  let tree = $derived.by(() => {
    const byTeam = new Map();
    for (const m of (memberships || [])) {
      const list = byTeam.get(m.team_name) || [];
      list.push(m);
      byTeam.set(m.team_name, list);
    }
    const out = [];
    const sortedTeams = (teams || []).slice().sort((a, b) =>
      a.name.localeCompare(b.name)
    );
    for (const t of sortedTeams) {
      const members = (byTeam.get(t.name) || []).slice().sort((a, b) =>
        a.agent_name.localeCompare(b.agent_name)
      );
      out.push({ team: t, members });
    }
    // Unaffiliated: agents with no membership, sorted ASC by name
    const memberAgents = new Set((memberships || []).map(m => m.agent_name));
    const unaffiliated = (sessions || [])
      .filter(s => !memberAgents.has(s.name))
      .sort((a, b) => a.name.localeCompare(b.name));
    if (unaffiliated.length) {
      out.push({
        team: { name: 'Unaffiliated', topology: 'mesh' },
        members: unaffiliated.map(s => ({ agent_name: s.name, role: s.role })),
      });
    }
    return out;
  });

  // #247 — multi-select substrate. `selectedAgent` (single string) is
  // preserved as the WORKSPACE-level focus prop (back-compat for every
  // downstream widget that reads it: AgentDetails / Terminal /
  // ShepherdAssessment / etc.). `selectedAgents` is a Set<string> that
  // tracks the multi-select state local to this list. When the
  // selection set has exactly 1 entry, we propagate it as the
  // workspace selectedAgent (single-select compat); when it has 0 or
  // >1, downstream still reads the LAST single selectedAgent so the
  // terminal pane etc. doesn't suddenly blank out. Operators who want
  // truly multi-pane behavior pick from the right-click menu later.
  let selectedAgents = $state(new Set());
  let lastClickedAgent = $state('');

  // Hydrate selectedAgents from the single-select prop on first
  // render so an operator who navigates in via "selected this agent"
  // sees that selection already lit.
  $effect(() => {
    if (selectedAgent && selectedAgents.size === 0) {
      selectedAgents = new Set([selectedAgent]);
      lastClickedAgent = selectedAgent;
    }
  });

  function handleAgentClick(ev, agentName) {
    if (ev.shiftKey && lastClickedAgent) {
      // Range select from lastClickedAgent to agentName in CURRENT sort order.
      // Stable sort (above) is what makes this deterministic.
      const flat = tree.flatMap(g => g.members.map(m => m.agent_name));
      const lastIdx = flat.indexOf(lastClickedAgent);
      const curIdx = flat.indexOf(agentName);
      if (lastIdx >= 0 && curIdx >= 0) {
        const [from, to] = lastIdx < curIdx ? [lastIdx, curIdx] : [curIdx, lastIdx];
        const rangeAgents = flat.slice(from, to + 1);
        selectedAgents = new Set([...selectedAgents, ...rangeAgents]);
      } else {
        selectedAgents = new Set([agentName]);
      }
    } else if (ev.ctrlKey || ev.metaKey) {
      // Ctrl/Cmd+click toggles individual rows.
      const next = new Set(selectedAgents);
      if (next.has(agentName)) next.delete(agentName); else next.add(agentName);
      selectedAgents = next;
    } else {
      // Plain click — replace selection.
      selectedAgents = new Set([agentName]);
    }
    lastClickedAgent = agentName;
    // Back-compat: propagate the LAST clicked agent as the single
    // workspace selection so downstream widgets keep working.
    selectAgent(agentName);
  }

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
            <li class:selected={selectedAgents.has(agent.name)}
                on:click={(ev) => handleAgentClick(ev, agent.name)}
                on:contextmenu={(ev) => openContext(ev, agent.name, group.team.name, m.role)}
                title="Click to select. Ctrl+click to toggle. Shift+click for range. Right-click for membership actions.">
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
    <p class="empty">No agents running — hit <strong>+ new</strong> to spawn one.</p>
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

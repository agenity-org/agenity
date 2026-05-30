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
    // Unaffiliated: agents with no membership, sorted ASC by name.
    // #421 P0 — split into "live unaffiliated" (running container, no
    // team membership) and "orphan" (persisted-but-not-running session
    // rows from prior bounces). Orphans look like real teammates if
    // mixed with live unaffiliated agents — operator quote: "when I
    // start agent, it also show some unaffiliated shadows as well I
    // don't know that they are". Render orphans in their own collapsed
    // section with a banner explaining what they are + pointing at
    // the existing Clean-up-orphans button.
    const memberAgents = new Set((memberships || []).map(m => m.agent_name));
    const unaffiliatedAll = (sessions || [])
      .filter(s => !memberAgents.has(s.name))
      .sort((a, b) => a.name.localeCompare(b.name));
    const liveUnaffiliated = unaffiliatedAll.filter(s => s && s.live !== false);
    const orphanUnaffiliated = unaffiliatedAll.filter(s => s && s.live === false);
    if (liveUnaffiliated.length) {
      out.push({
        team: { name: 'Unaffiliated', topology: 'mesh' },
        members: liveUnaffiliated.map(s => ({ agent_name: s.name, role: s.role })),
      });
    }
    if (orphanUnaffiliated.length) {
      out.push({
        team: { name: 'Orphans', topology: 'cleanup', isOrphanGroup: true },
        members: orphanUnaffiliated.map(s => ({ agent_name: s.name, role: s.role })),
      });
    }
    return out;
  });

  // #421 P0 — collapsed-by-default state for the Orphans section.
  // Operator can expand by clicking the team header; state is per-
  // session-tab (not persisted across reloads) so a fresh page load
  // keeps clutter hidden.
  let orphansExpanded = $state(false);

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

  // ── #253 BULK ACTIONS ────────────────────────────────────────────
  // Operator can multi-select via #247's Ctrl/Shift+click substrate;
  // when 2+ rows are selected we surface a bulk-action toolbar in the
  // pane header. Five actions: Pause / Resume / Restart / Stop /
  // Message. Restart + Stop require modal confirmation (destructive).
  // Dispatch uses Promise.allSettled so a single 404 doesn't abort
  // the whole batch; per-agent status rendered inline below the
  // toolbar. Successful agents drop out of selection; failed agents
  // stay selected for re-run.
  let bulkMessage = $state('');
  let confirmingAction = $state(null);   // 'stop' | 'restart' | null
  // launchResults shape: { [agentName]: 'pending' | 'ok' | { error } }
  let bulkResults = $state(null);
  let bulkInProgress = $state(false);

  function clearSelection() {
    selectedAgents = new Set();
    lastClickedAgent = '';
  }

  async function callOne(action, agent) {
    const url = `${API}/sessions/${encodeURIComponent(agent)}` +
      (action === 'stop' ? '' :
       action === 'pause' || action === 'resume' ? '/pause' :
       action === 'restart' ? '/restart' :
       action === 'message' ? '/messages' : '');
    const method = action === 'stop' ? 'DELETE' : 'POST';
    const init = { method };
    if (action === 'pause' || action === 'resume') {
      init.headers = { 'Content-Type': 'application/json' };
      init.body = JSON.stringify({ paused: action === 'pause' });
    } else if (action === 'message') {
      init.headers = { 'Content-Type': 'application/json' };
      init.body = JSON.stringify({ body: bulkMessage });
    }
    const r = await fetch(url, init);
    if (!r.ok) {
      const t = (await r.text()).trim();
      throw new Error(t || `HTTP ${r.status}`);
    }
    return true;
  }

  async function runBulkAction(action) {
    if (selectedAgents.size === 0) return;
    if ((action === 'stop' || action === 'restart') && confirmingAction !== action) {
      confirmingAction = action;
      return;
    }
    confirmingAction = null;
    bulkInProgress = true;
    const targets = [...selectedAgents];
    bulkResults = Object.fromEntries(targets.map(t => [t, 'pending']));
    const settled = await Promise.allSettled(
      targets.map(async (agent) => {
        try { await callOne(action, agent); return { agent, ok: true }; }
        catch (e) { return { agent, ok: false, error: String(e.message || e) }; }
      })
    );
    const nextResults = { ...bulkResults };
    const survived = new Set();
    for (const s of settled) {
      const { agent, ok, error } = s.status === 'fulfilled' ? s.value : { agent: '?', ok: false, error: String(s.reason) };
      nextResults[agent] = ok ? 'ok' : { error };
      if (!ok) survived.add(agent);
    }
    bulkResults = nextResults;
    bulkInProgress = false;
    // Successful agents drop out of selection; failed stay selected
    // so the operator can retry without re-selecting.
    selectedAgents = survived;
    if (action === 'message') bulkMessage = '';
    // Auto-clear the result strip after a short hold so the next
    // selection starts visually clean. Failed rows are still surfaced
    // by the persistent 'failed' class on the list item if needed.
    setTimeout(() => {
      if (!bulkInProgress) bulkResults = null;
    }, 4500);
  }

  function cancelConfirm() { confirmingAction = null; }

  function actionLabel(a) {
    return { stop: 'Stop', restart: 'Restart', pause: 'Pause', resume: 'Resume', message: 'Send' }[a] || a;
  }

  // #393 P0 — per-row × button + bulk "Clean up orphans" header
  // button. The right-click context menu already has "■ Stop" but
  // the tooltip says "membership actions" + there's no visible
  // affordance, so operators don't discover the delete path. The ×
  // is the most-discoverable affordance.
  let deletingNames = $state(new Set());
  let cleanupResult = $state(null); // { deleted: N, kept: M } | { error: '...' }
  let cleaningUp = $state(false);

  async function deleteOne(name, ev) {
    if (ev) ev.stopPropagation();
    if (deletingNames.has(name)) return;
    deletingNames = new Set([...deletingNames, name]);
    try {
      const r = await fetch(`${API}/sessions/${encodeURIComponent(name)}`, { method: 'DELETE' });
      if (!r.ok && r.status !== 404) {
        const t = (await r.text()).trim();
        throw new Error(t || `HTTP ${r.status}`);
      }
      // Row drops on next poll-refresh (parent re-fetches sessions
      // every ~2.5s). Until then, deletingNames keeps it visually
      // dimmed so the operator sees feedback.
      if (selectedAgents.has(name)) {
        const next = new Set(selectedAgents);
        next.delete(name);
        selectedAgents = next;
      }
    } catch (e) {
      // Surface the error inline; clear from deletingNames so the
      // operator can retry.
      const next = new Set(deletingNames);
      next.delete(name);
      deletingNames = next;
      alert(`Delete ${name} failed: ${e.message || e}`);
    }
  }

  async function cleanupOrphans() {
    if (cleaningUp) return;
    if (!confirm('Delete all sessions that have no running container? Live sessions are preserved.')) return;
    cleaningUp = true;
    cleanupResult = null;
    try {
      const r = await fetch(`${API}/sessions/_cleanup-orphans`, { method: 'POST' });
      if (!r.ok) {
        const t = (await r.text()).trim();
        throw new Error(t || `HTTP ${r.status}`);
      }
      cleanupResult = await r.json();
    } catch (e) {
      cleanupResult = { error: String(e.message || e) };
    } finally {
      cleaningUp = false;
      // Clear feedback after a hold so the next session-list state is
      // visually clean.
      setTimeout(() => { cleanupResult = null; }, 5000);
    }
  }

  // Orphan count derived from current sessions list — a session is
  // an orphan when its server-side `live` field is false (#357 P0
  // adds this field; persisted-but-not-running rows surface here).
  // Used to gate the "Clean up orphans" button so the operator only
  // sees it when there's actually orphans to clean.
  let orphanCount = $derived.by(() => {
    return (sessions || []).filter(s => s && s.live === false).length;
  });
</script>

<!--
  #253 — bulk-action toolbar appears when 2+ rows selected. Hidden
  for N=1 because the existing right-click context menu already covers
  per-agent actions.
-->
{#if selectedAgents.size > 1}
  <div class="bulk-toolbar">
    <div class="bulk-head">
      <span class="bulk-count">{selectedAgents.size} selected</span>
      <button class="bulk-clear" type="button" onclick={clearSelection} title="clear selection">×</button>
    </div>
    <div class="bulk-actions">
      <button type="button" class="ba pause" onclick={() => runBulkAction('pause')} disabled={bulkInProgress} title="Pause all selected agents">⏸ Pause</button>
      <button type="button" class="ba resume" onclick={() => runBulkAction('resume')} disabled={bulkInProgress} title="Resume all selected agents">▶ Resume</button>
      <button type="button" class="ba restart" onclick={() => runBulkAction('restart')} disabled={bulkInProgress} title="Restart all selected agents (conversation preserved)">↻ Restart</button>
      <button type="button" class="ba stop danger" onclick={() => runBulkAction('stop')} disabled={bulkInProgress} title="Stop all selected agents (destructive)">■ Stop</button>
    </div>
    <div class="bulk-msg">
      <input type="text" bind:value={bulkMessage} placeholder="Message all {selectedAgents.size}…" disabled={bulkInProgress}
             onkeydown={(e) => { if (e.key === 'Enter' && bulkMessage.trim()) runBulkAction('message'); }} />
      <button type="button" class="ba send" disabled={bulkInProgress || !bulkMessage.trim()} onclick={() => runBulkAction('message')}>Send →</button>
    </div>
    {#if bulkResults}
      <ul class="bulk-results">
        {#each Object.entries(bulkResults) as [agent, status]}
          <li class:ok={status === 'ok'} class:pending={status === 'pending'} class:failed={typeof status === 'object'}>
            {#if status === 'pending'}
              <span class="br-spin">⟳</span> <span class="br-name">{agent}</span> <span class="br-text">spawning…</span>
            {:else if status === 'ok'}
              <span class="br-ok">✓</span> <span class="br-name">{agent}</span>
            {:else}
              <span class="br-fail">✗</span> <span class="br-name">{agent}</span> <span class="br-text" title={status.error}>{status.error}</span>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </div>
{/if}

{#if confirmingAction}
  <div class="confirm-backdrop" onclick={cancelConfirm}>
    <div class="confirm-modal" onclick={(e) => e.stopPropagation()}>
      <h4>
        {#if confirmingAction === 'stop'}
          Stop {selectedAgents.size} agents?
        {:else}
          Restart {selectedAgents.size} agents?
        {/if}
      </h4>
      <p>
        {#if confirmingAction === 'stop'}
          This will kill the running session containers. Their work-tree state is preserved on disk but in-flight conversation context is lost.
        {:else}
          Sessions will restart in-place. Conversation context is preserved.
        {/if}
      </p>
      <div class="confirm-actions">
        <button type="button" class="cancel" onclick={cancelConfirm}>Cancel</button>
        <button type="button" class="confirm danger" onclick={() => runBulkAction(confirmingAction)}>
          {actionLabel(confirmingAction)} {selectedAgents.size}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- #393 P1 — header bar with orphan-count + "Clean up orphans"
     button. Only visible when there's actually orphans to clean
     (orphanCount > 0). Live feedback after click via cleanupResult. -->
{#if orphanCount > 0 || cleanupResult}
  <div class="list-header">
    {#if orphanCount > 0}
      <span class="orphan-count" title="Sessions persisted but no running container">
        {orphanCount} orphan{orphanCount === 1 ? '' : 's'}
      </span>
      <button type="button" class="cleanup-btn" disabled={cleaningUp}
              onclick={cleanupOrphans}
              title="Delete all sessions with no running container (live sessions preserved)">
        {cleaningUp ? '⟳ Cleaning…' : '✕ Clean up orphans'}
      </button>
    {/if}
    {#if cleanupResult}
      <span class="cleanup-result" class:err={cleanupResult.error}>
        {#if cleanupResult.error}
          ✗ {cleanupResult.error}
        {:else}
          ✓ Cleaned {cleanupResult.deleted} · Kept {cleanupResult.kept} live
        {/if}
      </span>
    {/if}
  </div>
{/if}

<div class="list">
  {#each tree as group (group.team.name)}
    <section class="team" class:orphan-group={group.team.isOrphanGroup}>
      {#if group.team.isOrphanGroup}
        <!-- #421 P0 — orphan group: collapsed by default, banner
             explains the rows, click team-head to expand. -->
        <h3 class="team-head orphan-head"
            onclick={() => orphansExpanded = !orphansExpanded}
            title="Orphan session rows from prior chepherd bounces — click to expand">
          <span class="team-name">{orphansExpanded ? '▾' : '▸'} {group.team.name}</span>
          <span class="team-meta">· {group.members.length} {group.members.length === 1 ? 'row' : 'rows'} from prior bounces</span>
        </h3>
        {#if orphansExpanded}
          <p class="orphan-banner">
            These are session rows from prior chepherd bounces — their containers are gone but the records remain. Click <code>×</code> on a row to delete one, or <code>✕ Clean up orphans</code> at the top of this pane to delete all at once.
          </p>
        {/if}
      {:else}
        <h3 class="team-head" onclick={() => openTeamSettings(group.team, group.members)} title="open team settings">
          <span class="team-name">{group.team.name}</span>
          <span class="team-meta">· {group.team.topology} · {group.members.length}</span>
        </h3>
      {/if}
      {#if !group.team.isOrphanGroup || orphansExpanded}
      <ul>
        {#each group.members as m (m.agent_name)}
          {@const agent = agentByName(m.agent_name)}
          {#if agent}
            {@const score = geomean(agent.scorecard)}
            <li class:selected={selectedAgents.has(agent.name)}
                class:deleting={deletingNames.has(agent.name)}
                class:orphan={agent.live === false}
                onclick={(ev) => handleAgentClick(ev, agent.name)}
                oncontextmenu={(ev) => openContext(ev, agent.name, group.team.name, m.role)}
                title="Click to select. Ctrl+click to toggle. Shift+click for range. Right-click for full menu (move team, change role, pause, stop). Hover for × to delete.">
              <span class="icon" class:shepherd={m.role === 'shepherd'}>{m.role === 'shepherd' ? '✻' : '●'}</span>
              <span class="name">{agent.name}</span>
              {#if agent.live === false}<span class="orphan-tag" title="Not running — orphan row">orphan</span>{/if}
              {#if score != null}<span class="score">{score.toFixed(1)}</span>{/if}
              <span class="age">{ageString(agent.created_at)}</span>
              <!-- #393 P0 — per-row × button (visible on hover). Disabled
                   while delete in-flight. stopPropagation so click
                   doesn't also fire row-select. -->
              <button type="button" class="row-delete"
                      disabled={deletingNames.has(agent.name)}
                      onclick={(ev) => deleteOne(agent.name, ev)}
                      title="Delete this session ({agent.live === false ? 'orphan' : 'live — will stop container'})">
                {deletingNames.has(agent.name) ? '⟳' : '×'}
              </button>
            </li>
          {/if}
        {/each}
      </ul>
      {/if}
    </section>
  {/each}
  {#if !tree.length}
    <p class="empty">No agents running — hit <strong>+ new</strong> to spawn one.</p>
  {/if}
</div>

{#if ctxMenu}
  <div class="ctx-backdrop" onclick={closeContext}>
    <div class="ctx-menu" style="left: {ctxMenu.x}px; top: {ctxMenu.y}px;" onclick={(e) => e.stopPropagation()}>
      <div class="ctx-head">{ctxMenu.agent} <small>· {ctxMenu.currentTeam} / {ctxMenu.currentRole}</small></div>
      <button onclick={() => { window.dispatchEvent(new CustomEvent('chepherd-open-agent-settings', { detail: { agentName: ctxMenu.agent } })); closeContext(); }}>⚙ Settings…</button>
      <button onclick={async () => { await fetch(`${API}/sessions/${ctxMenu.agent}/pause`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ paused: true }) }); closeContext(); }}>⏸ Pause</button>
      <button onclick={async () => { await fetch(`${API}/sessions/${ctxMenu.agent}/pause`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ paused: false }) }); closeContext(); }}>▶ Resume</button>
      <button onclick={async () => { await fetch(`${API}/sessions/${ctxMenu.agent}/restart`, { method: 'POST' }); closeContext(); }}>↻ Restart</button>
      <button class="danger" onclick={async () => { if (!confirm(`Delete session ${ctxMenu.agent}?`)) { closeContext(); return; } await fetch(`${API}/sessions/${ctxMenu.agent}`, { method: 'DELETE' }); closeContext(); }}>✕ Delete</button>
    </div>
  </div>
{/if}

<style>
  .list { padding: 0.5rem 0.5rem; height: 100%; overflow-y: auto; background: var(--bg); }
  /* #393 P1 — list header with orphan-count + Clean up orphans button */
  .list-header { display: flex; align-items: center; gap: 0.5rem; padding: 0.4rem 0.5rem; border-bottom: 1px solid var(--border); background: var(--bg-elev); font-size: 0.8rem; }
  .orphan-count { color: var(--warn, #d99); font-weight: 600; }
  .cleanup-btn { background: var(--btn-bg, #2a2a2a); color: var(--fg); border: 1px solid var(--border); border-radius: 4px; padding: 0.25rem 0.6rem; cursor: pointer; font-size: 0.78rem; }
  .cleanup-btn:hover:not(:disabled) { background: var(--btn-hover, #3a3a3a); border-color: var(--accent); }
  .cleanup-btn:disabled { opacity: 0.6; cursor: wait; }
  .cleanup-result { margin-left: auto; font-size: 0.78rem; color: var(--ok, #6c6); }
  .cleanup-result.err { color: var(--err, #e66); }
  .team { margin-bottom: 0.8rem; }
  .team h3 { font-size: 0.82rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.05em; margin: 0 0 0.3rem 0.2rem; font-weight: 600; }
  .team-head { cursor: pointer; }
  .team-head:hover { color: var(--accent); }
  .team-meta { color: var(--fg-faint); font-weight: normal; text-transform: none; letter-spacing: 0; }
  /* #421 P0 — orphan group: muted color, expansion indicator, banner */
  .orphan-group { opacity: 0.85; }
  .orphan-head { color: var(--fg-faint); }
  .orphan-head:hover { color: var(--warn, #d99); }
  .orphan-banner { background: var(--bg-elev); border-left: 2px solid var(--warn, #d99); padding: 0.45rem 0.6rem; margin: 0.2rem 0.5rem 0.4rem; font-size: 0.78rem; color: var(--fg-muted); border-radius: 0 4px 4px 0; }
  .orphan-banner code { background: var(--bg); padding: 0.05rem 0.3rem; border-radius: 3px; font-size: 0.74rem; color: var(--fg); }
  .team ul { list-style: none; padding: 0; margin: 0; }
  .team li { display: flex; align-items: center; gap: 0.45rem; padding: 0.4rem 0.5rem; border-radius: 4px; cursor: pointer; font-size: 0.85rem; border: 1px solid transparent; }
  .team li:hover { background: var(--bg-elev); border-color: var(--border); }
  .team li.selected { background: var(--select-bg); border-color: var(--select-border); }
  .team li .icon { color: var(--accent-2); }
  .team li .icon.shepherd { color: var(--accent); }
  .team li .name { flex: 1; font-weight: 500; }
  .team li .score { background: var(--accent); color: #000; padding: 0.05rem 0.4rem; border-radius: 8px; font-size: 0.72rem; font-weight: 600; }
  .team li .age { color: var(--fg-faint); font-size: 0.74rem; }
  /* #393 P0 — per-row × button. Visible only on row hover (or while
     deleting) to keep idle rows visually quiet but discoverable. */
  .team li .row-delete {
    background: transparent;
    color: var(--fg-faint);
    border: 1px solid transparent;
    border-radius: 3px;
    width: 22px;
    height: 22px;
    padding: 0;
    cursor: pointer;
    font-size: 0.95rem;
    line-height: 1;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    opacity: 0;
    transition: opacity 0.15s, background 0.15s, color 0.15s;
  }
  .team li:hover .row-delete,
  .team li.selected .row-delete,
  .team li.deleting .row-delete {
    opacity: 1;
  }
  .team li .row-delete:hover:not(:disabled) {
    background: var(--err, #e66);
    color: #fff;
    border-color: var(--err, #e66);
  }
  .team li .row-delete:disabled { cursor: wait; opacity: 0.7; }
  .team li.deleting { opacity: 0.5; }
  /* Orphan visual treatment so operator sees the persisted-but-not-
     running rows at a glance (not just "click ×"). */
  .team li.orphan .icon { color: var(--fg-faint); }
  .team li .orphan-tag {
    background: var(--warn, #d99);
    color: #000;
    padding: 0.02rem 0.35rem;
    border-radius: 8px;
    font-size: 0.66rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .empty { color: var(--fg-faint); font-size: 0.82rem; padding: 1rem; text-align: center; }
  .ctx-backdrop { position: fixed; inset: 0; z-index: 999; }
  .ctx-menu { position: fixed; background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.35rem; min-width: 200px; box-shadow: 0 6px 20px rgba(0,0,0,0.5); display: flex; flex-direction: column; gap: 0.05rem; }
  .ctx-head { padding: 0.3rem 0.5rem; color: var(--accent); font-weight: 600; font-size: 0.82rem; border-bottom: 1px solid var(--border); margin-bottom: 0.25rem; }
  .ctx-head small { color: var(--fg-muted); font-weight: normal; }
  .ctx-section { padding: 0.25rem 0.5rem; color: var(--fg-muted); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.06em; }
  .ctx-menu button { padding: 0.35rem 0.6rem; background: transparent; color: var(--fg); border: none; border-radius: 4px; cursor: pointer; text-align: left; font-size: 0.82rem; }
  .ctx-menu button:hover { background: var(--bg); color: var(--accent); }
  .ctx-menu button small { color: var(--fg-muted); }

  /* #253 — bulk action toolbar surfaces above the team list when 2+
     rows selected. Sticky-style header so it stays visible while the
     operator scrolls through long lists. */
  .bulk-toolbar {
    background: var(--bg-elev);
    border: 1px solid var(--border);
    border-radius: 6px;
    margin: 0.4rem 0.5rem 0.6rem 0.5rem;
    padding: 0.5rem 0.6rem;
    display: flex; flex-direction: column; gap: 0.5rem;
  }
  .bulk-head { display: flex; align-items: center; gap: 0.5rem; }
  .bulk-count {
    flex: 1; color: var(--accent, #87ceeb);
    font-size: 0.82rem; font-weight: 600;
  }
  .bulk-clear {
    background: transparent; border: 1px solid var(--border);
    color: var(--fg-muted); cursor: pointer;
    width: 1.6rem; height: 1.6rem; border-radius: 4px;
    font-size: 0.95rem; line-height: 1; padding: 0;
  }
  .bulk-clear:hover { color: var(--danger, #e74c3c); border-color: var(--danger, #e74c3c); }
  .bulk-actions { display: grid; grid-template-columns: repeat(4, 1fr); gap: 0.3rem; }
  .ba {
    padding: 0.32rem 0.4rem; font-size: 0.78rem; font-weight: 500;
    background: var(--bg, #0a0a0a); color: var(--fg, #f5f5f5);
    border: 1px solid var(--border, #2a2a2a); border-radius: 4px;
    cursor: pointer; font-family: inherit;
    transition: background 80ms, border-color 80ms;
  }
  .ba:hover { background: rgba(135, 206, 235, 0.10); border-color: var(--accent-2, #87ceeb); }
  .ba:disabled { opacity: 0.4; cursor: not-allowed; }
  .ba.danger { color: var(--danger, #e74c3c); }
  .ba.danger:hover { background: rgba(231, 76, 60, 0.12); border-color: var(--danger, #e74c3c); }
  .bulk-msg { display: flex; gap: 0.3rem; align-items: center; }
  .bulk-msg input {
    flex: 1; padding: 0.35rem 0.55rem; border-radius: 4px;
    border: 1px solid var(--border, #2a2a2a);
    background: var(--bg, #0a0a0a); color: var(--fg, #f5f5f5);
    font: inherit; font-size: 0.8rem;
  }
  .bulk-msg input:focus { border-color: var(--accent-2, #87ceeb); outline: none; }
  .ba.send { background: var(--accent-2, #87ceeb); color: #0a0a0a; border-color: var(--accent-2, #87ceeb); }
  .ba.send:hover:not(:disabled) { background: #6fb6d3; }
  .ba.send:disabled { background: var(--border, #2a2a2a); color: var(--fg-muted, #888); border-color: var(--border, #2a2a2a); }
  .bulk-results {
    list-style: none; padding: 0; margin: 0;
    border-top: 1px solid var(--border, #2a2a2a);
    padding-top: 0.4rem;
    max-height: 8rem; overflow-y: auto;
    font-size: 0.78rem;
  }
  .bulk-results li {
    display: flex; align-items: center; gap: 0.4rem;
    padding: 0.18rem 0.1rem;
  }
  .br-spin { color: var(--accent-2, #87ceeb); }
  .br-ok   { color: var(--success, #4ade80); }
  .br-fail { color: var(--danger, #e74c3c); }
  .br-name { font-weight: 500; }
  .br-text { color: var(--fg-muted, #888); font-size: 0.74rem; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bulk-results li.failed .br-text { color: var(--danger, #e74c3c); }

  /* Destructive confirmation modal — backdrop + centered panel.
     Stop / Restart require explicit confirm; Pause / Resume / Message
     fire inline. */
  .confirm-backdrop {
    position: fixed; inset: 0; z-index: 1000;
    background: rgba(0, 0, 0, 0.55);
    display: flex; align-items: center; justify-content: center;
  }
  .confirm-modal {
    background: var(--bg-elev, #1a1a1a);
    border: 1px solid var(--border-strong, #3a3a3a);
    border-radius: 8px;
    padding: 1.1rem 1.25rem;
    min-width: 320px; max-width: 460px;
    box-shadow: 0 12px 32px rgba(0, 0, 0, 0.6);
  }
  .confirm-modal h4 { margin: 0 0 0.6rem 0; font-size: 1rem; color: var(--fg, #f5f5f5); }
  .confirm-modal p { margin: 0 0 0.9rem 0; color: var(--fg-muted, #aaa); font-size: 0.85rem; line-height: 1.4; }
  .confirm-actions { display: flex; gap: 0.5rem; justify-content: flex-end; }
  .confirm-actions button {
    padding: 0.45rem 0.9rem; border-radius: 5px;
    cursor: pointer; font: inherit; font-size: 0.85rem; font-weight: 500;
    border: 1px solid var(--border, #2a2a2a);
    background: transparent; color: var(--fg, #f5f5f5);
  }
  .confirm-actions .cancel:hover { background: var(--bg, #0a0a0a); }
  .confirm-actions .confirm {
    background: var(--accent-2, #87ceeb); color: #0a0a0a; border-color: var(--accent-2, #87ceeb);
  }
  .confirm-actions .confirm.danger {
    background: var(--danger, #e74c3c); color: #ffffff; border-color: var(--danger, #e74c3c);
  }
  .confirm-actions .confirm:hover { filter: brightness(1.08); }
</style>

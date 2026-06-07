<!--
  CalmRail — the left roster. Agents grouped by team, each row carrying
  its identity color + role icon (HARD REQ #6). Clicking a row focuses
  that agent (rebinds the focused terminal pane → HARD REQ #1). The "＋"
  on a row opens that agent in a NEW terminal pane (layout flexibility).
-->
<script>
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let {
    sessions = [],
    teams = [],
    memberships = [],
    selectedAgent = '',
    onselect = () => {},
    onopennew = () => {},
  } = $props();

  const API = '/api/v1';
  let query = $state('');
  // Per-row lifecycle action state. busy[name] = 'pause'|'stop'… while a
  // request is in flight; pendingStop holds the name awaiting a 2nd click
  // (inline confirm — no browser dialogs, per UX bans).
  let busy = $state({});
  let pendingStop = $state('');
  let actionErr = $state('');

  async function lifecycle(e, name, kind, paused) {
    e.stopPropagation();
    if (busy[name]) return;
    // Stop is destructive → require a confirming second click.
    if (kind === 'stop' && pendingStop !== name) {
      pendingStop = name;
      setTimeout(() => { if (pendingStop === name) pendingStop = ''; }, 4000);
      return;
    }
    busy = { ...busy, [name]: kind }; actionErr = '';
    let url, method = 'POST', body = null;
    // Endpoints match v08/Workspace.svelte: POST pause {paused}, DELETE stop.
    if (kind === 'pause') { url = `${API}/sessions/${name}/pause`; body = JSON.stringify({ paused }); }
    else if (kind === 'stop') { url = `${API}/sessions/${name}`; method = 'DELETE'; pendingStop = ''; }
    try {
      const r = await fetch(url, { method, headers: body ? { 'Content-Type': 'application/json' } : {}, body });
      if (!r.ok) { const j = await r.json().catch(() => ({})); actionErr = `${name}: ${j.error || 'HTTP ' + r.status}`; }
    } catch (err) { actionErr = `${name}: ${err}`; }
    const { [name]: _, ...rest } = busy; busy = rest;
  }

  // Build team → [session] groups. Use memberships when present, fall
  // back to the session.team field; ungrouped agents land in "—".
  let groups = $derived.by(() => {
    const q = query.trim().toLowerCase();
    const match = (s) => !q || (s.name || '').toLowerCase().includes(q) || (s.role || '').toLowerCase().includes(q);
    const byTeam = new Map();
    const teamNames = teams.map((t) => t.name || t);
    for (const tn of teamNames) byTeam.set(tn, []);
    for (const s of sessions) {
      if (!match(s)) continue;
      const tn = s.team || '—';
      if (!byTeam.has(tn)) byTeam.set(tn, []);
      byTeam.get(tn).push(s);
    }
    // Stable: sort each group by name, drop empty groups.
    const out = [];
    for (const [tn, arr] of byTeam) {
      if (!arr.length) continue;
      arr.sort((a, b) => {
        const al = a.exited ? 1 : 0, bl = b.exited ? 1 : 0;
        if (al !== bl) return al - bl;
        return (a.name || '').localeCompare(b.name || '');
      });
      out.push({ team: tn, agents: arr });
    }
    out.sort((a, b) => a.team.localeCompare(b.team));
    return out;
  });

  let liveCount = $derived(sessions.filter((s) => !s.exited && s.live !== false).length);

  function statusOf(s) {
    if (s.exited) return 'exited';
    if (s.paused) return 'paused';
    if (s.live === false) return 'offline';
    return 'live';
  }
</script>

<div class="rail">
  <div class="rail-search">
    <input
      type="text"
      placeholder="Filter agents…"
      bind:value={query}
      aria-label="Filter agents"
    />
    <span class="count">{liveCount} live</span>
  </div>

  <div class="rail-scroll">
    {#if groups.length === 0}
      <div class="rail-empty">{sessions.length ? 'No matches' : 'No agents yet'}</div>
    {/if}
    {#each groups as g (g.team)}
      <div class="team-block">
        <div class="team-label">{g.team}</div>
        {#each g.agents as s (s.name)}
          {@const id = agentIdentity(s)}
          {@const st = statusOf(s)}
          <div
            class="row {selectedAgent === s.name ? 'is-sel' : ''}"
            role="button"
            tabindex="0"
            onclick={() => onselect(s.name)}
            onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onselect(s.name); } }}
            title={`${s.name} — ${s.role || 'agent'} — ${st}`}
          >
            <span class="row-ic" style={`color:${id.color}`}>{id.icon}</span>
            <span class="row-text">
              <span class="row-name">{s.name}</span>
              <span class="row-role">{s.role || 'agent'}</span>
            </span>
            <span class="row-dot {st}" title={st}></span>
            <span class="row-acts">
              {#if !s.exited}
                {#if s.paused}
                  <button class="row-act" title="Resume" aria-label={`Resume ${s.name}`} disabled={!!busy[s.name]} onclick={(e) => lifecycle(e, s.name, 'pause', false)}>{busy[s.name] === 'pause' ? '…' : '▶'}</button>
                {:else}
                  <button class="row-act" title="Pause" aria-label={`Pause ${s.name}`} disabled={!!busy[s.name]} onclick={(e) => lifecycle(e, s.name, 'pause', true)}>{busy[s.name] === 'pause' ? '…' : '⏸'}</button>
                {/if}
                <button
                  class="row-act danger {pendingStop === s.name ? 'confirm' : ''}"
                  title={pendingStop === s.name ? 'Click again to stop' : 'Stop'}
                  aria-label={`Stop ${s.name}`}
                  disabled={!!busy[s.name]}
                  onclick={(e) => lifecycle(e, s.name, 'stop')}
                >{busy[s.name] === 'stop' ? '…' : (pendingStop === s.name ? '✓?' : '■')}</button>
              {/if}
              <button
                class="row-act"
                title="Open in a new pane"
                onclick={(e) => { e.stopPropagation(); onopennew(s.name); }}
                aria-label={`Open ${s.name} in a new pane`}
              >＋</button>
            </span>
          </div>
        {/each}
      </div>
    {/each}
  </div>

  {#if actionErr}
    <div class="rail-err" role="alert">{actionErr}</div>
  {/if}
</div>

<style>
  .rail { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .rail-search { padding: 0.55rem 0.6rem; display: flex; align-items: center; gap: 0.5rem; border-bottom: 1px solid var(--calm-border); }
  .rail-search input {
    flex: 1; min-width: 0;
    padding: 0.4rem 0.6rem;
    background: var(--calm-input); color: var(--calm-fg);
    border: 1px solid var(--calm-border); border-radius: 6px;
    font-size: 0.8rem;
  }
  .rail-search input::placeholder { color: var(--calm-fg-faint); }
  .rail-search input:focus { outline: none; border-color: var(--calm-accent); }
  .count { font-size: 0.66rem; color: var(--calm-fg-faint); white-space: nowrap; }

  .rail-scroll { flex: 1; overflow-y: auto; padding: 0.4rem 0.4rem 1rem; }
  .rail-empty { color: var(--calm-fg-faint); font-size: 0.82rem; text-align: center; padding: 2rem 0; }

  .team-block { margin-bottom: 0.6rem; }
  .team-label {
    font-size: 0.64rem; text-transform: uppercase; letter-spacing: 0.08em;
    color: var(--calm-fg-faint); font-weight: 700;
    padding: 0.35rem 0.5rem 0.25rem;
  }

  .row {
    display: flex; align-items: center; gap: 0.55rem;
    padding: 0.45rem 0.5rem;
    border-radius: 6px;
    cursor: pointer;
    position: relative;
    transition: background 0.13s ease;
  }
  .row:hover { background: var(--calm-chip-hover); }
  .row.is-sel { background: color-mix(in srgb, var(--calm-accent) 16%, transparent); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--calm-accent) 40%, transparent); }
  .row-ic { font-size: 1rem; width: 1.2rem; text-align: center; flex: 0 0 auto; }
  .row-text { min-width: 0; flex: 1; display: flex; flex-direction: column; line-height: 1.15; }
  .row-name { font-size: 0.84rem; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; color: var(--calm-fg); }
  .row-role { font-size: 0.68rem; color: var(--calm-fg-faint); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

  .row-dot { width: 8px; height: 8px; border-radius: 50%; flex: 0 0 auto; }
  .row-dot.live { background: var(--calm-ok); box-shadow: 0 0 0 3px color-mix(in srgb, var(--calm-ok) 22%, transparent); }
  .row-dot.paused { background: var(--calm-warn); }
  .row-dot.exited, .row-dot.offline { background: var(--calm-fg-faint); }

  /* Per-row lifecycle actions (pause/resume/stop/+pane). Revealed on
     hover; a pending Stop stays visible until confirmed or it times out. */
  .row-acts { display: inline-flex; align-items: center; gap: 0.1rem; flex: 0 0 auto; }
  .row-act {
    width: 22px; height: 22px; flex: 0 0 auto;
    display: inline-flex; align-items: center; justify-content: center;
    background: transparent; border: 1px solid transparent; color: var(--calm-fg-muted);
    border-radius: 6px; cursor: pointer; font-size: 0.82rem; line-height: 1;
    opacity: 0; transition: opacity 0.13s ease, background 0.13s ease, color 0.13s ease;
  }
  .row:hover .row-act { opacity: 1; }
  .row-act:hover:not(:disabled) { background: var(--calm-chip); color: var(--calm-accent); }
  .row-act:disabled { cursor: progress; opacity: 0.5; }
  .row-act.danger:hover:not(:disabled) { color: var(--calm-danger); background: color-mix(in srgb, var(--calm-danger) 14%, transparent); }
  .row-act.confirm { opacity: 1; color: var(--calm-danger); background: color-mix(in srgb, var(--calm-danger) 16%, transparent); font-size: 0.7rem; font-weight: 700; }

  .rail-err {
    flex: 0 0 auto; margin: 0.4rem; padding: 0.4rem 0.55rem;
    background: color-mix(in srgb, var(--calm-danger) 12%, transparent);
    color: var(--calm-danger); border-radius: 6px;
    font-size: 0.72rem; word-break: break-word;
  }
</style>

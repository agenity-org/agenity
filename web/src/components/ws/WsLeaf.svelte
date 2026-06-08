<!--
  WsLeaf — a single framed GENERIC pane in a workspace's split-tree. A pane
  can be ANY content type — there are no special fixed regions:
    sessions | terminal | inspector | kanban | events | transcript | mesh |
    tasks | mcplog.

  Header (low-chrome):
    · a pane-type switcher (button + menu) — the per-pane CONTENT PICKER;
    · for terminals an inline agent picker (operator chooses which agent's
      LIVE terminal this pane shows; every glyph via agentIdentity.js);
    · split-right / split-down, collapse chevron, maximize, close controls.
  Right-click the header opens a pane context menu (split/collapse/
  maximize/close).

  Body hosts the chosen content. sessions → the agent list (replaces the old
  roster): click a row to focus its terminal, + to open in a new pane,
  right-click an agent or a team label for the settings menus; terminal →
  live WidgetTerminal; kanban → WidgetKanban; events → the FULL live runtime
  event stream (every kind/actor); mcplog → WidgetMCPLog (same stream filtered
  to MCP-call audit kinds); tasks / mesh → live A2A inbox / federation peers;
  transcript → TeamTranscript; inspector → CalmInspector.
-->
<script>
  import WidgetTerminal from '../v08/widgets/WidgetTerminal.svelte';
  import WidgetKanban from '../v08/widgets/WidgetKanban.svelte';
  import WidgetMCPLog from '../v08/widgets/WidgetMCPLog.svelte';
  import TeamTranscript from '../TeamTranscript.svelte';
  import CalmInspector from '../calm/CalmInspector.svelte';
  import { agentIdentity } from '../../lib/agentIdentity.js';
  import { onDestroy } from 'svelte';
  import { PANE_TYPES, paneMeta } from './layoutWs.js';

  let {
    node,
    sessions = [],
    teams = [],
    memberships = [],
    events = [],
    peers = [],
    tasks = [],
    focusedSession = null,
    selectedAgent = '',
    focused = false,
    canClose = true,
    maximized = false,
    collapse = null,          // null | { axis:'h'|'v', collapsed:bool }
    onfocus = () => {},
    onsplit = () => {},
    onclose = () => {},
    onsetagent = () => {},
    onsetwidget = () => {},
    onmaximize = () => {},
    oncollapse = () => {},
    onctxmenu = () => {},      // (clientX, clientY) → root opens the pane menu
    onpickagent = () => {},    // Sessions pane: click row → focus terminal
    onopenagentnew = () => {}, // Sessions pane: + → open in new pane
    onagentctxmenu = () => {}, // Sessions pane: right-click agent → menu
    onteamctxmenu = () => {},  // Sessions pane: right-click team → menu
  } = $props();

  let pickerOpen = $state(false);
  let widgetMenuOpen = $state(false);
  // Fixed-popover coordinates for whichever picker is open. Both menus are
  // rendered position:fixed at the document level so they ESCAPE the .leaf /
  // .head-left `overflow:hidden` (which is required for split-tree panes but
  // was CLIPPING the old position:absolute dropdowns → operator-reported
  // "panes drop down list is still not visible"). On open we capture the
  // trigger button's getBoundingClientRect() and clamp to the viewport.
  let widgetMenuPos = $state({ x: 0, y: 0 });
  let pickerPos = $state({ x: 0, y: 0 });
  const MENU_W = 256;   // ~16rem agent-menu; widget menu is narrower but clamp by the wider

  function popoverPos(btn) {
    const r = btn.getBoundingClientRect();
    const vw = (typeof window !== 'undefined' ? window.innerWidth : 1280);
    const vh = (typeof window !== 'undefined' ? window.innerHeight : 800);
    let x = r.left;
    let y = r.bottom + 4;
    if (x + MENU_W > vw - 8) x = Math.max(8, vw - MENU_W - 8);     // clamp right edge
    if (y > vh - 80) y = Math.max(8, r.top - 4 - 240);             // flip above if no room below
    return { x, y };
  }
  function toggleWidgetMenu(e) {
    e.stopPropagation();
    pickerOpen = false;
    if (!widgetMenuOpen) widgetMenuPos = popoverPos(e.currentTarget);
    widgetMenuOpen = !widgetMenuOpen;
  }
  function togglePicker(e) {
    e.stopPropagation();
    widgetMenuOpen = false;
    if (!pickerOpen) pickerPos = popoverPos(e.currentTarget);
    pickerOpen = !pickerOpen;
  }

  // Dismiss the content-picker + agent-picker on outside-click or ESC. The
  // menus are now fixed popovers carrying a `.ws-leaf-popover` marker; a click
  // anywhere outside the trigger wraps OR the popover (or an Escape keypress)
  // closes whichever is open. A window scroll/resize closes them too — the
  // captured coords would otherwise go stale. Listeners are window-level +
  // cheap; they early-return when nothing is open.
  function closePickers() { pickerOpen = false; widgetMenuOpen = false; }
  function onDocPointerDown(e) {
    if (!pickerOpen && !widgetMenuOpen) return;
    const el = e.target;
    // Scope the "inside" test to THIS leaf instance. The trigger wraps + the
    // fixed popovers all carry data-leaf-id={node.id}; a click only counts as
    // "inside" when its nearest tagged ancestor belongs to THIS leaf. Opening
    // pane B's picker therefore closes pane A's (the click is inside B's
    // [data-leaf-id], not A's), so only one picker is ever open across panes.
    const inside = el && el.closest ? el.closest('[data-leaf-id]') : null;
    if (inside && inside.getAttribute('data-leaf-id') === node.id) return;
    closePickers();
  }
  function onDocKeyDown(e) {
    if (e.key === 'Escape' && (pickerOpen || widgetMenuOpen)) {
      e.stopPropagation();   // swallow before the root's ESC-priority handler
      closePickers();
    }
  }
  function onViewportChange() { if (pickerOpen || widgetMenuOpen) closePickers(); }
  if (typeof window !== 'undefined') {
    window.addEventListener('pointerdown', onDocPointerDown, true);
    window.addEventListener('keydown', onDocKeyDown, true);
    window.addEventListener('scroll', onViewportChange, true);   // capture: catches nested scrollers
    window.addEventListener('resize', onViewportChange);
    onDestroy(() => {
      window.removeEventListener('pointerdown', onDocPointerDown, true);
      window.removeEventListener('keydown', onDocKeyDown, true);
      window.removeEventListener('scroll', onViewportChange, true);
      window.removeEventListener('resize', onViewportChange);
    });
  }

  let agentOptions = $derived(
    [...sessions].sort((a, b) => {
      const al = a.exited ? 1 : 0, bl = b.exited ? 1 : 0;
      if (al !== bl) return al - bl;
      return (a.name || '').localeCompare(b.name || '');
    })
  );

  let boundName = $derived(node.config?.agent || '');
  let boundSession = $derived(sessions.find((s) => s.name === boundName) || null);
  let id = $derived(boundSession ? agentIdentity(boundSession) : agentIdentity(boundName || '?'));
  let cur = $derived(paneMeta(node.widget));

  // Pick a repo-bearing agent for a kanban pane when none bound.
  let kanbanAgent = $derived(
    (boundSession?.github_url ? boundSession : null) ||
    (focusedSession?.github_url ? focusedSession : null) ||
    (sessions || []).find((s) => s.github_url && !s.exited) ||
    focusedSession || boundSession || null
  );

  function choose(name) { onsetagent(name); pickerOpen = false; }
  function chooseWidget(w) { onsetwidget(w); widgetMenuOpen = false; }
  function statusLabel(s) {
    if (!s) return '';
    if (s.exited) return 'exited';
    if (s.paused) return 'paused';
    if (s.live === false) return 'offline';
    return 'live';
  }
  function fmtTime(t) { if (!t) return ''; try { return new Date(t).toLocaleTimeString(); } catch { return String(t); } }

  // ---------------- Events pane (the LIVE event stream) ----------------
  // Distinct from the MCP Log pane: Events shows the WHOLE runtime event
  // stream (every kind, every actor) the dashboard already polls + streams,
  // newest first; MCP Log (WidgetMCPLog) filters that same stream down to
  // MCP-call audit kinds. Operator can grep by kind / actor / body.
  let evQuery = $state('');
  let eventFeed = $derived.by(() => {
    const f = (evQuery || '').trim().toLowerCase();
    return (events || [])
      .filter((e) => !f || [e.kind, e.actor, e.body].filter(Boolean).join(' ').toLowerCase().includes(f))
      .slice(-300)
      .reverse();
  });

  // ---------------- Sessions pane (replaces the old roster) ----------------
  const API = '/api/v1';
  let sQuery = $state('');
  let busy = $state({});          // name -> 'pause'|'stop' while in flight
  let pendingStop = $state('');   // inline-confirm for destructive stop
  let actErr = $state({});        // name -> short error string (transient) on a failed action

  // A non-PTY external A2A peer (and any exited row) has no controllable
  // container, so its lifecycle endpoints 404. Hide the buttons for those so
  // the operator never clicks an action that silently can't apply.
  function controllable(s) { return !!s && !s.exited && !(s.agent === 'external-a2a' || s.external); }

  // Build team → [session] groups (same shape as the former roster).
  let sessionGroups = $derived.by(() => {
    const q = sQuery.trim().toLowerCase();
    const match = (s) => !q || (s.name || '').toLowerCase().includes(q) || (s.role || '').toLowerCase().includes(q);
    const byTeam = new Map();
    for (const tn of teams.map((t) => t.name || t)) byTeam.set(tn, []);
    for (const s of sessions) {
      if (!match(s)) continue;
      const tn = s.team || '—';
      if (!byTeam.has(tn)) byTeam.set(tn, []);
      byTeam.get(tn).push(s);
    }
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

  async function lifecycle(e, name, kind, paused) {
    e.stopPropagation();
    if (busy[name]) return;
    if (kind === 'stop' && pendingStop !== name) {
      pendingStop = name;
      setTimeout(() => { if (pendingStop === name) pendingStop = ''; }, 4000);
      return;
    }
    busy = { ...busy, [name]: kind };
    const { [name]: _e, ...restErr } = actErr; actErr = restErr;   // clear any prior error
    let url, method = 'POST', body = null;
    if (kind === 'pause') { url = `${API}/sessions/${name}/pause`; body = JSON.stringify({ paused }); }
    else if (kind === 'stop') { url = `${API}/sessions/${name}`; method = 'DELETE'; pendingStop = ''; }
    try {
      // Surface failure instead of silently swallowing it. A non-PTY external
      // peer / orphan row 404s; the old `catch {}` looked like success.
      const r = await fetch(url, { method, headers: body ? { 'Content-Type': 'application/json' } : {}, body });
      if (!r.ok) actErr = { ...actErr, [name]: `failed (${r.status})` };
    } catch { actErr = { ...actErr, [name]: 'failed' }; }
    const { [name]: _, ...rest } = busy; busy = rest;
    if (actErr[name]) setTimeout(() => { const { [name]: _x, ...r2 } = actErr; actErr = r2; }, 4000);
  }
</script>

<div class="leaf {focused ? 'is-focused' : ''}" data-leaf-id={node.id} onmousedown={onfocus} oncontextmenu={(e) => { e.preventDefault(); onctxmenu(e.clientX, e.clientY); }} role="presentation">
  <header class="leaf-head">
    <div class="head-left">
      <span class="wbtn-wrap">
        <button class="wbtn" title="Change pane type" onclick={toggleWidgetMenu}>
          <span class="wglyph">{cur.glyph}</span>
          <span class="wlabel">{cur.label}</span>
          <span class="caret">⌄</span>
        </button>
      </span>

      {#if node.widget === 'terminal'}
        <span class="picker-wrap">
          <button class="agent-pill" title="Choose which agent this terminal shows" onclick={togglePicker}>
            <span class="dot" style={`background:${id.color}`}></span>
            <span class="ic" style={`color:${id.color}`}>{id.icon}</span>
            <span class="aname">{boundName || 'pick agent'}</span>
            {#if boundSession}<span class="status {statusLabel(boundSession)}">{statusLabel(boundSession)}</span>{/if}
            <span class="caret">⌄</span>
          </button>
        </span>
      {/if}
    </div>

    <div class="head-right">
      {#if collapse}
        {@const c = collapse.collapsed}
        {@const glyph = collapse.axis === 'h' ? (c ? '›' : '‹') : (c ? '⌄' : '⌃')}
        <button class="ctl" title={c ? 'Expand pane' : 'Collapse pane'} aria-label={c ? 'Expand pane' : 'Collapse pane'} onclick={(e) => { e.stopPropagation(); oncollapse(); }}>{glyph}</button>
      {/if}
      <button class="ctl" title="Split right" onclick={(e) => { e.stopPropagation(); onsplit('h'); }} aria-label="Split right">⬓</button>
      <button class="ctl" title="Split down" onclick={(e) => { e.stopPropagation(); onsplit('v'); }} aria-label="Split down">⬒</button>
      <button class="ctl" title={maximized ? 'Restore layout' : 'Maximize pane'} aria-label={maximized ? 'Restore layout' : 'Maximize pane'} onclick={(e) => { e.stopPropagation(); onmaximize(); }}>{maximized ? '🗗' : '⛶'}</button>
      {#if canClose}
        <button class="ctl ctl-close" title="Close pane" onclick={(e) => { e.stopPropagation(); onclose(); }} aria-label="Close pane">✕</button>
      {/if}
    </div>
  </header>

  <div class="leaf-body">
    {#if node.widget === 'sessions'}
      <div class="sessions">
        <div class="sess-search">
          <input type="text" placeholder="Filter agents…" bind:value={sQuery} aria-label="Filter agents" onclick={(e) => e.stopPropagation()} />
          <span class="sess-count">{liveCount} live</span>
        </div>
        <div class="sess-scroll">
          {#if sessionGroups.length === 0}
            <div class="hollow sm">{sessions.length ? 'No matches' : 'No agents yet'}</div>
          {/if}
          {#each sessionGroups as g (g.team)}
            <div class="sess-team">
              <!-- #11 right-click a team label → Team settings -->
              <button
                class="team-label"
                title={`${g.team} — right-click for team settings`}
                oncontextmenu={(e) => { e.preventDefault(); e.stopPropagation(); onteamctxmenu(g.team, e.clientX, e.clientY); }}
                onclick={(e) => e.stopPropagation()}
              >{g.team}</button>
              {#each g.agents as s (s.name)}
                {@const sid = agentIdentity(s)}
                {@const st = statusLabel(s)}
                <!-- #11 right-click an agent → Agent settings -->
                <div
                  class="srow {selectedAgent === s.name ? 'is-sel' : ''}"
                  role="button"
                  tabindex="0"
                  onclick={(e) => { e.stopPropagation(); onpickagent(s.name); }}
                  onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onpickagent(s.name); } }}
                  oncontextmenu={(e) => { e.preventDefault(); e.stopPropagation(); onagentctxmenu(s.name, e.clientX, e.clientY); }}
                  title={`${s.name} — ${s.role || 'agent'} — ${st} · right-click for agent settings`}
                >
                  <span class="srow-ic" style={`color:${sid.color}`}>{sid.icon}</span>
                  <span class="srow-text">
                    <span class="srow-name">{s.name}</span>
                    <span class="srow-role">{s.role || 'agent'}</span>
                  </span>
                  <span class="srow-dot {st}" title={st}></span>
                  {#if actErr[s.name]}<span class="srow-err" title={`Action ${actErr[s.name]}`}>{actErr[s.name]}</span>{/if}
                  <span class="srow-acts">
                    {#if controllable(s)}
                      {#if s.paused}
                        <button class="srow-act" title="Resume" aria-label={`Resume ${s.name}`} disabled={!!busy[s.name]} onclick={(e) => lifecycle(e, s.name, 'pause', false)}>{busy[s.name] === 'pause' ? '…' : '▶'}</button>
                      {:else}
                        <button class="srow-act" title="Pause" aria-label={`Pause ${s.name}`} disabled={!!busy[s.name]} onclick={(e) => lifecycle(e, s.name, 'pause', true)}>{busy[s.name] === 'pause' ? '…' : '⏸'}</button>
                      {/if}
                      <button class="srow-act danger {pendingStop === s.name ? 'confirm' : ''}" title={pendingStop === s.name ? 'Click again to stop' : 'Stop'} aria-label={`Stop ${s.name}`} disabled={!!busy[s.name]} onclick={(e) => lifecycle(e, s.name, 'stop')}>{busy[s.name] === 'stop' ? '…' : (pendingStop === s.name ? '✓?' : '■')}</button>
                    {/if}
                    <button class="srow-act" title="Open in a new pane" aria-label={`Open ${s.name} in a new pane`} onclick={(e) => { e.stopPropagation(); onopenagentnew(s.name); }}>＋</button>
                  </span>
                </div>
              {/each}
            </div>
          {/each}
        </div>
      </div>
    {:else if node.widget === 'terminal'}
      {#key node.id + '|' + (boundName || '')}
        <WidgetTerminal selectedAgent={boundName} {sessions} {node} />
      {/key}
    {:else if node.widget === 'kanban'}
      {#if kanbanAgent?.github_url}
        <div class="widget-host"><WidgetKanban agent={kanbanAgent} {sessions} team={kanbanAgent?.team} /></div>
      {:else}
        <div class="hollow">No agent with a linked repository. Bind a github_url-bearing agent (focus one in a Sessions pane) to see its board.</div>
      {/if}
    {:else if node.widget === 'events'}
      <div class="events">
        <div class="ev-bar">
          <input type="text" placeholder="Filter events…" bind:value={evQuery} aria-label="Filter events" onclick={(e) => e.stopPropagation()} />
          <span class="ev-count">{eventFeed.length}</span>
        </div>
        <div class="ev-scroll">
          {#each eventFeed as e, i (e.id ?? (e.at || '') + '|' + i)}
            <div class="ev-row">
              <span class="ev-time">{fmtTime(e.at)}</span>
              <span class="ev-kind">{e.kind || 'event'}</span>
              <span class="ev-actor">{e.actor || ''}</span>
              <span class="ev-body">{e.body || ''}</span>
            </div>
          {/each}
          {#if eventFeed.length === 0}
            <div class="hollow sm">{events.length ? 'No events match.' : 'No events yet.'}</div>
          {/if}
        </div>
      </div>
    {:else if node.widget === 'transcript'}
      <div class="full-host"><TeamTranscript team="all" /></div>
    {:else if node.widget === 'inspector'}
      <CalmInspector boundSession={boundSession || focusedSession} {sessions} />
    {:else if node.widget === 'mesh'}
      <div class="feed">
        <div class="feed-lede">Federation peers discovered through the hub.</div>
        {#if peers.length === 0}
          <div class="hollow sm">No peers connected.</div>
        {:else}
          {#each peers as p (p.sid)}
            <div class="frow">
              <span class="frow-ic">⇄</span>
              <div class="frow-main"><div class="frow-name">{p.name || p.sid}</div><div class="frow-sub">{p.card?.url || p.url || p.sid}</div></div>
              <span class="frow-meta">{fmtTime(p.syncedAt)}</span>
            </div>
          {/each}
        {/if}
      </div>
    {:else if node.widget === 'tasks'}
      <div class="feed">
        <div class="feed-lede">Inbound agent-to-agent task envelopes.</div>
        {#if tasks.length === 0}
          <div class="hollow sm">No tasks.</div>
        {:else}
          {#each tasks as t (t.id)}
            <div class="frow">
              <span class="frow-ic">☑</span>
              <div class="frow-main"><div class="frow-name">{t.method || 'task'}</div><div class="frow-sub mono">{t.id}</div></div>
              <span class="state-chip {t.state}">{t.state || '—'}</span>
              <span class="frow-meta">{fmtTime(t.updatedAt)}</span>
            </div>
          {/each}
        {/if}
      </div>
    {:else if node.widget === 'mcplog'}
      <div class="widget-host"><WidgetMCPLog {events} /></div>
    {/if}
  </div>
</div>

<!-- Picker popovers rendered OUTSIDE .leaf as position:fixed so they escape
     the .leaf / .head-left `overflow:hidden` that was clipping the old
     position:absolute dropdowns. Coords captured from the trigger rect on
     open + clamped to the viewport; closed on outside-click / ESC / scroll /
     resize via the window listeners above. -->
{#if widgetMenuOpen}
  <div class="menu ws-leaf-popover" data-leaf-id={node.id} role="menu" style={`left:${widgetMenuPos.x}px; top:${widgetMenuPos.y}px`}>
    {#each PANE_TYPES as w}
      <button class="menu-item" class:active={w.id === node.widget} onclick={(e) => { e.stopPropagation(); chooseWidget(w.id); }}>
        <span class="wglyph">{w.glyph}</span>{w.label}
      </button>
    {/each}
  </div>
{/if}
{#if pickerOpen}
  <div class="menu agent-menu ws-leaf-popover" data-leaf-id={node.id} role="menu" style={`left:${pickerPos.x}px; top:${pickerPos.y}px`}>
    {#if agentOptions.length === 0}<div class="menu-empty">no sessions yet</div>{/if}
    {#each agentOptions as s}
      {@const sid = agentIdentity(s)}
      <button class="menu-item" class:active={s.name === boundName} onclick={(e) => { e.stopPropagation(); choose(s.name); }}>
        <span class="ic" style={`color:${sid.color}`}>{sid.icon}</span>
        <span class="mname">{s.name}</span>
        <span class="mrole">{s.role || ''}</span>
        <span class="status {statusLabel(s)}">{statusLabel(s)}</span>
      </button>
    {/each}
  </div>
{/if}

<style>
  .leaf { display: flex; flex-direction: column; width: 100%; height: 100%; min-width: 0; min-height: 0; background: var(--calm-surface); border: 1px solid var(--calm-border); border-radius: 6px; overflow: hidden; box-shadow: var(--calm-shadow-sm); transition: border-color 0.18s ease, box-shadow 0.18s ease; }
  .leaf.is-focused { border-color: color-mix(in srgb, var(--calm-accent) 55%, var(--calm-border)); box-shadow: var(--calm-shadow-focus); }

  .leaf-head { display: flex; align-items: center; justify-content: space-between; gap: 0.5rem; padding: 0.4rem 0.55rem; border-bottom: 1px solid var(--calm-border); background: var(--calm-surface-2); flex: 0 0 auto; min-height: 0; }
  .head-left { display: flex; align-items: center; gap: 0.4rem; min-width: 0; flex: 1 1 auto; overflow: hidden; }
  .head-right { display: flex; align-items: center; gap: 0.15rem; flex: 0 0 auto; }

  .wbtn-wrap, .picker-wrap { position: relative; }
  .wbtn, .agent-pill { display: inline-flex; align-items: center; gap: 0.35rem; background: var(--calm-chip); border: 1px solid var(--calm-border); color: var(--calm-fg); border-radius: 8px; padding: 0.2rem 0.55rem; font-size: 0.78rem; cursor: pointer; max-width: 16rem; min-width: 0; transition: background 0.14s ease, border-color 0.14s ease; }
  .wbtn:hover, .agent-pill:hover { background: var(--calm-chip-hover); border-color: var(--calm-border-strong); }
  .wglyph { opacity: 0.85; font-size: 0.82rem; }
  .wlabel { font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .caret { opacity: 0.5; font-size: 0.7rem; }
  .agent-pill .dot { width: 7px; height: 7px; border-radius: 50%; flex: 0 0 auto; }
  .agent-pill .ic { font-size: 0.85rem; }
  .agent-pill .aname { font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

  .status { font-size: 0.62rem; text-transform: uppercase; letter-spacing: 0.04em; padding: 0.04rem 0.34rem; border-radius: 6px; font-weight: 700; flex: 0 0 auto; }
  .status.live { color: var(--calm-ok); background: color-mix(in srgb, var(--calm-ok) 16%, transparent); }
  .status.paused { color: var(--calm-warn); background: color-mix(in srgb, var(--calm-warn) 16%, transparent); }
  .status.exited, .status.offline { color: var(--calm-fg-faint); background: color-mix(in srgb, var(--calm-fg-faint) 16%, transparent); }

  .ctl { width: 26px; height: 26px; display: inline-flex; align-items: center; justify-content: center; background: transparent; border: 1px solid transparent; color: var(--calm-fg-muted); border-radius: 8px; cursor: pointer; font-size: 0.85rem; transition: background 0.14s ease, color 0.14s ease; }
  .ctl:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }
  .ctl-close:hover { color: var(--calm-danger); background: color-mix(in srgb, var(--calm-danger) 14%, transparent); }

  /* Fixed-positioned popover (rendered outside .leaf to escape overflow:hidden).
     left/top come from the inline style set on open; z-index sits above the
     split-tree but below the root ctx-menu (1300). */
  .menu { position: fixed; z-index: 200; min-width: 12rem; max-width: 16rem; background: var(--calm-surface); border: 1px solid var(--calm-border-strong); border-radius: 6px; padding: 0.3rem; box-shadow: var(--calm-shadow-lg); display: flex; flex-direction: column; gap: 0.1rem; max-height: 60vh; overflow: auto; }
  .agent-menu { min-width: 16rem; max-width: 20rem; }
  .menu-item { display: flex; align-items: center; gap: 0.5rem; padding: 0.4rem 0.55rem; background: transparent; border: 0; border-radius: 8px; color: var(--calm-fg); font: inherit; font-size: 0.8rem; text-align: left; cursor: pointer; width: 100%; }
  .menu-item:hover { background: var(--calm-chip-hover); }
  .menu-item.active { background: color-mix(in srgb, var(--calm-accent) 16%, transparent); }
  .menu-item .mname { font-weight: 600; flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .menu-item .mrole { color: var(--calm-fg-faint); font-size: 0.72rem; }
  .menu-empty { padding: 0.6rem; color: var(--calm-fg-faint); font-size: 0.78rem; text-align: center; }

  .leaf-body { flex: 1; min-height: 0; min-width: 0; overflow: hidden; position: relative; background: var(--calm-bg); }
  .full-host { height: 100%; overflow: hidden; }
  .widget-host { height: 100%; min-height: 0; min-width: 0; overflow: hidden; display: flex; flex-direction: column; }
  .widget-host > :global(*) { flex: 1; min-height: 0; }

  .hollow { color: var(--calm-fg-faint); font-size: 0.84rem; padding: 1.4rem; margin: 0.7rem; text-align: center; background: var(--calm-surface-2); border: 1px dashed var(--calm-border); border-radius: 6px; }
  .hollow.sm { padding: 0.9rem; }

  /* ---- Sessions pane (the agent list; replaces the old roster) ---- */
  .sessions { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .sess-search { flex: 0 0 auto; padding: 0.5rem 0.55rem; display: flex; align-items: center; gap: 0.5rem; border-bottom: 1px solid var(--calm-border); }
  .sess-search input { flex: 1; min-width: 0; padding: 0.38rem 0.55rem; background: var(--calm-input); color: var(--calm-fg); border: 1px solid var(--calm-border); border-radius: 6px; font-size: 0.78rem; }
  .sess-search input::placeholder { color: var(--calm-fg-faint); }
  .sess-search input:focus { outline: none; border-color: var(--calm-accent); }
  .sess-count { font-size: 0.64rem; color: var(--calm-fg-faint); white-space: nowrap; }
  .sess-scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 0.4rem 0.4rem 1rem; }
  .sess-team { margin-bottom: 0.55rem; }
  .team-label { display: block; width: 100%; text-align: left; font-size: 0.64rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--calm-fg-faint); font-weight: 700; padding: 0.35rem 0.5rem 0.25rem; background: transparent; border: 0; cursor: context-menu; }
  .team-label:hover { color: var(--calm-fg-muted); }
  .srow { display: flex; align-items: center; gap: 0.5rem; padding: 0.42rem 0.5rem; border-radius: 6px; cursor: pointer; transition: background 0.13s ease; }
  .srow:hover { background: var(--calm-chip-hover); }
  .srow.is-sel { background: color-mix(in srgb, var(--calm-accent) 16%, transparent); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--calm-accent) 40%, transparent); }
  .srow-ic { font-size: 1rem; width: 1.2rem; text-align: center; flex: 0 0 auto; }
  .srow-text { min-width: 0; flex: 1; display: flex; flex-direction: column; line-height: 1.15; }
  .srow-name { font-size: 0.83rem; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; color: var(--calm-fg); }
  .srow-role { font-size: 0.67rem; color: var(--calm-fg-faint); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .srow-dot { width: 8px; height: 8px; border-radius: 50%; flex: 0 0 auto; }
  .srow-dot.live { background: var(--calm-ok); box-shadow: 0 0 0 3px color-mix(in srgb, var(--calm-ok) 22%, transparent); }
  .srow-dot.paused { background: var(--calm-warn); }
  .srow-dot.exited, .srow-dot.offline { background: var(--calm-fg-faint); }
  .srow-acts { display: inline-flex; align-items: center; gap: 0.1rem; flex: 0 0 auto; }
  .srow-act { width: 22px; height: 22px; flex: 0 0 auto; display: inline-flex; align-items: center; justify-content: center; background: transparent; border: 1px solid transparent; color: var(--calm-fg-muted); border-radius: 6px; cursor: pointer; font-size: 0.8rem; line-height: 1; opacity: 0; transition: opacity 0.13s ease, background 0.13s ease, color 0.13s ease; }
  .srow:hover .srow-act { opacity: 1; }
  .srow-act:hover:not(:disabled) { background: var(--calm-chip); color: var(--calm-accent); }
  .srow-act:disabled { cursor: progress; opacity: 0.5; }
  .srow-act.danger:hover:not(:disabled) { color: var(--calm-danger); background: color-mix(in srgb, var(--calm-danger) 14%, transparent); }
  .srow-act.confirm { opacity: 1; color: var(--calm-danger); background: color-mix(in srgb, var(--calm-danger) 16%, transparent); font-size: 0.68rem; font-weight: 700; }
  .srow-err { font-size: 0.62rem; font-weight: 700; color: var(--calm-danger); background: color-mix(in srgb, var(--calm-danger) 14%, transparent); padding: 0.04rem 0.34rem; border-radius: 6px; white-space: nowrap; flex: 0 0 auto; }

  .feed { height: 100%; overflow: auto; padding: 0.7rem; display: flex; flex-direction: column; gap: 0.4rem; color: var(--calm-fg); }
  .feed-lede { color: var(--calm-fg-muted); font-size: 0.78rem; margin-bottom: 0.2rem; }
  .frow { display: flex; align-items: center; gap: 0.7rem; padding: 0.55rem 0.7rem; background: var(--calm-surface-2); border: 1px solid var(--calm-border); border-radius: 6px; }
  .frow-ic { font-size: 1rem; color: var(--calm-accent-2); }
  .frow-main { flex: 1; min-width: 0; }
  .frow-name { font-weight: 600; font-size: 0.86rem; }
  .frow-sub { color: var(--calm-fg-faint); font-size: 0.74rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .frow-meta { color: var(--calm-fg-faint); font-size: 0.72rem; white-space: nowrap; }
  .mono { font-family: ui-monospace, monospace; }
  .state-chip { font-size: 0.66rem; text-transform: uppercase; padding: 0.12rem 0.45rem; border-radius: 8px; font-weight: 700; background: var(--calm-chip); color: var(--calm-fg-muted); flex: 0 0 auto; }
  .state-chip.completed { color: var(--calm-ok); background: color-mix(in srgb, var(--calm-ok) 15%, transparent); }
  .state-chip.working, .state-chip.submitted { color: var(--calm-accent-2); background: color-mix(in srgb, var(--calm-accent-2) 15%, transparent); }
  .state-chip.failed { color: var(--calm-danger); background: color-mix(in srgb, var(--calm-danger) 15%, transparent); }

  /* ---- Events pane (the live runtime event stream) ---- */
  .events { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .ev-bar { flex: 0 0 auto; padding: 0.5rem 0.55rem; display: flex; align-items: center; gap: 0.5rem; border-bottom: 1px solid var(--calm-border); }
  .ev-bar input { flex: 1; min-width: 0; padding: 0.38rem 0.55rem; background: var(--calm-input); color: var(--calm-fg); border: 1px solid var(--calm-border); border-radius: 6px; font-size: 0.78rem; }
  .ev-bar input::placeholder { color: var(--calm-fg-faint); }
  .ev-bar input:focus { outline: none; border-color: var(--calm-accent); }
  .ev-count { font-size: 0.64rem; color: var(--calm-fg-faint); white-space: nowrap; }
  .ev-scroll { flex: 1; min-height: 0; overflow-y: auto; padding: 0.2rem 0; }
  .ev-row { display: grid; grid-template-columns: 64px 110px 96px 1fr; gap: 0.45rem; padding: 0.2rem 0.6rem; font-size: 0.72rem; font-family: ui-monospace, monospace; border-bottom: 1px solid var(--calm-border); align-items: baseline; }
  .ev-row:hover { background: var(--calm-chip-hover); }
  .ev-time { color: var(--calm-fg-faint); }
  .ev-kind { color: var(--calm-accent); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .ev-actor { color: var(--calm-fg); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .ev-body { color: var(--calm-fg-muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
</style>

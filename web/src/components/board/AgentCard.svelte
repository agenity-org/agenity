<!--
  AgentCard — a living "fleet" card for one agent on the orchestration
  board. Renders identity (color spine + role icon from agentIdentity.js),
  status (live / paused / exited), team, last-activity heartbeat, and a
  scorecard geomean badge.

  Click → focus this agent's terminal (the board's primary gesture).
  Double-click / "open ▣" → open the agent in a fresh terminal pane.
-->
<script>
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let {
    session,
    focused = false,
    onfocus = () => {},
    onopen = () => {},
    onaction = () => {},
  } = $props();

  let id = $derived(agentIdentity(session));

  let status = $derived.by(() => {
    if (session.exited) return { key: 'exited', label: session.exit_code ? `exited ${session.exit_code}` : 'exited' };
    if (session.paused) return { key: 'paused', label: 'paused' };
    if (session.live === false) return { key: 'down', label: 'down' };
    return { key: 'live', label: 'live' };
  });

  // geomean of the 5 scorecard axes (G,V,F,E,D) → 0..1 → percent
  let score = $derived.by(() => {
    const sc = session.scorecard;
    if (!sc) return null;
    const axes = ['G', 'V', 'F', 'E', 'D'].map(k => Number(sc[k]));
    if (axes.some(v => !isFinite(v) || v <= 0)) {
      const valid = axes.filter(v => isFinite(v) && v > 0);
      if (!valid.length) return null;
      const gm = Math.pow(valid.reduce((a, b) => a * b, 1), 1 / valid.length);
      return Math.round(gm * 100) / 100;
    }
    const gm = Math.pow(axes.reduce((a, b) => a * b, 1), 1 / axes.length);
    return Math.round(gm * 100) / 100;
  });

  function ago(sec) {
    if (sec == null) return '';
    if (sec < 5) return 'now';
    if (sec < 60) return `${Math.floor(sec)}s`;
    if (sec < 3600) return `${Math.floor(sec / 60)}m`;
    if (sec < 86400) return `${Math.floor(sec / 3600)}h`;
    return `${Math.floor(sec / 86400)}d`;
  }

  // Heartbeat intensity from recent throughput — drives the pulse opacity.
  let busy = $derived(status.key === 'live' && (session.bytes_5m || 0) > 0 && (session.idle_seconds ?? 999) < 30);

  let menuOpen = $state(false);
  function act(a, e) { e.stopPropagation(); menuOpen = false; onaction(session.name, a); }
</script>

<div
  class="card"
  class:focused
  class:dim={status.key === 'exited' || status.key === 'down'}
  style="--card-color:{id.color}"
  role="button"
  tabindex="0"
  onclick={() => onfocus(session.name)}
  ondblclick={() => onopen(session.name)}
  onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onfocus(session.name); } }}
  title="{session.name} — click to focus · double-click to open in a new pane"
>
  <div class="spine"></div>
  <div class="body">
    <div class="row-top">
      <span class="icon" style="color:{id.color}">{id.icon}</span>
      <span class="name">{session.name}</span>
      <span class="status {status.key}" class:busy>
        <span class="dot"></span>{status.label}
      </span>
    </div>
    <div class="row-meta">
      {#if session.role}<span class="role">{session.role}</span>{/if}
      {#if session.team}<span class="sep">·</span><span class="team">{session.team}</span>{/if}
    </div>
    <div class="row-foot">
      <span class="heartbeat" title="last activity">
        ♥ {ago(session.idle_seconds)}
      </span>
      {#if score != null}
        <span class="score" title="scorecard geomean (G·V·F·E·D)">{score.toFixed(2)}</span>
      {/if}
      {#if session.intervention_count}
        <span class="interv" title="shepherd interventions">⚠ {session.intervention_count}</span>
      {/if}
      <span class="grow"></span>
      <button class="kebab" title="actions" onclick={(e) => { e.stopPropagation(); menuOpen = !menuOpen; }} aria-label="agent actions">⋯</button>
    </div>
  </div>

  {#if menuOpen}
    <div class="backdrop" role="presentation" onclick={(e) => { e.stopPropagation(); menuOpen = false; }}></div>
    <div class="menu" role="menu">
      <button role="menuitem" onclick={(e) => { e.stopPropagation(); menuOpen = false; onopen(session.name); }}>▣ Open in new pane</button>
      {#if !session.exited}
        {#if session.paused}
          <button role="menuitem" onclick={(e) => act('unpause', e)}>▶ Resume</button>
        {:else}
          <button role="menuitem" onclick={(e) => act('pause', e)}>⏸ Pause</button>
        {/if}
        <button role="menuitem" onclick={(e) => act('restart', e)}>↻ Restart</button>
      {/if}
      <button role="menuitem" class="danger" onclick={(e) => act('stop', e)}>■ Stop</button>
    </div>
  {/if}
</div>

<style>
  .card {
    position: relative;
    display: flex;
    background: var(--board-surface);
    border: 1px solid var(--board-border);
    border-radius: 12px;
    overflow: hidden;
    cursor: pointer;
    transition: transform .12s ease, box-shadow .12s ease, border-color .12s ease;
    min-height: 96px;
    user-select: none;
  }
  .card:hover { transform: translateY(-2px); box-shadow: 0 8px 22px var(--board-shadow); border-color: var(--board-border-strong); }
  .card.focused {
    border-color: var(--card-color);
    box-shadow: 0 0 0 1px var(--card-color), 0 10px 26px var(--board-shadow);
  }
  .card.dim { opacity: 0.62; }

  .spine { width: 6px; background: var(--card-color); flex: 0 0 6px; }

  .body { flex: 1; min-width: 0; padding: 0.7rem 0.85rem 0.6rem; display: flex; flex-direction: column; gap: 0.32rem; }

  .row-top { display: flex; align-items: center; gap: 0.45rem; }
  .icon { font-size: 1.05rem; line-height: 1; flex: 0 0 auto; }
  .name {
    font-weight: 650; font-size: 0.92rem; color: var(--board-fg);
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis; flex: 1; min-width: 0;
  }

  .status { display: inline-flex; align-items: center; gap: 0.3rem; font-size: 0.68rem; font-weight: 600; padding: 0.1rem 0.42rem; border-radius: 999px; flex: 0 0 auto; }
  .status .dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
  .status.live { color: var(--board-ok); background: var(--board-ok-bg); }
  .status.paused { color: var(--board-warn); background: var(--board-warn-bg); }
  .status.exited, .status.down { color: var(--board-fg-faint); background: var(--board-chip-bg); }
  .status.busy .dot { animation: pulse 1.1s ease-in-out infinite; }
  @keyframes pulse { 0%,100% { opacity: 1; transform: scale(1); } 50% { opacity: .35; transform: scale(1.55); } }

  .row-meta { display: flex; align-items: center; gap: 0.35rem; font-size: 0.72rem; color: var(--board-fg-muted); }
  .role { text-transform: capitalize; }
  .sep { color: var(--board-fg-faint); }
  .team { color: var(--board-fg-faint); }

  .row-foot { display: flex; align-items: center; gap: 0.55rem; font-size: 0.7rem; color: var(--board-fg-muted); }
  .heartbeat { color: var(--board-fg-faint); }
  .score {
    font-family: ui-monospace, monospace; font-weight: 700;
    color: var(--board-accent); background: var(--board-accent-bg);
    padding: 0.04rem 0.36rem; border-radius: 5px;
  }
  .interv { color: var(--board-warn); }
  .grow { flex: 1; }
  .kebab {
    background: transparent; border: 0; color: var(--board-fg-faint);
    font-size: 1rem; line-height: 1; cursor: pointer; padding: 0 0.2rem; border-radius: 4px;
  }
  .kebab:hover { color: var(--board-fg); background: var(--board-chip-bg); }

  .backdrop { position: fixed; inset: 0; z-index: 40; }
  .menu {
    position: absolute; right: 0.6rem; bottom: 0.4rem; z-index: 41;
    background: var(--board-surface-2); border: 1px solid var(--board-border-strong);
    border-radius: 8px; padding: 0.25rem; min-width: 11rem;
    display: flex; flex-direction: column; gap: 0.1rem;
    box-shadow: 0 10px 28px var(--board-shadow);
  }
  .menu button {
    text-align: left; background: transparent; border: 0; color: var(--board-fg);
    font: inherit; font-size: 0.82rem; padding: 0.42rem 0.55rem; border-radius: 5px; cursor: pointer;
  }
  .menu button:hover { background: var(--board-hover); }
  .menu button.danger { color: var(--board-danger); }
</style>

<!--
  MissionInspector — identity + scorecard + runtime + recent-events readout
  for the focused agent (spec's "Context Column / Inspector"). Real data from
  the sessions wire (props) + the global events stream. Also offers the live
  session actions from the data layer: pause/resume, restart, stop, handoff.
-->
<script>
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let { sessions = [], memberships = [], events = [], selectedAgent = null, mode = 'dark' } = $props();

  const API = '/api/v1';
  const agent = $derived(sessions.find(s => s.name === selectedAgent) || null);
  const teamOf = $derived.by(() => {
    const m = memberships.find(x => x.agent_name === selectedAgent);
    return m ? m.team_name : (agent?.team || '');
  });
  const myEvents = $derived.by(() =>
    (events || [])
      .filter(e => e.agent === selectedAgent || e.name === selectedAgent || e.session === selectedAgent || e.from === selectedAgent)
      .slice(-12)
      .reverse()
  );

  let busy = $state('');
  let actionErr = $state('');
  let handoffTarget = $state('');
  let showHandoff = $state(false);

  async function action(act) {
    if (!selectedAgent) return;
    busy = act; actionErr = '';
    let url, method, body = null;
    if (act === 'pause')   { url = `${API}/sessions/${selectedAgent}/pause`;   method = 'PATCH'; body = JSON.stringify({ paused: true }); }
    if (act === 'resume')  { url = `${API}/sessions/${selectedAgent}/pause`;   method = 'PATCH'; body = JSON.stringify({ paused: false }); }
    if (act === 'restart') { url = `${API}/sessions/${selectedAgent}/restart`; method = 'POST'; }
    if (act === 'stop')    { url = `${API}/sessions/${selectedAgent}`;          method = 'DELETE'; }
    try {
      const r = await fetch(url, { method, headers: body ? { 'Content-Type': 'application/json' } : {}, body });
      if (!r.ok) { const e = await r.json().catch(() => ({})); actionErr = e.error || `HTTP ${r.status}`; }
    } catch (e) { actionErr = String(e); }
    busy = '';
  }
  async function doHandoff() {
    if (!selectedAgent || !handoffTarget) return;
    busy = 'handoff'; actionErr = '';
    try {
      const r = await fetch(`${API}/sessions/${selectedAgent}/handoff`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ target: handoffTarget }),
      });
      if (!r.ok) { const e = await r.json().catch(() => ({})); actionErr = e.error || `HTTP ${r.status}`; }
      else { showHandoff = false; handoffTarget = ''; }
    } catch (e) { actionErr = String(e); }
    busy = '';
  }

  function fmtTime(ts) {
    if (!ts) return '';
    try { return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }); } catch { return ''; }
  }
  const SC_KEYS = [['G', 'Goal'], ['V', 'Velocity'], ['F', 'Focus'], ['E', 'Efficiency'], ['D', 'Discipline']];
</script>

<div class="inspector">
  {#if !agent}
    <div class="empty">Select an agent to inspect.</div>
  {:else}
    {@const ident = agentIdentity(agent)}
    <div class="ident" style="--id-color:{ident.color}">
      <span class="big-ic">{ident.icon}</span>
      <div class="ident-text">
        <span class="nm">{agent.name}</span>
        <span class="role">{agent.role || 'agent'} · {teamOf || 'no team'}</span>
      </div>
      <span class="state {agent.exited ? 'dead' : agent.paused ? 'paused' : 'live'}">
        {agent.exited ? 'EXITED' : agent.paused ? 'PAUSED' : 'LIVE'}
      </span>
    </div>

    {#if agent.scorecard}
      <div class="sect-h">SCORECARD</div>
      <div class="scorecard">
        {#each SC_KEYS as [k, label]}
          {@const v = agent.scorecard[k]}
          <div class="sc-cell" title={label}>
            <span class="sc-k">{k}</span>
            <div class="sc-bar"><div class="sc-fill" style="width:{Math.max(0, Math.min(10, v || 0)) * 10}%"></div></div>
            <span class="sc-v">{v ?? '—'}</span>
          </div>
        {/each}
      </div>
    {/if}

    <div class="sect-h">RUNTIME</div>
    <dl class="kv">
      <dt>PID</dt><dd>{agent.pid ?? '—'}</dd>
      <dt>CWD</dt><dd class="mono trunc" title={agent.cwd}>{agent.cwd || '—'}</dd>
      {#if agent.branch}<dt>Branch</dt><dd class="mono">{agent.branch}</dd>{/if}
      <dt>Idle</dt><dd>{agent.idle_seconds != null ? agent.idle_seconds + 's' : '—'}</dd>
      <dt>5m I/O</dt><dd class="mono">{agent.bytes_5m ?? 0} B</dd>
      <dt>Total</dt><dd class="mono">{agent.total_bytes ?? 0} B</dd>
      {#if agent.intervention_count}<dt>Interv.</dt><dd>{agent.intervention_count}</dd>{/if}
    </dl>

    {#if agent.last_verdict}
      <div class="sect-h">LAST VERDICT</div>
      <div class="verdict">
        <span class="v-tag">{agent.last_verdict}</span>
        {#if agent.last_verdict_msg}<span class="v-msg">{agent.last_verdict_msg}</span>{/if}
        {#if agent.last_verdict_at}<span class="v-at">{fmtTime(agent.last_verdict_at)}</span>{/if}
      </div>
    {/if}

    <div class="sect-h">CONTROL</div>
    <div class="actions">
      {#if agent.paused}
        <button class="act" disabled={busy} onclick={() => action('resume')}>▶ Resume</button>
      {:else}
        <button class="act" disabled={busy} onclick={() => action('pause')}>⏸ Pause</button>
      {/if}
      <button class="act" disabled={busy} onclick={() => action('restart')}>↻ Restart</button>
      <button class="act" disabled={busy} onclick={() => (showHandoff = !showHandoff)}>⇄ Handoff</button>
      <button class="act danger" disabled={busy} onclick={() => action('stop')}>■ Stop</button>
    </div>
    {#if showHandoff}
      <div class="handoff">
        <select bind:value={handoffTarget}>
          <option value="">target…</option>
          {#each sessions.filter(s => s.name !== selectedAgent && !s.exited) as s}
            <option value={s.name}>{s.name}</option>
          {/each}
        </select>
        <button class="act" disabled={!handoffTarget || busy} onclick={doHandoff}>Go</button>
      </div>
    {/if}
    {#if actionErr}<div class="err">{actionErr}</div>{/if}

    {#if myEvents.length}
      <div class="sect-h">RECENT EVENTS</div>
      <ul class="events">
        {#each myEvents as e}
          <li><span class="ev-t">{fmtTime(e.at || e.created_at || e.ts)}</span><span class="ev-k">{e.kind || e.type || e.event || 'event'}</span><span class="ev-b">{(e.body || e.msg || e.message || '').slice(0, 90)}</span></li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>

<style>
  .inspector { height: 100%; overflow-y: auto; padding: 0.6rem 0.7rem; background: var(--m-panel); color: var(--m-fg); }
  .inspector::-webkit-scrollbar { width: 9px; }
  .inspector::-webkit-scrollbar-thumb { background: var(--m-scroll); border-radius: 5px; }
  .empty { color: var(--m-fg-faint); font-size: 0.8rem; padding: 1.5rem 0.5rem; }
  .ident { display: flex; align-items: center; gap: 0.6rem; padding: 0.4rem; border: 1px solid var(--m-border); border-left: 3px solid var(--id-color); border-radius: 6px; background: var(--m-panel-2); }
  .big-ic { font-size: 1.5rem; color: var(--id-color); }
  .ident-text { display: flex; flex-direction: column; min-width: 0; flex: 1; }
  .ident-text .nm { font-family: ui-monospace, monospace; font-weight: 700; font-size: 0.9rem; }
  .ident-text .role { font-size: 0.66rem; color: var(--m-fg-faint); text-transform: uppercase; letter-spacing: 0.04em; }
  .state { font-size: 0.6rem; font-weight: 700; letter-spacing: 0.08em; padding: 0.1rem 0.4rem; border-radius: 3px; }
  .state.live { color: var(--m-live); background: color-mix(in srgb, var(--m-live) 16%, transparent); }
  .state.paused { color: var(--m-paused); background: color-mix(in srgb, var(--m-paused) 16%, transparent); }
  .state.dead { color: var(--m-dead); background: var(--m-panel-3); }

  .sect-h { font-size: 0.6rem; letter-spacing: 0.14em; color: var(--m-fg-faint); font-weight: 700; margin: 0.9rem 0 0.4rem; }

  .scorecard { display: flex; flex-direction: column; gap: 0.3rem; }
  .sc-cell { display: grid; grid-template-columns: 1.2rem 1fr 1.6rem; align-items: center; gap: 0.4rem; }
  .sc-k { font-family: ui-monospace, monospace; font-size: 0.7rem; color: var(--m-fg-dim); }
  .sc-bar { height: 6px; background: var(--m-panel-3); border-radius: 3px; overflow: hidden; }
  .sc-fill { height: 100%; background: linear-gradient(90deg, var(--m-accent-2), var(--m-accent)); }
  .sc-v { font-family: ui-monospace, monospace; font-size: 0.7rem; text-align: right; color: var(--m-fg); }

  .kv { display: grid; grid-template-columns: auto 1fr; gap: 0.25rem 0.7rem; margin: 0; }
  .kv dt { font-size: 0.68rem; color: var(--m-fg-faint); }
  .kv dd { font-size: 0.72rem; margin: 0; text-align: right; color: var(--m-fg-dim); }
  .kv dd.mono { font-family: ui-monospace, monospace; }
  .kv dd.trunc { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%; }

  .verdict { display: flex; flex-wrap: wrap; align-items: center; gap: 0.4rem; font-size: 0.72rem; }
  .v-tag { font-weight: 700; color: var(--m-accent); }
  .v-msg { color: var(--m-fg-dim); }
  .v-at { color: var(--m-fg-faint); margin-left: auto; font-family: ui-monospace, monospace; }

  .actions { display: grid; grid-template-columns: 1fr 1fr; gap: 0.3rem; }
  .act {
    background: var(--m-panel-3); color: var(--m-fg); border: 1px solid var(--m-border-strong);
    border-radius: 5px; padding: 0.35rem 0.4rem; font: inherit; font-size: 0.72rem; cursor: pointer;
  }
  .act:hover:not(:disabled) { border-color: var(--m-accent-2); color: var(--m-accent-2); }
  .act:disabled { opacity: 0.45; cursor: progress; }
  .act.danger:hover:not(:disabled) { border-color: var(--m-danger); color: var(--m-danger); }
  .handoff { display: flex; gap: 0.3rem; margin-top: 0.4rem; }
  .handoff select { flex: 1; background: var(--m-bg); color: var(--m-fg); border: 1px solid var(--m-border-strong); border-radius: 4px; font: inherit; font-size: 0.72rem; padding: 0.25rem; }
  .err { color: var(--m-danger); font-size: 0.72rem; margin-top: 0.4rem; }

  .events { list-style: none; display: flex; flex-direction: column; gap: 0.2rem; }
  .events li { display: grid; grid-template-columns: auto auto 1fr; gap: 0.4rem; font-size: 0.66rem; align-items: baseline; }
  .ev-t { font-family: ui-monospace, monospace; color: var(--m-fg-faint); }
  .ev-k { color: var(--m-accent-2); font-weight: 600; }
  .ev-b { color: var(--m-fg-dim); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
</style>

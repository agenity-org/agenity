<!--
  CalmInspector — identity + scorecard + lifecycle actions for an agent.
  Reads from the live session object (real /api/v1/sessions data); writes
  via the documented session-action endpoints.
-->
<script>
  import { agentIdentity } from '../../lib/agentIdentity.js';
  import WidgetSpider from '../v08/widgets/WidgetSpider.svelte';
  import WidgetAgentPrompt from '../v08/widgets/WidgetAgentPrompt.svelte';

  let { boundSession = null, sessions = [] } = $props();

  const API = '/api/v1';
  let busy = $state('');
  let actionError = $state('');
  let handoffOpen = $state(false);
  let handoffTarget = $state('');
  let spiderOpen = $state(false);
  let promptOpen = $state(false);

  let s = $derived(boundSession);
  let id = $derived(s ? agentIdentity(s) : null);

  // Scorecard geomean (G,V,F,E,D each 0..5-ish) → single 0..100 figure.
  const SCORE_KEYS = ['G', 'V', 'F', 'E', 'D'];
  let geomean = $derived.by(() => {
    if (!s?.scorecard) return null;
    const vals = SCORE_KEYS.map((k) => Number(s.scorecard[k])).filter((n) => n > 0);
    if (!vals.length) return null;
    const prod = vals.reduce((a, b) => a * b, 1);
    return Math.round(Math.pow(prod, 1 / vals.length) * 20); // ~0..100
  });

  function fmtIdle(sec) {
    if (sec == null) return '—';
    if (sec < 60) return `${Math.round(sec)}s`;
    if (sec < 3600) return `${Math.round(sec / 60)}m`;
    return `${Math.round(sec / 3600)}h`;
  }
  function fmtBytes(n) {
    if (!n) return '0 B';
    if (n < 1024) return `${n} B`;
    if (n < 1048576) return `${(n / 1024).toFixed(1)} KB`;
    return `${(n / 1048576).toFixed(1)} MB`;
  }

  async function act(kind) {
    if (!s) return;
    busy = kind; actionError = '';
    let url, method = 'POST', body = null;
    // Backend pause is POST (JSON {paused}) — NOT PATCH (server.go #1640).
    if (kind === 'pause') { url = `${API}/sessions/${s.name}/pause`; method = 'POST'; body = JSON.stringify({ paused: true }); }
    else if (kind === 'unpause') { url = `${API}/sessions/${s.name}/pause`; method = 'POST'; body = JSON.stringify({ paused: false }); }
    else if (kind === 'restart') { url = `${API}/sessions/${s.name}/restart`; }
    else if (kind === 'stop') { url = `${API}/sessions/${s.name}`; method = 'DELETE'; }
    try {
      const r = await fetch(url, { method, headers: body ? { 'Content-Type': 'application/json' } : {}, body });
      if (!r.ok) { const e = await r.json().catch(() => ({})); actionError = e.error || `HTTP ${r.status}`; }
    } catch (e) { actionError = String(e); }
    busy = '';
  }

  async function doHandoff() {
    if (!s || !handoffTarget) return;
    busy = 'handoff'; actionError = '';
    try {
      const r = await fetch(`${API}/sessions/${s.name}/handoff`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ target: handoffTarget }),
      });
      if (!r.ok) { const e = await r.json().catch(() => ({})); actionError = e.error || `HTTP ${r.status}`; }
      else { handoffOpen = false; handoffTarget = ''; }
    } catch (e) { actionError = String(e); }
    busy = '';
  }

  let handoffOptions = $derived(sessions.filter((x) => x.name !== s?.name && !x.exited));
</script>

<div class="inspector">
  {#if !s}
    <div class="empty">
      <div class="empty-glyph">◉</div>
      <p>No agent in focus.</p>
      <p class="hint">Pick an agent in this pane's header or click one in the rail.</p>
    </div>
  {:else}
    <div class="ident">
      <span class="badge" style={`background:${id.color}`}>{id.icon}</span>
      <div class="ident-text">
        <div class="ident-name">{s.name}</div>
        <div class="ident-sub">
          <span>{s.role || 'agent'}</span>
          {#if s.team}<span class="sep">·</span><span>{s.team}</span>{/if}
        </div>
      </div>
      <span class="state {s.exited ? 'exited' : s.paused ? 'paused' : s.live === false ? 'offline' : 'live'}">
        {s.exited ? 'exited' : s.paused ? 'paused' : s.live === false ? 'offline' : 'live'}
      </span>
    </div>

    {#if geomean != null}
      <div class="score">
        <div class="score-head">
          <span>Scorecard</span>
          <span class="score-num">{geomean}</span>
        </div>
        <div class="score-bar"><span style={`width:${geomean}%`}></span></div>
        <div class="score-grid">
          {#each SCORE_KEYS as k}
            <div class="score-cell"><span class="sk">{k}</span><span class="sv">{s.scorecard?.[k] ?? '—'}</span></div>
          {/each}
        </div>
      </div>
    {/if}

    <div class="facts">
      <div class="fact"><span>Idle</span><b>{fmtIdle(s.idle_seconds)}</b></div>
      <div class="fact"><span>5m I/O</span><b>{fmtBytes(s.bytes_5m)}</b></div>
      <div class="fact"><span>Total</span><b>{fmtBytes(s.total_bytes)}</b></div>
      <div class="fact"><span>PID</span><b>{s.pid ?? '—'}</b></div>
      {#if s.branch}<div class="fact wide"><span>Branch</span><b class="mono">{s.branch}</b></div>{/if}
      {#if s.cwd}<div class="fact wide"><span>CWD</span><b class="mono ellip" title={s.cwd}>{s.cwd}</b></div>{/if}
    </div>

    {#if s.last_verdict}
      <div class="verdict">
        <div class="verdict-head">Last verdict · {s.last_verdict}</div>
        {#if s.last_verdict_msg}<div class="verdict-msg">{s.last_verdict_msg}</div>{/if}
      </div>
    {/if}

    <div class="actions">
      {#if s.paused}
        <button class="act" disabled={!!busy} onclick={() => act('unpause')}>{busy === 'unpause' ? '…' : 'Resume'}</button>
      {:else}
        <button class="act" disabled={!!busy} onclick={() => act('pause')}>{busy === 'pause' ? '…' : 'Pause'}</button>
      {/if}
      <button class="act" disabled={!!busy} onclick={() => act('restart')}>{busy === 'restart' ? '…' : 'Restart'}</button>
      <button class="act" disabled={!!busy || !handoffOptions.length} onclick={() => (handoffOpen = !handoffOpen)}>Handoff</button>
      <button class="act danger" disabled={!!busy} onclick={() => act('stop')}>{busy === 'stop' ? '…' : 'Stop'}</button>
    </div>

    {#if handoffOpen}
      <div class="handoff">
        <select bind:value={handoffTarget}>
          <option value="">choose target…</option>
          {#each handoffOptions as o}<option value={o.name}>{o.name}</option>{/each}
        </select>
        <button class="act" disabled={!handoffTarget || busy === 'handoff'} onclick={doHandoff}>Send</button>
      </div>
    {/if}

    {#if actionError}<div class="err">{actionError}</div>{/if}

    <!-- Expandable operator surfaces for THIS agent: full scorecard radar
         + a live prompt editor. Both hit the real endpoints (WidgetSpider
         reads the scorecard; WidgetAgentPrompt POSTs poke-prompt). -->
    <div class="more">
      <button class="more-tog {spiderOpen ? 'on' : ''}" onclick={() => (spiderOpen = !spiderOpen)}>
        <span>Scorecard radar</span><span class="chev">{spiderOpen ? '▾' : '▸'}</span>
      </button>
      {#if spiderOpen}
        <div class="more-body spider"><WidgetSpider selectedAgent={s.name} {sessions} /></div>
      {/if}

      <button class="more-tog {promptOpen ? 'on' : ''}" onclick={() => (promptOpen = !promptOpen)}>
        <span>Prompt</span><span class="chev">{promptOpen ? '▾' : '▸'}</span>
      </button>
      {#if promptOpen}
        <div class="more-body prompt"><WidgetAgentPrompt agent={s} /></div>
      {/if}
    </div>

    {#if s.github_url}
      <a class="link" href={s.github_url} target="_blank" rel="noreferrer">Open repo ↗</a>
    {/if}
  {/if}
</div>

<style>
  .inspector { height: 100%; overflow: auto; padding: 0.9rem; display: flex; flex-direction: column; gap: 0.85rem; color: var(--calm-fg); }
  .empty { margin: auto; text-align: center; color: var(--calm-fg-faint); display: flex; flex-direction: column; gap: 0.3rem; }
  .empty-glyph { font-size: 2rem; opacity: 0.5; }
  .empty .hint { font-size: 0.78rem; }

  .ident { display: flex; align-items: center; gap: 0.6rem; }
  .badge { width: 38px; height: 38px; border-radius: 8px; display: grid; place-items: center; font-size: 1.1rem; color: #0a0a0a; font-weight: 700; flex: 0 0 auto; }
  .ident-text { min-width: 0; flex: 1; }
  .ident-name { font-weight: 700; font-size: 0.98rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .ident-sub { color: var(--calm-fg-muted); font-size: 0.78rem; display: flex; gap: 0.3rem; }
  .ident-sub .sep { opacity: 0.5; }
  .state { font-size: 0.62rem; text-transform: uppercase; letter-spacing: 0.04em; padding: 0.1rem 0.45rem; border-radius: 8px; font-weight: 700; flex: 0 0 auto; }
  .state.live { color: var(--calm-ok); background: color-mix(in srgb, var(--calm-ok) 16%, transparent); }
  .state.paused { color: var(--calm-warn); background: color-mix(in srgb, var(--calm-warn) 16%, transparent); }
  .state.exited, .state.offline { color: var(--calm-fg-faint); background: color-mix(in srgb, var(--calm-fg-faint) 16%, transparent); }

  .score { background: var(--calm-surface-2); border: 1px solid var(--calm-border); border-radius: 6px; padding: 0.7rem; }
  .score-head { display: flex; justify-content: space-between; align-items: baseline; font-size: 0.78rem; color: var(--calm-fg-muted); }
  .score-num { font-size: 1.3rem; font-weight: 800; color: var(--calm-fg); }
  .score-bar { height: 6px; background: var(--calm-border); border-radius: 999px; margin: 0.5rem 0; overflow: hidden; }
  .score-bar span { display: block; height: 100%; background: linear-gradient(90deg, var(--calm-accent-2), var(--calm-accent)); border-radius: 999px; }
  .score-grid { display: grid; grid-template-columns: repeat(5, 1fr); gap: 0.3rem; }
  .score-cell { text-align: center; background: var(--calm-chip); border-radius: 6px; padding: 0.25rem 0; }
  .sk { display: block; font-size: 0.62rem; color: var(--calm-fg-faint); }
  .sv { display: block; font-weight: 700; font-size: 0.85rem; }

  .facts { display: grid; grid-template-columns: 1fr 1fr; gap: 0.4rem; }
  .fact { background: var(--calm-surface-2); border: 1px solid var(--calm-border); border-radius: 6px; padding: 0.4rem 0.55rem; display: flex; flex-direction: column; gap: 0.1rem; min-width: 0; }
  .fact.wide { grid-column: 1 / -1; }
  .fact span { font-size: 0.66rem; text-transform: uppercase; letter-spacing: 0.04em; color: var(--calm-fg-faint); }
  .fact b { font-size: 0.85rem; font-weight: 600; }
  .mono { font-family: ui-monospace, monospace; font-size: 0.76rem; }
  .ellip { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

  .verdict { background: var(--calm-surface-2); border: 1px solid var(--calm-border); border-radius: 6px; padding: 0.5rem 0.6rem; }
  .verdict-head { font-size: 0.72rem; font-weight: 700; color: var(--calm-fg-muted); }
  .verdict-msg { font-size: 0.78rem; margin-top: 0.2rem; color: var(--calm-fg); }

  .actions { display: grid; grid-template-columns: 1fr 1fr; gap: 0.4rem; }
  .act { padding: 0.45rem 0.5rem; background: var(--calm-chip); border: 1px solid var(--calm-border); color: var(--calm-fg); border-radius: 6px; font-size: 0.8rem; font-weight: 600; cursor: pointer; transition: background 0.14s ease; }
  .act:hover:not(:disabled) { background: var(--calm-chip-hover); }
  .act:disabled { opacity: 0.45; cursor: not-allowed; }
  .act.danger { color: var(--calm-danger); border-color: color-mix(in srgb, var(--calm-danger) 35%, var(--calm-border)); }
  .act.danger:hover:not(:disabled) { background: color-mix(in srgb, var(--calm-danger) 14%, transparent); }

  .handoff { display: flex; gap: 0.4rem; }
  .handoff select { flex: 1; padding: 0.4rem; background: var(--calm-input); color: var(--calm-fg); border: 1px solid var(--calm-border); border-radius: 6px; font-size: 0.8rem; }

  .err { color: var(--calm-danger); font-size: 0.78rem; background: color-mix(in srgb, var(--calm-danger) 10%, transparent); border-radius: 8px; padding: 0.4rem 0.55rem; }
  .link { color: var(--calm-accent-2); font-size: 0.8rem; text-decoration: none; }
  .link:hover { text-decoration: underline; }

  /* Expandable per-agent surfaces (radar + prompt editor). */
  .more { display: flex; flex-direction: column; gap: 0.4rem; }
  .more-tog {
    display: flex; align-items: center; justify-content: space-between;
    padding: 0.45rem 0.6rem; background: var(--calm-surface-2);
    border: 1px solid var(--calm-border); border-radius: 6px;
    color: var(--calm-fg-muted); font: inherit; font-size: 0.78rem; font-weight: 600;
    cursor: pointer; transition: background 0.14s ease, color 0.14s ease;
  }
  .more-tog:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }
  .more-tog.on { color: var(--calm-fg); border-color: var(--calm-border-strong); }
  .more-tog .chev { color: var(--calm-fg-faint); }
  .more-body { border: 1px solid var(--calm-border); border-radius: 6px; overflow: hidden; background: var(--calm-surface-2); }
  .more-body.prompt { height: 320px; display: flex; flex-direction: column; }
  .more-body.prompt > :global(*) { flex: 1; min-height: 0; }
</style>

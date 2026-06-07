<!--
  CalmSettings — full-viewport settings / operator console for the calm
  dashboard. EVERY always-available backend capability has a home here so
  nothing is hidden behind a removed surface (founder hard requirement).

  Native calm sections:
    Appearance (theme + font), Mesh (federation peers), Tasks (A2A inbox),
    Activity (event stream), About.

  Re-hosted v08 operator widgets (they already hit the real /api-v08/v1
  endpoints — we only bridge the calm CSS tokens so they look native):
    Accounts & Providers → WidgetAccounts
    Roles                → WidgetRoleMatrix
    Skills               → WidgetAgentSkills
    Canon                → WidgetCanon
    Prompts              → WidgetAgentPrompt
    MCP Log              → WidgetMCPLog
    Kanban               → WidgetKanban
-->
<script>
  import { onMount } from 'svelte';
  import WidgetAccounts from '../v08/widgets/WidgetAccounts.svelte';
  import WidgetRoleMatrix from '../v08/widgets/WidgetRoleMatrix.svelte';
  import WidgetAgentSkills from '../v08/widgets/WidgetAgentSkills.svelte';
  import WidgetCanon from '../v08/widgets/WidgetCanon.svelte';
  import WidgetAgentPrompt from '../v08/widgets/WidgetAgentPrompt.svelte';
  import WidgetMCPLog from '../v08/widgets/WidgetMCPLog.svelte';
  import WidgetKanban from '../v08/widgets/WidgetKanban.svelte';
  import AuditLog from '../v09/AuditLog.svelte';

  let {
    theme = 'dark',
    fontSize = 14,
    ontheme = () => {},
    onfont = () => {},
    events = [],
    sessions = [],
    teams = [],
    focusedSession = null,
    onclose = () => {},
  } = $props();

  const API = '/api/v1';
  let section = $state('appearance');

  let peers = $state([]);
  let tasks = $state([]);
  let loadErr = $state('');

  async function loadMesh() {
    try {
      const r = await fetch(`${API}/peers`);
      if (r.ok) { const j = await r.json(); peers = j.peers || []; }
    } catch (e) { loadErr = String(e); }
  }
  async function loadTasks() {
    try {
      const r = await fetch(`${API}/tasks`);
      if (r.ok) { const j = await r.json(); tasks = j.tasks || []; }
    } catch (e) { loadErr = String(e); }
  }

  onMount(() => {
    loadMesh(); loadTasks();
    const iv = setInterval(() => { loadMesh(); loadTasks(); }, 4000);
    return () => clearInterval(iv);
  });

  // Grouped nav so the operator console reads as a real settings app, not
  // a flat wall of tabs. Every backend capability lives under one group.
  const GROUPS = [
    {
      title: 'Workspace',
      items: [
        { id: 'appearance', label: 'Appearance', glyph: '◐' },
        { id: 'about', label: 'About', glyph: 'ⓘ' },
      ],
    },
    {
      title: 'Fleet',
      items: [
        { id: 'accounts', label: 'Accounts & Providers', glyph: '⚿' },
        { id: 'roles', label: 'Roles', glyph: '🜲' },
        { id: 'kanban', label: 'Kanban', glyph: '▦' },
      ],
    },
    {
      title: 'Focused agent',
      items: [
        { id: 'prompts', label: 'Prompts', glyph: '✎' },
        { id: 'skills', label: 'Skills', glyph: '◇' },
        { id: 'canon', label: 'Canon', glyph: '❡' },
      ],
    },
    {
      title: 'Observe',
      items: [
        { id: 'mesh', label: 'Mesh', glyph: '⇄' },
        { id: 'tasks', label: 'Tasks', glyph: '☑' },
        { id: 'activity', label: 'Activity', glyph: '〜' },
        { id: 'mcplog', label: 'MCP Log', glyph: '☷' },
        { id: 'audit', label: 'Audit', glyph: '⊟' },
      ],
    },
  ];

  // Sections that re-host a v08 widget render edge-to-edge (the widget
  // owns its own header/padding), so we drop the section chrome for them.
  const WIDGET_SECTIONS = new Set([
    'accounts', 'roles', 'skills', 'canon', 'prompts', 'mcplog', 'kanban', 'audit',
  ]);

  function fmtTime(t) {
    if (!t) return '';
    try { return new Date(t).toLocaleTimeString(); } catch { return String(t); }
  }
</script>

<div class="settings-overlay" role="dialog" aria-label="Settings">
  <div class="settings-card">
    <aside class="settings-nav">
      <div class="nav-title">Console</div>
      <div class="nav-scroll">
        {#each GROUPS as grp}
          <div class="nav-group">{grp.title}</div>
          {#each grp.items as s}
            <button class="nav-item {section === s.id ? 'active' : ''}" onclick={() => (section = s.id)}>
              <span class="nav-glyph">{s.glyph}</span>{s.label}
            </button>
          {/each}
        {/each}
      </div>
      <button class="nav-close" onclick={onclose}>Close ✕</button>
    </aside>

    <main class="settings-body {WIDGET_SECTIONS.has(section) ? 'flush' : ''}">
      {#if section === 'appearance'}
        <h2>Appearance</h2>
        <p class="lede">Tune the calm workspace to your light. Saved to this browser.</p>

        <div class="field">
          <div class="field-label">Theme</div>
          <div class="theme-toggle">
            <button class="theme-opt {theme === 'light' ? 'on' : ''}" onclick={() => ontheme('light')}>
              <span class="swatch light"></span> Light
            </button>
            <button class="theme-opt {theme === 'dark' ? 'on' : ''}" onclick={() => ontheme('dark')}>
              <span class="swatch dark"></span> Dark
            </button>
          </div>
        </div>

        <div class="field">
          <div class="field-label">Text size <span class="fs-val">{fontSize}px</span></div>
          <div class="font-row">
            <button class="fbtn" onclick={() => onfont(-1)} aria-label="Smaller text">A−</button>
            <input type="range" min="9" max="22" value={fontSize} oninput={(e) => onfont(Number(e.target.value) - fontSize)} />
            <button class="fbtn" onclick={() => onfont(1)} aria-label="Larger text">A+</button>
          </div>
          <p class="aaa">Scales every pane, the rail, transcript, and the terminal alike.</p>
        </div>

      {:else if section === 'mesh'}
        <h2>Mesh</h2>
        <p class="lede">Federation peers discovered through the hub (read-only).</p>
        {#if peers.length === 0}
          <div class="hollow">No peers connected.</div>
        {:else}
          <div class="rows">
            {#each peers as p (p.sid)}
              <div class="srow">
                <span class="srow-ic">⇄</span>
                <div class="srow-main">
                  <div class="srow-name">{p.name || p.sid}</div>
                  <div class="srow-sub">{p.card?.url || p.sid}</div>
                </div>
                <span class="srow-meta">{fmtTime(p.syncedAt)}</span>
              </div>
            {/each}
          </div>
        {/if}

      {:else if section === 'tasks'}
        <h2>A2A Tasks</h2>
        <p class="lede">Inbound agent-to-agent task envelopes.</p>
        {#if tasks.length === 0}
          <div class="hollow">No tasks.</div>
        {:else}
          <div class="rows">
            {#each tasks as t (t.id)}
              <div class="srow">
                <span class="srow-ic">☑</span>
                <div class="srow-main">
                  <div class="srow-name">{t.method || 'task'}</div>
                  <div class="srow-sub mono">{t.id}</div>
                </div>
                <span class="state-chip {t.state}">{t.state || '—'}</span>
                <span class="srow-meta">{fmtTime(t.updatedAt)}</span>
              </div>
            {/each}
          </div>
        {/if}

      {:else if section === 'activity'}
        <h2>Activity</h2>
        <p class="lede">Live global event stream (last {events.length}).</p>
        {#if events.length === 0}
          <div class="hollow">Nothing yet.</div>
        {:else}
          <div class="rows">
            {#each [...events].slice(-80).reverse() as e, i (i)}
              <div class="erow">
                <span class="erow-kind">{e.kind || e.type || 'event'}</span>
                <span class="erow-body">{e.message || e.body || e.summary || JSON.stringify(e).slice(0, 120)}</span>
                <span class="srow-meta">{fmtTime(e.at || e.created_at)}</span>
              </div>
            {/each}
          </div>
        {/if}

      {:else if section === 'about'}
        <h2>calm</h2>
        <p class="lede">A spacious, focus-first dashboard for the chepherd mesh.</p>
        <ul class="about-list">
          <li><b>Focus stage</b> — one primary terminal, big and quiet.</li>
          <li><b>Peek strip</b> — every open pane as a live thumbnail; tap to bring forward.</li>
          <li><b>Split & resize</b> — split right / down, drag the seam to balance.</li>
          <li><b>Real data</b> — sessions, teams, transcript, peers, tasks over the live API.</li>
          <li><b>Full console</b> — accounts, roles, skills, canon, prompts, kanban, mcp-log; nothing hidden.</li>
        </ul>
        {#if loadErr}<div class="hollow">Note: {loadErr}</div>{/if}

      <!-- ===== re-hosted v08 operator widgets (flush, edge-to-edge) ===== -->
      {:else if section === 'accounts'}
        <div class="widget-host"><WidgetAccounts /></div>

      {:else if section === 'roles'}
        <div class="widget-host"><WidgetRoleMatrix /></div>

      {:else if section === 'kanban'}
        <div class="widget-host"><WidgetKanban agent={focusedSession} {sessions} team={focusedSession?.team} /></div>

      {:else if section === 'prompts'}
        {#if !focusedSession}
          <div class="widget-host pad"><div class="hollow">Focus an agent in the rail to read or edit its prompt.</div></div>
        {:else}
          <div class="widget-host"><WidgetAgentPrompt agent={focusedSession} /></div>
        {/if}

      {:else if section === 'skills'}
        {#if !focusedSession}
          <div class="widget-host pad"><div class="hollow">Focus an agent in the rail to edit its stat sheet.</div></div>
        {:else}
          <div class="widget-host"><WidgetAgentSkills agent={focusedSession} /></div>
        {/if}

      {:else if section === 'canon'}
        <div class="widget-host"><WidgetCanon agent={focusedSession} {teams} /></div>

      {:else if section === 'mcplog'}
        <div class="widget-host"><WidgetMCPLog {events} /></div>

      {:else if section === 'audit'}
        <div class="widget-host scroll"><AuditLog /></div>
      {/if}
    </main>
  </div>
</div>

<style>
  .settings-overlay {
    position: fixed; inset: 0; z-index: 1200;
    background: color-mix(in srgb, var(--calm-bg) 78%, transparent);
    backdrop-filter: blur(6px);
    display: flex; align-items: center; justify-content: center;
    padding: 2.5vh 2.5vw;
  }
  .settings-card {
    width: 100%; max-width: 980px; height: 100%; max-height: 760px;
    display: flex;
    background: var(--calm-surface);
    border: 1px solid var(--calm-border-strong);
    border-radius: 10px;
    overflow: hidden;
    box-shadow: var(--calm-shadow-lg);
  }
  .settings-nav {
    width: 216px; flex: 0 0 auto;
    background: var(--calm-surface-2);
    border-right: 1px solid var(--calm-border);
    padding: 1.1rem 0.7rem;
    display: flex; flex-direction: column; gap: 0.2rem;
    min-height: 0;
  }
  .nav-title { font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.1em; color: var(--calm-fg-faint); font-weight: 700; padding: 0 0.6rem 0.6rem; }
  .nav-scroll { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; gap: 0.1rem; }
  .nav-group {
    font-size: 0.62rem; text-transform: uppercase; letter-spacing: 0.1em;
    color: var(--calm-fg-faint); font-weight: 700;
    padding: 0.7rem 0.6rem 0.25rem;
  }
  .nav-group:first-child { padding-top: 0.1rem; }
  .nav-item {
    display: flex; align-items: center; gap: 0.55rem;
    padding: 0.5rem 0.65rem; border-radius: 6px;
    background: transparent; border: 0; color: var(--calm-fg-muted);
    font: inherit; font-size: 0.85rem; text-align: left; cursor: pointer;
  }
  .nav-item:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }
  .nav-item.active { background: color-mix(in srgb, var(--calm-accent) 16%, transparent); color: var(--calm-fg); font-weight: 600; }
  .nav-glyph { width: 1.1rem; text-align: center; }
  .nav-close { margin-top: 0.6rem; flex: 0 0 auto; padding: 0.55rem 0.65rem; background: transparent; border: 1px solid var(--calm-border); color: var(--calm-fg-muted); border-radius: 6px; cursor: pointer; font-size: 0.82rem; }
  .nav-close:hover { color: var(--calm-fg); border-color: var(--calm-border-strong); }

  .settings-body { flex: 1; min-width: 0; overflow: auto; padding: 1.6rem 1.8rem; color: var(--calm-fg); }
  /* Widget-backed sections render edge-to-edge: the widget owns its own
     header + padding + scroll, so the body must not double-pad or scroll. */
  .settings-body.flush { padding: 0; overflow: hidden; display: flex; flex-direction: column; }
  .widget-host { flex: 1; min-height: 0; min-width: 0; overflow: hidden; display: flex; flex-direction: column; }
  .widget-host > :global(*) { flex: 1; min-height: 0; }
  .widget-host.pad { padding: 1.6rem 1.8rem; }
  /* AuditLog (v09) brings its own dark card + internal layout; let it
     scroll within the host rather than be clipped. */
  .widget-host.scroll { overflow: auto; }
  h2 { font-size: 1.3rem; font-weight: 700; margin: 0 0 0.3rem; }
  .lede { color: var(--calm-fg-muted); font-size: 0.88rem; margin: 0 0 1.4rem; }

  .field { margin-bottom: 1.6rem; }
  .field-label { font-size: 0.82rem; font-weight: 600; margin-bottom: 0.55rem; display: flex; gap: 0.5rem; align-items: baseline; }
  .fs-val { color: var(--calm-fg-faint); font-weight: 500; font-size: 0.76rem; }

  .theme-toggle { display: flex; gap: 0.6rem; }
  .theme-opt {
    display: flex; align-items: center; gap: 0.5rem;
    padding: 0.6rem 0.9rem; border-radius: 6px;
    background: var(--calm-chip); border: 1px solid var(--calm-border);
    color: var(--calm-fg); cursor: pointer; font-size: 0.85rem; font-weight: 600;
  }
  .theme-opt:hover { background: var(--calm-chip-hover); }
  .theme-opt.on { border-color: var(--calm-accent); box-shadow: 0 0 0 1px var(--calm-accent); }
  .swatch { width: 18px; height: 18px; border-radius: 6px; border: 1px solid var(--calm-border-strong); }
  .swatch.light { background: linear-gradient(135deg, #ffffff, #e8eef6); }
  .swatch.dark { background: linear-gradient(135deg, #1a1f29, #0a0d12); }

  .font-row { display: flex; align-items: center; gap: 0.8rem; }
  .font-row input[type="range"] { flex: 1; accent-color: var(--calm-accent); }
  .fbtn { padding: 0.4rem 0.7rem; background: var(--calm-chip); border: 1px solid var(--calm-border); border-radius: 6px; color: var(--calm-fg); cursor: pointer; font-weight: 700; font-size: 0.8rem; }
  .fbtn:hover { background: var(--calm-chip-hover); }
  .aaa { color: var(--calm-fg-faint); font-size: 0.76rem; margin-top: 0.5rem; }

  .hollow { color: var(--calm-fg-faint); font-size: 0.86rem; padding: 1.4rem; text-align: center; background: var(--calm-surface-2); border: 1px dashed var(--calm-border); border-radius: 6px; }

  .rows { display: flex; flex-direction: column; gap: 0.4rem; }
  .srow { display: flex; align-items: center; gap: 0.7rem; padding: 0.6rem 0.7rem; background: var(--calm-surface-2); border: 1px solid var(--calm-border); border-radius: 6px; }
  .srow-ic { font-size: 1rem; color: var(--calm-accent-2); }
  .srow-main { flex: 1; min-width: 0; }
  .srow-name { font-weight: 600; font-size: 0.86rem; }
  .srow-sub { color: var(--calm-fg-faint); font-size: 0.74rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .srow-meta { color: var(--calm-fg-faint); font-size: 0.72rem; white-space: nowrap; }
  .mono { font-family: ui-monospace, monospace; }
  .state-chip { font-size: 0.66rem; text-transform: uppercase; padding: 0.12rem 0.45rem; border-radius: 8px; font-weight: 700; background: var(--calm-chip); color: var(--calm-fg-muted); }
  .state-chip.completed { color: var(--calm-ok); background: color-mix(in srgb, var(--calm-ok) 15%, transparent); }
  .state-chip.working, .state-chip.submitted { color: var(--calm-accent-2); background: color-mix(in srgb, var(--calm-accent-2) 15%, transparent); }
  .state-chip.failed { color: var(--calm-danger); background: color-mix(in srgb, var(--calm-danger) 15%, transparent); }

  .erow { display: grid; grid-template-columns: auto 1fr auto; gap: 0.6rem; align-items: baseline; padding: 0.4rem 0.6rem; border-bottom: 1px solid var(--calm-border); }
  .erow-kind { font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.04em; color: var(--calm-accent-2); font-weight: 700; }
  .erow-body { font-size: 0.8rem; color: var(--calm-fg); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

  .about-list { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 0.6rem; }
  .about-list li { font-size: 0.88rem; color: var(--calm-fg-muted); }
  .about-list b { color: var(--calm-fg); }
</style>

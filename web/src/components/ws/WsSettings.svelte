<!--
  WsSettings — the CONFIG-ONLY operator console for the WORKSPACES layout.

  Observability (Kanban / Events / Tasks / Mesh / MCP Log) are WORKSPACE
  PANE TYPES, NOT here — MCP Log moved out per the operator's punch-list
  (#6). This modal holds CONFIGURATION only, reached from the 👤 user menu:

    Workspace : Appearance (incl. text size), About
    Fleet     : Accounts & Providers, Roles, Grants
    Agent     : Prompts, Skills (full catalogue), Canon (org default), Review axes
    Runtime   : Runtime / Global config (global-md + claude-profile + claude-status)

  Notes:
    · Skills (#8)   → GET /api/v1/skills shows the WHOLE catalogue, not just
                       the focused agent's.
    · Canon (#7)    → the DEFAULT / org-level canon teams inherit; per-team
                       canon is edited via Team settings (right-click a team).
    · Grants        → GET/POST /api/v1/grants, PATCH/DELETE /api/v1/grants/{id}
    · Review axes    → GET /api/v1/reviews/{target}  (NO trailing slash after
                       the target — the backend trims only "/api/v1/reviews/"
                       then looks up the remainder verbatim, so a trailing
                       slash would query "{target}/" and never match)

  Re-hosts v08 widgets unchanged (they already hit real endpoints); we only
  bridge calm CSS tokens. ESC closes the modal (root owns the listener).
-->
<script>
  import WidgetAccounts from '../v08/widgets/WidgetAccounts.svelte';
  import WidgetRoleMatrix from '../v08/widgets/WidgetRoleMatrix.svelte';
  import WidgetCanon from '../v08/widgets/WidgetCanon.svelte';
  import WidgetAgentPrompt from '../v08/widgets/WidgetAgentPrompt.svelte';

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

  // ---- Runtime / Global config ----
  let globalMd = $state('');
  let claudeProfile = $state(null);
  let claudeStatus = $state(null);
  let runtimeErr = $state('');
  let runtimeLoaded = $state(false);
  async function loadRuntime() {
    runtimeErr = '';
    try {
      const [g, p, st] = await Promise.all([
        fetch(`${API}/runtime/global-md`).then((r) => (r.ok ? r.json() : null)).catch(() => null),
        fetch(`${API}/runtime/claude-profile`).then((r) => (r.ok ? r.json() : null)).catch(() => null),
        fetch(`${API}/runtime/claude-status`).then((r) => (r.ok ? r.json() : null)).catch(() => null),
      ]);
      globalMd = g?.body ?? '';
      claudeProfile = p;
      claudeStatus = st;
      runtimeLoaded = true;
    } catch (e) { runtimeErr = String(e); }
  }

  // ---- Grants ----
  let grants = $state([]);
  let grantsErr = $state('');
  let grantBusy = $state('');
  let newGrant = $state({ granter_org: '', grantee_org: '', scope: '', granted_by: 'operator' });
  async function loadGrants() {
    grantsErr = '';
    try {
      const r = await fetch(`${API}/grants`);
      if (r.ok) { const j = await r.json(); grants = j.grants || []; } else grantsErr = `HTTP ${r.status}`;
    } catch (e) { grantsErr = String(e); }
  }
  async function createGrant() {
    if (!newGrant.granter_org || !newGrant.grantee_org || !newGrant.scope) { grantsErr = 'all fields required'; return; }
    grantBusy = 'new'; grantsErr = '';
    try {
      const r = await fetch(`${API}/grants`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(newGrant) });
      if (!r.ok) { const e = await r.json().catch(() => ({})); grantsErr = e.error || `HTTP ${r.status}`; }
      else { newGrant = { granter_org: '', grantee_org: '', scope: '', granted_by: 'operator' }; await loadGrants(); }
    } catch (e) { grantsErr = String(e); }
    grantBusy = '';
  }
  async function patchGrant(id, status) {
    grantBusy = id; grantsErr = '';
    try {
      const r = await fetch(`${API}/grants/${id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ status }) });
      if (!r.ok) { const e = await r.json().catch(() => ({})); grantsErr = e.error || `HTTP ${r.status}`; } else await loadGrants();
    } catch (e) { grantsErr = String(e); }
    grantBusy = '';
  }
  async function deleteGrant(id) {
    grantBusy = id; grantsErr = '';
    try {
      const r = await fetch(`${API}/grants/${id}`, { method: 'DELETE' });
      if (!r.ok) { const e = await r.json().catch(() => ({})); grantsErr = e.error || `HTTP ${r.status}`; } else await loadGrants();
    } catch (e) { grantsErr = String(e); }
    grantBusy = '';
  }

  // ---- Review axes (NO trailing slash after target — see header note) ----
  let reviews = $state([]);
  let reviewsErr = $state('');
  let reviewTarget = $state('');
  async function loadReviews(target) {
    reviewsErr = ''; reviews = [];
    if (!target) return;
    try {
      const r = await fetch(`${API}/reviews/${encodeURIComponent(target)}`);
      if (r.ok) { const j = await r.json(); reviews = j.reviews || []; } else reviewsErr = `HTTP ${r.status}`;
    } catch (e) { reviewsErr = String(e); }
  }

  // ---- Skills — the FULL catalogue (#8), not just the focused agent's ----
  let allSkills = $state([]);
  let skillsErr = $state('');
  let skillsLoaded = $state(false);
  let skillQuery = $state('');
  async function loadSkills() {
    skillsErr = '';
    try {
      const r = await fetch(`${API}/skills`);
      if (r.ok) { const j = await r.json(); allSkills = Array.isArray(j) ? j : (j.skills || []); skillsLoaded = true; }
      else skillsErr = `HTTP ${r.status}`;
    } catch (e) { skillsErr = String(e); }
  }
  let skillsFiltered = $derived.by(() => {
    const q = skillQuery.trim().toLowerCase();
    const arr = [...allSkills].sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0) || (a.name || a.id || '').localeCompare(b.name || b.id || ''));
    if (!q) return arr;
    return arr.filter((s) =>
      (s.name || '').toLowerCase().includes(q) ||
      (s.id || '').toLowerCase().includes(q) ||
      (s.description || '').toLowerCase().includes(q) ||
      (s.tags || []).some((t) => String(t).toLowerCase().includes(q))
    );
  });

  $effect(() => {
    if (section === 'runtime' && !runtimeLoaded) loadRuntime();
    if (section === 'grants' && grants.length === 0 && !grantsErr) loadGrants();
    if (section === 'skills' && !skillsLoaded && !skillsErr) loadSkills();
    if (section === 'reviews' && focusedSession && reviewTarget !== focusedSession.name) {
      reviewTarget = focusedSession.name;
      loadReviews(reviewTarget);
    }
  });

  const GROUPS = [
    { title: 'Workspace', items: [
      { id: 'appearance', label: 'Appearance', glyph: '◐' },
      { id: 'about', label: 'About', glyph: 'ⓘ' },
    ] },
    { title: 'Fleet', items: [
      { id: 'accounts', label: 'Accounts & Providers', glyph: '⚿' },
      { id: 'roles', label: 'Roles', glyph: '🜲' },
      { id: 'grants', label: 'Grants', glyph: '⇄' },
    ] },
    { title: 'Agent', items: [
      { id: 'prompts', label: 'Prompts', glyph: '✎' },
      { id: 'skills', label: 'Skills', glyph: '◇' },
      { id: 'canon', label: 'Canon', glyph: '❡' },
      { id: 'reviews', label: 'Review axes', glyph: '⚖' },
    ] },
    { title: 'Runtime', items: [
      { id: 'runtime', label: 'Runtime / Global', glyph: '⚙' },
    ] },
  ];
  const WIDGET_SECTIONS = new Set(['accounts', 'roles', 'canon', 'prompts']);

  function fmtTime(t) {
    if (!t) return '';
    try { return new Date(typeof t === 'number' ? t * (t < 1e12 ? 1000 : 1) : t).toLocaleString(); } catch { return String(t); }
  }
  const AXES = ['G', 'V', 'F', 'E', 'D'];
</script>

<div class="settings-overlay" role="dialog" aria-label="Settings" aria-modal="true">
  <div class="settings-card">
    <aside class="settings-nav">
      <div class="nav-title">Settings</div>
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
        <p class="lede">Tune the workspaces to your light. Saved to this browser.</p>
        <div class="field">
          <div class="field-label">Theme</div>
          <div class="theme-toggle">
            <button class="theme-opt {theme === 'light' ? 'on' : ''}" onclick={() => ontheme('light')}><span class="swatch light"></span> Light</button>
            <button class="theme-opt {theme === 'dark' ? 'on' : ''}" onclick={() => ontheme('dark')}><span class="swatch dark"></span> Dark</button>
          </div>
        </div>
        <div class="field">
          <div class="field-label">Text size <span class="fs-val">{fontSize}px</span></div>
          <div class="font-row">
            <button class="fbtn" onclick={() => onfont(-1)} aria-label="Smaller text">A−</button>
            <input type="range" min="9" max="22" value={fontSize} oninput={(e) => onfont(Number(e.target.value) - fontSize)} />
            <button class="fbtn" onclick={() => onfont(1)} aria-label="Larger text">A+</button>
          </div>
          <p class="aaa">Scales every pane, the roster, the inspector, and the live terminal alike.</p>
        </div>

      {:else if section === 'about'}
        <h2>workspaces</h2>
        <p class="lede">An OS-virtual-desktop dashboard for the chepherd mesh.</p>
        <ul class="about-list">
          <li><b>Workspaces</b> — named, saved desktops; each holds its own split-tree of panes.</li>
          <li><b>Switch fast</b> — Ctrl+1..9 jump, Ctrl+` / Ctrl+Tab cycle, or click the strip.</li>
          <li><b>Compose freely</b> — split / resize / collapse / maximize panes; 7 pane types.</li>
          <li><b>Persisted</b> — every workspace + layout saved per-user to localStorage.</li>
          <li><b>Nothing hidden</b> — accounts, roles, grants, skills, canon, prompts, reviews, runtime config, mcp-log.</li>
        </ul>

      {:else if section === 'runtime'}
        <h2>Runtime / Global config</h2>
        <p class="lede">The operator's global instructions + Claude login state (read-only).</p>
        {#if runtimeErr}<div class="hollow">Note: {runtimeErr}</div>{/if}
        <div class="acct-cards">
          <div class="acct-card">
            <div class="acct-title">Claude account</div>
            {#if claudeProfile || claudeStatus}
              <div class="kv"><span>Logged in</span><b>{(claudeProfile?.logged_in ?? claudeStatus?.logged_in) ? 'yes' : 'no'}</b></div>
              {#if claudeProfile?.email}<div class="kv"><span>Email</span><b>{claudeProfile.email}</b></div>{/if}
              <div class="kv"><span>Subscription</span><b>{claudeProfile?.subscription_type || claudeStatus?.subscription_type || '—'}</b></div>
              <div class="kv"><span>Rate limit</span><b>{claudeProfile?.rate_limit_tier || claudeStatus?.rate_limit_tier || '—'}</b></div>
              {#if claudeStatus?.expires_at}<div class="kv"><span>Expires</span><b>{fmtTime(claudeStatus.expires_at)}</b></div>{/if}
              {#if claudeStatus?.scopes?.length}<div class="kv"><span>Scopes</span><b class="mono">{claudeStatus.scopes.join(', ')}</b></div>{/if}
            {:else}
              <div class="hollow sm">No Claude profile reported.</div>
            {/if}
          </div>
        </div>
        <div class="field">
          <div class="field-label">Global CLAUDE.md <span class="fs-val">~/.claude/CLAUDE.md</span></div>
          {#if globalMd}<pre class="codeblock">{globalMd}</pre>{:else}<div class="hollow sm">Empty or unavailable.</div>{/if}
        </div>

      {:else if section === 'grants'}
        <h2>Grants</h2>
        <p class="lede">Federation permission grants between orgs. Create, approve, revoke.</p>
        {#if grantsErr}<div class="hollow">Note: {grantsErr}</div>{/if}
        <div class="grant-new">
          <input placeholder="granter_org" bind:value={newGrant.granter_org} />
          <input placeholder="grantee_org" bind:value={newGrant.grantee_org} />
          <input placeholder="scope" bind:value={newGrant.scope} />
          <button class="act" disabled={grantBusy === 'new'} onclick={createGrant}>{grantBusy === 'new' ? '…' : '+ Grant'}</button>
        </div>
        {#if grants.length === 0}
          <div class="hollow">No grants.</div>
        {:else}
          <div class="rows">
            {#each grants as g (g.id)}
              <div class="srow">
                <div class="srow-main">
                  <div class="srow-name">{g.granter_org} → {g.grantee_org}</div>
                  <div class="srow-sub mono">{g.scope} · {fmtTime(g.issued_at)}{g.granted_by ? ` · by ${g.granted_by}` : ''}</div>
                </div>
                <span class="state-chip {g.status}">{g.status || '—'}</span>
                {#if g.status !== 'approved'}
                  <button class="mini" disabled={grantBusy === g.id} onclick={() => patchGrant(g.id, 'approved')} title="Approve">✓</button>
                {:else}
                  <button class="mini" disabled={grantBusy === g.id} onclick={() => patchGrant(g.id, 'revoked')} title="Revoke">⦸</button>
                {/if}
                <button class="mini danger" disabled={grantBusy === g.id} onclick={() => deleteGrant(g.id)} title="Delete">✕</button>
              </div>
            {/each}
          </div>
        {/if}

      {:else if section === 'reviews'}
        <h2>Review axes</h2>
        <p class="lede">Historical GVFED reviews for the focused agent.</p>
        {#if !focusedSession}
          <div class="hollow">Focus an agent in the roster to load its reviews.</div>
        {:else}
          <div class="rev-head">
            <span class="rev-target">{focusedSession.name}</span>
            <button class="mini" onclick={() => loadReviews(focusedSession.name)} title="Refresh">↻</button>
          </div>
          {#if reviewsErr}<div class="hollow">Note: {reviewsErr}</div>{/if}
          {#if reviews.length === 0}
            <div class="hollow">No reviews recorded.</div>
          {:else}
            <div class="rows">
              {#each reviews as rv (rv.id)}
                <div class="rev-row">
                  <div class="rev-row-top">
                    <span class="rev-reviewer">{rv.reviewer || 'reviewer'}</span>
                    {#if rv.verdict}<span class="state-chip {rv.verdict}">{rv.verdict}</span>{/if}
                    <span class="srow-meta">{fmtTime(rv.at)}</span>
                  </div>
                  <div class="rev-axes">
                    {#each AXES as ax}<span class="axc"><span class="axk">{ax}</span><span class="axv">{rv.axes?.[ax] ?? '—'}</span></span>{/each}
                  </div>
                  {#if rv.note}<div class="rev-note">{rv.note}</div>{/if}
                </div>
              {/each}
            </div>
          {/if}
        {/if}

      {:else if section === 'accounts'}
        <div class="widget-host"><WidgetAccounts /></div>
      {:else if section === 'roles'}
        <div class="widget-host"><WidgetRoleMatrix /></div>
      {:else if section === 'prompts'}
        {#if !focusedSession}
          <div class="widget-host pad"><div class="hollow">Focus an agent to read or edit its prompt.</div></div>
        {:else}
          <div class="widget-host"><WidgetAgentPrompt agent={focusedSession} /></div>
        {/if}
      {:else if section === 'skills'}
        <h2>Skills</h2>
        <p class="lede">The full skill catalogue available to the fleet — every built-in and user-defined skill, not just one agent's.</p>
        <div class="skills-bar">
          <input class="skills-search" placeholder="Filter skills…" bind:value={skillQuery} aria-label="Filter skills" />
          <span class="skills-count">{skillsFiltered.length} / {allSkills.length}</span>
          <button class="mini" title="Refresh" onclick={() => { skillsLoaded = false; loadSkills(); }}>↻</button>
        </div>
        {#if skillsErr}<div class="hollow">Note: {skillsErr}</div>{/if}
        {#if allSkills.length === 0 && !skillsErr}
          <div class="hollow">No skills reported.</div>
        {:else}
          <div class="rows">
            {#each skillsFiltered as sk (sk.id)}
              <div class="srow">
                <span class="skill-ic">{sk.icon || '◇'}</span>
                <div class="srow-main">
                  <div class="srow-name">{sk.name || sk.id}</div>
                  {#if sk.description}<div class="srow-sub">{sk.description}</div>{/if}
                  {#if sk.tags?.length}<div class="skill-tags">{#each sk.tags as tg}<span class="tag">{tg}</span>{/each}</div>{/if}
                </div>
                <span class="state-chip {sk.read_only ? 'pending' : 'approved'}">{sk.read_only ? 'built-in' : 'custom'}</span>
              </div>
            {/each}
          </div>
        {/if}
      {:else if section === 'canon'}
        <h2>Canon (org default — teams inherit)</h2>
        <p class="lede">The DEFAULT / org-level canon every team inherits. To edit a single team's canon, right-click that team in a Sessions pane → Team settings.</p>
        <div class="widget-host canon-host"><WidgetCanon agent={focusedSession} {teams} /></div>
      {/if}
    </main>
  </div>
</div>

<style>
  .settings-overlay { position: fixed; inset: 0; z-index: 1200; background: color-mix(in srgb, var(--calm-bg) 78%, transparent); backdrop-filter: blur(6px); display: flex; align-items: center; justify-content: center; padding: 2.5vh 2.5vw; }
  .settings-card { width: 100%; max-width: 1000px; height: 100%; max-height: 780px; display: flex; background: var(--calm-surface); border: 1px solid var(--calm-border-strong); border-radius: 10px; overflow: hidden; box-shadow: var(--calm-shadow-lg); }
  .settings-nav { width: 220px; flex: 0 0 auto; background: var(--calm-surface-2); border-right: 1px solid var(--calm-border); padding: 1.1rem 0.7rem; display: flex; flex-direction: column; gap: 0.2rem; min-height: 0; }
  .nav-title { font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.1em; color: var(--calm-fg-faint); font-weight: 700; padding: 0 0.6rem 0.6rem; }
  .nav-scroll { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; gap: 0.1rem; }
  .nav-group { font-size: 0.62rem; text-transform: uppercase; letter-spacing: 0.1em; color: var(--calm-fg-faint); font-weight: 700; padding: 0.7rem 0.6rem 0.25rem; }
  .nav-group:first-child { padding-top: 0.1rem; }
  .nav-item { display: flex; align-items: center; gap: 0.55rem; padding: 0.5rem 0.65rem; border-radius: 6px; background: transparent; border: 0; color: var(--calm-fg-muted); font: inherit; font-size: 0.85rem; text-align: left; cursor: pointer; }
  .nav-item:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }
  .nav-item.active { background: color-mix(in srgb, var(--calm-accent) 16%, transparent); color: var(--calm-fg); font-weight: 600; }
  .nav-glyph { width: 1.1rem; text-align: center; }
  .nav-close { margin-top: 0.6rem; flex: 0 0 auto; padding: 0.55rem 0.65rem; background: transparent; border: 1px solid var(--calm-border); color: var(--calm-fg-muted); border-radius: 6px; cursor: pointer; font-size: 0.82rem; }
  .nav-close:hover { color: var(--calm-fg); border-color: var(--calm-border-strong); }

  .settings-body { flex: 1; min-width: 0; overflow: auto; padding: 1.6rem 1.8rem; color: var(--calm-fg); }
  .settings-body.flush { padding: 0; overflow: hidden; display: flex; flex-direction: column; }
  .widget-host { flex: 1; min-height: 0; min-width: 0; overflow: hidden; display: flex; flex-direction: column; }
  .widget-host > :global(*) { flex: 1; min-height: 0; }
  .widget-host.pad { padding: 1.6rem 1.8rem; }

  h2 { font-size: 1.3rem; font-weight: 700; margin: 0 0 0.3rem; }
  .lede { color: var(--calm-fg-muted); font-size: 0.88rem; margin: 0 0 1.4rem; }

  .field { margin-bottom: 1.6rem; }
  .field-label { font-size: 0.82rem; font-weight: 600; margin-bottom: 0.55rem; display: flex; gap: 0.5rem; align-items: baseline; }
  .fs-val { color: var(--calm-fg-faint); font-weight: 500; font-size: 0.76rem; }

  .theme-toggle { display: flex; gap: 0.6rem; }
  .theme-opt { display: flex; align-items: center; gap: 0.5rem; padding: 0.6rem 0.9rem; border-radius: 6px; background: var(--calm-chip); border: 1px solid var(--calm-border); color: var(--calm-fg); cursor: pointer; font-size: 0.85rem; font-weight: 600; }
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
  .hollow.sm { padding: 0.7rem; }

  .about-list { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 0.6rem; }
  .about-list li { font-size: 0.88rem; color: var(--calm-fg-muted); }
  .about-list b { color: var(--calm-fg); }

  .rows { display: flex; flex-direction: column; gap: 0.4rem; }
  .srow { display: flex; align-items: center; gap: 0.6rem; padding: 0.55rem 0.7rem; background: var(--calm-surface-2); border: 1px solid var(--calm-border); border-radius: 6px; }
  .srow-main { flex: 1; min-width: 0; }
  .srow-name { font-weight: 600; font-size: 0.86rem; }
  .srow-sub { color: var(--calm-fg-faint); font-size: 0.74rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .srow-meta { color: var(--calm-fg-faint); font-size: 0.72rem; white-space: nowrap; }
  .mono { font-family: ui-monospace, monospace; }
  .state-chip { font-size: 0.66rem; text-transform: uppercase; padding: 0.12rem 0.45rem; border-radius: 8px; font-weight: 700; background: var(--calm-chip); color: var(--calm-fg-muted); flex: 0 0 auto; }
  .state-chip.approved, .state-chip.pass, .state-chip.completed { color: var(--calm-ok); background: color-mix(in srgb, var(--calm-ok) 15%, transparent); }
  .state-chip.pending, .state-chip.working { color: var(--calm-accent-2); background: color-mix(in srgb, var(--calm-accent-2) 15%, transparent); }
  .state-chip.revoked, .state-chip.fail, .state-chip.failed { color: var(--calm-danger); background: color-mix(in srgb, var(--calm-danger) 15%, transparent); }

  .mini { width: 26px; height: 26px; flex: 0 0 auto; display: inline-flex; align-items: center; justify-content: center; background: var(--calm-chip); border: 1px solid var(--calm-border); color: var(--calm-fg-muted); border-radius: 6px; cursor: pointer; font-size: 0.8rem; }
  .mini:hover:not(:disabled) { background: var(--calm-chip-hover); color: var(--calm-accent); }
  .mini.danger:hover:not(:disabled) { color: var(--calm-danger); background: color-mix(in srgb, var(--calm-danger) 14%, transparent); }
  .mini:disabled { opacity: 0.5; cursor: progress; }

  .grant-new { display: flex; gap: 0.4rem; margin-bottom: 1rem; flex-wrap: wrap; }
  .grant-new input { flex: 1 1 8rem; min-width: 0; padding: 0.45rem 0.6rem; background: var(--calm-input); color: var(--calm-fg); border: 1px solid var(--calm-border); border-radius: 6px; font-size: 0.8rem; }
  .act { padding: 0.45rem 0.8rem; background: var(--calm-chip); border: 1px solid var(--calm-border); color: var(--calm-fg); border-radius: 6px; font-size: 0.8rem; font-weight: 600; cursor: pointer; }
  .act:hover:not(:disabled) { background: var(--calm-chip-hover); }
  .act:disabled { opacity: 0.5; cursor: progress; }

  .acct-cards { display: flex; gap: 0.7rem; flex-wrap: wrap; margin-bottom: 1.4rem; }
  .acct-card { flex: 1 1 16rem; background: var(--calm-surface-2); border: 1px solid var(--calm-border); border-radius: 8px; padding: 0.8rem; }
  .acct-title { font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--calm-fg-faint); font-weight: 700; margin-bottom: 0.5rem; }
  .kv { display: flex; justify-content: space-between; gap: 0.6rem; padding: 0.18rem 0; font-size: 0.82rem; }
  .kv span { color: var(--calm-fg-faint); }
  .kv b { font-weight: 600; text-align: right; word-break: break-word; }

  .codeblock { background: var(--calm-input); border: 1px solid var(--calm-border); border-radius: 6px; padding: 0.7rem; font-family: ui-monospace, monospace; font-size: 0.74rem; color: var(--calm-fg-muted); white-space: pre-wrap; word-break: break-word; max-height: 360px; overflow: auto; margin: 0; }

  .rev-head { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.8rem; }
  .rev-target { font-weight: 700; font-size: 0.95rem; }
  .rev-row { background: var(--calm-surface-2); border: 1px solid var(--calm-border); border-radius: 6px; padding: 0.6rem 0.7rem; display: flex; flex-direction: column; gap: 0.45rem; }
  .rev-row-top { display: flex; align-items: center; gap: 0.6rem; }
  .rev-reviewer { font-weight: 600; font-size: 0.84rem; flex: 1; }
  .rev-axes { display: grid; grid-template-columns: repeat(5, 1fr); gap: 0.3rem; }
  .axc { text-align: center; background: var(--calm-chip); border-radius: 6px; padding: 0.25rem 0; }
  .axk { display: block; font-size: 0.6rem; color: var(--calm-fg-faint); }
  .axv { display: block; font-weight: 700; font-size: 0.85rem; }
  .rev-note { font-size: 0.8rem; color: var(--calm-fg-muted); }

  /* skills catalogue (#8) */
  .skills-bar { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.9rem; }
  .skills-search { flex: 1; min-width: 0; padding: 0.45rem 0.6rem; background: var(--calm-input); color: var(--calm-fg); border: 1px solid var(--calm-border); border-radius: 6px; font-size: 0.82rem; }
  .skills-search:focus { outline: none; border-color: var(--calm-accent); }
  .skills-count { font-size: 0.72rem; color: var(--calm-fg-faint); white-space: nowrap; }
  .skill-ic { font-size: 1.1rem; width: 1.4rem; text-align: center; flex: 0 0 auto; }
  .skill-tags { display: flex; flex-wrap: wrap; gap: 0.25rem; margin-top: 0.3rem; }
  .tag { font-size: 0.62rem; color: var(--calm-fg-muted); background: var(--calm-chip); border: 1px solid var(--calm-border); border-radius: 5px; padding: 0.05rem 0.35rem; }
  .canon-host { min-height: 22rem; }
</style>

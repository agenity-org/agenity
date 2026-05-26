<!--
  SpawnWizard v0.8 — provider-first, zero directory exposure.
  Stage 1: Shape
  Stage 2: Git repo (registered providers — or register new)
  Stage 3: Fresh or resume per member
  Stage 4: Confirm + launch
-->
<script>
  import { onMount } from 'svelte';
  let { onClose, onLaunched } = $props();
  const API = '/api-v08/v1';

  // ── wizard state ──────────────────────────────────────────────────────────
  let stage = $state(1);
  let shape = $state(null);
  let teamName = $state('');
  let topology = $state('');
  let selectedProviderId = $state(null);
  let memberOverrides = $state({});
  let availableSessions = $state([]);
  let templates = $state([]);
  let savedTeams = $state([]);
  let providers = $state([]);          // registered git providers
  let busy = $state(false);
  let error = $state('');

  // provider registration form state
  let showRegisterForm = $state(false);
  let regKind = $state('github');
  let regUrl = $state('');
  let regToken = $state('');
  let regName = $state('');
  let regBusy = $state(false);
  let regError = $state('');

  let pickedResurrect = $state(null);

  const SHAPES = [
    { id: 'solo',            icon: '🧑',  title: 'Solo',             blurb: 'One agent, no shepherd. Quick exploration.' },
    { id: 'solo-supervised', icon: '👤',  title: 'Solo + Chepherd',  blurb: 'One worker + one chepherd watching. The daily default.' },
    { id: 'pair',            icon: '👥',  title: 'Pair',             blurb: 'Implementer + reviewer + shepherd. Code review built in.' },
    { id: 'council',         icon: '🏛',  title: 'Council',          blurb: '5 agents: implementer + tester + 2 reviewers + orchestrator.' },
    { id: 'resurrect',       icon: '↻',   title: 'Resurrect a team', blurb: 'Bring back a previously-spawned team with their last sessions.' },
    { id: 'custom-yaml',     icon: '🛠',  title: 'Custom (YAML)',    blurb: 'Define your own team in catalog YAML.' },
  ];
  const SHAPE_IDS = new Set(SHAPES.map(s => s.id));
  let catalogExtras = $derived(templates.filter(t => !SHAPE_IDS.has(t.name)));

  const PROVIDER_KINDS = [
    { id: 'github',    label: 'GitHub' },
    { id: 'gitlab',    label: 'GitLab' },
    { id: 'gitea',     label: 'Gitea' },
    { id: 'bitbucket', label: 'Bitbucket' },
    { id: 'embedded',  label: 'Embedded Gitea (local pod)' },
  ];

  onMount(async () => {
    try { const r = await fetch(`${API}/templates`); templates = (await r.json()).templates || []; } catch {}
    try { const r = await fetch(`${API}/teams/saved`); savedTeams = ((await r.json()).teams || []).filter(t => !t.live); } catch {}
    await loadProviders();
    await loadSessions();
  });

  async function loadProviders() {
    try {
      const r = await fetch(`${API}/git-providers`);
      providers = (await r.json()).providers || [];
    } catch { providers = []; }
  }
  async function loadSessions() {
    try {
      const r = await fetch(`${API}/claude-sessions`);
      availableSessions = (await r.json()).sessions || [];
    } catch { availableSessions = []; }
  }

  function selectedTemplate() {
    if (shape === 'custom-yaml') return null;
    return templates.find(t => t.name === shape);
  }
  function membersOfShape() {
    if (shape === 'solo') return [{ name: teamName || 'solo', role: 'worker', agent: 'claude-code' }];
    const t = selectedTemplate();
    return t ? (t.member_specs || []) : [];
  }
  function setMember(name, val) {
    memberOverrides = { ...memberOverrides, [name]: { resume_uuid: val } };
  }

  function pickShape(s) {
    shape = s;
    teamName = s;
    stage = 2;
  }

  async function registerProvider() {
    regBusy = true; regError = '';
    try {
      const body = { kind: regKind, repo_url: regUrl, token: regToken, display_name: regName || regUrl };
      const r = await fetch(`${API}/git-providers`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) { const e = await r.json().catch(()=>({})); throw new Error(e.error || `HTTP ${r.status}`); }
      const d = await r.json();
      await loadProviders();
      selectedProviderId = d.id;
      showRegisterForm = false;
      regUrl = ''; regToken = ''; regName = '';
    } catch (e) { regError = e.message || String(e); }
    regBusy = false;
  }

  async function doResurrect() {
    if (!pickedResurrect) return;
    busy = true; error = '';
    try {
      const r = await fetch(`${API}/teams/${pickedResurrect}/resurrect`, { method: 'POST' });
      if (!r.ok) { const e = await r.json().catch(()=>({})); error = e.error || `HTTP ${r.status}`; }
      else { onLaunched?.(); onClose?.(); }
    } catch (e) { error = String(e); }
    busy = false;
  }

  async function launch() {
    busy = true; error = '';
    try {
      if (shape === 'resurrect') { await doResurrect(); return; }
      if (shape === 'custom-yaml') {
        throw new Error('Drop your YAML at the catalog directory then re-open this wizard.');
      }

      const provider = providers.find(p => p.id === selectedProviderId);
      if (!provider) throw new Error('No git repo selected.');

      if (shape === 'solo') {
        const soloName = teamName || 'solo';
        const body = {
          name: soloName, agent: 'claude-code',
          team: teamName || 'default', role: 'worker',
          provider_id: selectedProviderId,
          use_default_prompt: true,
        };
        const ov = memberOverrides[soloName] || {};
        if (ov.resume_uuid) body.resume_uuid = ov.resume_uuid;
        const r = await fetch(`${API}/sessions`, {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (!r.ok) { const e = await r.json().catch(()=>({})); throw new Error(e.error || `HTTP ${r.status}`); }
      } else {
        const mo = {};
        for (const [k, v] of Object.entries(memberOverrides)) {
          const entry = {};
          if (v.resume_uuid === 'FRESH') entry.fresh = true;
          else if (v.resume_uuid) entry.resume_uuid = v.resume_uuid;
          if (Object.keys(entry).length) mo[k] = entry;
        }
        const r = await fetch(`${API}/templates/${shape}/apply`, {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ team: teamName || shape, provider_id: selectedProviderId, topology, member_overrides: mo }),
        });
        if (!r.ok) { const e = await r.json().catch(()=>({})); throw new Error(e.error || `HTTP ${r.status}`); }
      }
      onLaunched?.(); onClose?.();
    } catch (e) { error = e.message || String(e); }
    busy = false;
  }

  function back() { if (stage > 1) { stage -= 1; error = ''; } }
  function nextEnabled() {
    if (stage === 1) return !!shape;
    if (stage === 2 && shape === 'resurrect') return !!pickedResurrect;
    if (stage === 2) return !!selectedProviderId;
    return true;
  }
  function next() {
    if (stage === 2 && shape === 'resurrect') { doResurrect(); return; }
    if (stage === 2 && shape === 'custom-yaml') { stage = 4; return; }
    stage += 1;
  }

  let selectedProvider = $derived(providers.find(p => p.id === selectedProviderId) || null);
</script>

<div class="backdrop" on:click={onClose}>
  <div class="modal" on:click|stopPropagation>
    <header>
      <h2>+ new {shape ? '· ' + shape : ''}</h2>
      <div class="dots">
        <span class:active={stage===1}></span>
        <span class:active={stage===2}></span>
        {#if shape && shape !== 'custom-yaml' && shape !== 'resurrect'}<span class:active={stage===3}></span>{/if}
        <span class:active={stage===4}></span>
      </div>
      <button class="close-btn" on:click={onClose}>×</button>
    </header>

    <div class="body">

      <!-- ── STAGE 1: Shape ──────────────────────────────────────────────── -->
      {#if stage === 1}
        <h3>What do you want to spin up?</h3>
        <p class="prose">Pick a shape. Each is a recipe — you fill in only what matters.</p>
        <div class="shapes">
          {#each SHAPES as s}
            <button class="shape" class:active={shape===s.id} on:click={() => pickShape(s.id)}>
              <div class="s-icon">{s.icon}</div>
              <div class="s-title">{s.title}</div>
              <div class="s-blurb">{s.blurb}</div>
            </button>
          {/each}
        </div>
        {#if catalogExtras.length > 0}
          <div class="section-label">Stack templates</div>
          <div class="shapes">
            {#each catalogExtras as t}
              <button class="shape" class:active={shape===t.name} on:click={() => pickShape(t.name)}>
                <div class="s-icon">📋</div>
                <div class="s-title">{t.name}</div>
                <div class="s-blurb">{t.description || 'Catalog template'}</div>
              </button>
            {/each}
          </div>
        {/if}

      <!-- ── STAGE 2 (resurrect): Pick saved team ────────────────────────── -->
      {:else if stage === 2 && shape === 'resurrect'}
        <h3>Pick a team to resurrect</h3>
        {#if !savedTeams.length}
          <p class="empty">No saved teams yet — apply a template first.</p>
        {:else}
          <div class="saved-teams">
            {#each savedTeams as t}
              <button class="saved-team" class:active={pickedResurrect===t.name} on:click={() => (pickedResurrect = t.name)}>
                <div class="st-head"><strong>{t.name}</strong> <small>· {(t.members||[]).length} members</small></div>
                <div class="st-meta">last active: {t.last_active ? new Date(t.last_active).toLocaleString() : '—'}</div>
                <ul class="m-list">
                  {#each (t.members||[]) as m}
                    <li>{m.role === 'shepherd' ? '✻' : '●'} {m.name} <small>({m.role})</small></li>
                  {/each}
                </ul>
              </button>
            {/each}
          </div>
        {/if}

      <!-- ── STAGE 2: Git repo ───────────────────────────────────────────── -->
      {:else if stage === 2}
        <h3>Which repo will they work in?</h3>

        {#if providers.length > 0 && !showRegisterForm}
          <div class="provider-grid">
            {#each providers as p}
              <button class="provider-card" class:active={selectedProviderId===p.id}
                      on:click={() => selectedProviderId = p.id}>
                <div class="p-kind">{p.kind}</div>
                <div class="p-name">{p.display_name || p.repo_url}</div>
                {#if p.has_token}<div class="p-token">🔑 token registered</div>{/if}
              </button>
            {/each}
          </div>
          <button class="add-repo-btn" on:click={() => { showRegisterForm = true; regError = ''; }}>
            + Add repo
          </button>
        {/if}

        {#if providers.length === 0 || showRegisterForm}
          {#if providers.length === 0}
            <p class="prose">No repos registered yet. Connect one to get started.</p>
          {/if}
          <div class="register-form">
            <div class="kind-tabs">
              {#each PROVIDER_KINDS as k}
                <button class="kind-tab" class:active={regKind===k.id} on:click={() => regKind = k.id}>{k.label}</button>
              {/each}
            </div>
            {#if regKind !== 'embedded'}
              <label class="field-label">Repo URL
                <input bind:value={regUrl} placeholder="https://github.com/org/repo" />
              </label>
              <label class="field-label">Access token
                <input type="password" bind:value={regToken} placeholder="ghp_… or similar" />
              </label>
              <label class="field-label">Label <small>(optional)</small>
                <input bind:value={regName} placeholder="{regUrl || 'my-repo'}" />
              </label>
            {:else}
              <p class="prose">chepherd will spin up an embedded Gitea container. No external account needed.</p>
            {/if}
            {#if regError}<div class="error">{regError}</div>{/if}
            <div class="reg-actions">
              {#if showRegisterForm && providers.length > 0}
                <button class="ghost" on:click={() => { showRegisterForm = false; regError = ''; }}>Cancel</button>
              {/if}
              <button class="primary" on:click={registerProvider} disabled={regBusy || (regKind !== 'embedded' && !regUrl)}>
                {regBusy ? 'Registering…' : 'Register repo'}
              </button>
            </div>
          </div>
        {/if}

        <label class="field-label" style="margin-top:1rem">Team name
          <input bind:value={teamName} placeholder={shape} />
        </label>
        {#if shape !== 'solo' && shape !== 'custom-yaml'}
          <label class="field-label">Topology
            <select bind:value={topology}>
              <option value="">(template default)</option>
              <option value="hub">hub (chepherd in the middle)</option>
              <option value="mesh">mesh (peer-to-peer)</option>
            </select>
          </label>
        {/if}

      <!-- ── STAGE 3: Fresh or resume ────────────────────────────────────── -->
      {:else if stage === 3}
        {@const ms = membersOfShape()}
        <h3>{ms.length === 1 ? 'Fresh or resume?' : `${ms.length} members — fresh or resume?`}</h3>
        <p class="prose">Leave on Fresh for a clean start, or pick a prior session to resume.</p>
        <div class="members">
          {#each ms as m}
            <div class="member-row">
              <div class="m-head">
                <span class="m-icon" class:shepherd={m.role==='shepherd'}>{m.role==='shepherd' ? '✻' : '●'}</span>
                <span class="m-name">{m.name}</span>
                <span class="m-role">· {m.role}</span>
              </div>
              <select on:change={(e) => setMember(m.name, e.target.value)}>
                <option value="">⊕ Fresh (default)</option>
                {#each availableSessions.slice(0, 25) as s}
                  <option value={s.uuid}>↻ {s.uuid.slice(0,8)} · {new Date(s.modified).toLocaleString()} · {(s.first_message || '').slice(0,45)}</option>
                {/each}
              </select>
            </div>
          {/each}
        </div>

      <!-- ── STAGE 4: Confirm ────────────────────────────────────────────── -->
      {:else if stage === 4}
        <h3>Ready to launch</h3>
        <ul class="confirm">
          <li><strong>Shape:</strong> {shape}</li>
          <li><strong>Team:</strong> {teamName || shape}</li>
          <li><strong>Repo:</strong> {selectedProvider?.display_name || selectedProvider?.repo_url || '—'} <span class="p-badge">{selectedProvider?.kind || ''}</span></li>
          {#if shape !== 'solo' && shape !== 'custom-yaml'}
            <li><strong>Members:</strong>
              <ul>
                {#each membersOfShape() as m}
                  <li>{m.name} ({m.role}) — {memberOverrides[m.name]?.resume_uuid ? '↻ resume' : '⊕ fresh'}</li>
                {/each}
              </ul>
            </li>
          {/if}
        </ul>
      {/if}

      {#if error}<div class="error">{error}</div>{/if}
    </div>

    <footer>
      <button class="ghost" on:click={back} disabled={stage===1}>← Back</button>
      <div class="spacer"></div>
      <button class="ghost" on:click={onClose}>Cancel</button>
      {#if stage < 4}
        <button class="primary" on:click={next} disabled={!nextEnabled()}>Next →</button>
      {:else}
        <button class="primary" on:click={launch} disabled={busy}>{busy ? 'Launching…' : '🚀 Launch'}</button>
      {/if}
    </footer>
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.65); display: flex; align-items: center; justify-content: center; z-index: 1000; backdrop-filter: blur(2px); }
  .modal { width: min(780px, 96vw); max-height: 94vh; background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 10px; display: flex; flex-direction: column; }
  header { display: flex; align-items: center; gap: 0.7rem; padding: 0.85rem 1.2rem; border-bottom: 1px solid var(--border); }
  h2 { margin: 0; color: var(--accent); font-size: 1.05rem; flex: 0; white-space: nowrap; }
  .dots { display: flex; gap: 0.4rem; flex: 1; justify-content: center; }
  .dots span { width: 8px; height: 8px; border-radius: 50%; background: var(--border-strong); transition: background 0.15s; }
  .dots span.active { background: var(--accent); }
  .close-btn { background: transparent; color: var(--fg-muted); border: none; font-size: 1.5rem; cursor: pointer; line-height: 1; }
  .body { padding: 1rem 1.2rem; overflow-y: auto; flex: 1; min-height: 240px; }
  h3 { color: var(--fg); margin: 0 0 0.4rem 0; }
  .prose { color: var(--fg-muted); margin: 0 0 1rem 0; font-size: 0.9rem; }
  /* Shapes */
  .shapes { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 0.7rem; margin-bottom: 0.5rem; }
  .shape { padding: 0.9rem 1rem; background: var(--bg); border: 1px solid var(--border-strong); border-radius: 8px; cursor: pointer; text-align: left; color: var(--fg); transition: border 0.1s; }
  .shape:hover { border-color: var(--accent-2); }
  .shape.active { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 6%, transparent); }
  .s-icon { font-size: 1.8rem; }
  .s-title { font-weight: 600; margin: 0.3rem 0 0.2rem; color: var(--accent); }
  .s-blurb { color: var(--fg-muted); font-size: 0.82rem; }
  .section-label { color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.06em; font-size: 0.72rem; font-weight: 600; margin: 1rem 0 0.4rem; }
  /* Provider cards */
  .provider-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 0.5rem; margin-bottom: 0.7rem; }
  .provider-card { padding: 0.75rem 0.9rem; background: var(--bg); border: 1px solid var(--border-strong); border-radius: 8px; cursor: pointer; text-align: left; color: var(--fg); transition: border 0.1s; }
  .provider-card:hover { border-color: var(--accent-2); }
  .provider-card.active { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 6%, transparent); }
  .p-kind { font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--fg-faint); margin-bottom: 0.25rem; }
  .p-name { font-weight: 600; color: var(--fg); word-break: break-all; }
  .p-token { font-size: 0.72rem; color: var(--fg-muted); margin-top: 0.25rem; }
  .p-badge { font-size: 0.68rem; background: color-mix(in srgb, var(--accent) 12%, transparent); color: var(--accent); border-radius: 4px; padding: 0.1rem 0.4rem; margin-left: 0.3rem; }
  .add-repo-btn { background: transparent; border: 1px dashed var(--border-strong); color: var(--fg-muted); border-radius: 6px; padding: 0.4rem 0.85rem; cursor: pointer; font-size: 0.85rem; margin-bottom: 0.5rem; }
  .add-repo-btn:hover { border-color: var(--accent); color: var(--accent); }
  /* Register form */
  .register-form { background: var(--bg); border: 1px solid var(--border-strong); border-radius: 8px; padding: 0.9rem 1rem; }
  .kind-tabs { display: flex; flex-wrap: wrap; gap: 0.35rem; margin-bottom: 0.85rem; }
  .kind-tab { padding: 0.3rem 0.75rem; background: transparent; border: 1px solid var(--border-strong); border-radius: 6px; cursor: pointer; color: var(--fg-muted); font-size: 0.82rem; }
  .kind-tab:hover { border-color: var(--accent-2); color: var(--fg); }
  .kind-tab.active { border-color: var(--accent); color: var(--accent); background: color-mix(in srgb, var(--accent) 8%, transparent); }
  .field-label { display: block; color: var(--fg-muted); font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.04em; margin-top: 0.65rem; }
  .field-label input, .field-label select { display: block; width: 100%; margin-top: 0.25rem; padding: 0.45rem 0.6rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; font-size: 0.9rem; box-sizing: border-box; }
  .field-label small { text-transform: none; letter-spacing: normal; color: var(--fg-faint); }
  .reg-actions { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 0.85rem; }
  /* Saved teams */
  .saved-teams { display: flex; flex-direction: column; gap: 0.5rem; max-height: 360px; overflow-y: auto; }
  .saved-team { padding: 0.7rem 0.85rem; background: var(--bg); border: 1px solid var(--border-strong); border-radius: 6px; cursor: pointer; text-align: left; color: var(--fg); }
  .saved-team:hover { border-color: var(--accent-2); }
  .saved-team.active { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 6%, transparent); }
  .st-head { color: var(--accent); }
  .st-head small { color: var(--fg-muted); }
  .st-meta { color: var(--fg-muted); margin-top: 0.2rem; font-size: 0.82rem; }
  .m-list { margin: 0.35rem 0 0; padding-left: 1.2rem; color: var(--fg-muted); font-size: 0.82rem; }
  /* Members */
  .members { display: flex; flex-direction: column; gap: 0.55rem; margin-top: 0.4rem; }
  .member-row { display: grid; grid-template-columns: 1fr 1.6fr; gap: 0.6rem; align-items: center; padding: 0.4rem 0.6rem; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; }
  .m-head { display: flex; align-items: center; gap: 0.3rem; }
  .m-icon { color: var(--accent-2); }
  .m-icon.shepherd { color: var(--accent); }
  .m-name { font-weight: 600; }
  .m-role { color: var(--fg-muted); font-size: 0.82rem; }
  .member-row select { width: 100%; padding: 0.35rem 0.5rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 4px; cursor: pointer; }
  /* Confirm */
  .confirm { margin: 0; padding-left: 1.2rem; color: var(--fg); line-height: 1.8; }
  .confirm strong { color: var(--accent-2); }
  .confirm ul { margin: 0.3rem 0 0; padding-left: 1rem; color: var(--fg-muted); }
  /* Misc */
  .empty { color: var(--fg-faint); text-align: center; padding: 1.2rem; }
  .error { margin-top: 0.8rem; padding: 0.55rem 0.8rem; background: color-mix(in srgb, var(--danger) 10%, transparent); border: 1px solid var(--danger); color: var(--danger); border-radius: 6px; font-size: 0.88rem; }
  footer { display: flex; align-items: center; gap: 0.55rem; padding: 0.85rem 1.2rem; border-top: 1px solid var(--border); }
  footer .spacer { flex: 1; }
  button.primary { background: var(--accent); color: #fff; border: none; border-radius: 6px; padding: 0.45rem 1rem; font-weight: 600; cursor: pointer; font-size: 0.9rem; }
  button.primary:disabled { opacity: 0.4; cursor: not-allowed; }
  button.ghost { background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.45rem 0.95rem; cursor: pointer; font-size: 0.9rem; }
  button.ghost:disabled { opacity: 0.4; cursor: not-allowed; }
</style>

<!--
  SpawnWizard — replaces SpawnModal + TemplatePicker. One entry point,
  stage-by-stage flow:
    1. Shape: solo / solo-supervised / pair / council / custom (icons)
    2. Working dir + git handling (init repo / connect remote / skip)
    3. Members: per-member fresh-or-resume picker (only shown when > 1 member)
    4. Confirm + launch
  No big-dump form — every screen has a single decision.
-->
<script>
  import { onMount } from 'svelte';
  let { onClose, onLaunched } = $props();
  const API = '/api-v08/v1';

  // ----- wizard state -----
  let stage = $state(1);
  let shape = $state(null);                  // null | 'solo' | 'solo-supervised' | 'pair' | 'council' | 'custom-yaml'
  let cwd = $state('/home/openova/repos/chepherd');
  let teamName = $state('');
  let gitMode = $state('use-existing');      // 'use-existing' | 'init-new' | 'connect-remote' | 'no-git'
  let remoteUrl = $state('');
  let gitInfo = $state(null);                // { is_git, remote, branch }
  let recentFolders = $state([]);
  let memberOverrides = $state({});          // memberName → { resume_uuid | FRESH | cwd }
  let availableSessions = $state([]);
  let templates = $state([]);
  let savedTeams = $state([]);               // dormant teams operator can resurrect
  let topology = $state('');                 // override topology when applying template
  let busy = $state(false);
  let error = $state('');

  const SHAPES = [
    { id: 'solo',             icon: '🧑',  title: 'Solo',            blurb: 'One agent, no shepherd. Quick exploration.' },
    { id: 'solo-supervised',  icon: '👤',  title: 'Solo + Chepherd', blurb: 'One worker + one chepherd watching. The daily default.' },
    { id: 'pair',             icon: '👥',  title: 'Pair',            blurb: 'Implementer + reviewer + shepherd. Code review built in.' },
    { id: 'council',          icon: '🏛',  title: 'Council',         blurb: '5 agents: implementer + tester + 2 specialist reviewers + orchestrator. Heavy or risky work.' },
    { id: 'resurrect',        icon: '↻',   title: 'Resurrect a team', blurb: 'Bring back a previously-spawned team — each member resumes its last Claude session.' },
    { id: 'custom-yaml',      icon: '🛠',  title: 'Custom (YAML)',   blurb: 'Define your own team in catalog YAML — topology, members, prompts. Or fork an existing template to start from.' },
  ];

  // ----- mount: pre-load templates + folders + sessions + saved teams -----
  onMount(async () => {
    try {
      const r = await fetch(`${API}/templates`);
      const d = await r.json();
      templates = d.templates || [];
    } catch {}
    try {
      const r = await fetch(`${API}/folders/recent`);
      const d = await r.json();
      recentFolders = d.folders || [];
    } catch {}
    try {
      const r = await fetch(`${API}/teams/saved`);
      const d = await r.json();
      savedTeams = (d.teams || []).filter(t => !t.live);
    } catch {}
    refreshSessions();
    refreshGitInfo();
  });

  async function refreshSessions() {
    try {
      const r = await fetch(`${API}/claude-sessions?cwd=${encodeURIComponent(cwd)}`);
      const d = await r.json();
      availableSessions = d.sessions || [];
    } catch { availableSessions = []; }
  }
  async function refreshGitInfo() {
    try {
      const r = await fetch(`${API}/folders/git-info?cwd=${encodeURIComponent(cwd)}`);
      gitInfo = await r.json();
      gitMode = gitInfo.is_git ? 'use-existing' : 'no-git';
    } catch { gitInfo = { is_git: false }; gitMode = 'no-git'; }
  }
  $effect(() => { if (cwd) { refreshSessions(); refreshGitInfo(); } });

  function pickShape(s) {
    shape = s;
    teamName = s; // default team name = shape id; user can edit
    if (s === 'resurrect') {
      // Resurrect flow has its own stage 2 (pick which saved team).
      stage = 2;
      return;
    }
    stage = 2;
  }
  let pickedResurrect = $state(null); // selected saved team name
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
  function pickFolder(p) { cwd = p; }

  function selectedTemplate() {
    // Map shape → catalog template name. The wizard names match catalog YAML.
    const id = shape;
    if (id === 'custom-yaml') return null;
    return templates.find(t => t.name === id);
  }
  function membersOfShape() {
    const t = selectedTemplate();
    if (!t) return [];
    return t.member_specs || [];
  }

  function setMember(name, val) {
    memberOverrides = { ...memberOverrides, [name]: { resume_uuid: val } };
  }

  async function launch() {
    busy = true; error = '';
    try {
      // Optional: git init / remote add
      if (gitMode === 'init-new' || (gitMode === 'connect-remote' && remoteUrl)) {
        const r = await fetch(`${API}/folders/git-setup`, {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ cwd, mode: gitMode, remote: remoteUrl }),
        });
        if (!r.ok) { const e = await r.json().catch(()=>({})); throw new Error('git setup: ' + (e.error || r.status)); }
      }
      // Solo: spawn one agent. Resume option from Stage 3 if picked.
      if (shape === 'solo') {
        const soloName = teamName || 'solo';
        const soloOverride = memberOverrides[soloName] || {};
        const body = {
          name: soloName,
          agent: 'claude-code',
          team: teamName || 'default',
          role: 'worker',
          cwd,
          use_default_prompt: true,
        };
        if (soloOverride.resume_uuid && soloOverride.resume_uuid !== 'FRESH') {
          body.resume_uuid = soloOverride.resume_uuid;
        }
        const r = await fetch(`${API}/sessions`, {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (!r.ok) { const e = await r.json().catch(()=>({})); throw new Error(e.error || `HTTP ${r.status}`); }
      } else if (shape === 'custom-yaml') {
        // Open the catalog directory in the operator's file manager doesn't make sense here.
        // Drop a hint instead.
        throw new Error('Custom YAML mode: drop your YAML at ~/.local/state/chepherd-v06/catalog/<name>.yaml then re-open this wizard — your template will appear as a shape choice.');
      } else if (shape === 'resurrect') {
        await doResurrect();
        return;
      } else {
        // Template apply
        const mo = {};
        for (const [k, v] of Object.entries(memberOverrides)) {
          const entry = {};
          if (v.resume_uuid === 'FRESH') entry.fresh = true;
          else if (v.resume_uuid) entry.resume_uuid = v.resume_uuid;
          if (v.cwd) entry.cwd = v.cwd;
          if (Object.keys(entry).length) mo[k] = entry;
        }
        const r = await fetch(`${API}/templates/${shape}/apply`, {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ team: teamName || shape, cwd, topology, member_overrides: mo }),
        });
        if (!r.ok) { const e = await r.json().catch(()=>({})); throw new Error(e.error || `HTTP ${r.status}`); }
      }
      onLaunched?.();
      onClose?.();
    } catch (e) {
      error = e.message || String(e);
    }
    busy = false;
  }

  function back() { if (stage > 1) stage -= 1; }
  function nextEnabled() {
    if (stage === 1) return !!shape;
    if (stage === 2 && shape === 'resurrect') return !!pickedResurrect;
    if (stage === 2) return !!cwd && (gitMode !== 'connect-remote' || !!remoteUrl);
    if (stage === 3) return true;
    return true;
  }
  function next() {
    if (stage === 2 && shape === 'resurrect') { doResurrect(); return; }
    if (stage === 2 && shape === 'custom-yaml') { stage = 4; return; }
    stage += 1;
  }
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
      <button on:click={onClose}>×</button>
    </header>

    <div class="body">
      {#if stage === 1}
        <h3>What do you want to spin up?</h3>
        <p class="prose">Pick a shape. From lean (one agent) to complex (multi-role council). Each shape is a recipe — you'll fill in only what matters for that shape.</p>
        <div class="shapes">
          {#each SHAPES as s}
            <button class="shape" class:active={shape===s.id} on:click={() => pickShape(s.id)}>
              <div class="icon">{s.icon}</div>
              <div class="title">{s.title}</div>
              <div class="blurb">{s.blurb}</div>
            </button>
          {/each}
        </div>

      {:else if stage === 2 && shape === 'resurrect'}
        <h3>Pick a team to resurrect</h3>
        <p class="prose">Past templates you applied. Each member will re-spawn with its last-known Claude session as <code>--resume</code>.</p>
        {#if !savedTeams.length}
          <p class="empty">No saved teams yet — apply a template first.</p>
        {/if}
        <div class="saved-teams">
          {#each savedTeams as t}
            <button class="saved-team" class:active={pickedResurrect===t.name} on:click={() => (pickedResurrect = t.name)}>
              <div class="head"><strong>{t.name}</strong> <small>· template: {t.template} · {(t.members||[]).length} members</small></div>
              <div class="meta">cwd: <code>{t.cwd}</code></div>
              <div class="meta">last active: {t.last_active ? new Date(t.last_active).toLocaleString() : '—'}</div>
              <ul class="m-list">
                {#each (t.members||[]) as m}
                  <li>{m.role === 'shepherd' ? '✻' : '●'} {m.name} <small>({m.role}{m.claude_uuid ? ' · ↻ ' + m.claude_uuid.slice(0,8) : ' · ⊕ fresh'})</small></li>
                {/each}
              </ul>
            </button>
          {/each}
        </div>

      {:else if stage === 2}
        <h3>Where will they work?</h3>
        <p class="prose">Pick a working directory. Agents read + write inside this folder. If it isn't a git repo we'll offer to set one up.</p>
        <label>Working directory <input bind:value={cwd} placeholder="/home/openova/repos/..." /></label>
        {#if recentFolders.length}
          <div class="chips">
            {#each recentFolders.slice(0, 10) as f}
              <button class="chip" class:active={cwd===f.path} on:click={() => pickFolder(f.path)}>
                <code>{f.path}</code>
              </button>
            {/each}
          </div>
        {/if}
        {#if gitInfo}
          <div class="git-block">
            {#if gitInfo.is_git}
              <div class="ok-line">✓ Git repo detected · branch <code>{gitInfo.branch || 'unknown'}</code>{#if gitInfo.remote} · remote <code>{gitInfo.remote}</code>{/if}</div>
            {:else}
              <div class="warn-line">⚠ Not a git repo. Choose:</div>
              <label class="radio"><input type="radio" bind:group={gitMode} value="init-new" /> Init a new local repo (<code>git init</code>)</label>
              <label class="radio"><input type="radio" bind:group={gitMode} value="connect-remote" /> Connect to a remote</label>
              {#if gitMode === 'connect-remote'}
                <input class="indent" bind:value={remoteUrl} placeholder="git@github.com:user/repo.git or https://…" />
              {/if}
              <label class="radio"><input type="radio" bind:group={gitMode} value="no-git" /> Skip — work without git for now</label>
            {/if}
          </div>
        {/if}
        <label>Team name <input bind:value={teamName} placeholder={shape} /></label>
        {#if shape !== 'solo' && shape !== 'custom-yaml'}
          <label>Topology
            <select bind:value={topology}>
              <option value="">(template default)</option>
              <option value="hub">hub (chepherd in the middle)</option>
              <option value="mesh">mesh (peer-to-peer)</option>
              <option value="custom">custom</option>
            </select>
          </label>
        {/if}

      {:else if stage === 3}
        {@const ms = shape === 'solo' ? [{ name: teamName || 'solo', role: 'worker', agent: 'claude-code' }] : membersOfShape()}
        <h3>{ms.length === 1 ? 'Fresh or resume?' : `${ms.length} members — fresh, resume, custom cwd?`}</h3>
        <p class="prose">By default every member shares the team cwd. Click <em>per-member cwd</em> on a row to override (e.g. multi-repo work). Pick a session to resume; leave on Fresh for a clean start.</p>
        <div class="members">
          {#each ms as m}
            <div class="member-row">
              <div class="m-head">
                <span class="icon" class:shepherd={m.role==='shepherd'}>{m.role==='shepherd' ? '✻' : '●'}</span>
                <span class="name">{m.name}</span>
                <span class="role">· {m.role}</span>
              </div>
              <select on:change={(e) => setMember(m.name, e.target.value)}>
                <option value="">⊕ Fresh (default)</option>
                {#each availableSessions.slice(0, 25) as s}
                  <option value={s.uuid}>↻ {s.uuid.slice(0,8)} · {new Date(s.modified).toLocaleString()} · {(s.first_message || '').slice(0,45)}</option>
                {/each}
              </select>
              <details class="cwd-override">
                <summary>per-member cwd</summary>
                <input placeholder="/home/.../other-repo (leave blank = team cwd)"
                       value={memberOverrides[m.name]?.cwd || ''}
                       on:input={(e) => memberOverrides = { ...memberOverrides, [m.name]: { ...(memberOverrides[m.name] || {}), cwd: e.target.value } }} />
              </details>
            </div>
          {/each}
        </div>

      {:else if stage === 4}
        <h3>Ready to launch</h3>
        <ul class="confirm">
          <li><strong>Shape:</strong> {shape}</li>
          <li><strong>Team:</strong> {teamName || shape}</li>
          <li><strong>cwd:</strong> <code>{cwd}</code></li>
          <li><strong>Git:</strong> {gitInfo?.is_git ? 'existing repo' : (gitMode === 'init-new' ? 'will init local repo' : gitMode === 'connect-remote' ? `will connect to ${remoteUrl}` : 'no git')}</li>
          {#if shape !== 'solo' && shape !== 'custom-yaml'}
            <li><strong>Members:</strong>
              <ul>
                {#each membersOfShape() as m}
                  <li>{m.name} ({m.role}) — {memberOverrides[m.name]?.resume_uuid ? '↻ resume ' + memberOverrides[m.name].resume_uuid.slice(0,8) : '⊕ fresh'}</li>
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
  h2 { margin: 0; color: var(--accent); font-size: 1.05rem; flex: 0; }
  .dots { display: flex; gap: 0.4rem; flex: 1; justify-content: center; }
  .dots span { width: 8px; height: 8px; border-radius: 50%; background: var(--border-strong); transition: background 0.15s; }
  .dots span.active { background: var(--accent); }
  header > button:last-child { background: transparent; color: var(--fg-muted); border: none; font-size: 1.5rem; cursor: pointer; }
  .body { padding: 1rem 1.2rem; overflow-y: auto; flex: 1; min-height: 240px; }
  h3 { color: var(--fg); margin: 0 0 0.4rem 0; }
  .prose { color: var(--fg-muted); margin: 0 0 1rem 0; }
  .shapes { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 0.7rem; }
  .shape { padding: 0.9rem 1rem; background: var(--bg); border: 1px solid var(--border-strong); border-radius: 8px; cursor: pointer; text-align: left; color: var(--fg); transition: border 0.1s; }
  .shape:hover { border-color: var(--accent-2); }
  .shape.active { border-color: var(--accent); background: rgba(255,165,0,0.06); }
  .shape .icon { font-size: 1.8rem; }
  .shape .title { font-weight: 600; margin: 0.3rem 0 0.2rem 0; color: var(--accent); }
  .shape .blurb { color: var(--fg-muted); }
  label { display: block; margin-top: 0.7rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.04em; }
  input { width: 100%; padding: 0.45rem 0.6rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; font-family: ui-monospace, monospace; margin-top: 0.2rem; box-sizing: border-box; }
  input.indent { margin-left: 1.3rem; width: calc(100% - 1.3rem); }
  .chips { display: flex; flex-wrap: wrap; gap: 0.3rem; margin-top: 0.5rem; }
  .chip { background: var(--bg); border: 1px solid var(--border-strong); border-radius: 4px; padding: 0.3rem 0.5rem; cursor: pointer; color: var(--fg-muted); }
  .chip:hover, .chip.active { border-color: var(--accent); }
  .chip code { color: var(--accent-2); }
  .git-block { margin-top: 0.8rem; padding: 0.7rem 0.85rem; background: var(--bg); border: 1px solid var(--border-strong); border-radius: 6px; }
  .ok-line { color: #5cd57f; }
  .warn-line { color: #ffaa55; margin-bottom: 0.4rem; }
  .radio { display: flex; align-items: center; gap: 0.4rem; margin-top: 0.35rem; text-transform: none; letter-spacing: normal; color: var(--fg); }
  .radio input { width: auto; margin: 0; }
  .members { display: flex; flex-direction: column; gap: 0.55rem; margin-top: 0.4rem; }
  .member-row { display: grid; grid-template-columns: 1fr 1.8fr; gap: 0.6rem; align-items: center; padding: 0.4rem 0.6rem; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; }
  .m-head { }
  .m-head .icon { color: var(--accent-2); margin-right: 0.4rem; }
  .m-head .icon.shepherd { color: var(--accent); }
  .m-head .name { font-weight: 600; }
  .m-head .role { color: var(--fg-muted); }
  .member-row { grid-template-columns: 1fr 1.4fr; }
  .member-row select { width: 100%; padding: 0.35rem 0.5rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 4px; cursor: pointer; }
  .cwd-override { grid-column: 1 / span 2; margin-top: 0.35rem; }
  .cwd-override summary { cursor: pointer; color: var(--fg-muted); font-size: 0.78rem; user-select: none; }
  .cwd-override input { width: 100%; padding: 0.3rem 0.5rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 4px; font-family: ui-monospace, monospace; margin-top: 0.3rem; box-sizing: border-box; }
  .saved-teams { display: flex; flex-direction: column; gap: 0.5rem; max-height: 360px; overflow-y: auto; }
  .saved-team { padding: 0.7rem 0.85rem; background: var(--bg); border: 1px solid var(--border-strong); border-radius: 6px; cursor: pointer; text-align: left; color: var(--fg); }
  .saved-team:hover { border-color: var(--accent-2); }
  .saved-team.active { border-color: var(--accent); background: rgba(255,165,0,0.06); }
  .saved-team .head { color: var(--accent); }
  .saved-team .head small { color: var(--fg-muted); }
  .saved-team .meta { color: var(--fg-muted); margin-top: 0.2rem; }
  .saved-team .m-list { margin: 0.35rem 0 0 0; padding-left: 1.2rem; color: var(--fg-muted); }
  .empty { color: var(--fg-faint); text-align: center; padding: 1.2rem; }
  .confirm { margin: 0; padding-left: 1.2rem; color: var(--fg); }
  .confirm strong { color: var(--accent-2); }
  .confirm ul { margin: 0.3rem 0 0 0; padding-left: 1rem; color: var(--fg-muted); }
  .error { margin-top: 0.8rem; padding: 0.55rem 0.8rem; background: rgba(255,107,107,0.1); border: 1px solid var(--danger); color: var(--danger); border-radius: 6px; }
  footer { display: flex; align-items: center; gap: 0.55rem; padding: 0.85rem 1.2rem; border-top: 1px solid var(--border); }
  footer .spacer { flex: 1; }
  button.primary { background: var(--accent); color: #000; border: none; border-radius: 6px; padding: 0.45rem 1rem; font-weight: 600; cursor: pointer; }
  button.primary:disabled { opacity: 0.4; cursor: not-allowed; }
  button.ghost { background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.45rem 0.95rem; cursor: pointer; }
  button.ghost:disabled { opacity: 0.4; cursor: not-allowed; }
</style>

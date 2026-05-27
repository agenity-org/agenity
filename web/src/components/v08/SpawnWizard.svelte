<!--
  SpawnWizard v0.8 — provider-first, zero directory exposure.
  Stage 1: Shape
  Stage 2: Git repo (registered providers — or register new)
  Stage 3: Fresh or resume per member
  Stage 4: Claude account picker (skipped silently if exactly one token exists)
  Stage 5: Confirm + launch

  R5 (#136): Claude OAuth is a first-class wizard step. If the vault is
  empty AND there's no host ~/.claude/.credentials.json, stage 4 forces the
  operator to paste a credentials.json (or harvest one from a freshly-spawned
  agent after they complete the OAuth-URL login in the browser). Tokens are
  reusable across all subsequent spawns (R4), with per-agent override.
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
  let regSavedOK = $state(false);  // ✓ tick after auto-save (#165)

  let pickedResurrect = $state(null);

  // Agent-type registry (#127 R5 redo / Wizard agent selector). Picked
  // at stage 1; defaults to claude-code so existing flows are unchanged.
  let agents = $state([]);
  let selectedAgentSlug = $state('claude-code');

  // Claude OAuth tokens (R5 / #136) — vault entries + host fallback
  let claudeTokens = $state([]);
  let selectedClaudeTokenId = $state('');   // default: empty → pick newest claude-oauth at spawn

  // Live OAuth-capture flow state
  let oauthMode = $state(false);           // true while a login capture is in progress
  let oauthAgentName = $state('');         // ephemeral agent name backing this capture
  let oauthURL = $state('');               // captured OAuth URL once visible
  let oauthCode = $state('');              // user-pasted auth code
  let oauthLabel = $state('');             // optional vault-entry label
  let oauthBusy = $state(false);           // network in flight
  let oauthError = $state('');
  let oauthStatus = $state('');            // human-readable progress line
  let oauthPollHandle = null;

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

  // Auto-detect provider kind from a repo URL.
  function detectKind(url) {
    if (!url) return 'github';
    if (url.includes('github.com')) return 'github';
    if (url.includes('gitlab.com') || /gitlab\.[a-z]/.test(url)) return 'gitlab';
    if (url.includes('bitbucket.org')) return 'bitbucket';
    return 'gitea';
  }
  // Return a "get token" link for a given kind.
  function tokenLink(kind) {
    switch (kind) {
      case 'github':    return { url: 'https://github.com/settings/tokens/new?scopes=repo', label: 'GitHub → Settings → Personal Access Tokens' };
      case 'gitlab':    return { url: 'https://gitlab.com/-/user_settings/personal_access_tokens', label: 'GitLab → Preferences → Access Tokens' };
      case 'bitbucket': return { url: 'https://bitbucket.org/account/settings/app-passwords/', label: 'Bitbucket → Account → App Passwords' };
      default:          return null;
    }
  }

  // Reactive: auto-update regKind whenever regUrl changes.
  $effect(() => { if (regUrl) regKind = detectKind(regUrl); });

  let detectedKind = $derived(detectKind(regUrl));
  let tLink = $derived(tokenLink(detectedKind));

  onMount(async () => {
    try { const r = await fetch(`${API}/templates`); templates = (await r.json()).templates || []; } catch {}
    try { const r = await fetch(`${API}/teams/saved`); savedTeams = ((await r.json()).teams || []).filter(t => !t.live); } catch {}
    // #172 — /api/v1/agents now serves the first-class Agent entity
    // CRUD; the agent-type catalog moved to /api/v1/agent-types.
    try { const r = await fetch(`${API}/agent-types`); agents = (await r.json()).agents || []; } catch {}
    await loadProviders();
    await loadSessions();
    await loadClaudeTokens();
  });

  let selectedAgent = $derived(agents.find(a => a.slug === selectedAgentSlug) || null);
  // Stage 4 (Claude account) is only relevant for claude-oauth agents.
  let needsClaudeAccount = $derived(selectedAgent?.requires_auth === 'claude-oauth');

  async function loadClaudeTokens() {
    try {
      const r = await fetch(`${API}/claude-tokens`);
      claudeTokens = (await r.json()).tokens || [];
    } catch { claudeTokens = []; }
  }

  // ── OAuth-capture flow ────────────────────────────────────────────────────
  // 1. POST /claude-tokens/login-begin → spawn ephemeral agent
  // 2. Poll GET  /claude-tokens/login-url/<name>  → returns {url} once visible
  // 3. User clicks URL → authorises in their browser → gets redirected to a page
  //    showing an authorisation code
  // 4. User pastes that code → POST /claude-tokens/login-submit/<name>
  // 5. Server injects code into agent PTY, harvests credentials.json,
  //    upserts vault, terminates ephemeral agent → returns new token id.
  async function beginOAuthLogin() {
    oauthBusy = true; oauthError = ''; oauthURL = ''; oauthCode = '';
    oauthStatus = 'Spawning capture agent…';
    try {
      const r = await fetch(`${API}/claude-tokens/login-begin`, { method: 'POST' });
      if (!r.ok) throw new Error(await r.text());
      const d = await r.json();
      oauthAgentName = d.name;
      oauthMode = true;
      oauthStatus = 'Waiting for Claude to print the login URL…';
      pollForOAuthURL();
    } catch (e) {
      oauthError = e.message || String(e);
      oauthBusy = false;
    }
  }

  function pollForOAuthURL() {
    let attempts = 0;
    const tick = async () => {
      if (!oauthMode || !oauthAgentName) return;
      attempts++;
      try {
        const r = await fetch(`${API}/claude-tokens/login-url/${oauthAgentName}`);
        if (r.status === 200) {
          const d = await r.json();
          oauthURL = d.url;
          oauthStatus = 'Click the link below to authorise, then paste the code that Claude shows you.';
          oauthBusy = false;
          return;
        }
      } catch {}
      if (attempts > 60) {  // 60 * 1s = 60s
        oauthError = 'Capture agent never printed a Claude login URL. Cancel and retry.';
        oauthBusy = false;
        return;
      }
      oauthPollHandle = setTimeout(tick, 1000);
    };
    tick();
  }

  async function submitOAuthCode() {
    if (!oauthCode) { oauthError = 'Paste the code Claude showed you after authorising.'; return; }
    oauthBusy = true; oauthError = '';
    oauthStatus = 'Submitting code to capture agent…';
    try {
      const r = await fetch(`${API}/claude-tokens/login-submit/${oauthAgentName}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code: oauthCode.trim(), label: oauthLabel || '' }),
      });
      if (!r.ok) throw new Error(await r.text());
      const d = await r.json();
      await loadClaudeTokens();
      selectedClaudeTokenId = d.id;
      oauthMode = false; oauthAgentName = ''; oauthURL = ''; oauthCode = ''; oauthLabel = '';
      oauthStatus = '';
    } catch (e) {
      oauthError = e.message || String(e);
    }
    oauthBusy = false;
  }

  async function cancelOAuthLogin() {
    if (oauthPollHandle) { clearTimeout(oauthPollHandle); oauthPollHandle = null; }
    if (oauthAgentName) {
      try { await fetch(`${API}/claude-tokens/login-cancel/${oauthAgentName}`, { method: 'POST' }); } catch {}
    }
    oauthMode = false; oauthAgentName = ''; oauthURL = ''; oauthCode = ''; oauthLabel = '';
    oauthBusy = false; oauthError = ''; oauthStatus = '';
  }

  async function loadProviders() {
    try {
      const r = await fetch(`${API}/git-providers`);
      providers = (await r.json()).providers || [];
    } catch { providers = []; }
    // R3 (#134/#145) — never leave the repo field blank. If exactly one
    // provider is registered, preselect it. If multiple are registered,
    // preselect the FIRST one too so Next is never stuck disabled —
    // operator can still click another card to change.
    if (providers.length > 0 && !selectedProviderId) {
      selectedProviderId = providers[0].id;
    }
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
    memberOverrides = { ...memberOverrides, [name]: { ...(memberOverrides[name] || {}), resume_uuid: val } };
  }
  // #164 — generic per-member field setter (agent/claude_token_id/resume_uuid).
  function setMemberField(name, key, val) {
    memberOverrides = { ...memberOverrides, [name]: { ...(memberOverrides[name] || {}), [key]: val } };
  }

  function pickShape(s) {
    shape = s;
    teamName = '';   // cleared — set by smart-default on stage 3
    stage = 2;
  }

  async function registerProvider() {
    regBusy = true; regError = '';
    try {
      // #159 + #160 — no separate Label / Embedded-repo-name fields.
      // Embedded providers derive name from team; external use the URL.
      const displayName = regKind === 'embedded'
        ? (teamName || shape || 'workspace')
        : regUrl;
      const body = { kind: regKind, repo_url: regUrl, token: regToken, display_name: displayName };
      const r = await fetch(`${API}/git-providers`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) { const e = await r.json().catch(()=>({})); throw new Error(e.error || `HTTP ${r.status}`); }
      const d = await r.json();
      await loadProviders();
      selectedProviderId = d.id;
      showRegisterForm = false;
      regUrl = ''; regToken = '';
    } catch (e) { regError = e.message || String(e); }
    regBusy = false;
  }

  // #165 — auto-save on token-field blur. No Save&Continue button; only
  // the wizard footer Next moves to stage 3. ✓ tick on success.
  async function autoSaveProvider() {
    regError = ''; regSavedOK = false;
    if (!regUrl) return; // empty URL = nothing to save
    if (detectedKind !== 'embedded' && !regToken) return; // wait for token
    await registerProvider();
    if (!regError) { regSavedOK = true; }
  }

  // #162 — first-time setup shortcut: spawn the embedded Gitea via the
  // existing /git-providers POST with kind=embedded (server bootstraps
  // the container on first agent spawn), then advance.
  async function useEmbeddedGitea() {
    regBusy = true; regError = '';
    try {
      const r = await fetch(`${API}/git-providers`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ kind: 'embedded', display_name: teamName || shape || 'workspace' }),
      });
      if (!r.ok) { throw new Error(await r.text()); }
      const d = await r.json();
      await loadProviders();
      selectedProviderId = d.id;
      next();
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

      const effectiveTeam = teamName || defaultTeamName;

      if (shape === 'solo') {
        const soloName = effectiveTeam || 'solo';
        const ov = memberOverrides[soloName] || memberOverrides['solo'] || {};
        const body = {
          name: soloName,
          agent: ov.agent || 'claude-code',
          team: effectiveTeam,
          role: 'worker',
          provider_id: selectedProviderId,
          use_default_prompt: true,
          claude_token_id: ov.claude_token_id || '',
        };
        if (ov.resume_uuid) body.resume_uuid = ov.resume_uuid;
        const r = await fetch(`${API}/sessions`, {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (!r.ok) {
          // #163 — surface BOTH structured + raw error so silent failures
          // become visible. Some endpoints return text/plain (auth errors)
          // and we were treating those as "ok" because .json() threw.
          let msg = '';
          const ct = r.headers.get('Content-Type') || '';
          if (ct.includes('json')) {
            const e = await r.json().catch(() => ({}));
            msg = e.error || JSON.stringify(e);
          } else {
            msg = await r.text();
          }
          throw new Error(`spawn ${r.status}: ${msg || 'unknown error'}`);
        }
      } else {
        // #164 — forward per-member overrides (agent + claude_token_id +
        // fresh/resume) to the backend's MemberOverrideReq.
        const mo = {};
        for (const [k, v] of Object.entries(memberOverrides)) {
          const entry = {};
          if (v.resume_uuid === 'FRESH') entry.fresh = true;
          else if (v.resume_uuid) entry.resume_uuid = v.resume_uuid;
          if (v.agent) entry.agent = v.agent;
          if (v.claude_token_id) entry.claude_token_id = v.claude_token_id;
          if (Object.keys(entry).length) mo[k] = entry;
        }
        const r = await fetch(`${API}/templates/${shape}/apply`, {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ team: effectiveTeam, provider_id: selectedProviderId, topology, member_overrides: mo }),
        });
        if (!r.ok) {
          let msg = '';
          const ct = r.headers.get('Content-Type') || '';
          if (ct.includes('json')) {
            const e = await r.json().catch(() => ({}));
            msg = e.error || JSON.stringify(e);
          } else {
            msg = await r.text();
          }
          throw new Error(`apply ${r.status}: ${msg || 'unknown error'}`);
        }
      }
      onLaunched?.(); onClose?.();
    } catch (e) { error = e.message || String(e); }
    busy = false;
  }

  // Claude-token stage logic:
  //   - if vault has zero claude-oauth tokens AND no host fallback → MUST visit stage 4
  //   - if exactly one token → skip stage 4 silently (selected automatically)
  //   - if multiple → stage 4 lets operator pick / paste new
  let needsClaudeAuth = $derived(claudeTokens.length === 0);
  let multipleClaudeTokens = $derived(claudeTokens.length > 1);

  function back() { if (stage > 1) { stage -= 1; error = ''; } }
  function nextEnabled() {
    if (stage === 1) return !!shape;
    if (stage === 2 && shape === 'resurrect') return !!pickedResurrect;
    if (stage === 2) return !!selectedProviderId;
    if (stage === 3) return !!(teamName || defaultTeamName);
    return true;
  }
  function next() {
    if (stage === 2 && shape === 'resurrect') { doResurrect(); return; }
    if (stage === 2 && shape === 'custom-yaml') { stage = 4; return; }
    // Advancing 2 → 3: apply the smart team default.
    if (stage === 2 && !teamName) {
      teamName = defaultTeamName;
    }
    stage += 1;
  }

  let selectedProvider = $derived(providers.find(p => p.id === selectedProviderId) || null);
  // #166 — smart team default. Scan live sessions for any matching the
  // selected provider; if found, default to joining that team. Otherwise
  // use the provider's display_name or shape id.
  let matchingExistingTeams = $derived.by(() => {
    if (!selectedProvider || !availableSessions) return [];
    const want = selectedProvider.repo_url || selectedProvider.id;
    const set = new Set();
    for (const s of (availableSessions || [])) {
      if (!s.team) continue;
      if (s.github_url === want || s.cwd?.includes(want)) set.add(s.team);
    }
    return Array.from(set);
  });
  // Sanitise to a valid container/team identifier — alnum + dash only.
  function sanitiseTeam(s) {
    if (!s) return '';
    return s.toString().toLowerCase().replace(/^https?:\/\//,'').replace(/\.git$/,'').replace(/[^a-z0-9]+/g,'-').replace(/^-+|-+$/g,'').slice(0, 40);
  }
  let defaultTeamName = $derived(
    matchingExistingTeams[0]
    || sanitiseTeam(selectedProvider?.display_name)
    || (selectedProvider?.repo_url ? sanitiseTeam(selectedProvider.repo_url.split('/').slice(-1)[0]) : '')
    || shape
    || ''
  );
</script>

<!-- #152: backdrop click does NOT silently close — require an explicit
     Cancel or × click, otherwise an accidental click on the dim
     overlay loses everything the operator just configured. -->
<div class="backdrop">
  <div class="modal" on:click|stopPropagation>
    <header>
      <h2>+ new {shape ? '· ' + shape : ''}</h2>
      <!-- 4-stage flow (#167): shape → repo → members → launch -->
      <div class="dots">
        <span class:active={stage===1}></span>
        <span class:active={stage===2}></span>
        {#if shape && shape !== 'resurrect'}<span class:active={stage===3}></span>{/if}
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
                <!-- #149 — truncate raw operator-instruction descriptions
                     so they don't render as a wall of text. Full text
                     visible on hover via title attribute. -->
                <div class="s-blurb" title={t.description || 'Catalog template'}>{((t.description || 'Catalog template').split('\n')[0] || '').slice(0, 110)}</div>
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

      <!-- ── STAGE 2: Git repo (only) — #164/#165 ─────────────────────────── -->
      {:else if stage === 2}
        <h3>Which repo will they work in?</h3>

        <!-- #165 — Embedded Gitea CTA ALWAYS visible, even after other
             providers are registered. One click → boot Gitea + advance. -->
        <button class="embedded-cta" on:click={useEmbeddedGitea} disabled={regBusy}>
          <div class="cta-title">⚡ Use chepherd's built-in Gitea</div>
          <div class="cta-blurb">No external account needed. chepherd boots a local Gitea container and creates the repo for you.</div>
          {#if regBusy && !regUrl}<div class="cta-status">Starting Gitea…</div>{/if}
        </button>
        {#if providers.length > 0}
          <p class="prose" style="text-align:center;margin:0.8rem 0">— or pick a registered repo —</p>
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
        {/if}

        <p class="prose" style="text-align:center;margin:0.8rem 0">— or connect a new repo —</p>
        <div class="register-form">
          <label class="field-label">Repo URL
            <input bind:value={regUrl} placeholder="https://github.com/org/repo" />
            {#if regUrl}
              <div class="detected-kind">
                <span class="kind-badge">{detectedKind}</span> detected
                {#if tLink}<span class="sep">·</span><a class="token-link" href={tLink.url} target="_blank" rel="noopener">{tLink.label} ↗</a>{/if}
              </div>
            {:else}
              <div class="detected-kind muted">Paste a URL — provider type is auto-detected</div>
            {/if}
          </label>
          {#if detectedKind !== 'embedded'}
            <!-- #165 — token auto-saves on blur (no Save&Continue button).
                 Spinner during validation; ✓ when saved. -->
            <label class="field-label">Access token
              <span class="token-wrap">
                <input type="password" bind:value={regToken} on:blur={autoSaveProvider} placeholder="paste ghp_… and tab away" />
                {#if regBusy}<span class="token-status spinner-inline" title="saving…"></span>{/if}
                {#if regSavedOK && !regBusy}<span class="token-status ok" title="saved">✓</span>{/if}
              </span>
            </label>
          {/if}
          {#if regError}<div class="error">{regError}</div>{/if}
        </div>

      <!-- ── STAGE 3 (#164/#166): team name + per-member agent+cred+fresh ── -->
      {:else if stage === 3}
        {@const ms = membersOfShape()}

        <!-- #166 — team name with smart default. existingTeam is preset from
             live agents that share the chosen repo; operator can override
             to create a new team. -->
        <h3>Team</h3>
        {#if matchingExistingTeams.length > 0}
          <p class="prose">A team already runs on this repo. Add the new agent(s) to it, or pick another name to create a fresh team.</p>
        {/if}
        <label class="field-label">Team name
          <input bind:value={teamName} placeholder={defaultTeamName} list="existing-teams" />
          <datalist id="existing-teams">
            {#each matchingExistingTeams as t}<option value={t}></option>{/each}
          </datalist>
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

        <h3 style="margin-top:1.2rem">{ms.length === 1 ? 'Agent' : `${ms.length} members`}</h3>
        <p class="prose">Each member runs its own CLI with its own credentials. Defaults match the template; adjust per-row as needed.</p>
        <div class="members">
          {#each ms as m}
            {@const mo = memberOverrides[m.name] || {}}
            {@const agentSlug = mo.agent || m.agent || 'claude-code'}
            {@const agentObj = agents.find(a => a.slug === agentSlug)}
            <div class="member-row-grid">
              <div class="m-head">
                <span class="m-icon" class:shepherd={m.role==='shepherd'}>{m.role==='shepherd' ? '✻' : '●'}</span>
                <span class="m-name">{m.name}</span>
                <span class="m-role">· {m.role}</span>
              </div>
              <div class="m-grid">
                <label class="m-sub">Agent
                  <select on:change={(e) => setMemberField(m.name, 'agent', e.target.value)}>
                    {#each agents as a}<option value={a.slug} selected={a.slug === agentSlug}>{a.label}</option>{/each}
                  </select>
                </label>
                {#if agentObj?.requires_auth === 'claude-oauth'}
                  <label class="m-sub">Claude account
                    <select on:change={(e) => setMemberField(m.name, 'claude_token_id', e.target.value)}>
                      <option value="">(auto — newest vault token)</option>
                      {#each claudeTokens as t}<option value={t.id === 'host' ? '' : t.id} selected={(mo.claude_token_id || '') === (t.id === 'host' ? '' : t.id)}>{t.label || t.id}</option>{/each}
                    </select>
                  </label>
                {/if}
                <label class="m-sub">Session
                  <select on:change={(e) => setMemberField(m.name, 'resume_uuid', e.target.value)}>
                    <option value="">⊕ Fresh</option>
                    {#each availableSessions.slice(0, 25) as s}
                      <option value={s.uuid} selected={mo.resume_uuid === s.uuid}>↻ {s.uuid.slice(0,8)} · {new Date(s.modified).toLocaleString()}</option>
                    {/each}
                  </select>
                </label>
              </div>
            </div>
          {/each}
        </div>
        {#if agents.some(a => a.requires_auth === 'claude-oauth') && claudeTokens.length === 0}
          <!-- Inline OAuth-login affordance — if any member needs Claude
               but no token exists, prompt to log in here rather than a
               dedicated stage. -->
          <div class="login-inline">
            <button class="add-repo-btn" on:click={beginOAuthLogin} disabled={oauthBusy}>+ Log in to Claude</button>
            <span class="prose tiny" style="margin-left:.6rem">No Claude credentials yet — you'll need one for the claude-code agent(s).</span>
          </div>
        {/if}

      <!-- (#167 — old stage 4 deleted; Claude account now per-member on stage 3) -->
      <!-- ── STAGE 4: Confirm + Launch ───────────────────────────────────── -->
      {:else if stage === 4}
        <h3>Ready to launch</h3>
        <ul class="confirm">
          <li><strong>Shape:</strong> {shape}</li>
          <li><strong>Team:</strong> {teamName || defaultTeamName || shape}</li>
          <li><strong>Repo:</strong> {selectedProvider?.display_name || selectedProvider?.repo_url || '—'} <span class="p-badge">{selectedProvider?.kind || ''}</span></li>
          <li><strong>Members:</strong>
            <ul>
              {#each membersOfShape() as m}
                {@const mo = memberOverrides[m.name] || {}}
                <li>{m.name} ({m.role}) — agent: {mo.agent || m.agent || 'claude-code'} — {mo.resume_uuid ? '↻ resume' : '⊕ fresh'}{#if mo.claude_token_id} — claude token: {(claudeTokens.find(t => t.id === mo.claude_token_id)?.label) || mo.claude_token_id}{/if}</li>
              {/each}
            </ul>
          </li>
        </ul>

        <!-- OAuth-capture form (inline) — surfaces when operator clicked
             '+ Log in to Claude' on stage 3 but the flow is still mid-air. -->
        {#if oauthMode}
          <div class="register-form" style="margin-top:1rem">
            <p class="prose" style="margin-top:0"><strong>Step 1.</strong> Authorise chepherd at the link below, then paste the code Claude shows you.</p>
            {#if oauthBusy && !oauthURL}
              <div class="login-status">{oauthStatus}</div>
              <div class="spinner-row"><div class="spinner"></div></div>
            {:else if oauthURL}
              <a class="oauth-link" href={oauthURL} target="_blank" rel="noopener">🔑 Open Claude login →</a>
              <label class="field-label">Authorisation code
                <input bind:value={oauthCode} placeholder="(paste the code)" />
              </label>
            {/if}
            {#if oauthError}<div class="error">{oauthError}</div>{/if}
            <div class="reg-actions">
              <button class="ghost" on:click={cancelOAuthLogin}>Cancel</button>
              {#if oauthURL}
                <button class="primary" on:click={submitOAuthCode} disabled={oauthBusy || !oauthCode}>{oauthBusy ? 'Capturing…' : 'Save token'}</button>
              {/if}
            </div>
          </div>
        {/if}
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
  .field-label { display: block; color: var(--fg-muted); font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.04em; margin-top: 0.65rem; }
  .field-label input, .field-label select, .field-label textarea { display: block; width: 100%; margin-top: 0.25rem; padding: 0.45rem 0.6rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; font-size: 0.9rem; box-sizing: border-box; font-family: ui-monospace, monospace; resize: vertical; }
  .field-label small { text-transform: none; letter-spacing: normal; color: var(--fg-faint); }
  .reg-actions { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 0.85rem; }
  /* Auto-detected kind hint + token link */
  .detected-kind { margin-top: 0.3rem; font-size: 0.78rem; color: var(--fg-muted); text-transform: none; letter-spacing: normal; display: flex; align-items: center; gap: 0.35rem; flex-wrap: wrap; }
  .detected-kind.muted { color: var(--fg-faint); font-style: italic; }
  .kind-badge { background: color-mix(in srgb, var(--accent) 12%, transparent); color: var(--accent); border-radius: 4px; padding: 0.1rem 0.4rem; font-size: 0.72rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; }
  .sep { color: var(--fg-faint); }
  .token-link { color: var(--accent-2); text-decoration: none; }
  .token-link:hover { text-decoration: underline; }
  /* Saved teams */
  .saved-teams { display: flex; flex-direction: column; gap: 0.5rem; max-height: 360px; overflow-y: auto; }
  .saved-team { padding: 0.7rem 0.85rem; background: var(--bg); border: 1px solid var(--border-strong); border-radius: 6px; cursor: pointer; text-align: left; color: var(--fg); }
  .saved-team:hover { border-color: var(--accent-2); }
  .saved-team.active { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 6%, transparent); }
  .st-head { color: var(--accent); }
  .st-head small { color: var(--fg-muted); }
  .st-meta { color: var(--fg-muted); margin-top: 0.2rem; font-size: 0.82rem; }
  .m-list { margin: 0.35rem 0 0; padding-left: 1.2rem; color: var(--fg-muted); font-size: 0.82rem; }
  /* #164 — per-member grid: agent + creds + fresh/resume row */
  .member-row-grid { background: var(--bg); border: 1px solid var(--border); border-radius: 6px; padding: 0.55rem 0.7rem; margin-bottom: 0.5rem; }
  .member-row-grid .m-head { margin-bottom: 0.4rem; }
  .m-grid { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 0.45rem; }
  .m-sub { display: block; color: var(--fg-muted); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.04em; }
  .m-sub select { width: 100%; margin-top: 0.18rem; padding: 0.32rem 0.45rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 4px; cursor: pointer; }
  .login-inline { display: flex; align-items: center; margin-top: 0.6rem; }
  /* #165 — token field inline save status */
  .token-wrap { position: relative; display: block; }
  .token-status { position: absolute; right: 0.6rem; top: 50%; transform: translateY(-50%); font-size: 0.9rem; }
  .token-status.ok { color: #5cd57f; font-weight: 700; }
  .token-status.spinner-inline { width: 14px; height: 14px; border: 2px solid color-mix(in srgb, var(--accent) 30%, transparent); border-top-color: var(--accent); border-radius: 50%; animation: chep-spin 0.8s linear infinite; }
  /* Legacy single-column member row */
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
  /* #162 — embedded-Gitea call-to-action card */
  .embedded-cta { display: block; width: 100%; padding: 1rem 1.2rem; background: color-mix(in srgb, var(--accent) 12%, var(--bg)); border: 1px solid var(--accent); border-radius: 10px; cursor: pointer; text-align: left; color: var(--fg); margin-bottom: 0.5rem; transition: background 0.1s; }
  .embedded-cta:hover { background: color-mix(in srgb, var(--accent) 20%, var(--bg)); }
  .embedded-cta:disabled { opacity: 0.7; cursor: progress; }
  .cta-title { font-weight: 600; color: var(--accent); font-size: 1rem; margin-bottom: 0.3rem; }
  .cta-blurb { color: var(--fg-muted); font-size: 0.85rem; line-height: 1.4; }
  .cta-status { color: var(--accent-2); font-size: 0.8rem; margin-top: 0.4rem; }
  /* Agent type cards */
  .agent-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 0.5rem; margin-bottom: 0.5rem; }
  .agent-card { padding: 0.7rem 0.85rem; background: var(--bg); border: 1px solid var(--border-strong); border-radius: 8px; cursor: pointer; text-align: left; color: var(--fg); transition: border 0.1s; }
  .agent-card:hover { border-color: var(--accent-2); }
  .agent-card.active { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 6%, transparent); }
  .a-name { font-weight: 600; color: var(--accent); margin-bottom: 0.2rem; font-size: 0.9rem; }
  .a-desc { color: var(--fg-muted); font-size: 0.78rem; line-height: 1.35; }
  .a-auth { color: var(--fg-faint); font-size: 0.7rem; margin-top: 0.3rem; }
  /* OAuth flow */
  .login-status { color: var(--fg-muted); font-size: 0.88rem; margin: 0.4rem 0; }
  .spinner-row { display: flex; justify-content: center; padding: 0.5rem; }
  .spinner { width: 18px; height: 18px; border: 2px solid color-mix(in srgb, var(--accent) 30%, transparent); border-top-color: var(--accent); border-radius: 50%; animation: chep-spin 0.8s linear infinite; }
  @keyframes chep-spin { to { transform: rotate(360deg); } }
  .oauth-link { display: inline-block; padding: 0.6rem 1.1rem; background: color-mix(in srgb, var(--accent) 14%, transparent); border: 1px solid var(--accent); color: var(--accent); border-radius: 8px; text-decoration: none; font-weight: 600; margin: 0.4rem 0; word-break: break-all; }
  .oauth-link:hover { background: color-mix(in srgb, var(--accent) 22%, transparent); }
</style>

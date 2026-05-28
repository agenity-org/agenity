<!--
  Stage2Repo — v0.9 SpawnWizard Stage 2 (#178, architect pivot 2026-05-27).

  6-card 2×3 provider grid mirroring Stage 1's layout:
    Row 1: Built-in · GitHub · GitLab
    Row 2: Bitbucket · Gitea · On-prem

  Per-card below-the-fold content + per-provider PAT-helper links in
  the Connect modal. Built-in path is a single inline create form
  (operator complaint: 'button over button' — fixed).

  Token-id semantics per #195: when the UI passes a token-id to the
  Discovery API, it's the saved-provider's opaque ID (vault UUID or
  short slug like 'embedded'). Never construct provider:URL composites.

  Props:
    selectedRepo: $bindable — { kind, token_id, full_name, clone_url, default_branch }
    onselect(sel): callback when operator finalises a selection
-->
<script>
  import DiscoveryTree from './DiscoveryTree.svelte';

  let { selectedRepo = $bindable(null), onselect } = $props();

  // Per-provider PAT-create helper links. The scopes are the minimum
  // chepherd needs: discover orgs + repos, clone, push branches +
  // commits, open pull requests. Operators can tighten further per-
  // provider if their orgs require it.
  //
  //   GitHub:    repo + read:org
  //              - repo:        clone / push / branches / PRs
  //              - read:org:    enumerate orgs the user belongs to
  //              (workflow not needed unless chepherd updates Actions)
  //   GitLab:    read_api + read_repository + write_repository
  //              - read_api:           org/project enumeration + PRs
  //              - read_repository:    clone
  //              - write_repository:   push branches + commits
  //              (api scope is "full access" — too broad; use the
  //              three least-privilege scopes instead)
  //   Bitbucket: app passwords don't accept scope params via URL —
  //              instruct operators inline below the link.
  //   Gitea / on-prem: instance-specific, operator's call.
  const PAT_HELPERS = {
    github: {
      url:   'https://github.com/settings/tokens/new?scopes=repo,read:org&description=chepherd',
      label: 'Create a GitHub PAT (scopes: repo + read:org)',
    },
    gitlab: {
      url:   'https://gitlab.com/-/user_settings/personal_access_tokens?scopes=read_api,read_repository,write_repository',
      label: 'Create a GitLab PAT (scopes: read_api + read_repository + write_repository)',
    },
    bitbucket: {
      url:   'https://bitbucket.org/account/settings/app-passwords/',
      label: 'Create a Bitbucket app password — tick: Account(read) · Workspace(read) · Repositories(read+write) · Pull requests(read+write)',
    },
    gitea: {
      url:   '',
      label: 'Create a Gitea access token in your instance settings (scopes: repo + read:org)',
    },
    onprem: {
      url:   '',
      label: 'Open your instance\'s token-create page (scopes: repo read + repo write at minimum)',
    },
  };

  // 6 provider cards in canonical order.
  const CARDS = [
    { id: 'builtin',   name: 'Built-in',   sub: 'Embedded sandbox',     icon: 'Home',      kind: 'builtin'  },
    { id: 'github',    name: 'GitHub',     sub: 'github.com',           icon: 'GitHub',    kind: 'github'   },
    { id: 'gitlab',    name: 'GitLab',     sub: 'gitlab.com',           icon: 'GitLab',    kind: 'gitlab'   },
    { id: 'bitbucket', name: 'Bitbucket',  sub: 'bitbucket.org',        icon: 'Bitbucket', kind: 'bitbucket'},
    { id: 'gitea',     name: 'Gitea',      sub: 'self-hosted',          icon: 'Gitea',     kind: 'gitea'    },
    { id: 'onprem',    name: 'On-prem',    sub: 'custom git URL',       icon: 'Globe',     kind: 'onprem'   },
  ];

  // Inline SVG icon paths.
  const icons = {
    Home:      '<path d="M10 20v-6h4v6h5v-8h3L12 3 2 12h3v8z"/>',
    GitHub:    '<path d="M12 2C6.5 2 2 6.5 2 12c0 4.4 2.9 8.2 6.8 9.5.5.1.7-.2.7-.5v-1.7c-2.8.6-3.4-1.4-3.4-1.4-.5-1.2-1.1-1.5-1.1-1.5-.9-.6.1-.6.1-.6 1 .1 1.5 1 1.5 1 .9 1.5 2.3 1.1 2.9.8.1-.7.4-1.1.7-1.4-2.2-.3-4.6-1.1-4.6-5 0-1.1.4-2 1-2.7-.1-.2-.4-1.3.1-2.7 0 0 .8-.3 2.7 1 .8-.2 1.7-.3 2.5-.3.8 0 1.7.1 2.5.3 1.9-1.3 2.7-1 2.7-1 .5 1.4.2 2.5.1 2.7.6.7 1 1.6 1 2.7 0 3.9-2.4 4.7-4.6 5 .4.3.7.9.7 1.9V21c0 .3.2.6.7.5C19.1 20.2 22 16.4 22 12c0-5.5-4.5-10-10-10z"/>',
    GitLab:    '<path d="M22.7 13.5l-1.3-4-2.6-7.9c-.1-.4-.6-.6-1-.5-.2.1-.4.3-.5.5L14.7 9.5H9.3L6.7 1.6c-.1-.4-.6-.6-1-.5-.2.1-.4.3-.5.5L2.6 9.5l-1.3 4c-.1.4 0 .8.3 1L12 22.5l10.4-8c.3-.2.4-.6.3-1z"/>',
    Bitbucket: '<path d="M3 2l2.4 13.5c.1.7.7 1.2 1.4 1.2h10.4c.5 0 1-.4 1.1-.9L21 2H3zm10.5 10.7h-3l-.7-4.4h4.4l-.7 4.4z"/>',
    Gitea:     '<circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" stroke-width="2"/><path d="M9 8v4c0 1.7 1.3 3 3 3" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
    Globe:     '<circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" stroke-width="2"/><line x1="3" y1="12" x2="21" y2="12" stroke="currentColor" stroke-width="2"/><path d="M12 3 a9 9 0 0 1 0 18 a9 9 0 0 1 0 -18" fill="none" stroke="currentColor" stroke-width="2"/>',
  };

  let mode = $state('');                 // "" | id from CARDS
  let providers = $state([]);
  let activeTokenID = $state('');
  let newSandboxName = $state('');
  let pasteToken = $state('');
  let pasteURL = $state('');

  async function loadProviders() {
    try {
      const r = await fetch('/api-v08/v1/git-providers');
      if (!r.ok) return;
      const j = await r.json();
      // Empty-state bug: the API returns {"providers": null} on a
      // fresh state-dir. Old code "j.providers || j || []" then set
      // providers to the literal {providers:null} object (truthy),
      // which broke .filter() in builtinSandboxes + killed every
      // {#if mode === 'builtin'} block. Always coerce to array.
      providers = Array.isArray(j.providers) ? j.providers
                : Array.isArray(j) ? j
                : [];
    } catch {}
  }
  $effect(() => { loadProviders(); });

  // Filter saved providers to those matching the active card's kind.
  const providersForKind = $derived.by(() => {
    if (!mode || mode === 'builtin') return [];
    const k = mode === 'onprem' ? 'gitea' : mode; // on-prem reuses gitea provider kind for now
    return providers.filter(p => (p.kind || '') === k);
  });
  // Auto-select the most-recent saved provider for the active kind.
  $effect(() => {
    if (providersForKind.length > 0 && !activeTokenID) {
      activeTokenID = providersForKind[0].id;
    }
  });
  // Reset active when mode changes
  $effect(() => {
    if (mode) { activeTokenID = ''; }
  });

  const builtinProvider = $derived(providers.find(p => (p.kind || '') === 'embedded' || p.id === 'embedded'));
  const builtinSandboxes = $derived(providers.filter(p => (p.kind || '') === 'embedded'));

  function pickBuiltin(name) {
    selectedRepo = {
      kind: 'builtin',
      token_id: 'embedded',
      full_name: 'chepherd-admin/' + name,
      clone_url: '',
      default_branch: 'main',
    };
    onselect?.(selectedRepo);
  }
  function pickRemote(repo) {
    selectedRepo = {
      kind: mode,
      token_id: activeTokenID,
      full_name: repo.full_name,
      clone_url: repo.clone_url,
      default_branch: repo.default_branch,
    };
    onselect?.(selectedRepo);
  }

  // One token per provider kind. The "saved providers list" concept
  // is GONE — no chip selectors, no Connect modal. Either there's a
  // valid token (discovery tree renders) or there isn't (inline paste
  // form renders). Server-side validate-before-save guarantees bad
  // tokens never enter state, so the "stale provider" UX category
  // doesn't exist by design.
  let connectError = $state('');
  let connectSaving = $state(false);

  // showPasteFor[kind] = true → render inline paste form even when a
  // token is already saved (operator wants to swap to a different token).
  let showPasteFor = $state({});

  // Canonical homepage URL per provider kind. For token-only flows
  // (paste a PAT, no specific repo URL), the saved provider record
  // points at the operator's account on the provider's homepage; the
  // discovery layer enumerates orgs/repos the token grants access to.
  const DEFAULT_REPO_URL = {
    github:    'https://github.com',
    gitlab:    'https://gitlab.com',
    bitbucket: 'https://bitbucket.org',
    gitea:     '',          // user must supply instance URL
    onprem:    '',          // user must supply instance URL
  };

  async function saveProviderInline(kind) {
    connectError = '';
    if (!pasteToken.trim()) {
      connectError = 'Paste a token first.';
      return;
    }
    const effectiveURL = pasteURL.trim() || DEFAULT_REPO_URL[kind] || '';
    if (!effectiveURL) {
      connectError = 'Instance URL is required for ' + kind + '.';
      return;
    }
    const body = {
      kind: kind === 'onprem' ? 'gitea' : kind,
      display_name: pasteURL.trim() || effectiveURL,
      repo_url: effectiveURL,
      token: pasteToken.trim(),
    };
    connectSaving = true;
    try {
      const r = await fetch('/api-v08/v1/git-providers', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) {
        const t = await r.text();
        let msg = t.trim();
        let detail = '';
        try {
          const j = JSON.parse(t);
          msg = j.error || msg;
          detail = j.detail || '';
        } catch {}
        connectError = detail ? `${msg} — ${detail}` : (msg || `HTTP ${r.status}`);
        return;
      }
      const saved = await r.json();
      pasteToken = '';
      pasteURL = '';
      showPasteFor[kind] = false;
      await loadProviders();
      // Auto-select the (now sole) provider for this kind.
      if (saved && saved.id) activeTokenID = saved.id;
    } catch (e) {
      connectError = 'Network error: ' + (e?.message || e);
    } finally {
      connectSaving = false;
    }
  }
</script>

<div class="stage2">
  <h2>Where's the code?</h2>
  <p class="lead">Pick a provider. Saved accounts auto-load their repo tree below.</p>

  <div class="grid" role="radiogroup" aria-label="provider">
    {#each CARDS as c}
      <button
        type="button"
        class="card"
        class:selected={mode === c.id}
        aria-pressed={mode === c.id}
        onclick={() => mode = c.id}
      >
        <span class="icon" aria-hidden="true">
          <svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor">{@html icons[c.icon] || '<circle cx="12" cy="12" r="6"/>'}</svg>
        </span>
        <span class="name">{c.name}</span>
        <span class="sub">{c.sub}</span>
      </button>
    {/each}
  </div>

  {#if mode === 'builtin'}
    <section class="below">
      <h3>Existing repos</h3>
      {#if builtinSandboxes.length === 0}
        <p class="hint">No repos yet. Name a new one below — valid name auto-commits.</p>
      {:else}
        <div class="sandbox-grid">
          {#each builtinSandboxes as p}
            <button
              type="button"
              class="sandbox-card"
              class:selected={selectedRepo?.full_name?.endsWith('/' + (p.display_name || p.id))}
              onclick={() => pickBuiltin(p.display_name || p.id)}
            >
              <span class="sand-name">⌘ {p.display_name || p.id}</span>
              <span class="sand-when">{new Date(p.registered_at || Date.now()).toLocaleDateString()}</span>
            </button>
          {/each}
        </div>
      {/if}

      <div class="newsandbox">
        <label for="new-sb">Or create a new repo</label>
        <input
          id="new-sb"
          type="text"
          bind:value={newSandboxName}
          placeholder="my-repo"
          onblur={() => {
            const v = newSandboxName.trim();
            if (/^[a-z0-9-]{3,40}$/.test(v)) pickBuiltin(v);
          }}
          onkeydown={(e) => {
            if (e.key === 'Enter') {
              const v = newSandboxName.trim();
              if (/^[a-z0-9-]{3,40}$/.test(v)) pickBuiltin(v);
            }
          }}
        />
        {#if newSandboxName && !/^[a-z0-9-]{3,40}$/.test(newSandboxName.trim())}
          <p class="hint name-hint">Use lowercase letters, digits, hyphens — 3 to 40 chars.</p>
        {/if}
      </div>

      {#if selectedRepo?.kind === 'builtin'}
        <p class="picked">Selected: <strong>{selectedRepo.full_name}</strong></p>
      {/if}
    </section>
  {:else if mode}
    <section class="below">
      {#if providersForKind.length > 0 && !showPasteFor[mode]}
        <!-- Connected — show discovery tree + small "use a different token" toggle.
             The "stale provider" category is impossible by design: validate-
             before-save on the server (POST /api/v1/git-providers calls the
             provider's /user endpoint with the token); we only persist 2xx
             responses. One token per provider kind — saving replaces. -->
        <header class="connected-header">
          <span class="connected">⚓ {providersForKind[0].display_name || providersForKind[0].id}</span>
          <button type="button" class="link" onclick={() => { showPasteFor[mode] = true; pasteToken = ''; pasteURL = ''; connectError = ''; }}>↻ Use a different token</button>
        </header>
        <DiscoveryTree
          tokenID={providersForKind[0].id}
          onpick={pickRemote}
          selected={selectedRepo?.full_name || ''}
        />
        {#if selectedRepo?.kind === mode}
          <p class="picked">Selected: <strong>{selectedRepo.full_name}</strong> ({selectedRepo.default_branch})</p>
        {/if}
      {:else}
        <!-- No saved token yet (or operator clicked "use different token"): paste form inline.
             No modal, no chip list, no Connect-new button. The form IS the
             entry point. -->
        <header class="paste-header">
          <h3>Connect {mode === 'github' ? 'GitHub' : mode === 'gitlab' ? 'GitLab' : mode === 'bitbucket' ? 'Bitbucket' : mode === 'gitea' ? 'Gitea' : 'on-prem Git'}</h3>
          {#if providersForKind.length > 0}
            <button type="button" class="link" onclick={() => { showPasteFor[mode] = false; connectError = ''; }}>Cancel — keep existing token</button>
          {/if}
        </header>
        {#if PAT_HELPERS[mode]?.url}
          <p class="helper">
            <a href={PAT_HELPERS[mode].url} target="_blank" rel="noopener">{PAT_HELPERS[mode].label} ↗</a>
          </p>
        {:else if PAT_HELPERS[mode]?.label}
          <p class="helper">{PAT_HELPERS[mode].label}</p>
        {/if}
        {#if mode === 'gitea' || mode === 'onprem'}
          <label class="onprem-row">
            <span>Instance URL</span>
            <input type="text" bind:value={pasteURL} placeholder={mode === 'onprem' ? 'https://git.example.com' : 'https://gitea.example.com'} />
          </label>
        {/if}
        <!-- For github / gitlab / bitbucket the URL is the canonical
             host (github.com etc.) — no field shown. Operators using
             self-hosted GitHub Enterprise / GitLab self-managed pick
             the "On-prem" card instead, which collects the URL. -->
        <label class="onprem-row">
          <span>Personal Access Token</span>
          <input type="password" bind:value={pasteToken} placeholder="paste here" autocomplete="off" />
        </label>
        {#if connectError}
          <p class="connect-error" role="alert">⚠ {connectError}</p>
        {/if}
        <button class="primary" onclick={() => saveProviderInline(mode)} disabled={!pasteToken.trim() || connectSaving}>
          {connectSaving ? 'Validating…' : 'Save + Connect'}
        </button>
      {/if}
    </section>
  {/if}
</div>

<style>
  .stage2 { padding: 1.25rem; }
  h2 { font-size: 1.15rem; margin: 0 0 0.35rem 0; }
  h3 { font-size: 0.92rem; margin: 0 0 0.5rem 0; color: var(--fg, #fff); }
  .lead { color: var(--fg-muted, #888); margin: 0 0 1.2rem 0; font-size: 0.9rem; }
  .grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 0.7rem; margin-bottom: 1rem; }
  .card {
    display: flex; flex-direction: column; align-items: center; gap: 0.4rem;
    padding: 1.05rem 0.55rem 0.95rem; border-radius: 8px;
    background: var(--bg-elevated, #1a1a1a);
    border: 1.5px solid var(--border, #2a2a2a);
    color: var(--fg, #f5f5f5);
    cursor: pointer; font: inherit; text-align: center;
  }
  .card:hover { border-color: var(--accent-2, #87ceeb); }
  .card.selected { border-color: var(--accent-2, #87ceeb); background: rgba(135,206,235,0.08); box-shadow: 0 0 0 2px rgba(135,206,235,0.18) inset; }
  .card .icon { color: var(--accent-2, #87ceeb); }
  .card .name { font-weight: 600; font-size: 0.92rem; }
  .card .sub { color: var(--fg-muted, #888); font-size: 0.74rem; }
  .below { background: var(--bg-elevated, #1a1a1a); border: 1px solid var(--border, #2a2a2a); border-radius: 8px; padding: 0.85rem 1rem; }
  /* #200 Bug 5: compact 3-per-row card grid for existing repos */
  .sandbox-grid {
    display: grid; grid-template-columns: repeat(3, 1fr); gap: 0.45rem;
    margin: 0 0 0.75rem 0;
  }
  .sandbox-card {
    display: flex; flex-direction: column; align-items: flex-start; gap: 0.2rem;
    background: var(--bg, #0a0a0a); border: 1px solid var(--border, #2a2a2a);
    color: var(--fg, #f5f5f5); padding: 0.45rem 0.6rem; border-radius: 5px;
    cursor: pointer; font: inherit; text-align: left;
    transition: border-color 80ms;
  }
  .sandbox-card:hover { border-color: var(--accent-2, #87ceeb); }
  .sandbox-card.selected { background: rgba(135,206,235,0.12); border-color: var(--accent-2, #87ceeb); }
  .sand-name { font-weight: 600; font-size: 0.84rem; }
  .sand-when { color: var(--fg-muted, #888); font-size: 0.72rem; }
  .newsandbox { margin-top: 0.6rem; }
  .newsandbox label { color: var(--fg-muted, #888); font-size: 0.82rem; margin-bottom: 0.3rem; display: block; }
  .newsandbox input { width: 100%; box-sizing: border-box; padding: 0.4rem 0.55rem; border-radius: 4px; border: 1px solid var(--border, #2a2a2a); background: var(--bg, #0a0a0a); color: var(--fg, #f5f5f5); font: inherit; }
  .name-hint { color: var(--danger, #e74c3c); font-size: 0.74rem; margin: 0.3rem 0 0 0; font-style: italic; }
  .primary { background: var(--accent-2, #87ceeb); border: 0; color: #0a0a0a; padding: 0.45rem 0.95rem; border-radius: 4px; cursor: pointer; font-weight: 600; }
  .primary:disabled { opacity: 0.4; cursor: not-allowed; }
  .chips { display: flex; flex-wrap: wrap; gap: 0.4rem; margin-bottom: 0.65rem; }
  .chip { background: var(--bg, #0a0a0a); border: 1px solid var(--border, #2a2a2a); color: var(--fg, #f5f5f5); padding: 0.32rem 0.65rem; border-radius: 999px; cursor: pointer; font: inherit; font-size: 0.85rem; }
  .chip:hover { border-color: var(--accent-2, #87ceeb); }
  .chip.active { background: rgba(135,206,235,0.18); border-color: var(--accent-2, #87ceeb); }
  .chip.add { color: var(--accent-2, #87ceeb); border-style: dashed; }
  .picked { margin-top: 0.65rem; color: var(--fg, #fff); font-size: 0.88rem; }
  .picked strong { color: var(--accent-2, #87ceeb); }
  .hint { color: var(--fg-muted, #888); font-size: 0.85rem; }
  .onprem-form { display: flex; flex-direction: column; gap: 0.5rem; }
  .onprem-row { display: flex; flex-direction: column; gap: 0.2rem; font-size: 0.78rem; color: var(--fg-muted, #888); }
  .onprem-row input { padding: 0.4rem 0.55rem; border-radius: 4px; border: 1px solid var(--border, #2a2a2a); background: var(--bg, #0a0a0a); color: var(--fg, #f5f5f5); font: inherit; }

  /* Connected-token header (inline, no modal) */
  .connected-header { display: flex; align-items: center; gap: 0.6rem; margin-bottom: 0.65rem; }
  .connected { font-weight: 600; color: var(--accent-2, #87ceeb); font-size: 0.88rem; }
  .paste-header { display: flex; align-items: center; gap: 0.6rem; margin-bottom: 0.55rem; }
  .paste-header h3 { margin: 0; flex: 1; }
  .helper { margin: 0 0 0.35rem 0; font-size: 0.82rem; color: var(--fg-muted, #aaa); }
  .helper a { color: var(--accent-2, #87ceeb); text-decoration: none; }
  .helper a:hover { text-decoration: underline; }
  .link {
    background: transparent; border: 0; color: var(--accent-2, #87ceeb);
    cursor: pointer; font: inherit; font-size: 0.78rem; padding: 0;
    text-decoration: underline;
  }
  .link:hover { color: var(--fg, #fff); }
  .connect-error {
    margin: 0; padding: 0.45rem 0.7rem;
    background: rgba(231,76,60,0.10); border: 1px solid rgba(231,76,60,0.35);
    border-radius: 4px; color: #e74c3c; font-size: 0.85rem;
  }
  .optional { color: var(--fg-muted, #888); font-style: italic; font-size: 0.72rem; font-weight: 400; }
</style>

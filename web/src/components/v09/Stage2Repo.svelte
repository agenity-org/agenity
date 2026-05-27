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

  // Per-provider PAT-create helper links — the architect-flagged fix
  // for "no URL guidance for creating tokens". Each opens the
  // provider's scoped token-create page.
  const PAT_HELPERS = {
    github:    { url: 'https://github.com/settings/tokens/new?scopes=repo,read:org&description=chepherd', label: 'Create a GitHub PAT' },
    gitlab:    { url: 'https://gitlab.com/-/user_settings/personal_access_tokens?scopes=api,read_repository', label: 'Create a GitLab PAT' },
    bitbucket: { url: 'https://bitbucket.org/account/settings/app-passwords/', label: 'Create a Bitbucket app password' },
    gitea:     { url: '', label: 'Create a Gitea access token (in your instance settings)' },
    onprem:    { url: '', label: 'Open your instance\'s token-create page' },
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
  let connectModalOpen = $state(false);
  let connectKind = $state('');
  let pasteToken = $state('');
  let pasteURL = $state('');

  async function loadProviders() {
    try {
      const r = await fetch('/api-v08/v1/git-providers');
      if (!r.ok) return;
      const j = await r.json();
      providers = j.providers || j || [];
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

  function openConnectModal(kind) {
    connectKind = kind;
    pasteToken = '';
    pasteURL = '';
    connectModalOpen = true;
  }
  async function saveTokenAndConnect() {
    if (!pasteToken.trim()) return;
    const body = {
      kind: connectKind === 'onprem' ? 'gitea' : connectKind,
      display_name: pasteURL || (connectKind + '-' + new Date().toISOString().slice(0,10)),
      repo_url: pasteURL || '',
      token: pasteToken.trim(),
    };
    try {
      const r = await fetch('/api-v08/v1/git-providers', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!r.ok) {
        const t = await r.text();
        alert('Save failed: ' + t);
        return;
      }
      const saved = await r.json();
      connectModalOpen = false;
      await loadProviders();
      // Auto-select the newly-created provider
      if (saved && saved.id) activeTokenID = saved.id;
    } catch (e) {
      alert('Network error: ' + e);
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
  {:else if mode === 'onprem'}
    <section class="below">
      <h3>On-prem / self-hosted Git</h3>
      <div class="onprem-form">
        <label class="onprem-row">
          <span>URL</span>
          <input type="text" bind:value={pasteURL} placeholder="https://git.example.com" />
        </label>
        <label class="onprem-row">
          <span>PAT</span>
          <input type="password" bind:value={pasteToken} placeholder="paste your access token" />
        </label>
        <button class="primary" onclick={saveTokenAndConnect} disabled={!pasteToken.trim() || !pasteURL.trim()}>Connect + Discover</button>
      </div>
    </section>
  {:else if mode}
    <section class="below">
      <h3>Accounts</h3>
      {#if providersForKind.length === 0}
        <p class="hint">No saved {mode} accounts yet.</p>
        <button class="primary" onclick={() => openConnectModal(mode)}>+ Connect new account</button>
      {:else}
        <div class="chips">
          {#each providersForKind as p}
            <button
              type="button"
              class="chip"
              class:active={activeTokenID === p.id}
              onclick={() => activeTokenID = p.id}
            >⚓ {p.display_name || p.id}</button>
          {/each}
          <button type="button" class="chip add" onclick={() => openConnectModal(mode)}>+ Connect new</button>
        </div>
        {#if activeTokenID}
          <DiscoveryTree
            tokenID={activeTokenID}
            onpick={pickRemote}
            selected={selectedRepo?.full_name || ''}
          />
        {/if}
        {#if selectedRepo?.kind === mode}
          <p class="picked">Selected: <strong>{selectedRepo.full_name}</strong> ({selectedRepo.default_branch})</p>
        {/if}
      {/if}
    </section>
  {/if}
</div>

{#if connectModalOpen}
  <div class="modal-bg" role="dialog" aria-modal="true">
    <div class="modal">
      <header>
        <h3>Connect {connectKind === 'github' ? 'GitHub' : connectKind === 'gitlab' ? 'GitLab' : connectKind === 'bitbucket' ? 'Bitbucket' : 'Gitea'}</h3>
        <button class="x" onclick={() => connectModalOpen = false} aria-label="close">×</button>
      </header>
      <div class="modal-body">
        {#if PAT_HELPERS[connectKind]?.url}
          <p class="helper">
            <a href={PAT_HELPERS[connectKind].url} target="_blank" rel="noopener">{PAT_HELPERS[connectKind].label} ↗</a>
          </p>
        {:else}
          <p class="helper">{PAT_HELPERS[connectKind]?.label}</p>
        {/if}
        {#if connectKind === 'gitea'}
          <label class="onprem-row"><span>Instance URL</span><input type="text" bind:value={pasteURL} placeholder="https://gitea.example.com" /></label>
        {/if}
        <label class="onprem-row">
          <span>Personal Access Token</span>
          <input type="password" bind:value={pasteToken} placeholder="paste here" />
        </label>
        <div class="modal-actions">
          <button class="ghost" onclick={() => connectModalOpen = false}>Cancel</button>
          <button class="primary" onclick={saveTokenAndConnect} disabled={!pasteToken.trim()}>Save + Connect</button>
        </div>
      </div>
    </div>
  </div>
{/if}

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

  /* Connect modal */
  .modal-bg { position: fixed; inset: 0; background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 200; }
  .modal { background: #0a0a0a; border: 1px solid #2a2a2a; border-radius: 10px; width: 480px; max-width: 92vw; }
  .modal header { display: flex; align-items: center; padding: 0.6rem 1rem; border-bottom: 1px solid #2a2a2a; }
  .modal header h3 { flex: 1; margin: 0; font-size: 0.95rem; }
  .x { background: transparent; border: 0; color: #888; cursor: pointer; font-size: 1.2rem; padding: 0 0.4rem; }
  .modal-body { padding: 0.9rem 1rem 1.1rem; display: flex; flex-direction: column; gap: 0.6rem; }
  .helper a { color: var(--accent-2, #87ceeb); text-decoration: none; }
  .helper a:hover { text-decoration: underline; }
  .modal-actions { display: flex; gap: 0.4rem; justify-content: flex-end; }
  .ghost { background: transparent; border: 1px solid #2a2a2a; color: var(--fg-muted, #888); padding: 0.45rem 0.75rem; border-radius: 4px; cursor: pointer; }
</style>

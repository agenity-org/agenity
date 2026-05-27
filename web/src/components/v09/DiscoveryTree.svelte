<!--
  DiscoveryTree — Org/Repo tree picker for v0.9 SpawnWizard Stage 2
  (#174 + #178). Renders the result of GET /api/v1/discovery/?token-id=…
  as an expandable tree. Repos are click-selectable; the selection
  bubbles up via onpick(repo).

  Props:
    tokenID:    saved-provider id (e.g. "github:https://github.com/org")
    selected:   currently-selected repo's full_name ("" if none)
    onpick(repo): callback when operator clicks a repo

  States:
    - loading: spinner while the first fetch is in flight
    - error:   when the provider errors (e.g. no token saved)
    - empty:   token resolved but no repos visible
    - ready:   tree rendered with search + selection
-->
<script>
  let { tokenID, selected = $bindable(''), onpick } = $props();

  let result = $state(null);
  let loading = $state(false);
  let error = $state('');
  let query = $state('');

  async function load(force = false) {
    if (!tokenID) return;
    loading = true;
    error = '';
    try {
      const url = force ? `/api-v08/v1/discovery/${encodeURIComponent(tokenID)}/refresh` : `/api-v08/v1/discovery/?token-id=${encodeURIComponent(tokenID)}`;
      const r = await fetch(url, { method: force ? 'POST' : 'GET' });
      if (!r.ok) {
        const t = await r.text();
        throw new Error(t.trim() || `HTTP ${r.status}`);
      }
      result = await r.json();
    } catch (e) {
      error = String(e.message || e);
      result = null;
    } finally {
      loading = false;
    }
  }

  $effect(() => { load(); });

  // Filter the tree by query (client-side; server-side ?q= used when
  // server-reported tree size > 500 — that case lives in the parent
  // which calls .../repos?q= directly).
  const filtered = $derived.by(() => {
    if (!result) return [];
    const q = query.trim().toLowerCase();
    return result.orgs.map(org => ({
      ...org,
      repos: q === '' ? org.repos : org.repos.filter(r => r.full_name.toLowerCase().includes(q)),
    })).filter(org => org.repos.length > 0);
  });

  function pick(repo) {
    selected = repo.full_name;
    onpick?.(repo);
  }
</script>

<div class="tree">
  {#if loading && !result}
    <p class="hint">Fetching repos…</p>
  {:else if error}
    <p class="err">⚠ {error}</p>
    <button class="ghost" onclick={() => load(true)} type="button">Try again</button>
  {:else if !result}
    <p class="hint">No data.</p>
  {:else}
    <div class="row">
      <input
        class="search"
        type="search"
        placeholder="Search repos…"
        bind:value={query}
      />
      <button class="ghost refresh" onclick={() => load(true)} type="button" title="Refresh">↻</button>
    </div>
    <div class="identity">
      Signed in as <strong>{result.identity.login}</strong>
      {#if result.identity.display_name}<span class="dim">· {result.identity.display_name}</span>{/if}
    </div>
    {#if filtered.length === 0}
      <p class="hint">No repos match "{query}".</p>
    {/if}
    <ul class="orgs">
      {#each filtered as org}
        <li class="org">
          <div class="org-head">📁 {org.name}</div>
          <ul class="repos">
            {#each org.repos as repo}
              <li>
                <button
                  type="button"
                  class="repo"
                  class:selected={repo.full_name === selected}
                  onclick={() => pick(repo)}
                >
                  <span class="repo-name">{repo.name}</span>
                  <span class="vis vis-{repo.visibility}">{repo.visibility}</span>
                  <span class="branch">· {repo.default_branch || 'main'}</span>
                </button>
              </li>
            {/each}
          </ul>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .tree { padding: 0.6rem 0.85rem; max-height: 360px; overflow-y: auto; }
  .row { display: flex; gap: 0.5rem; margin-bottom: 0.5rem; }
  .search { flex: 1; padding: 0.35rem 0.55rem; border-radius: 4px; border: 1px solid var(--border, #2a2a2a); background: var(--bg, #0a0a0a); color: var(--fg, #f5f5f5); font: inherit; }
  .ghost { background: transparent; border: 1px solid var(--border, #2a2a2a); color: var(--fg-muted, #888); padding: 0.35rem 0.6rem; border-radius: 4px; cursor: pointer; }
  .ghost:hover { color: var(--accent-2, #87ceeb); border-color: var(--accent-2, #87ceeb); }
  .refresh { padding: 0.35rem 0.55rem; }
  .identity { color: var(--fg-muted, #888); font-size: 0.82rem; margin-bottom: 0.55rem; }
  .dim { opacity: 0.65; }
  .orgs { list-style: none; padding: 0; margin: 0; }
  .org { margin-bottom: 0.65rem; }
  .org-head { font-weight: 600; font-size: 0.88rem; color: var(--fg, #fff); margin-bottom: 0.2rem; }
  .repos { list-style: none; padding: 0 0 0 0.65rem; margin: 0; }
  .repos li { margin: 0.15rem 0; }
  .repo {
    display: inline-flex; align-items: center; gap: 0.45rem;
    width: 100%; text-align: left;
    background: transparent; border: 1px solid transparent;
    color: var(--fg, #f5f5f5);
    padding: 0.28rem 0.55rem; border-radius: 4px; cursor: pointer; font: inherit; font-size: 0.85rem;
  }
  .repo:hover { background: rgba(135, 206, 235, 0.05); }
  .repo.selected { background: rgba(135, 206, 235, 0.18); border-color: var(--accent-2, #87ceeb); }
  .repo-name { font-weight: 600; }
  .vis { font-size: 0.7rem; padding: 0.04rem 0.4rem; border-radius: 3px; }
  .vis-public { background: rgba(95, 215, 95, 0.18); color: #5fd75f; }
  .vis-private { background: rgba(255, 165, 0, 0.18); color: #ffa500; }
  .branch { color: var(--fg-muted, #888); font-size: 0.78rem; }
  .hint { color: var(--fg-muted, #888); font-size: 0.85rem; }
  .err { color: var(--danger, #e74c3c); font-size: 0.85rem; }
</style>

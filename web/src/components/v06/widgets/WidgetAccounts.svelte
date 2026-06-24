<!--
  WidgetAccounts — one place to see + manage every connected credential.
  Lists vault entries grouped by provider category:
    Claude  (claude-oauth, anthropic-api)
    GitHub  (github-pat, plus the legacy git-providers list for github kind)
    GitLab  (gitlab-pat + git-providers)
    Bitbucket (git-providers)
    Gitea (git-providers)
    Other (anything else)

  Per-entry actions: delete + open the wizard's connect surface (when
  the operator wants to swap a token).

  Read-only on the actual token VALUE — chepherd's vault never returns
  the secret over the API once stored (has_token: bool only).
-->
<script>
  let vault = $state([]);
  let providers = $state([]);
  let loading = $state(true);
  let error = $state('');

  const API = '/api-v08/v1';

  async function load() {
    loading = true; error = '';
    try {
      const [vRes, pRes] = await Promise.all([
        fetch(`${API}/vault`),
        fetch(`${API}/git-providers`),
      ]);
      const vJ = vRes.ok ? await vRes.json() : [];
      vault = Array.isArray(vJ) ? vJ : (Array.isArray(vJ.creds) ? vJ.creds : []);
      const pJ = pRes.ok ? await pRes.json() : { providers: [] };
      providers = Array.isArray(pJ.providers) ? pJ.providers : (Array.isArray(pJ) ? pJ : []);
    } catch (e) {
      error = String(e?.message || e);
    } finally {
      loading = false;
    }
  }

  async function deleteVaultEntry(id) {
    if (!confirmDelete(id)) return;
    try {
      const r = await fetch(`${API}/vault/${encodeURIComponent(id)}`, { method: 'DELETE' });
      if (!r.ok) throw new Error(await r.text());
      await load();
    } catch (e) {
      error = 'delete failed: ' + (e?.message || e);
    }
  }

  async function deleteGitProvider(id) {
    if (!confirmDelete(id)) return;
    try {
      // Git-provider IDs are composite ("github:https://...") — must
      // use the ?id= query-param form, not path segment (Go ServeMux
      // 301-redirects on "//" and breaks the lookup).
      const r = await fetch(`${API}/git-providers/?id=${encodeURIComponent(id)}`, { method: 'DELETE' });
      if (!r.ok) throw new Error(await r.text());
      await load();
    } catch (e) {
      error = 'delete failed: ' + (e?.message || e);
    }
  }

  // Inline confirmation state instead of window.confirm (no browser dialogs).
  let pendingDelete = $state('');
  function confirmDelete(id) {
    if (pendingDelete === id) {
      pendingDelete = '';
      return true;
    }
    pendingDelete = id;
    setTimeout(() => { if (pendingDelete === id) pendingDelete = ''; }, 5000);
    return false;
  }

  // Group every credential into one list with normalised display.
  const sections = $derived.by(() => {
    const groups = {
      claude:    { label: 'Claude',    icon: '✻', entries: [] },
      github:    { label: 'GitHub',    icon: '⓪', entries: [] },
      gitlab:    { label: 'GitLab',    icon: '⓪', entries: [] },
      bitbucket: { label: 'Bitbucket', icon: '⓪', entries: [] },
      gitea:     { label: 'Gitea',     icon: '⓪', entries: [] },
      other:     { label: 'Other',     icon: '⚓', entries: [] },
    };
    for (const v of vault) {
      const p = (v.provider || '').toLowerCase();
      let bucket = 'other';
      if (p === 'claude-oauth' || p === 'anthropic-api')           bucket = 'claude';
      else if (p === 'github-pat')                                  bucket = 'github';
      else if (p === 'gitlab-pat')                                  bucket = 'gitlab';
      else if (p.startsWith('github'))                              bucket = 'github';
      else if (p.startsWith('gitlab'))                              bucket = 'gitlab';
      else if (p.startsWith('bitbucket'))                           bucket = 'bitbucket';
      else if (p.startsWith('gitea'))                               bucket = 'gitea';
      groups[bucket].entries.push({
        kind: 'vault', id: v.id, provider: v.provider,
        label: v.label || v.id,
        sub: v.provider_label || v.provider,
        when: v.created_at || v.updated_at,
        del: () => deleteVaultEntry(v.id),
      });
    }
    // git-providers list — separate API but operator-equivalent.
    for (const p of providers) {
      const kind = (p.kind || '').toLowerCase();
      let bucket = 'other';
      if (kind === 'github')         bucket = 'github';
      else if (kind === 'gitlab')    bucket = 'gitlab';
      else if (kind === 'bitbucket') bucket = 'bitbucket';
      else if (kind === 'gitea')     bucket = 'gitea';
      else if (kind === 'embedded')  continue; // not an operator account
      groups[bucket].entries.push({
        kind: 'provider', id: p.id, provider: kind,
        label: p.display_name || p.repo_url || p.id,
        sub: p.has_token ? p.repo_url : 'NO TOKEN — re-paste on the wizard',
        when: p.registered_at,
        del: () => deleteGitProvider(p.id),
        bad: !p.has_token,
      });
    }
    return Object.entries(groups)
      .filter(([_, g]) => g.entries.length > 0)
      .map(([k, g]) => ({ k, ...g }));
  });

  function ageString(iso) {
    if (!iso) return '—';
    const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s/60)}m ago`;
    if (s < 86400) return `${Math.floor(s/3600)}h ago`;
    return `${Math.floor(s/86400)}d ago`;
  }

  $effect(() => { load(); });
</script>

<div class="accounts">
  <header class="row top">
    <h4>Accounts</h4>
    <button type="button" class="link" onclick={load} title="reload">↻</button>
  </header>

  {#if loading}
    <p class="hint">Loading…</p>
  {:else if error}
    <p class="err">⚠ {error}</p>
    <button class="link" onclick={load}>Try again</button>
  {:else if sections.length === 0}
    <p class="hint">
      No accounts connected yet. Connect Claude or a git provider via the
      <strong>+ new</strong> wizard — saved tokens will appear here.
    </p>
  {:else}
    {#each sections as section (section.k)}
      <section class="group">
        <h5><span class="g-icon">{section.icon}</span> {section.label} <span class="g-count">({section.entries.length})</span></h5>
        <ul>
          {#each section.entries as entry (entry.kind + ':' + entry.id)}
            <li class="entry" class:bad={entry.bad}>
              <div class="line">
                <span class="label">{entry.label}</span>
                <span class="when">{ageString(entry.when)}</span>
              </div>
              <div class="sub-line">
                <span class="sub">{entry.sub}</span>
                {#if pendingDelete === entry.id}
                  <button type="button" class="danger" onclick={() => entry.del()}>Confirm delete</button>
                {:else}
                  <button type="button" class="del" onclick={() => entry.del()} title="remove this credential">✕</button>
                {/if}
              </div>
            </li>
          {/each}
        </ul>
      </section>
    {/each}
  {/if}
</div>

<style>
  .accounts { padding: 0.7rem 0.85rem; height: 100%; overflow-y: auto; background: var(--bg); }
  .row.top { display: flex; align-items: center; gap: 0.5rem; margin: 0 0 0.45rem 0; }
  h4 { margin: 0; flex: 1; font-size: 0.74rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; }
  .group { margin-bottom: 0.95rem; }
  h5 {
    margin: 0 0 0.4rem 0; font-size: 0.78rem; color: var(--fg, #f5f5f5);
    display: flex; align-items: center; gap: 0.35rem; font-weight: 600;
  }
  .g-icon { color: var(--accent-2, #87ceeb); }
  .g-count { color: var(--fg-muted, #888); font-size: 0.72rem; font-weight: 400; }
  ul { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 0.32rem; }
  .entry {
    background: var(--bg-elevated, #1a1a1a); border: 1px solid var(--border, #2a2a2a);
    border-radius: 4px; padding: 0.42rem 0.6rem;
    display: flex; flex-direction: column; gap: 0.18rem;
  }
  .entry.bad { border-color: rgba(231,76,60,0.4); background: rgba(231,76,60,0.06); }
  .line { display: flex; align-items: baseline; gap: 0.5rem; }
  .label { font-weight: 600; font-size: 0.86rem; color: var(--fg, #f5f5f5); flex: 1; word-break: break-all; }
  .when { font-size: 0.72rem; color: var(--fg-muted, #888); }
  .sub-line { display: flex; align-items: center; gap: 0.5rem; }
  .sub { font-size: 0.76rem; color: var(--fg-muted, #aaa); flex: 1; word-break: break-all; }
  .entry.bad .sub { color: #e74c3c; }
  .del {
    background: transparent; border: 0; color: var(--fg-muted, #888);
    cursor: pointer; padding: 0 0.32rem; font: inherit; font-size: 0.9rem;
  }
  .del:hover { color: var(--danger, #e74c3c); }
  .danger {
    background: rgba(231,76,60,0.18); border: 1px solid rgba(231,76,60,0.45);
    color: #e74c3c; padding: 0.18rem 0.55rem; border-radius: 3px;
    cursor: pointer; font: inherit; font-size: 0.72rem; font-weight: 600;
  }
  .link {
    background: transparent; border: 0; color: var(--accent-2, #87ceeb);
    cursor: pointer; font: inherit; font-size: 0.86rem; padding: 0;
  }
  .link:hover { text-decoration: underline; }
  .hint { color: var(--fg-muted, #888); font-size: 0.84rem; line-height: 1.45; margin: 0.4rem 0; }
  .hint strong { color: var(--accent-2, #87ceeb); }
  .err { color: var(--danger, #e74c3c); font-size: 0.84rem; margin: 0.4rem 0; }
</style>

<!--
  WidgetKanban — GitHub/Gitea issue board.
  Columns: Backlog | In Progress | UAT | Completed | Parked
  Data source: selected agent's github_url (repo) via /api/v1/sessions/<name>/issues proxy,
  which calls the GitHub/Gitea API with the runtime's token.
  Drag a card between columns to update its status/* label.
-->
<script>
  let { agent, sessions } = $props();

  const API = '/api-v08/v1';

  const COLUMNS = [
    { id: 'backlog',     label: 'Backlog',     label_val: null,                  color: '#555' },
    { id: 'in-progress', label: 'In Progress', label_val: 'status/in-progress',  color: '#0072F5' },
    { id: 'uat',         label: 'UAT',         label_val: 'status/uat',           color: '#f5a623' },
    { id: 'completed',   label: 'Completed',   label_val: 'status/completed',     color: '#5cd57f' },
    { id: 'parked',      label: 'Parked',      label_val: 'status/parked',        color: '#888' },
  ];

  const STATUS_LABELS = new Set(COLUMNS.filter(c => c.label_val).map(c => c.label_val));

  let issues = $state([]);
  let loading = $state(false);
  let error = $state('');
  let repoUrl = $state('');
  let dragIssue = $state(null);
  let dragOverCol = $state(null);

  // Derive the repo from the selected agent's github_url.
  $effect(() => {
    const ag = agent || (sessions || []).find(s => !s.exited && s.role !== 'shepherd');
    const url = ag?.github_url || '';
    if (url !== repoUrl) {
      repoUrl = url;
      if (url) loadIssues();
      else { issues = []; error = ''; }
    }
  });

  async function loadIssues() {
    if (!repoUrl) return;
    loading = true; error = '';
    try {
      // The runtime proxies /api/v1/kanban?repo=<url> → GitHub/Gitea issues API.
      const r = await fetch(`${API}/kanban?repo=${encodeURIComponent(repoUrl)}&state=open&per_page=100`);
      if (!r.ok) { error = `HTTP ${r.status}`; return; }
      const d = await r.json();
      issues = d.issues || [];
    } catch (e) { error = String(e); }
    finally { loading = false; }
  }

  function issueColumn(issue) {
    const labels = (issue.labels || []).map(l => l.name || l);
    for (const col of COLUMNS) {
      if (col.label_val && labels.includes(col.label_val)) return col.id;
    }
    return 'backlog';
  }

  function colIssues(colId) {
    return issues.filter(i => issueColumn(i) === colId);
  }

  function onDragStart(issue) { dragIssue = issue; }
  function onDragOver(colId) { dragOverCol = colId; }
  function onDragEnd() { dragIssue = null; dragOverCol = null; }

  async function onDrop(colId) {
    if (!dragIssue) return;
    const targetCol = COLUMNS.find(c => c.id === colId);
    if (!targetCol) return;
    const issueNum = dragIssue.number;
    try {
      const r = await fetch(`${API}/kanban/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          repo: repoUrl,
          issue_number: issueNum,
          status_label: targetCol.label_val || null,
          remove_labels: [...STATUS_LABELS],
        }),
      });
      if (r.ok) await loadIssues();
    } catch {}
    dragIssue = null; dragOverCol = null;
  }
</script>

<div class="kanban">
  <div class="toolbar">
    {#if repoUrl}
      <span class="repo-hint">{repoUrl.replace('https://github.com/', '')}</span>
    {:else}
      <span class="repo-hint muted">Select an agent with a GitHub repo to see its issues.</span>
    {/if}
    <button class="reload" on:click={loadIssues} title="Reload issues" disabled={loading || !repoUrl}>↻</button>
  </div>

  {#if error}
    <p class="err">{error}</p>
  {/if}

  {#if loading}
    <p class="hint">Loading issues…</p>
  {:else}
    <div class="board">
      {#each COLUMNS as col}
        <div
          class="col"
          class:drop-target={dragOverCol === col.id}
          on:dragover|preventDefault={() => onDragOver(col.id)}
          on:drop|preventDefault={() => onDrop(col.id)}
          on:dragleave={() => { if (dragOverCol === col.id) dragOverCol = null; }}
        >
          <div class="col-head" style="border-top: 2px solid {col.color}">
            <span class="col-label">{col.label}</span>
            <span class="col-count">{colIssues(col.id).length}</span>
          </div>
          <div class="col-body">
            {#each colIssues(col.id) as issue (issue.number)}
              <div
                class="card"
                draggable="true"
                on:dragstart={() => onDragStart(issue)}
                on:dragend={onDragEnd}
              >
                <div class="card-title">{issue.title}</div>
                <div class="card-meta">
                  <span class="card-num">#{issue.number}</span>
                  {#each (issue.labels || []).filter(l => !(l.name||l).startsWith('status/')) as lbl}
                    <span class="label-chip" style="background: #{(lbl.color || '555')}">{lbl.name || lbl}</span>
                  {/each}
                </div>
              </div>
            {/each}
            {#if colIssues(col.id).length === 0}
              <div class="empty-col">—</div>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .kanban { display: flex; flex-direction: column; height: 100%; overflow: hidden; }
  .toolbar { display: flex; align-items: center; padding: 0.35rem 0.6rem; gap: 0.5rem; border-bottom: 1px solid var(--border); background: var(--bg-elev); }
  .repo-hint { flex: 1; font-size: 0.72rem; color: var(--fg-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .repo-hint.muted { color: var(--fg-faint); }
  button.reload { background: transparent; border: 1px solid var(--border); border-radius: 4px; color: var(--fg-muted); cursor: pointer; padding: 0.15rem 0.4rem; font-size: 0.82rem; }
  button.reload:hover { color: var(--accent); }
  .err { color: var(--danger); font-size: 0.78rem; padding: 0.5rem 0.7rem; }
  .hint { color: var(--fg-muted); font-size: 0.78rem; padding: 0.5rem 0.7rem; }
  .board { display: flex; flex: 1; overflow-x: auto; overflow-y: hidden; gap: 0; }
  .col { display: flex; flex-direction: column; min-width: 160px; flex: 1; border-right: 1px solid var(--border); }
  .col:last-child { border-right: none; }
  .col.drop-target { background: color-mix(in srgb, var(--accent) 5%, transparent); }
  .col-head { display: flex; align-items: center; padding: 0.4rem 0.55rem 0.3rem; background: var(--bg-elev); gap: 0.4rem; }
  .col-label { font-size: 0.76rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.05em; font-weight: 600; flex: 1; }
  .col-count { font-size: 0.7rem; color: var(--fg-faint); background: var(--bg); border-radius: 999px; padding: 0.05rem 0.4rem; }
  .col-body { flex: 1; overflow-y: auto; padding: 0.4rem 0.35rem; display: flex; flex-direction: column; gap: 0.35rem; }
  .empty-col { color: var(--fg-faint); font-size: 0.72rem; text-align: center; padding: 0.5rem; }
  .card { background: var(--bg-elev); border: 1px solid var(--border); border-radius: 5px; padding: 0.45rem 0.55rem; cursor: grab; user-select: none; }
  .card:active { cursor: grabbing; }
  .card:hover { border-color: var(--border-strong); }
  .card-title { font-size: 0.8rem; color: var(--fg); line-height: 1.35; margin-bottom: 0.3rem; }
  .card-meta { display: flex; align-items: center; gap: 0.3rem; flex-wrap: wrap; }
  .card-num { font-size: 0.68rem; color: var(--fg-faint); font-family: ui-monospace, monospace; }
  .label-chip { font-size: 0.62rem; padding: 0.05rem 0.35rem; border-radius: 999px; color: #fff; font-weight: 500; }
</style>

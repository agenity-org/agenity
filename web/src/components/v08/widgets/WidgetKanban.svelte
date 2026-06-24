<!--
  WidgetKanban — GitHub/Gitea issue board.
  Columns: Backlog | In Progress | UAT | Completed | Parked
  Data source: selected agent's github_url (repo) via /api/v1/sessions/<name>/issues proxy,
  which calls the GitHub/Gitea API with the runtime's token.
  Drag a card between columns to update its status/* label.

  Deep-link wiring (#665):
    - Listens for `chepherd-ticket-focus` (from TeamTranscript #-clicks),
      scrolls the matching card into view, pulses .highlighted for 2s.
    - Renders "💬 N" badge per card from
      `GET /api/v1/teams/{team}/ticket-mentions` (count of transcript
      messages mentioning #N). Clicking the badge dispatches
      `chepherd-transcript-filter` so the transcript filters to those rows.
-->
<script>
  import { onMount } from 'svelte';

  let { agent, sessions, team } = $props();

  const API = '/api-v08/v1';
  const TRANSCRIPT_API = '/api/v1';

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
  let mentionCounts = $state({});    // { "651": 3 }
  let highlightedNum = $state(null); // ticket # currently pulsing
  let cardEls = $state({});          // { 651: <div> } for scroll-into-view

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

  // Mention counts per ticket # — fetched once on mount, refreshed every 30s.
  // Backend ships GET /api/v1/teams/{name}/ticket-mentions → {"<num>":<count>}.
  // Degrades silently when the endpoint isn't live yet (graceful per #665).
  async function loadMentions() {
    if (!team) return;
    try {
      const r = await fetch(`${TRANSCRIPT_API}/teams/${encodeURIComponent(team)}/ticket-mentions`);
      if (!r.ok) return;
      const j = await r.json();
      if (j && typeof j === 'object') {
        mentionCounts = j;
      }
    } catch {}
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

  // Click on the "💬 N" badge — ask the transcript to filter to rows
  // mentioning this ticket. We don't expand or scroll; the transcript
  // surfaces its own filter chip with an [×] to clear.
  function onBadgeClick(num, ev) {
    ev?.stopPropagation?.();
    try {
      window.dispatchEvent(new CustomEvent('chepherd-transcript-filter', {
        detail: { repo: repoUrl, num },
      }));
    } catch {}
  }

  // Focus event from transcript: scroll the card into view + pulse highlight.
  function focusCard(num) {
    if (!num) return;
    highlightedNum = num;
    setTimeout(() => {
      const el = cardEls[num];
      if (el && el.scrollIntoView) {
        el.scrollIntoView({ behavior: 'smooth', block: 'center' });
      }
    }, 30);
    setTimeout(() => {
      if (highlightedNum === num) highlightedNum = null;
    }, 2000);
  }

  onMount(() => {
    loadMentions();
    const id = setInterval(loadMentions, 30000);
    const onFocus = (ev) => {
      const d = ev.detail || {};
      if (!d.num) return;
      focusCard(d.num);
    };
    window.addEventListener('chepherd-ticket-focus', onFocus);
    return () => {
      clearInterval(id);
      window.removeEventListener('chepherd-ticket-focus', onFocus);
    };
  });

  // When the team prop changes, reload mention counts.
  $effect(() => {
    team;
    loadMentions();
  });
</script>

<div class="kanban">
  <div class="toolbar">
    {#if repoUrl}
      <span class="repo-hint">{repoUrl.replace('https://github.com/', '')}</span>
    {:else}
      <span class="repo-hint muted">Select an agent with a GitHub repo to see its issues.</span>
    {/if}
    <button class="reload" onclick={loadIssues} title="Reload issues" disabled={loading || !repoUrl}>↻</button>
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
          ondragover={(e) => { e.preventDefault(); onDragOver(col.id); }}
          ondrop={(e) => { e.preventDefault(); onDrop(col.id); }}
          ondragleave={() => { if (dragOverCol === col.id) dragOverCol = null; }}
        >
          <div class="col-head" style="border-top: 2px solid {col.color}">
            <span class="col-label">{col.label}</span>
            <span class="col-count">{colIssues(col.id).length}</span>
          </div>
          <div class="col-body">
            {#each colIssues(col.id) as issue (issue.number)}
              <div
                class="card"
                class:highlighted={highlightedNum === issue.number}
                draggable="true"
                bind:this={cardEls[issue.number]}
                data-testid="kanban-card"
                data-issue-num={issue.number}
                ondragstart={() => onDragStart(issue)}
                ondragend={onDragEnd}
              >
                <div class="card-title">{issue.title}</div>
                <div class="card-meta">
                  <span class="card-num">#{issue.number}</span>
                  {#if mentionCounts[String(issue.number)] > 0}
                    <button
                      class="mention-badge"
                      title="Filter transcript to messages mentioning #{issue.number}"
                      data-testid="mention-badge"
                      onclick={(e) => onBadgeClick(issue.number, e)}
                    >💬 {mentionCounts[String(issue.number)]}</button>
                  {/if}
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
  .card { background: var(--bg-elev); border: 1px solid var(--border); border-radius: 5px; padding: 0.45rem 0.55rem; cursor: grab; user-select: none; transition: box-shadow 0.2s, border-color 0.2s; }
  .card:active { cursor: grabbing; }
  .card:hover { border-color: var(--border-strong); }
  .card.highlighted {
    border-color: var(--accent, #87ceeb);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent, #87ceeb) 45%, transparent);
    animation: pulse 0.6s ease-in-out 2;
  }
  @keyframes pulse {
    0%, 100% { box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent, #87ceeb) 45%, transparent); }
    50%      { box-shadow: 0 0 0 5px color-mix(in srgb, var(--accent, #87ceeb) 25%, transparent); }
  }
  .card-title { font-size: 0.8rem; color: var(--fg); line-height: 1.35; margin-bottom: 0.3rem; }
  .card-meta { display: flex; align-items: center; gap: 0.3rem; flex-wrap: wrap; }
  .card-num { font-size: 0.68rem; color: var(--fg-faint); font-family: ui-monospace, monospace; }
  .mention-badge {
    font-size: 0.66rem; padding: 0.05rem 0.4rem; border-radius: 999px;
    background: rgba(135,206,235,0.18); color: var(--accent, #87ceeb);
    border: 0; cursor: pointer; font-family: ui-monospace, monospace;
  }
  .mention-badge:hover { background: rgba(135,206,235,0.32); }
  .label-chip { font-size: 0.62rem; padding: 0.05rem 0.35rem; border-radius: 999px; color: #fff; font-weight: 500; }
</style>

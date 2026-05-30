<!--
  WidgetA2AInbox — #329 (#225 row G2).
  Lists recent A2A tasks from /api/v1/tasks. Shows task method, state,
  id-prefix, and updated-at so operator can confirm peer-originated
  message/send + tasks/get calls are landing as Task records.
  Polls every 5s.
-->
<script>
  const API = '/api-v08/v1';
  let tasks = $state([]);
  let lastError = $state('');

  async function refresh() {
    try {
      const r = await fetch(`${API}/tasks`);
      const data = await r.json();
      tasks = data.tasks || [];
      lastError = '';
    } catch (e) {
      lastError = e?.message || 'fetch failed';
    }
  }

  function relTime(ts) {
    if (!ts) return '';
    const s = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s/60)}m ago`;
    if (s < 86400) return `${Math.floor(s/3600)}h ago`;
    return `${Math.floor(s/86400)}d ago`;
  }

  function stateClass(state) {
    switch (state) {
      case 'completed': return 'state-completed';
      case 'failed':    return 'state-failed';
      case 'working':   return 'state-working';
      case 'input-required': return 'state-input';
      case 'submitted': return 'state-submitted';
      default: return '';
    }
  }

  $effect(() => {
    refresh();
    const id = setInterval(refresh, 5000);
    return () => clearInterval(id);
  });
</script>

<div class="a2a-inbox" data-testid="a2a-inbox">
  <h4>
    A2A Inbox
    <span class="count">({tasks.length})</span>
  </h4>
  {#if tasks.length === 0}
    <p class="hint">No A2A tasks yet. Tasks land here when peers send messages via A2A wire.</p>
  {/if}
  <ul>
    {#each tasks as task (task.id)}
      <li>
        <div class="meta">
          <span class="dot">◈</span>
          <strong>{task.method}</strong>
          <span class="badge {stateClass(task.state)}">{task.state}</span>
        </div>
        <div class="sub">{task.id.slice(0, 12)}… · {relTime(task.updatedAt)}</div>
      </li>
    {/each}
  </ul>
  {#if lastError}
    <p class="err">last fetch: {lastError}</p>
  {/if}
</div>

<style>
  .a2a-inbox { padding: 0.75rem 0.85rem; height: 100%; overflow-y: auto; background: var(--bg); }
  h4 { font-size: 0.82rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.05em; margin: 0 0 0.55rem 0; font-weight: 600; display: flex; align-items: center; gap: 0.45rem; }
  .count { color: var(--fg-faint); font-weight: 400; text-transform: none; letter-spacing: 0; }
  .hint { color: var(--fg-faint); font-size: 0.82rem; }
  ul { list-style: none; padding: 0; margin: 0; }
  li { padding: 0.45rem 0.5rem; border-left: 3px solid transparent; border-bottom: 1px solid var(--border); font-size: 0.83rem; color: var(--fg-muted); }
  li:hover { border-left-color: var(--accent-2); color: var(--fg); }
  .meta { display: flex; align-items: center; gap: 0.4rem; }
  .dot { color: var(--accent); }
  .sub { color: var(--fg-faint); font-size: 0.72rem; margin-top: 0.15rem; padding-left: 1.1rem; }
  .badge { padding: 0.05rem 0.4rem; border-radius: 9px; font-size: 0.7rem; font-weight: 600; background: var(--bg-soft); margin-left: auto; }
  .badge.state-completed { background: var(--ok, #4ade80); color: #000; }
  .badge.state-failed    { background: var(--err, #ff6464); color: #fff; }
  .badge.state-working   { background: var(--accent, #ffa500); color: #000; }
  .badge.state-input     { background: var(--accent-2, #87ceeb); color: #000; }
  .badge.state-submitted { background: var(--fg-muted, #888); color: #000; }
  .err { color: var(--err, #ff6464); font-size: 0.72rem; margin-top: 0.5rem; }
</style>

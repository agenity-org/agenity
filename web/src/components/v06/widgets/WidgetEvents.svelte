<script>
  let { events } = $props();
  let filter = $state('');
  let filtered = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return events || [];
    return (events || []).filter(e =>
      (e.kind || '').toLowerCase().includes(q) ||
      (e.actor || '').toLowerCase().includes(q) ||
      (e.body || '').toLowerCase().includes(q)
    );
  });
  function shortTime(ts) {
    if (!ts) return '';
    try { return new Date(ts).toLocaleTimeString('en-US', { hour12: false }); } catch { return ts; }
  }
  function kindClass(k) {
    switch (k) {
      case 'spawn': return 'k-spawn';
      case 'exit': return 'k-exit';
      case 'scorecard': return 'k-score';
      case 'verdict': return 'k-verdict';
      case 'shepherd_refresh': return 'k-shep'; // back-compat: wire event kind
      case 'template_applied': return 'k-template';
      case 'note': return 'k-note';
      default: return '';
    }
  }
</script>

<div class="events">
  <header>
    <h4>Events</h4>
    <input bind:value={filter} placeholder="filter by kind/actor/body…" />
    <span class="count">{filtered.length}</span>
  </header>
  <ul>
    {#each filtered.slice().reverse() as e (e.id)}
      <li class={kindClass(e.kind)}>
        <span class="time">{shortTime(e.at)}</span>
        <span class="kind">{e.kind}</span>
        <span class="actor">{e.actor}</span>
        <span class="body">{e.body}</span>
      </li>
    {/each}
    {#if !filtered.length}
      <li class="empty">No events yet.</li>
    {/if}
  </ul>
</div>

<style>
  .events { padding: 0.4rem 0.5rem; height: 100%; display: flex; flex-direction: column; background: var(--bg); }
  header { display: flex; align-items: center; gap: 0.45rem; margin-bottom: 0.3rem; }
  h4 { font-size: 0.74rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.06em; margin: 0; font-weight: 600; }
  input { flex: 1; padding: 0.2rem 0.4rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border); border-radius: 4px; font-size: 0.78rem; font-family: ui-monospace, monospace; }
  .count { color: var(--fg-faint); font-size: 0.72rem; }
  ul { list-style: none; padding: 0; margin: 0; flex: 1; overflow-y: auto; }
  li { display: flex; gap: 0.5rem; padding: 0.18rem 0.3rem; font-family: ui-monospace, monospace; font-size: 0.76rem; border-bottom: 1px dashed var(--border); align-items: baseline; }
  li.empty { color: var(--fg-faint); justify-content: center; padding: 0.6rem; }
  .time { color: var(--fg-faint); width: 70px; flex-shrink: 0; }
  .kind { color: var(--accent-2); width: 90px; flex-shrink: 0; }
  .actor { color: var(--fg-muted); width: 110px; flex-shrink: 0; }
  .body { color: var(--fg); flex: 1; word-break: break-word; }
  .k-spawn .kind { color: #34d399; }
  .k-exit .kind { color: var(--danger); }
  .k-score .kind { color: var(--accent); }
  .k-verdict .kind { color: #87ceeb; }
  .k-shep .kind { color: var(--accent); }
  .k-template .kind { color: #c084fc; }
</style>

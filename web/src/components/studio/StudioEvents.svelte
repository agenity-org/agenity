<!--
  StudioEvents — live global-event log (the "Output / Problems" surface of
  the studio bottom dock). Renders the events list already maintained by the
  root via EventSource(/api/v1/events/stream) + the /events poll. Read-only.
-->
<script>
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let { events = [] } = $props();

  let filter = $state('');
  let kindFilter = $state('all');

  // events are appended chronologically; show newest first
  let rows = $derived.by(() => {
    let list = [...(events || [])].reverse();
    if (kindFilter !== 'all') {
      list = list.filter(e => kindLabel(e).toLowerCase().includes(kindFilter));
    }
    const f = filter.trim().toLowerCase();
    if (f) {
      list = list.filter(e => JSON.stringify(e).toLowerCase().includes(f));
    }
    return list.slice(0, 300);
  });

  function kindLabel(e) {
    return e?.kind || e?.type || e?.event || 'event';
  }
  function actor(e) {
    return e?.agent || e?.from || e?.actor || e?.name || e?.session || '';
  }
  function summary(e) {
    return e?.body || e?.message || e?.summary || e?.detail || e?.msg ||
      (typeof e === 'object' ? '' : String(e));
  }
  function ts(e) {
    const t = e?.at || e?.created_at || e?.ts || e?.time;
    if (!t) return '';
    try { return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }); }
    catch { return ''; }
  }
  function kindCls(e) {
    const k = kindLabel(e).toLowerCase();
    if (k.includes('fail') || k.includes('error')) return 'k-err';
    if (k.includes('stuck') || k.includes('warn')) return 'k-warn';
    if (k.includes('accomplish') || k.includes('done') || k.includes('ok')) return 'k-ok';
    if (k.includes('spawn') || k.includes('join')) return 'k-info';
    return '';
  }
</script>

<div class="ev">
  <div class="ev-bar">
    <input class="ev-search" placeholder="filter events…" bind:value={filter} />
    <select class="ev-kind" bind:value={kindFilter}>
      <option value="all">all</option>
      <option value="fail">failures</option>
      <option value="stuck">stuck</option>
      <option value="spawn">lifecycle</option>
    </select>
    <span class="ev-count">{rows.length}</span>
  </div>
  <div class="ev-list">
    {#if !rows.length}
      <div class="ev-empty">No events match.</div>
    {:else}
      {#each rows as e, i (i)}
        {@const a = actor(e)}
        <div class="ev-row {kindCls(e)}">
          <span class="ev-ts">{ts(e)}</span>
          <span class="ev-k">{kindLabel(e)}</span>
          {#if a}<span class="ev-actor" style="color:{agentIdentity(a).color}">{agentIdentity(a).icon} {a}</span>{/if}
          <span class="ev-msg">{summary(e)}</span>
        </div>
      {/each}
    {/if}
  </div>
</div>

<style>
  .ev { height: 100%; display: flex; flex-direction: column; min-height: 0; background: var(--st-bg); }
  .ev-bar { display: flex; align-items: center; gap: 0.5rem; padding: 0.4rem 0.6rem; border-bottom: 1px solid var(--st-border); }
  .ev-search { flex: 1; background: var(--st-chip); border: 1px solid var(--st-border); border-radius: 6px; color: var(--st-fg); padding: 0.25rem 0.5rem; font: inherit; font-size: 0.78rem; }
  .ev-search:focus { outline: none; border-color: var(--st-accent); }
  .ev-kind { background: var(--st-chip); border: 1px solid var(--st-border); border-radius: 6px; color: var(--st-fg); padding: 0.25rem 0.4rem; font: inherit; font-size: 0.76rem; }
  .ev-count { font-size: 0.72rem; color: var(--st-fg-muted); min-width: 2rem; text-align: right; }
  .ev-list { flex: 1; overflow-y: auto; padding: 0.2rem 0; font-family: ui-monospace, monospace; }
  .ev-empty { color: var(--st-fg-faint); padding: 1rem; text-align: center; font-size: 0.82rem; }
  .ev-row { display: flex; gap: 0.6rem; align-items: baseline; padding: 0.15rem 0.7rem; font-size: 0.76rem; border-left: 2px solid transparent; }
  .ev-row:hover { background: var(--st-hover); }
  .ev-row.k-err { border-left-color: var(--st-danger); }
  .ev-row.k-warn { border-left-color: var(--st-accent); }
  .ev-row.k-ok { border-left-color: var(--st-ok); }
  .ev-row.k-info { border-left-color: var(--st-accent-2); }
  .ev-ts { color: var(--st-fg-faint); white-space: nowrap; }
  .ev-k { color: var(--st-fg-muted); min-width: 6rem; }
  .ev-actor { white-space: nowrap; font-weight: 600; }
  .ev-msg { color: var(--st-fg); overflow-wrap: anywhere; flex: 1; }
</style>

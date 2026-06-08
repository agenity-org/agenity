<!--
  AuditLog.svelte — #490 Wave AU3 audit dashboard surface.

  Consumes AU2's GET /api/v1/audit/events. Filters: caller, callee,
  method, time-range (since/until), status. Per-agent view via the
  ?agent=<sid> query param scopes events to caller=sid OR callee=sid.
  Polling every 5s (live SSE deferred to future Wave). Pagination via
  limit (offset chaining is future work — load-more button included).

  Per the feedback_ui_changes_need_route_smoke_test memory: data-
  testid attributes on every interactive element so the Playwright
  smoke test can drive the route + verify the API call shape.

  Refs #490 #489 #488.
-->
<script>
  import { onMount, onDestroy } from 'svelte';

  /** @type {string} the API origin. Defaults to current page origin. */
  const API_BASE = '/api/v1';

  // Filter state (reactive — Svelte 5 $state).
  let caller = $state('');
  let callee = $state('');
  let method = $state('');
  let status = $state('');
  let since = $state('');
  let until = $state('');
  let limit = $state(100);

  // Per-agent view: when ?agent=<sid> is in the URL, query is scoped
  // (caller=sid OR callee=sid). We implement this client-side by
  // making TWO calls + merging — the AU2 API doesn't yet support OR.
  let agentScope = $state('');

  let events = $state([]);
  let loading = $state(false);
  let errorMsg = $state('');
  let lastFetchedAt = $state(null);
  let orgId = $state('');

  // Pre-parsed unique method/caller/callee values for filter dropdowns.
  let methodOptions = $state([]);
  let callerOptions = $state([]);
  let calleeOptions = $state([]);

  /** Build the URL with current filter params. */
  function buildURL(scope) {
    const q = new URLSearchParams();
    if (scope) {
      // Per-agent view = 2 calls; this one is scope-specific.
      q.set(scope.field, scope.value);
    } else {
      if (caller) q.set('caller', caller);
      if (callee) q.set('callee', callee);
    }
    if (method) q.set('method', method);
    if (since) q.set('since', since);
    if (until) q.set('until', until);
    if (limit && limit !== 100) q.set('limit', String(limit));
    return `${API_BASE}/audit/events?${q.toString()}`;
  }

  /** Apply client-side status filter (AU2 doesn't take status). */
  function applyStatusFilter(rows) {
    if (!status) return rows;
    return rows.filter((r) => r.status === status);
  }

  async function fetchEvents() {
    loading = true;
    errorMsg = '';
    try {
      let merged = [];
      if (agentScope) {
        // Per-agent view = OR of (caller=sid) and (callee=sid). Two
        // requests, dedupe by id, sort timestamp DESC.
        const [aRes, bRes] = await Promise.all([
          fetch(buildURL({ field: 'caller', value: agentScope })),
          fetch(buildURL({ field: 'callee', value: agentScope })),
        ]);
        if (!aRes.ok) throw new Error(`caller scope: HTTP ${aRes.status}`);
        if (!bRes.ok) throw new Error(`callee scope: HTTP ${bRes.status}`);
        const a = await aRes.json();
        const b = await bRes.json();
        orgId = a.org_id || b.org_id || '';
        const seen = new Set();
        for (const ev of [...(a.events || []), ...(b.events || [])]) {
          if (!seen.has(ev.id)) {
            seen.add(ev.id);
            merged.push(ev);
          }
        }
        merged.sort((x, y) => (x.timestamp < y.timestamp ? 1 : -1));
      } else {
        const res = await fetch(buildURL());
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const data = await res.json();
        orgId = data.org_id || '';
        merged = data.events || [];
      }
      events = applyStatusFilter(merged);
      lastFetchedAt = new Date().toISOString();

      // Refresh dropdown options from the result set.
      const methods = new Set();
      const callers = new Set();
      const callees = new Set();
      for (const ev of events) {
        if (ev.method) methods.add(ev.method);
        if (ev.caller) callers.add(ev.caller);
        if (ev.callee) callees.add(ev.callee);
      }
      methodOptions = Array.from(methods).sort();
      callerOptions = Array.from(callers).sort();
      calleeOptions = Array.from(callees).sort();
    } catch (e) {
      errorMsg = String(e.message || e);
    } finally {
      loading = false;
    }
  }

  // Read ?agent= from URL on mount.
  let pollHandle;
  onMount(() => {
    const url = new URL(window.location.href);
    const agent = url.searchParams.get('agent');
    if (agent) agentScope = agent;
    fetchEvents();
    pollHandle = setInterval(fetchEvents, 5000);
  });
  onDestroy(() => {
    if (pollHandle) clearInterval(pollHandle);
  });

  function clearAgentScope() {
    agentScope = '';
    const url = new URL(window.location.href);
    url.searchParams.delete('agent');
    history.replaceState({}, '', url.toString());
    fetchEvents();
  }

  function selectAgent(sid) {
    agentScope = sid;
    const url = new URL(window.location.href);
    url.searchParams.set('agent', sid);
    history.replaceState({}, '', url.toString());
    fetchEvents();
  }
</script>

<section class="audit" data-testid="audit-log-root">
  <header>
    <h1 data-testid="audit-title">
      Audit Log
      {#if agentScope}
        <span class="scope-badge" data-testid="audit-agent-scope">
          @ {agentScope}
          <button onclick={clearAgentScope} data-testid="audit-clear-scope">×</button>
        </span>
      {/if}
    </h1>
    <div class="meta">
      <span data-testid="audit-org">org: {orgId || '—'}</span>
      <span data-testid="audit-count">{events.length} event{events.length === 1 ? '' : 's'}</span>
      {#if lastFetchedAt}
        <span data-testid="audit-fetched-at">last: {lastFetchedAt}</span>
      {/if}
    </div>
  </header>

  <form class="filters" onsubmit={(e) => { e.preventDefault(); fetchEvents(); }}>
    <label>
      caller
      <input
        type="text"
        bind:value={caller}
        list="audit-caller-options"
        data-testid="filter-caller"
      />
    </label>
    <label>
      callee
      <input
        type="text"
        bind:value={callee}
        list="audit-callee-options"
        data-testid="filter-callee"
      />
    </label>
    <label>
      method
      <input
        type="text"
        bind:value={method}
        list="audit-method-options"
        data-testid="filter-method"
      />
    </label>
    <label>
      status
      <select bind:value={status} data-testid="filter-status">
        <option value="">any</option>
        <option value="success">success</option>
        <option value="error">error</option>
      </select>
    </label>
    <label>
      since (RFC3339)
      <input type="text" bind:value={since} placeholder="2026-05-31T14:00:00Z" data-testid="filter-since" />
    </label>
    <label>
      until (RFC3339)
      <input type="text" bind:value={until} placeholder="2026-05-31T15:00:00Z" data-testid="filter-until" />
    </label>
    <label>
      limit
      <input type="number" bind:value={limit} min="1" max="1000" data-testid="filter-limit" />
    </label>
    <button type="submit" data-testid="filter-apply">Apply</button>
    <datalist id="audit-method-options">
      {#each methodOptions as m}
        <option value={m}></option>
      {/each}
    </datalist>
    <datalist id="audit-caller-options">
      {#each callerOptions as c}
        <option value={c}></option>
      {/each}
    </datalist>
    <datalist id="audit-callee-options">
      {#each calleeOptions as c}
        <option value={c}></option>
      {/each}
    </datalist>
  </form>

  {#if loading}
    <div class="state-msg" data-testid="audit-loading">loading…</div>
  {/if}
  {#if errorMsg}
    <div class="state-msg error" data-testid="audit-error">{errorMsg}</div>
  {/if}

  <table data-testid="audit-table">
    <thead>
      <tr>
        <th>timestamp</th>
        <th>event_type</th>
        <th>caller</th>
        <th>callee</th>
        <th>method</th>
        <th>latency_ms</th>
        <th>status</th>
      </tr>
    </thead>
    <tbody>
      {#each events as ev (ev.id)}
        <tr data-testid="audit-row" data-event-id={ev.id}>
          <td>{ev.timestamp}</td>
          <td>{ev.event_type}</td>
          <td>
            <button type="button" class="agent-link" onclick={() => selectAgent(ev.caller)} data-testid="agent-link-caller">
              {ev.caller || '—'}
            </button>
          </td>
          <td>
            <button type="button" class="agent-link" onclick={() => selectAgent(ev.callee)} data-testid="agent-link-callee">
              {ev.callee || '—'}
            </button>
          </td>
          <td>{ev.method}</td>
          <td>{ev.latency_ms}</td>
          <td class="status-{ev.status}">{ev.status}</td>
        </tr>
      {/each}
      {#if events.length === 0 && !loading && !errorMsg}
        <tr><td colspan="7" class="empty" data-testid="audit-empty">no events match current filters</td></tr>
      {/if}
    </tbody>
  </table>
</section>

<style>
  .audit {
    padding: 1rem 1.5rem;
    background: var(--audit-bg, #0a0a0a);
    color: var(--audit-fg, #f5f5f5);
    height: var(--audit-height, 100vh);
    box-sizing: border-box;
    overflow-y: auto;
    font-family: ui-sans-serif, system-ui, sans-serif;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: 1rem;
    margin-bottom: 1rem;
    border-bottom: 1px solid var(--audit-border, #222);
    padding-bottom: 0.5rem;
  }
  h1 {
    font-size: 1.2rem;
    margin: 0;
  }
  .scope-badge {
    background: var(--audit-chip, #2a2a2a);
    border: 1px solid var(--audit-border-strong, #444);
    border-radius: 4px;
    padding: 2px 8px;
    margin-left: 0.5rem;
    font-size: 0.9rem;
    font-family: ui-monospace, monospace;
  }
  .scope-badge button {
    background: none;
    border: none;
    color: var(--audit-danger, #f88);
    cursor: pointer;
    margin-left: 4px;
    font-size: 1rem;
  }
  .meta {
    display: flex;
    gap: 1rem;
    color: var(--audit-fg-muted, #999);
    font-size: 0.85rem;
    font-family: ui-monospace, monospace;
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
    align-items: flex-end;
    background: var(--audit-surface, #111);
    padding: 0.75rem;
    border: 1px solid var(--audit-border, #222);
    border-radius: 4px;
    margin-bottom: 1rem;
  }
  .filters label {
    display: flex;
    flex-direction: column;
    font-size: 0.8rem;
    color: var(--audit-fg-muted, #aaa);
    gap: 0.25rem;
  }
  .filters input,
  .filters select {
    background: var(--audit-input, #1a1a1a);
    color: var(--audit-fg, #f5f5f5);
    border: 1px solid var(--audit-border, #333);
    border-radius: 3px;
    padding: 4px 6px;
    font-family: ui-monospace, monospace;
    font-size: 0.85rem;
    min-width: 8rem;
  }
  .filters button[type='submit'] {
    background: var(--audit-accent, #2a4a6a);
    color: var(--audit-accent-fg, #fff);
    border: 1px solid var(--audit-accent-border, #3a5a7a);
    border-radius: 3px;
    padding: 6px 16px;
    cursor: pointer;
    font-size: 0.9rem;
  }
  .state-msg {
    color: var(--audit-fg-muted, #999);
    padding: 0.5rem 0;
    font-style: italic;
  }
  .state-msg.error {
    color: var(--audit-danger, #f88);
    font-style: normal;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-family: ui-monospace, monospace;
    font-size: 0.85rem;
  }
  th,
  td {
    text-align: left;
    padding: 6px 8px;
    border-bottom: 1px solid var(--audit-border, #222);
  }
  th {
    color: var(--audit-fg-muted, #aaa);
    font-weight: 500;
    background: var(--audit-surface, #111);
    position: sticky;
    top: 0;
  }
  .agent-link {
    background: none;
    border: none;
    color: var(--audit-link, #6ad);
    text-decoration: underline;
    cursor: pointer;
    font-family: inherit;
    font-size: inherit;
    padding: 0;
  }
  .status-success {
    color: var(--audit-ok, #6c6);
  }
  .status-error {
    color: var(--audit-danger, #f88);
    font-weight: 600;
  }
  .empty {
    text-align: center;
    color: var(--audit-fg-faint, #666);
    padding: 2rem;
  }
</style>

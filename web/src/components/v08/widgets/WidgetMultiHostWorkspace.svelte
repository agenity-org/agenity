<!--
  WidgetMultiHostWorkspace — #321 (#225 row G3).
  Sessions across all trusted peer instances in one pane.
  Polls /api/v1/peers (cached agent cards) + each peer's /api/v1/sessions
  (via the FederatedDeliverer's @peer-sid contextID convention).
  Local sessions appear in the LOCAL group at the top.
  Polls every 8s (multi-host data is slow-changing + cross-host HTTP).
-->
<script>
  const API = '/api-v08/v1';

  let localSessions = $state([]);
  let peersWithSessions = $state([]); // [{sid, name, sessions, error}]
  let lastError = $state('');
  let lastRefresh = $state(0);

  function tokenHeader() {
    const t = (typeof window !== 'undefined' && localStorage.getItem('chepherd-token')) || '';
    return t ? { Authorization: `Bearer ${t}` } : {};
  }

  async function fetchLocalSessions() {
    try {
      const r = await fetch(`${API}/sessions`);
      const data = await r.json();
      localSessions = data.sessions || data || [];
    } catch (e) {
      // surfaced via lastError only when ALL fetches fail
    }
  }

  async function fetchPeerSessions(peer) {
    // For now we cross-fetch via the peer's public URL if it has one in
    // the cached AgentCard. Empty url = peer unreachable in this view.
    if (!peer || !peer.card || !peer.card.url) {
      return { sid: peer.sid, name: peer.name || peer.sid, sessions: [], error: 'no public URL on agent-card' };
    }
    // Strip the trailing /jsonrpc to get the peer's base URL
    const base = peer.card.url.replace(/\/jsonrpc\/?$/, '');
    try {
      // Cross-host call uses the peer's bearer (when we have one in
      // localStorage), else anonymous. Most peers will 401 — that's OK,
      // we surface it as "auth required" so the operator sees the gap.
      const r = await fetch(`${base}/api/v1/sessions`, { headers: tokenHeader() });
      if (r.status === 401) {
        return { sid: peer.sid, name: peer.name || peer.sid, sessions: [], error: '401 — need peer trust + token' };
      }
      const data = await r.json();
      return { sid: peer.sid, name: peer.name || peer.sid, sessions: data.sessions || [], error: '' };
    } catch (e) {
      return { sid: peer.sid, name: peer.name || peer.sid, sessions: [], error: e?.message || 'unreachable' };
    }
  }

  async function refresh() {
    lastRefresh = Date.now();
    try {
      // 1. local sessions (always)
      const localP = fetchLocalSessions();

      // 2. peers from federation cache
      const r = await fetch(`${API}/peers`, { headers: tokenHeader() });
      const data = await r.json();
      const peers = data.peers || [];

      // 3. fan-out fetch peer sessions in parallel
      const peerResults = await Promise.all(peers.map(fetchPeerSessions));
      peersWithSessions = peerResults;
      await localP;
      lastError = '';
    } catch (e) {
      lastError = e?.message || 'multi-host fetch failed';
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

  $effect(() => {
    refresh();
    const id = setInterval(refresh, 8000);
    return () => clearInterval(id);
  });

  let totalSessions = $derived(localSessions.length + peersWithSessions.reduce((n, p) => n + p.sessions.length, 0));
  let totalHosts = $derived(1 + peersWithSessions.length);
</script>

<div class="multi-host" data-testid="multi-host-workspace">
  <h4>
    Multi-host workspace
    <span class="count">({totalSessions} sessions · {totalHosts} hosts)</span>
  </h4>

  <!-- LOCAL group -->
  <div class="host-group">
    <div class="host-head">
      <span class="dot local">●</span>
      <strong>local</strong>
      <span class="badge">{localSessions.length}</span>
    </div>
    <ul>
      {#each localSessions.slice(0, 20) as s (s.id || s.name)}
        <li>
          <span class="role-dot role-{s.role || 'worker'}">●</span>
          <span class="name">{s.name}</span>
          <span class="when">{s.agent || '?'} · {relTime(s.created_at)}</span>
        </li>
      {/each}
      {#if localSessions.length === 0}
        <li class="empty">No local sessions.</li>
      {/if}
    </ul>
  </div>

  <!-- PEER groups -->
  {#each peersWithSessions as peer (peer.sid)}
    <div class="host-group">
      <div class="host-head">
        <span class="dot peer">⇄</span>
        <strong>{peer.name}</strong>
        <span class="badge">{peer.sessions.length}</span>
      </div>
      {#if peer.error}
        <p class="err-line">{peer.error}</p>
      {/if}
      <ul>
        {#each peer.sessions.slice(0, 20) as s (s.id || s.name)}
          <li>
            <span class="role-dot role-{s.role || 'worker'}">●</span>
            <span class="name">{s.name}</span>
            <span class="when">{s.agent || '?'} · {relTime(s.created_at)}</span>
          </li>
        {/each}
        {#if peer.sessions.length === 0 && !peer.error}
          <li class="empty">No sessions on this peer.</li>
        {/if}
      </ul>
    </div>
  {/each}

  {#if peersWithSessions.length === 0}
    <p class="hint">No peers cached yet. Configure <code>--federation-registry-url</code> to discover other chepherd instances.</p>
  {/if}

  {#if lastError}
    <p class="err">last fetch: {lastError}</p>
  {/if}
</div>

<style>
  .multi-host { padding: 0.75rem 0.85rem; height: 100%; overflow-y: auto; background: var(--bg); }
  h4 { font-size: 0.82rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.05em; margin: 0 0 0.65rem 0; font-weight: 600; display: flex; align-items: center; gap: 0.45rem; }
  .count { color: var(--fg-faint); font-weight: 400; text-transform: none; letter-spacing: 0; }
  .host-group { margin-bottom: 0.85rem; border: 1px solid var(--border); border-radius: 6px; padding: 0.55rem 0.65rem; background: var(--bg-soft, #131316); }
  .host-head { display: flex; align-items: center; gap: 0.45rem; margin-bottom: 0.45rem; font-size: 0.83rem; color: var(--fg); }
  .dot { font-size: 0.85rem; }
  .dot.local { color: var(--accent); }
  .dot.peer { color: var(--accent-2); }
  .badge { margin-left: auto; padding: 0.05rem 0.4rem; border-radius: 99px; background: var(--bg); color: var(--fg-muted); font-size: 0.7rem; }
  ul { list-style: none; padding: 0; margin: 0; }
  li { padding: 0.3rem 0.25rem; font-size: 0.82rem; color: var(--fg-muted); display: flex; align-items: center; gap: 0.4rem; border-bottom: 1px solid var(--border); }
  li:last-child { border-bottom: 0; }
  li:hover { color: var(--fg); }
  .role-dot { font-size: 0.7rem; }
  .role-worker { color: var(--accent); }
  .role-scrummaster { color: var(--accent-2); }
  .role-reviewer { color: #c084fc; }
  .role-tester { color: #fb923c; }
  .name { font-weight: 500; }
  .when { margin-left: auto; color: var(--fg-faint); font-size: 0.72rem; }
  .empty { color: var(--fg-faint); font-style: italic; }
  .hint { color: var(--fg-faint); font-size: 0.82rem; margin-top: 0.5rem; }
  .err, .err-line { color: var(--err, #ff6464); font-size: 0.72rem; margin-top: 0.35rem; }
  code { background: var(--bg-soft); padding: 0 0.2rem; border-radius: 3px; }
</style>

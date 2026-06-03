<!--
  WidgetFederation — #329 (#225 row G1).
  Lists cached federation peer Agent Cards from /api/v1/peers.
  Polls every 5s (federation is slow-changing).
  Empty state prompts operator to configure --federation-registry-url.
-->
<script>
  const API = '/api-v08/v1';
  let peers = $state([]);
  let lastError = $state('');

  async function refresh() {
    try {
      const r = await fetch(`${API}/peers`);
      const data = await r.json();
      peers = data.peers || [];
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

  $effect(() => {
    refresh();
    const id = setInterval(refresh, 5000);
    return () => clearInterval(id);
  });
</script>

<div class="federation" data-testid="federation-peers">
  <h4>
    Federation
    <span class="count">({peers.length})</span>
  </h4>
  {#if peers.length === 0}
    <p class="hint">No peers. Start the daemon with <code>--hub-url</code> + <code>--org-id</code> to join the hub-relayed mesh (or <code>--federation-registry-url</code> for a hosted registry) to discover other chepherd instances.</p>
  {/if}
  <ul>
    {#each peers as p (p.sid)}
      <li>
        <div class="meta">
          <span class="dot">⇄</span>
          <strong>{p.name || p.sid}</strong>
          <span class="when">synced {relTime(p.syncedAt)}</span>
        </div>
      </li>
    {/each}
  </ul>
  {#if lastError}
    <p class="err">last fetch: {lastError}</p>
  {/if}
</div>

<style>
  .federation { padding: 0.75rem 0.85rem; height: 100%; overflow-y: auto; background: var(--bg); }
  h4 { font-size: 0.82rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.05em; margin: 0 0 0.55rem 0; font-weight: 600; display: flex; align-items: center; gap: 0.45rem; }
  .count { color: var(--fg-faint); font-weight: 400; text-transform: none; letter-spacing: 0; }
  .hint { color: var(--fg-faint); font-size: 0.82rem; }
  ul { list-style: none; padding: 0; margin: 0; }
  li { padding: 0.45rem 0.5rem; border-left: 3px solid transparent; border-bottom: 1px solid var(--border); font-size: 0.83rem; color: var(--fg-muted); }
  li:hover { border-left-color: var(--accent-2); color: var(--fg); }
  .meta { display: flex; align-items: center; gap: 0.4rem; }
  .dot { color: var(--accent-2); }
  .when { margin-left: auto; color: var(--fg-faint); font-size: 0.72rem; }
  .err { color: var(--err, #ff6464); font-size: 0.72rem; margin-top: 0.5rem; }
  code { background: var(--bg-soft); padding: 0 0.2rem; border-radius: 3px; }
</style>

<script>
  let { inbox } = $props();
  const API = '/api-v08/v1';
  async function markRead(id) {
    try { await fetch(`${API}/inbox/${id}/read`, { method: 'POST' }); } catch {}
  }
  async function markAllRead() {
    try { await fetch(`${API}/inbox/read-all`, { method: 'POST' }); } catch {}
  }
  function relTime(ts) {
    if (!ts) return '';
    const s = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s/60)}m ago`;
    return `${Math.floor(s/3600)}h ago`;
  }
  let unread = $derived((inbox || []).filter(m => !m.read).length);
</script>

<div class="inbox">
  <h4>
    Inbox
    {#if unread > 0}
      <span class="badge">{unread}</span>
      <button class="link" on:click={markAllRead}>mark all read</button>
    {/if}
  </h4>
  {#if !(inbox && inbox.length)}
    <p class="hint">No messages. Shepherd writes here only for accomplishments / failures / stuck / questions.</p>
  {/if}
  <ul>
    {#each (inbox || []).slice().reverse() as m (m.id)}
      <li class:unread={!m.read} on:click={() => markRead(m.id)}>
        <div class="meta"><strong>@{m.from}</strong><span class="when">{relTime(m.at)}</span></div>
        <div class="body">{m.body}</div>
      </li>
    {/each}
  </ul>
</div>

<style>
  .inbox { padding: 0.75rem 0.85rem; height: 100%; overflow-y: auto; background: var(--bg); }
  h4 { font-size: 0.74rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.06em; margin: 0 0 0.55rem 0; font-weight: 600; display: flex; align-items: center; gap: 0.45rem; }
  .badge { background: var(--accent); color: #000; border-radius: 9px; padding: 0.04rem 0.45rem; font-size: 0.7rem; font-weight: 700; text-transform: none; }
  .link { margin-left: auto; background: transparent; border: none; color: var(--accent-2); cursor: pointer; font-size: 0.72rem; text-decoration: underline; }
  .hint { color: var(--fg-faint); font-size: 0.82rem; }
  ul { list-style: none; padding: 0; margin: 0; }
  li { padding: 0.45rem 0.5rem; border-left: 3px solid transparent; border-bottom: 1px solid var(--border); cursor: pointer; font-size: 0.83rem; color: var(--fg-muted); }
  li.unread { border-left-color: var(--accent); color: var(--fg); background: rgba(255,165,0,0.05); }
  li.unread strong { color: var(--accent); }
  .meta { display: flex; justify-content: space-between; align-items: baseline; }
  .meta strong { color: var(--accent-2); font-size: 0.82rem; }
  .when { color: var(--fg-faint); font-size: 0.7rem; }
  .body { line-height: 1.35; margin-top: 0.15rem; }
</style>

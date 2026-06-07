<!--
  MissionTranscript — team messaging surface (spec's "Team Transcript").
  Real data: GET /api/v1/transcript?teams=all (multi) or
  /api/v1/teams/{team}/messages (single); POST to send as operator.
  Sender chips use agentIdentity (color + icon). @-mentions render in the
  mentioned agent's color; #N becomes a GitHub ticket link. Live SSE tick
  per team + slow safety poll. Auto-scroll unless the user scrolled up.
-->
<script>
  import { onMount } from 'svelte';
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let { teams = [], initialTeam = 'all', mode = 'dark' } = $props();

  const API = '/api/v1';
  let scope = $state(initialTeam);
  let messages = $state([]);
  let body = $state('');
  let sending = $state(false);
  let err = $state('');
  let listEl;
  let scrolledUp = $state(false);
  let lastCount = 0;

  const teamNames = $derived((teams || []).map(t => t.name || t));

  async function refresh() {
    try {
      let msgs = [];
      if (scope === 'all') {
        const r = await fetch(`${API}/transcript?teams=all`);
        if (r.ok) { const j = await r.json(); msgs = Array.isArray(j.messages) ? j.messages : []; }
        else {
          for (const t of (teamNames.length ? teamNames : ['default'])) {
            const rr = await fetch(`${API}/teams/${encodeURIComponent(t)}/messages`);
            if (rr.ok) { const jj = await rr.json(); (jj.messages || []).forEach(m => msgs.push({ ...m, team: t })); }
          }
        }
      } else {
        const r = await fetch(`${API}/teams/${encodeURIComponent(scope)}/messages`);
        if (r.ok) { const j = await r.json(); msgs = (j.messages || []).map(m => ({ ...m, team: scope })); }
      }
      msgs.sort((a, b) => new Date(a.created_at) - new Date(b.created_at));
      const grew = msgs.length > lastCount; lastCount = msgs.length;
      messages = msgs;
      if (grew && !scrolledUp) setTimeout(() => { if (listEl) listEl.scrollTop = listEl.scrollHeight; }, 40);
      err = '';
    } catch (e) { err = e?.message || 'fetch failed'; }
  }

  async function send() {
    const txt = body.trim();
    if (!txt) return;
    sending = true; err = '';
    const team = scope === 'all' ? (teamNames[0] || 'default') : scope;
    try {
      const r = await fetch(`${API}/teams/${encodeURIComponent(team)}/messages`, {
        method: 'POST', headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ author: 'operator', body: txt }),
      });
      if (!r.ok) { const e = await r.json().catch(() => ({})); err = e.error || `HTTP ${r.status}`; }
      else { body = ''; await refresh(); }
    } catch (e) { err = e?.message || 'send failed'; }
    finally { sending = false; }
  }

  function onScroll() {
    if (!listEl) return;
    scrolledUp = listEl.scrollHeight - listEl.scrollTop - listEl.clientHeight >= 40;
  }
  function onKey(e) {
    if (e.key === 'Enter' && !e.shiftKey && !e.ctrlKey && !e.metaKey) { e.preventDefault(); send(); }
  }

  function timeHM(ts) {
    try { return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }); } catch { return ''; }
  }
  function renderBody(m) {
    let html = (m.body || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    html = html.replace(/@([a-zA-Z][a-zA-Z0-9_-]*)/g, (_x, nm) =>
      `<span class="mention" style="color:${agentIdentity(nm).color}">@${nm}</span>`);
    const repo = (m.team_github_url ? m.team_github_url.replace(/\/+$/, '') : 'https://github.com/chepherd/chepherd');
    html = html.replace(/#(\d+)\b/g, (_x, n) => `<a class="ticket" href="${repo}/issues/${n}" target="_blank" rel="noopener">#${n}↗</a>`);
    return html;
  }

  let es = null;
  function subscribe() {
    if (es) { es.close(); es = null; }
    if (scope === 'all') return;
    let tok = ''; try { tok = localStorage.getItem('chepherd-token') || ''; } catch {}
    const q = tok ? ('?token=' + encodeURIComponent(tok)) : '';
    try { es = new EventSource(`${API}/teams/${encodeURIComponent(scope)}/stream${q}`); es.addEventListener('tick', () => refresh()); } catch { es = null; }
  }

  onMount(() => {
    refresh(); subscribe();
    const id = setInterval(refresh, 15000);
    return () => { clearInterval(id); if (es) es.close(); };
  });
  $effect(() => { scope; refresh(); subscribe(); });
</script>

<div class="mt">
  <header class="mt-head">
    <span class="mt-title">✉ TRANSCRIPT</span>
    <select bind:value={scope} class="scope" title="Team scope">
      <option value="all">all teams</option>
      {#each teamNames as t}<option value={t}>{t}</option>{/each}
    </select>
  </header>
  <div class="mt-list" bind:this={listEl} onscroll={onScroll}>
    {#each messages as m (m.id || (m.author + m.created_at))}
      {@const ident = agentIdentity(m.author)}
      <article class="msg">
        <span class="chip" style="color:{ident.color}" title={'@' + m.author}>{ident.icon} {m.author}</span>
        {#if scope === 'all' && m.team}<span class="team">{m.team}</span>{/if}
        <span class="ts">{timeHM(m.created_at)}</span>
        <div class="body">{@html renderBody(m)}</div>
      </article>
    {/each}
    {#if !messages.length}<p class="empty">No messages yet.</p>{/if}
    {#if err}<p class="err">{err}</p>{/if}
  </div>
  <footer class="mt-compose">
    <textarea bind:value={body} onkeydown={onKey} placeholder="Message team · @mention to wake an agent · Enter sends" disabled={sending}></textarea>
    <button class="send" onclick={send} disabled={sending || !body.trim()}>{sending ? '…' : 'Send'}</button>
  </footer>
</div>

<style>
  .mt { display: flex; flex-direction: column; height: 100%; min-height: 0; background: var(--m-panel); color: var(--m-fg); }
  .mt-head { display: flex; align-items: center; justify-content: space-between; gap: 0.5rem; padding: 0.45rem 0.6rem; border-bottom: 1px solid var(--m-border); }
  .mt-title { font-size: 0.62rem; letter-spacing: 0.14em; color: var(--m-fg-faint); font-weight: 700; }
  .scope { background: var(--m-panel-3); color: var(--m-fg); border: 1px solid var(--m-border-strong); border-radius: 4px; font: inherit; font-size: 0.72rem; padding: 0.18rem 0.3rem; }
  .mt-list { flex: 1; overflow-y: auto; padding: 0.5rem 0.6rem; display: flex; flex-direction: column; gap: 0.5rem; }
  .mt-list::-webkit-scrollbar { width: 9px; }
  .mt-list::-webkit-scrollbar-thumb { background: var(--m-scroll); border-radius: 5px; }
  .msg { display: grid; grid-template-columns: auto auto 1fr; gap: 0.3rem 0.45rem; align-items: baseline; }
  .chip { font-family: ui-monospace, monospace; font-size: 0.74rem; font-weight: 600; white-space: nowrap; }
  .team { font-size: 0.6rem; background: var(--m-panel-3); color: var(--m-fg-dim); padding: 0.02rem 0.3rem; border-radius: 3px; }
  .ts { font-size: 0.64rem; color: var(--m-fg-faint); font-family: ui-monospace, monospace; text-align: right; }
  .body { grid-column: 1 / -1; font-size: 0.8rem; line-height: 1.45; white-space: pre-wrap; word-break: break-word; color: var(--m-fg-dim); padding-left: 0.2rem; border-left: 2px solid var(--m-border); }
  .body :global(.mention) { font-weight: 600; }
  .body :global(.ticket) { color: var(--m-accent); text-decoration: none; font-weight: 600; }
  .body :global(.ticket:hover) { text-decoration: underline; }
  .empty { color: var(--m-fg-faint); font-size: 0.78rem; }
  .err { color: var(--m-danger); font-size: 0.74rem; }
  .mt-compose { border-top: 1px solid var(--m-border); padding: 0.5rem 0.6rem; display: flex; gap: 0.4rem; align-items: flex-end; }
  .mt-compose textarea {
    flex: 1; box-sizing: border-box; min-height: 2.4rem; max-height: 8rem; resize: vertical;
    background: var(--m-bg); color: var(--m-fg); border: 1px solid var(--m-border-strong);
    border-radius: 5px; font: inherit; font-size: 0.78rem; padding: 0.4rem 0.5rem; font-family: ui-monospace, monospace;
  }
  .mt-compose textarea::placeholder { color: var(--m-fg-faint); }
  .send { background: var(--m-accent-2); color: var(--m-bg); border: 0; border-radius: 5px; padding: 0.45rem 0.9rem; font-weight: 700; font-size: 0.78rem; cursor: pointer; }
  .send:disabled { opacity: 0.45; cursor: not-allowed; }
</style>

<!--
  BoardTranscript — team conversation surface for the "board" dashboard.

  Reuses the real messaging data layer:
    GET  /api/v1/transcript?teams=all                  (multi-team)
    GET  /api/v1/teams/{team}/messages                 (single team)
    POST /api/v1/teams/{team}/messages {body, author}  (send)

  Sender chips use agentIdentity (color + role icon). Auto-scrolls to
  bottom unless the operator has scrolled up. Enter sends.
-->
<script>
  import { onMount, onDestroy } from 'svelte';
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let { teams = [] } = $props();

  const API = '/api/v1';
  let scope = $state('all');               // 'all' | <team name>
  let messages = $state([]);
  let body = $state('');
  let sending = $state(false);
  let err = $state('');
  let listEl = $state(null);
  let userScrolledUp = $state(false);
  let lastCount = 0;
  let poll = null;

  let teamNames = $derived((teams || []).map(t => t.name || t).filter(Boolean));

  async function refresh() {
    try {
      let msgs = [];
      if (scope === 'all') {
        const r = await fetch(`${API}/transcript?teams=all`);
        if (r.ok) {
          const j = await r.json();
          if (Array.isArray(j.messages)) msgs = j.messages;
        }
        if (!msgs.length) {
          const scopes = teamNames.length ? teamNames : ['default'];
          for (const s of scopes) {
            const rr = await fetch(`${API}/teams/${encodeURIComponent(s)}/messages`);
            if (!rr.ok) continue;
            try { const j = JSON.parse(await rr.text()); (j.messages || []).forEach(m => msgs.push({ ...m, team: s })); } catch {}
          }
        }
      } else {
        const r = await fetch(`${API}/teams/${encodeURIComponent(scope)}/messages`);
        if (r.ok) { try { const j = JSON.parse(await r.text()); (j.messages || []).forEach(m => msgs.push({ ...m, team: scope })); } catch {} }
      }
      msgs.sort((a, b) => String(a.created_at || '').localeCompare(String(b.created_at || '')));
      const grew = msgs.length > lastCount;
      messages = msgs;
      lastCount = msgs.length;
      err = '';
      if (grew && !userScrolledUp) queueMicrotask(scrollToBottom);
    } catch (e) {
      err = e?.message || 'fetch failed';
    }
  }

  function scrollToBottom() {
    setTimeout(() => { if (listEl) listEl.scrollTop = listEl.scrollHeight; }, 30);
  }
  function onScroll() {
    if (!listEl) return;
    userScrolledUp = (listEl.scrollHeight - listEl.scrollTop - listEl.clientHeight) > 48;
  }

  async function send() {
    const text = body.trim();
    if (!text || sending) return;
    const sendTeam = scope === 'all' ? (teamNames[0] || 'default') : scope;
    sending = true; err = '';
    try {
      const r = await fetch(`${API}/teams/${encodeURIComponent(sendTeam)}/messages`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ body: text, author: 'operator' }),
      });
      if (!r.ok) { err = `send failed (HTTP ${r.status})`; return; }
      body = '';
      userScrolledUp = false;
      await refresh();
    } catch (e) { err = e?.message || 'send failed'; }
    finally { sending = false; }
  }

  function onKey(e) {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
  }

  function fmtTime(ts) {
    if (!ts) return '';
    const d = new Date(ts);
    if (isNaN(d)) return '';
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  // Linkify #N ticket refs into a span so they stand out.
  function bodyParts(text) {
    const out = [];
    const re = /#(\d+)/g;
    let last = 0, m;
    while ((m = re.exec(text || '')) !== null) {
      if (m.index > last) out.push({ t: 'txt', v: text.slice(last, m.index) });
      out.push({ t: 'tk', v: m[0] });
      last = m.index + m[0].length;
    }
    if (last < (text || '').length) out.push({ t: 'txt', v: text.slice(last) });
    return out;
  }

  $effect(() => { scope; refresh(); });

  onMount(() => {
    refresh();
    poll = setInterval(refresh, 2500);
    queueMicrotask(scrollToBottom);
  });
  onDestroy(() => { if (poll) clearInterval(poll); });
</script>

<div class="tx">
  <header class="tx-head">
    <span class="tx-title">Conversation</span>
    <select class="tx-scope" bind:value={scope} title="filter by team">
      <option value="all">All teams</option>
      {#each teamNames as t}<option value={t}>{t}</option>{/each}
    </select>
  </header>

  <div class="tx-list" bind:this={listEl} onscroll={onScroll}>
    {#if !messages.length}
      <div class="tx-empty">No messages yet. Say something to the team below.</div>
    {/if}
    {#each messages as m (m.id || (m.author + m.created_at + m.body))}
      {@const idn = agentIdentity(m.author || 'unknown')}
      <div class="msg" class:mine={(m.author || '') === 'operator'}>
        <span class="chip" style="--c:{idn.color}">
          <span class=" chip-ico">{idn.icon}</span>{m.author || 'unknown'}
        </span>
        {#if scope === 'all' && m.team}<span class="team-chip">{m.team}</span>{/if}
        <span class="time">{fmtTime(m.created_at)}</span>
        <div class="text">{#each bodyParts(m.body) as p}{#if p.t === 'tk'}<span class="ticket">{p.v}</span>{:else}{p.v}{/if}{/each}</div>
      </div>
    {/each}
  </div>

  {#if err}<div class="tx-err">{err}</div>{/if}

  <div class="tx-compose">
    <textarea
      bind:value={body}
      onkeydown={onKey}
      placeholder={scope === 'all' ? `Message ${teamNames[0] || 'team'}… (Enter to send)` : `Message ${scope}… (Enter to send)`}
      rows="2"
    ></textarea>
    <button class="send" onclick={send} disabled={sending || !body.trim()}>{sending ? '…' : 'Send'}</button>
  </div>
</div>

<style>
  .tx { display: flex; flex-direction: column; height: 100%; min-height: 0; background: var(--board-surface); }
  .tx-head {
    display: flex; align-items: center; gap: 0.5rem; padding: 0.55rem 0.75rem;
    border-bottom: 1px solid var(--board-border); flex: 0 0 auto;
  }
  .tx-title { font-weight: 650; font-size: 0.84rem; color: var(--board-fg); flex: 1; }
  .tx-scope {
    background: var(--board-input); color: var(--board-fg);
    border: 1px solid var(--board-border-strong); border-radius: 6px;
    font-size: 0.75rem; padding: 0.2rem 0.4rem;
  }

  .tx-list { flex: 1; min-height: 0; overflow-y: auto; padding: 0.6rem 0.7rem; display: flex; flex-direction: column; gap: 0.6rem; }
  .tx-empty { color: var(--board-fg-faint); font-size: 0.8rem; text-align: center; margin: auto; padding: 1rem; }

  .msg { display: flex; flex-wrap: wrap; align-items: center; gap: 0.4rem; }
  .chip {
    display: inline-flex; align-items: center; gap: 0.28rem;
    font-size: 0.74rem; font-weight: 600; color: var(--c);
    background: color-mix(in srgb, var(--c) 16%, transparent);
    border: 1px solid color-mix(in srgb, var(--c) 45%, transparent);
    padding: 0.06rem 0.42rem; border-radius: 999px;
  }
  .team-chip {
    font-size: 0.66rem; color: var(--board-fg-muted); background: var(--board-chip-bg);
    border-radius: 5px; padding: 0.04rem 0.34rem;
  }
  .time { font-size: 0.66rem; color: var(--board-fg-faint); }
  .text { flex: 1 0 100%; font-size: 0.82rem; color: var(--board-fg); line-height: 1.45; white-space: pre-wrap; word-break: break-word; padding-left: 0.1rem; }
  .ticket { color: var(--board-accent); font-weight: 600; }
  .msg.mine .text { color: var(--board-fg); }

  .tx-err { color: var(--board-danger); font-size: 0.74rem; padding: 0.3rem 0.75rem; }

  .tx-compose { display: flex; gap: 0.5rem; padding: 0.55rem 0.7rem; border-top: 1px solid var(--board-border); flex: 0 0 auto; }
  .tx-compose textarea {
    flex: 1; resize: none; font: inherit; font-size: 0.82rem;
    background: var(--board-input); color: var(--board-fg);
    border: 1px solid var(--board-border-strong); border-radius: 8px;
    padding: 0.45rem 0.55rem; line-height: 1.4;
  }
  .tx-compose textarea:focus { outline: none; border-color: var(--board-accent); }
  .send {
    align-self: stretch; background: var(--board-accent); color: var(--board-accent-fg);
    border: 0; border-radius: 8px; padding: 0 1rem; font-weight: 650; font-size: 0.82rem; cursor: pointer;
  }
  .send:disabled { opacity: 0.5; cursor: not-allowed; }
  .send:hover:not(:disabled) { filter: brightness(1.08); }
</style>

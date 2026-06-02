<!--
  TeamTranscript — unified messaging pane (#659 epic #654).
  Shows ALL messages across team(s) with sender/recipient chips, ticket
  auto-links, color-coded team chips, expand-collapse for long bodies,
  Enter-sends compose. Single source of truth replacing A2A Inbox + Inbox.
-->
<script>
  import { onMount } from 'svelte';

  let { team = 'default' } = $props();

  let selectedScope = $state(team); // 'all' or specific team name
  let teams = $state([]);            // list of teams from /api/v1/teams
  let transcript = $state({ channel: null, messages: [] });
  let composeBody = $state('');
  let composeSending = $state(false);
  let composeError = $state('');
  let lastFetchError = $state('');
  let messagesEl = $state(null);
  let lastMessageCount = $state(0);
  let userScrolledUp = $state(false);
  let expanded = $state({});         // {msgId: bool} for show-more bodies

  const API = '/api/v1';

  // Pick a stable color per team name (HSL hash). Used for the team chip
  // when "all" scope is selected so the operator can scan by color.
  function teamColor(name) {
    if (!name) return '#888';
    let h = 0;
    for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
    return `hsl(${h % 360}, 55%, 55%)`;
  }

  async function loadTeams() {
    try {
      const r = await fetch(`${API}/teams`);
      if (!r.ok) return;
      const j = await r.json();
      teams = (j.teams || []).map(t => t.name || t);
    } catch {}
  }

  async function refresh() {
    try {
      // 'all' scope fetches every team's transcript and merges.
      // For now backend doesn't have a multi-team endpoint; iterate.
      let merged = { channel: { name: selectedScope, members: [] }, messages: [] };
      const scopes = selectedScope === 'all' ? (teams.length ? teams : [team]) : [selectedScope];
      const allMsgs = [];
      for (const s of scopes) {
        const r = await fetch(`${API}/teams/${encodeURIComponent(s)}/messages`);
        if (!r.ok) continue;
        const text = await r.text();
        try {
          const j = JSON.parse(text);
          (j.messages || []).forEach(m => allMsgs.push({ ...m, team: s }));
          if (selectedScope !== 'all' && j.channel) merged.channel = j.channel;
        } catch {}
      }
      merged.messages = allMsgs;
      const grew = merged.messages.length > lastMessageCount;
      transcript = merged;
      lastMessageCount = merged.messages.length;
      if (grew && !userScrolledUp) {
        setTimeout(() => { if (messagesEl) messagesEl.scrollTop = messagesEl.scrollHeight; }, 50);
      }
      lastFetchError = '';
    } catch (e) {
      lastFetchError = e?.message || 'fetch failed';
    }
  }

  function onScroll() {
    if (!messagesEl) return;
    const atBottom = messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight < 40;
    userScrolledUp = !atBottom;
  }

  async function send() {
    const body = composeBody.trim();
    if (!body) return;
    composeError = '';
    composeSending = true;
    const targetTeam = selectedScope === 'all' ? team : selectedScope;
    try {
      const r = await fetch(`${API}/teams/${encodeURIComponent(targetTeam)}/messages`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ author: 'operator', body }),
      });
      const text = await r.text();
      if (!r.ok) {
        let msg = `HTTP ${r.status}`;
        try { msg = JSON.parse(text).error || msg; } catch { msg = text.slice(0, 80); }
        composeError = msg;
        return;
      }
      composeBody = '';
      await refresh();
    } catch (e) {
      composeError = e?.message || 'send failed';
    } finally {
      composeSending = false;
    }
  }

  // Live cost preview — count distinct @-handles in compose box
  const previewMentions = $derived.by(() => {
    const m = (composeBody.match(/@([a-zA-Z][a-zA-Z0-9_-]*)/g) || []);
    return [...new Set(m.map(s => s.slice(1)))];
  });
  const previewWillWake = $derived(previewMentions.length);
  const previewTokens = $derived(previewWillWake * 400);
  const previewCost = $derived((previewTokens * 0.000015).toFixed(4));

  // Sort + group messages
  const sortedMessages = $derived.by(() => {
    return (transcript.messages || [])
      .slice()
      .sort((a, b) => new Date(a.created_at) - new Date(b.created_at));
  });
  const groupedMessages = $derived.by(() => {
    const groups = new Map();
    for (const m of sortedMessages) {
      const day = new Date(m.created_at).toLocaleDateString();
      if (!groups.has(day)) groups.set(day, []);
      groups.get(day).push(m);
    }
    return [...groups.entries()];
  });

  function relTime(ts) {
    if (!ts) return '';
    const s = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s/60)}m`;
    if (s < 86400) return `${Math.floor(s/3600)}h`;
    return `${Math.floor(s/86400)}d`;
  }

  // Body rendering: @-mention highlight + #-ticket auto-link.
  // Tickets resolve against the team's github_url (auto-derived from
  // any team member's spawn-time clone_url; same source the kanban widget
  // already uses). For 'all' scope we use the message's own team.
  function renderBody(m) {
    let html = m.body || '';
    // escape minimal HTML
    html = html.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    // @mention
    html = html.replace(/@([a-zA-Z][a-zA-Z0-9_-]*)/g, '<span class="mention">@$1</span>');
    // #ticket — best-effort link to GitHub (the team's repo). Kanban
    // widget knows the real per-team repo; here we default to the
    // chepherd repo until per-team repo prop is threaded through.
    html = html.replace(/#(\d+)\b/g, '<a class="ticket" href="https://github.com/chepherd/chepherd/issues/$1" target="_blank" rel="noopener">#$1 ↗</a>');
    return html;
  }

  function recipientLabel(m) {
    if (!m.recipients || m.recipients.length === 0) return '(no recipients)';
    if (m.recipients.includes('everyone')) return '@everyone';
    if (m.recipients.length === 1) return '@' + m.recipients[0];
    return m.recipients.map(r => '@' + r).join(', ') + ` (${m.recipients.length})`;
  }

  // Truncate long bodies (>2 lines / ~200 chars) — show [show more] toggle.
  function isLong(body) {
    if (!body) return false;
    return body.length > 200 || (body.match(/\n/g) || []).length >= 2;
  }
  function preview(body) {
    if (!body) return '';
    const firstNL = body.indexOf('\n');
    const cut = firstNL > 0 && firstNL < 200 ? firstNL : 200;
    return body.slice(0, cut) + (body.length > cut ? '…' : '');
  }

  // Compose keys: Enter sends, Shift+Enter newline (Slack-style).
  function handleKeydown(e) {
    if (e.key === 'Enter' && !e.shiftKey && !e.metaKey && !e.ctrlKey) {
      e.preventDefault();
      send();
    }
  }

  onMount(() => {
    loadTeams();
    refresh();
    const id = setInterval(refresh, 5000);
    return () => clearInterval(id);
  });

  // When the user changes scope via the dropdown, refresh immediately.
  $effect(() => {
    selectedScope;
    refresh();
  });
</script>

<section class="transcript" data-testid="team-transcript">
  <header class="header">
    <div class="header-row">
      <label>Teams:
        <select bind:value={selectedScope} class="team-picker" data-testid="team-picker">
          <option value="all">▾ all</option>
          {#each teams as t}
            <option value={t}>{t}</option>
          {/each}
          {#if !teams.includes(team) && team !== 'all'}
            <option value={team}>{team}</option>
          {/if}
        </select>
      </label>
      <div class="members">
        {#if selectedScope === 'all'}
          Member of: {teams.join(' · ') || '(no teams)'}
        {:else}
          members: {(transcript.channel?.members || []).map(m => '@' + m).join(' ') || '(none)'}
        {/if}
      </div>
    </div>
  </header>

  <div class="messages" bind:this={messagesEl} onscroll={onScroll}>
    {#each groupedMessages as [day, msgs]}
      <div class="day-divider">── {day} ──</div>
      {#each msgs as m (m.id)}
        <article class="msg">
          <div class="row1">
            {#if selectedScope === 'all' && m.team}
              <span class="team-chip" style="background: {teamColor(m.team)}">{m.team}</span>
            {/if}
            <span class="chip from">@{m.author}</span>
            <span class="arrow">→</span>
            <span class="chip to" title={(m.recipients || []).join(', ')}>{recipientLabel(m)}</span>
            <span class="ts">{relTime(m.created_at)}</span>
            {#if m.recipients && m.recipients.length > 1}
              <span class="badge multi">multi ({m.recipients.length})</span>
            {/if}
          </div>
          {#if isLong(m.body) && !expanded[m.id]}
            <div class="body">
              {@html renderBody({ body: preview(m.body) })}
              <button class="expand" onclick={() => expanded = { ...expanded, [m.id]: true }}>▾ show more</button>
            </div>
          {:else}
            <div class="body">{@html renderBody(m)}</div>
            {#if isLong(m.body) && expanded[m.id]}
              <button class="expand" onclick={() => expanded = { ...expanded, [m.id]: false }}>▴ show less</button>
            {/if}
          {/if}
        </article>
      {/each}
    {/each}
    {#if !transcript.messages?.length}
      <p class="empty">No messages yet. Use the compose box below to start the conversation.</p>
    {/if}
    {#if lastFetchError}
      <p class="err">last fetch: {lastFetchError}</p>
    {/if}
  </div>

  <footer class="compose">
    <textarea
      bind:value={composeBody}
      placeholder="Type a message — @-mention to ping specific agents (e.g., @tech-lead) or @everyone for broadcast. Enter sends · Shift+Enter newline."
      onkeydown={handleKeydown}
      disabled={composeSending}
      data-testid="transcript-compose"
    ></textarea>
    <div class="compose-meta">
      {#if previewWillWake > 0}
        <span class="cost-preview">
          Will wake: <strong>{previewWillWake}</strong> agent{previewWillWake === 1 ? '' : 's'}
          (~{previewTokens.toLocaleString()} tokens, ~${previewCost})
        </span>
      {:else}
        <span class="cost-preview muted">No @-mentions — will be posted to transcript only (no agent wake).</span>
      {/if}
      <button
        class="primary"
        onclick={send}
        disabled={composeSending || !composeBody.trim()}
        data-testid="transcript-send"
      >
        {composeSending ? '⟳ sending…' : 'Send'}
      </button>
    </div>
    {#if composeError}
      <p class="err">{composeError}</p>
    {/if}
  </footer>
</section>

<style>
  .transcript {
    display: flex; flex-direction: column; height: 100%;
    background: var(--bg, #0a0a0a); color: var(--fg, #f5f5f5);
    border: 1px solid var(--border, #2a2a2a); border-radius: 8px;
    overflow: hidden; min-height: 24rem;
  }
  .header { padding: 0.6rem 1rem; border-bottom: 1px solid var(--border, #2a2a2a); }
  .header-row { display: flex; align-items: center; gap: 1rem; flex-wrap: wrap; }
  .team-picker {
    background: var(--bg-elevated, #1a1a1a); color: var(--fg, #f5f5f5);
    border: 1px solid var(--border, #2a2a2a); border-radius: 4px;
    padding: 0.25rem 0.5rem; font: inherit; font-size: 0.88rem;
  }
  .members { color: var(--fg-muted, #888); font-size: 0.78rem; flex: 1; }
  .messages { flex: 1; overflow-y: auto; padding: 0.8rem 1rem; }
  .day-divider { color: var(--fg-muted, #666); font-size: 0.75rem; text-align: center; margin: 0.8rem 0 0.5rem; }
  .msg { margin-bottom: 0.85rem; }
  .row1 { display: flex; align-items: center; gap: 0.4rem; flex-wrap: wrap; margin-bottom: 0.25rem; }
  .team-chip {
    color: #0a0a0a; font-weight: 700; font-size: 0.7rem;
    padding: 0.08rem 0.45rem; border-radius: 3px; text-transform: lowercase;
  }
  .chip {
    background: var(--bg-elevated, #1a1a1a); border: 1px solid var(--border, #333);
    padding: 0.1rem 0.45rem; border-radius: 999px; font-size: 0.78rem; font-family: ui-monospace, monospace;
  }
  .chip.from { color: var(--accent-2, #87ceeb); border-color: var(--accent-2, #87ceeb); }
  .chip.to { color: #aaa; }
  .arrow { color: var(--fg-muted, #666); }
  .ts { color: var(--fg-muted, #666); font-size: 0.75rem; margin-left: auto; }
  .badge.multi { background: rgba(135,206,235,0.18); color: var(--accent-2, #87ceeb); font-size: 0.7rem; padding: 0.05rem 0.4rem; border-radius: 3px; }
  .body { padding: 0.35rem 0.6rem 0.35rem 0.8rem; background: var(--bg-elevated, #131313); border-left: 2px solid var(--border, #2a2a2a); border-radius: 0 4px 4px 0; white-space: pre-wrap; word-break: break-word; font-size: 0.88rem; }
  .body :global(.mention) { color: var(--accent-2, #87ceeb); font-weight: 600; }
  .body :global(.ticket) { color: #ffb86c; text-decoration: none; font-weight: 600; }
  .body :global(.ticket:hover) { text-decoration: underline; }
  .expand {
    display: inline-block; margin-top: 0.2rem; padding: 0.05rem 0.4rem;
    background: transparent; border: 0; color: var(--accent-2, #87ceeb);
    cursor: pointer; font: inherit; font-size: 0.75rem; text-decoration: underline;
  }
  .empty { color: var(--fg-muted, #888); font-size: 0.85rem; }
  .err { color: #e74c3c; font-size: 0.8rem; }
  .compose { border-top: 1px solid var(--border, #2a2a2a); padding: 0.7rem 1rem; }
  .compose textarea { width: 100%; box-sizing: border-box; min-height: 3.5rem; padding: 0.45rem 0.6rem; background: var(--bg-elevated, #1a1a1a); color: var(--fg, #f5f5f5); border: 1px solid var(--border, #2a2a2a); border-radius: 4px; font: inherit; font-family: ui-monospace, monospace; font-size: 0.85rem; resize: vertical; }
  .compose-meta { display: flex; align-items: center; gap: 0.8rem; margin-top: 0.4rem; }
  .cost-preview { color: var(--fg-muted, #888); font-size: 0.78rem; flex: 1; }
  .cost-preview.muted { color: var(--fg-muted, #666); }
  .cost-preview strong { color: var(--accent-2, #87ceeb); }
  .primary { background: var(--accent-2, #87ceeb); border: 0; color: #0a0a0a; padding: 0.4rem 1rem; border-radius: 4px; cursor: pointer; font-weight: 600; }
  .primary:disabled { opacity: 0.4; cursor: not-allowed; }
</style>

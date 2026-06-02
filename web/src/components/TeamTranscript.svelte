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
  let composeEl = $state(null);
  let lastMessageCount = $state(0);
  let userScrolledUp = $state(false);
  let expanded = $state({});         // {msgId: bool} for show-more bodies

  // @-autocomplete state (#664)
  let mentionOpen = $state(false);
  let mentionQuery = $state('');     // text after the @ up to the caret
  let mentionStart = $state(-1);     // index in composeBody of the '@'
  let mentionIndex = $state(0);      // highlighted item index
  let leadTargets = $state({});      // { teamName: leadHandle } populated lazily (#662)

  // Role aliases — surfaced in autocomplete so the operator can ping by role
  // even when they don't remember the exact agent handle. Backend resolves
  // these via resolveTeamLead / role lookups.
  const ROLE_ALIASES = [
    'tech-lead', 'scrum-master', 'orchestrator', 'architect',
    'worker', 'reviewer', 'qa', 'frontend', 'backend',
  ];

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
      mentionOpen = false;
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

  // ── @-autocomplete (#664) ────────────────────────────────────────────
  // List of candidate handles: live agents from channel.members + role
  // aliases + the two magic broadcast tokens.
  const mentionCandidates = $derived.by(() => {
    const live = (transcript.channel?.members || []).slice();
    // Also surface members of OTHER teams in 'all' scope by scraping
    // recent message authors/recipients — best-effort, no extra fetch.
    const seen = new Set(live);
    for (const m of (transcript.messages || [])) {
      if (m.author && !seen.has(m.author) && m.author !== 'operator') {
        seen.add(m.author); live.push(m.author);
      }
      for (const r of (m.recipients || [])) {
        if (!seen.has(r)) { seen.add(r); live.push(r); }
      }
    }
    const aliases = ROLE_ALIASES.filter(a => !seen.has(a));
    return [...live, ...aliases, 'everyone', 'all-teams'];
  });

  const mentionFiltered = $derived.by(() => {
    if (!mentionOpen) return [];
    const q = (mentionQuery || '').toLowerCase();
    if (!q) return mentionCandidates.slice(0, 8);
    return mentionCandidates.filter(c => c.toLowerCase().startsWith(q)).slice(0, 8);
  });

  // Re-evaluate whether the autocomplete should be open based on the
  // caret position + current composeBody contents.
  function updateMentionState() {
    if (!composeEl) { mentionOpen = false; return; }
    const caret = composeEl.selectionStart ?? composeBody.length;
    // Walk back from caret to find the most recent '@'. Stop at whitespace
    // or a non-mention character — autocomplete only fires for fresh tokens.
    let i = caret - 1;
    while (i >= 0) {
      const ch = composeBody[i];
      if (ch === '@') break;
      if (/[\s,;]/.test(ch)) { i = -1; break; }
      // Allow letters/digits/_-
      if (!/[a-zA-Z0-9_-]/.test(ch)) { i = -1; break; }
      i--;
    }
    if (i < 0) { mentionOpen = false; return; }
    // The '@' must be at start-of-string or follow whitespace/punctuation —
    // otherwise it's an email address or similar.
    if (i > 0 && !/[\s(,;]/.test(composeBody[i - 1])) {
      mentionOpen = false; return;
    }
    mentionStart = i;
    mentionQuery = composeBody.slice(i + 1, caret);
    mentionOpen = true;
    mentionIndex = 0;
  }

  function acceptMention(handle) {
    if (mentionStart < 0 || !composeEl) return;
    const caret = composeEl.selectionStart ?? composeBody.length;
    const before = composeBody.slice(0, mentionStart);
    const after = composeBody.slice(caret);
    const insert = '@' + handle + ' ';
    composeBody = before + insert + after;
    mentionOpen = false;
    mentionQuery = '';
    mentionStart = -1;
    // Restore caret to just after the inserted token+space.
    const newCaret = (before + insert).length;
    setTimeout(() => {
      if (composeEl) {
        composeEl.focus();
        composeEl.setSelectionRange(newCaret, newCaret);
      }
    }, 0);
  }

  // Compose keys: Enter sends, Shift+Enter newline (Slack-style). When the
  // mention popup is open, ArrowUp/Down navigate, Enter/Tab accept, Esc
  // closes (intercepts the keys before they hit the textarea).
  function handleKeydown(e) {
    if (mentionOpen && mentionFiltered.length > 0) {
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        mentionIndex = (mentionIndex + 1) % mentionFiltered.length;
        return;
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        mentionIndex = (mentionIndex - 1 + mentionFiltered.length) % mentionFiltered.length;
        return;
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault();
        acceptMention(mentionFiltered[mentionIndex]);
        return;
      }
      if (e.key === 'Escape') {
        e.preventDefault();
        mentionOpen = false;
        return;
      }
    }
    if (e.key === 'Enter' && !e.shiftKey && !e.metaKey && !e.ctrlKey) {
      e.preventDefault();
      send();
    }
  }

  function handleInput() {
    // After the textarea has updated composeBody (bind:value already fired),
    // re-evaluate the mention popup.
    updateMentionState();
  }

  function handleClickOrSelect() {
    // Caret moved without typing — close popup if we drifted off the token.
    updateMentionState();
  }

  function handleBlur() {
    // Give the popup-click a chance to fire before closing.
    setTimeout(() => { mentionOpen = false; }, 150);
  }

  // ── Default-route hint (#662) ────────────────────────────────────────
  // Lazy-fetch the team's lead handle when the scope changes; show in the
  // compose meta strip. If the backend doesn't have the endpoint yet we
  // just leave the hint empty (graceful degrade).
  async function ensureLead(teamName) {
    if (!teamName || teamName === 'all') return;
    if (leadTargets[teamName] !== undefined) return;
    leadTargets = { ...leadTargets, [teamName]: '' }; // mark in-flight
    try {
      const r = await fetch(`${API}/teams/${encodeURIComponent(teamName)}/lead`);
      if (r.ok) {
        const j = await r.json();
        const lead = j.lead || j.handle || '';
        leadTargets = { ...leadTargets, [teamName]: lead };
        return;
      }
    } catch {}
    // Fallback: scan channel members for known lead roles. We don't have
    // role metadata in the members list, so this is best-effort by handle
    // pattern — backend ticket will plug the real resolver.
    const members = (transcript.channel?.members || []);
    const candidates = ['scrum-master', 'tech-lead', 'orchestrator', 'architect'];
    let lead = '';
    for (const c of candidates) {
      const hit = members.find(m => m === c || m.endsWith('-' + c));
      if (hit) { lead = hit; break; }
    }
    if (!lead && members.length > 0) lead = members[0];
    leadTargets = { ...leadTargets, [teamName]: lead };
  }

  const defaultTarget = $derived(
    selectedScope === 'all' ? '' : (leadTargets[selectedScope] || '')
  );

  // ── Per-row alert-kind styling (#667) ────────────────────────────────
  function kindClass(m) {
    const k = (m && m.kind) || '';
    if (k === 'alert:failure') return 'alert-failure';
    if (k === 'alert:stuck') return 'alert-stuck';
    if (k === 'alert:question') return 'alert-question';
    if (k === 'alert:accomplishment') return 'alert-accomplishment';
    return '';
  }
  function kindLabel(m) {
    const k = (m && m.kind) || '';
    if (!k || k === 'message' || !k.startsWith('alert:')) return '';
    const bare = k.slice('alert:'.length);
    const icon =
      bare === 'failure' ? '⛔' :
      bare === 'stuck' ? '⏸' :
      bare === 'question' ? '⚠' :
      bare === 'accomplishment' ? '✅' : '•';
    return `${icon} ${bare}`;
  }

  onMount(() => {
    loadTeams();
    refresh();
    const id = setInterval(refresh, 5000);
    return () => clearInterval(id);
  });

  // When the user changes scope via the dropdown, refresh immediately
  // and fetch the default target for the lead-hint (#662).
  $effect(() => {
    selectedScope;
    refresh();
    ensureLead(selectedScope);
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
        <article class="msg {kindClass(m)}" data-testid="transcript-row" data-kind={m.kind || 'message'}>
          <div class="row1">
            {#if selectedScope === 'all' && m.team}
              <span class="team-chip" style="background: {teamColor(m.team)}">{m.team}</span>
            {/if}
            <span class="chip from">@{m.author}</span>
            <span class="arrow">→</span>
            <span class="chip to" title={(m.recipients || []).join(', ')}>{recipientLabel(m)}</span>
            {#if kindLabel(m)}
              <span class="kind-label kind-{(m.kind || '').replace('alert:', '')}">{kindLabel(m)}</span>
            {/if}
            <span class="ts">{relTime(m.created_at)}</span>
            {#if m.recipients && m.recipients.length > 1}
              <span class="badge multi">multi ({m.recipients.length})</span>
            {/if}
          </div>
          {#if m.routed_to_default && m.default_target}
            <div class="routed-sub" data-testid="routed-sub">
              ↪ routed to <span class="mention">@{m.default_target}</span> <span class="muted">(lead)</span>
            </div>
          {/if}
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
    <div class="compose-wrap">
      <textarea
        bind:this={composeEl}
        bind:value={composeBody}
        placeholder="Type a message — @-mention to ping specific agents (e.g., @tech-lead) or @everyone for broadcast. Enter sends · Shift+Enter newline."
        onkeydown={handleKeydown}
        oninput={handleInput}
        onclick={handleClickOrSelect}
        onkeyup={handleClickOrSelect}
        onblur={handleBlur}
        disabled={composeSending}
        data-testid="transcript-compose"
      ></textarea>
      {#if mentionOpen && mentionFiltered.length > 0}
        <ul class="mention-popup" data-testid="mention-popup">
          {#each mentionFiltered as cand, i}
            <li
              class:active={i === mentionIndex}
              data-testid="mention-item"
              onmousedown={(e) => { e.preventDefault(); acceptMention(cand); }}
              onmouseenter={() => mentionIndex = i}
            >@{cand}</li>
          {/each}
        </ul>
      {/if}
    </div>
    <div class="compose-meta">
      {#if previewWillWake > 0}
        <span class="cost-preview">
          Will wake: <strong>{previewWillWake}</strong> agent{previewWillWake === 1 ? '' : 's'}
          (~{previewTokens.toLocaleString()} tokens, ~${previewCost})
        </span>
      {:else if defaultTarget}
        <span class="cost-preview" data-testid="default-target-hint">
          → default: <strong>@{defaultTarget}</strong>
          <span class="muted">(no @-mention — will route to team lead)</span>
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
  .msg { margin-bottom: 0.85rem; padding-left: 0.5rem; border-left: 3px solid transparent; }
  .msg.alert-failure { border-left-color: #e74c3c; background: rgba(231,76,60,0.04); }
  .msg.alert-stuck { border-left-color: #f39c12; background: rgba(243,156,18,0.04); }
  .msg.alert-question { border-left-color: #f1c40f; background: rgba(241,196,15,0.04); }
  .msg.alert-accomplishment { border-left-color: #5cd57f; background: rgba(92,213,127,0.04); }
  .row1 { display: flex; align-items: center; gap: 0.4rem; flex-wrap: wrap; margin-bottom: 0.25rem; }
  .kind-label { font-size: 0.72rem; font-weight: 600; padding: 0.05rem 0.4rem; border-radius: 3px; text-transform: lowercase; }
  .kind-label.kind-failure { background: rgba(231,76,60,0.18); color: #e74c3c; }
  .kind-label.kind-stuck { background: rgba(243,156,18,0.18); color: #f39c12; }
  .kind-label.kind-question { background: rgba(241,196,15,0.18); color: #f1c40f; }
  .kind-label.kind-accomplishment { background: rgba(92,213,127,0.18); color: #5cd57f; }
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
  .routed-sub { font-size: 0.75rem; color: var(--fg-muted, #888); margin: 0.1rem 0 0.25rem 0.1rem; }
  .routed-sub .muted { color: var(--fg-muted, #666); }
  .routed-sub .mention { color: var(--accent-2, #87ceeb); font-weight: 600; }
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
  .compose-wrap { position: relative; }
  .compose textarea { width: 100%; box-sizing: border-box; min-height: 3.5rem; padding: 0.45rem 0.6rem; background: var(--bg-elevated, #1a1a1a); color: var(--fg, #f5f5f5); border: 1px solid var(--border, #2a2a2a); border-radius: 4px; font: inherit; font-family: ui-monospace, monospace; font-size: 0.85rem; resize: vertical; }
  .mention-popup {
    position: absolute; left: 0.4rem; bottom: calc(100% + 0.2rem);
    list-style: none; margin: 0; padding: 0.25rem 0;
    background: var(--bg-elevated, #1a1a1a);
    border: 1px solid var(--border, #2a2a2a); border-radius: 6px;
    min-width: 12rem; max-height: 14rem; overflow-y: auto;
    box-shadow: 0 4px 14px rgba(0,0,0,0.4);
    font-family: ui-monospace, monospace; font-size: 0.82rem;
    z-index: 50;
  }
  .mention-popup li { padding: 0.25rem 0.7rem; cursor: pointer; color: var(--fg, #f5f5f5); }
  .mention-popup li.active, .mention-popup li:hover { background: rgba(135,206,235,0.18); color: var(--accent-2, #87ceeb); }
  .compose-meta { display: flex; align-items: center; gap: 0.8rem; margin-top: 0.4rem; }
  .cost-preview { color: var(--fg-muted, #888); font-size: 0.78rem; flex: 1; }
  .cost-preview.muted { color: var(--fg-muted, #666); }
  .cost-preview strong { color: var(--accent-2, #87ceeb); }
  .cost-preview .muted { color: var(--fg-muted, #666); margin-left: 0.3rem; }
  .primary { background: var(--accent-2, #87ceeb); border: 0; color: #0a0a0a; padding: 0.4rem 1rem; border-radius: 4px; cursor: pointer; font-weight: 600; }
  .primary:disabled { opacity: 0.4; cursor: not-allowed; }
</style>

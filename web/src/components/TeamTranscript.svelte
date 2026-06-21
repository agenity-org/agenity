<!--
  TeamTranscript — unified messaging pane (#659 epic #654).
  Shows ALL messages across team(s) with sender/recipient chips, ticket
  auto-links, color-coded team chips, expand-collapse for long bodies,
  Enter-sends compose. Single source of truth replacing A2A Inbox + Inbox.
-->
<script>
  import { agentIdentity } from '../lib/agentIdentity.js';
  import { onMount } from 'svelte';

  let { team = 'default' } = $props();

  // The "All" aggregate scope was removed from the picker — it confused more
  // than it helped (team = the current view; recipient lives in the @mention).
  // Consumers may still pass team="all" as a prop, so resolve that to a real
  // team rather than leaving an unselectable scope. The backend ?teams=all
  // endpoint is untouched; we just stop offering it in the UI.
  let selectedScope = $state(team === 'all' ? 'default' : team);
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

  // Ticket-filter state (#665) — set when kanban dispatches chepherd-transcript-filter
  let ticketFilter = $state(null);   // { repo, num } or null

  // Role aliases — surfaced in autocomplete so the operator can ping by role
  // even when they don't remember the exact agent handle. Backend resolves
  // these via resolveTeamLead / role lookups.
  const ROLE_ALIASES = [
    'tech-lead', 'scrum-master', 'orchestrator', 'architect',
    'worker', 'reviewer', 'qa', 'frontend', 'backend',
  ];

  const API = '/api/v1';

  async function loadTeams() {
    try {
      const r = await fetch(`${API}/teams`);
      if (!r.ok) return;
      const j = await r.json();
      teams = (j.teams || []).map(t => t.name || t);
      // If the resolved scope isn't a real team (e.g. consumer passed
      // team="all", or "default" doesn't exist), snap to the first team so
      // the picker always shows a valid, selectable option.
      if (teams.length && !teams.includes(selectedScope)) {
        selectedScope = teams[0];
      }
    } catch {}
  }

  async function refresh() {
    try {
      // selectedScope is always a single real team (the "all" aggregate was
      // removed from the picker in 991e700 and consumers no longer pass it).
      let merged = { channel: { name: selectedScope, members: [] }, messages: [] };
      const r = await fetch(`${API}/teams/${encodeURIComponent(selectedScope)}/messages`);
      if (r.ok) {
        const text = await r.text();
        try {
          const j = JSON.parse(text);
          (j.messages || []).forEach(m => merged.messages.push({ ...m, team: selectedScope }));
          if (j.channel) merged.channel = j.channel;
        } catch {}
      }
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
    try {
      const r = await fetch(`${API}/teams/${encodeURIComponent(selectedScope)}/messages`, {
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

  // Sort + group messages (with optional ticket-filter from kanban click, #665)
  const filteredMessages = $derived.by(() => {
    const all = (transcript.messages || []).slice();
    if (!ticketFilter || !ticketFilter.num) return all;
    // Word-boundary-ish match: #N not followed by another digit
    const re = new RegExp(`#${ticketFilter.num}(?!\\d)`);
    return all.filter(m => re.test(m.body || ''));
  });
  const sortedMessages = $derived.by(() => {
    return filteredMessages
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

  // #695 — compact one-line rendering helpers.
  // timeHM: right-aligned muted HH:MM; full date on hover (title).
  function timeHM(ts) {
    if (!ts) return '';
    // #709.8 — fixed-width 24h (14:02), never locale 12h ("03:25 AM").
    return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
  }
  // Consecutive same-author messages within 3 min group under one chip.
  function isGrouped(msgs, i) {
    // #709.9 (review fix) — compute the row's POSITION within its
    // consecutive same-author run and chip at every 7th position
    // (0, 7, 14…). The first cut counted backwards without stopping at
    // re-chip boundaries, so past row 6 every row chipped (a 15-message
    // run rendered 9 chips). Position-modulo gives runs of 15 exactly
    // 3 anchors.
    let pos = 0;
    let j = i;
    while (j > 0 && isGroupedShallow(msgs, j)) { pos++; j--; }
    return pos > 0 && pos % 7 !== 0;
  }
  function isGroupedShallow(msgs, i) {
    if (i === 0) return false;
    const prev = msgs[i - 1], cur = msgs[i];
    return prev && prev.author === cur.author &&
      (new Date(cur.created_at) - new Date(prev.created_at)) < 180000;
  }
  // Body rendering: @-mention highlight + #-ticket auto-link.
  // Per-row team_github_url (backend #665/#662 contract) drives the repo
  // for #-ticket links; falls back to the chepherd repo for legacy rows
  // that don't carry the field.
  function repoForMsg(m) {
    if (m && m.team_github_url) {
      return m.team_github_url.replace(/\/+$/, '');
    }
    return 'https://github.com/agenity-org/agenity';
  }

  function renderBody(m) {
    let html = m.body || '';
    // escape minimal HTML
    html = html.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    // @mention
    // #695 — @mentions render in the mentioned member's identity color.
    html = html.replace(/@([a-zA-Z][a-zA-Z0-9_-]*)/g, (_mm, nm) =>
      `<span class="mention" style="color: ${agentIdentity(nm).color}">@${nm}</span>`);
    // #ticket — link to GitHub. Use data-ticket attrs so the click
    // handler can intercept and dispatch chepherd-ticket-focus (#665)
    // alongside the GitHub navigation.
    const repo = repoForMsg(m);
    html = html.replace(/#(\d+)\b/g, (_match, num) => {
      const url = `${repo}/issues/${num}`;
      return `<a class="ticket" href="${url}" target="_blank" rel="noopener" data-ticket-num="${num}" data-ticket-repo="${repo}">#${num} ↗</a>`;
    });
    return html;
  }

  // Intercept clicks inside the message body so #-ticket clicks ALSO emit
  // the chepherd-ticket-focus event (kanban listens, scrolls to card). We
  // don't preventDefault — the link still opens in a new tab — but the
  // event fires for the in-dashboard kanban link (#665).
  function onMessagesClick(e) {
    const a = e.target?.closest?.('a.ticket');
    if (!a) return;
    const num = parseInt(a.getAttribute('data-ticket-num') || '0', 10);
    const repo = a.getAttribute('data-ticket-repo') || '';
    if (!num) return;
    try {
      window.dispatchEvent(new CustomEvent('chepherd-ticket-focus', {
        detail: { repo, num },
      }));
    } catch {}
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
    // Also surface handles seen in recent message authors/recipients that
    // aren't current channel members — best-effort, no extra fetch.
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
    if (!teamName) return;
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

  const defaultTarget = $derived(leadTargets[selectedScope] || '');

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

  // #660 — live push via SSE. The per-team /stream endpoint ticks the
  // instant the transcript changes (operator post OR agent activity);
  // we refetch on tick (~50ms) instead of busy-polling every 5s. A slow
  // 15s safety poll remains to survive a dropped SSE.
  let evtSource = null;
  function subscribeStream() {
    if (evtSource) { evtSource.close(); evtSource = null; }
    let tok = '';
    try { tok = localStorage.getItem('chepherd-token') || ''; } catch {}
    const q = tok ? ('?token=' + encodeURIComponent(tok)) : '';
    try {
      evtSource = new EventSource(`${API}/teams/${encodeURIComponent(selectedScope)}/stream${q}`);
      evtSource.addEventListener('tick', () => refresh());
      // onerror: EventSource auto-reconnects; the safety poll bridges any gap.
    } catch { evtSource = null; }
  }

  onMount(() => {
    loadTeams();
    refresh();
    subscribeStream();
    const id = setInterval(refresh, 15000); // #660 — slow safety net, not the primary path

    // Listen for kanban → transcript filter requests (#665).
    const onFilter = (ev) => {
      const d = ev.detail || {};
      if (!d.num) return;
      ticketFilter = { repo: d.repo || '', num: d.num };
    };
    window.addEventListener('chepherd-transcript-filter', onFilter);

    return () => {
      clearInterval(id);
      if (evtSource) { evtSource.close(); evtSource = null; }
      window.removeEventListener('chepherd-transcript-filter', onFilter);
    };
  });

  // When the user changes scope via the dropdown, refresh immediately,
  // re-point the live stream at the new team, and fetch the lead-hint.
  $effect(() => {
    selectedScope;
    refresh();
    subscribeStream();
    ensureLead(selectedScope);
  });
</script>

<section class="transcript" data-testid="team-transcript">
  <header class="header">
    <div class="header-row">
      <label>Teams:
        <select bind:value={selectedScope} class="team-picker" data-testid="team-picker">
          {#each teams as t}
            <option value={t}>{t}</option>
          {/each}
          {#if !teams.includes(selectedScope)}
            <option value={selectedScope}>{selectedScope}</option>
          {/if}
        </select>
      </label>
      <div class="members">
        {#each (transcript.channel?.members || []).slice(0, 8) as mem}
          <span class="member-chip" style="color: {agentIdentity(mem).color}">{agentIdentity(mem).icon} {mem}</span>
        {/each}
        {#if (transcript.channel?.members || []).length > 8}
          <span class="member-chip muted">+{(transcript.channel?.members || []).length - 8}</span>
        {/if}
        {#if !(transcript.channel?.members || []).length}(none){/if}
      </div>
    </div>
  </header>

  {#if ticketFilter && ticketFilter.num}
    <div class="filter-chip" data-testid="transcript-filter-chip">
      filtering <strong>#{ticketFilter.num}</strong>
      <button class="x" onclick={() => ticketFilter = null} title="Clear filter">×</button>
    </div>
  {/if}

  <div class="messages" bind:this={messagesEl} onscroll={onScroll} onclick={onMessagesClick}>
    {#each groupedMessages as [day, msgs]}
      <div class="day-divider">── {day} ──</div>
      {#each msgs as m, mi (m.id)}
        <!-- #695 — one-line compact rows: [identity chip | inline content | HH:MM].
             Consecutive same-author messages (3-min window) suppress the chip;
             recipient appears inline (▸ name) ONLY when not a broadcast. -->
        <article class="msg compact {kindClass(m)}" class:grouped={isGrouped(msgs, mi)} data-testid="transcript-row" data-kind={m.kind || 'message'}>
          <span class="chip-col">
            <!-- #709.9 — chip always rendered; grouped rows reveal it on
                 hover so orphaned runs keep their author reachable. -->
            <span class="chip from" class:ghost={isGrouped(msgs, mi)} style="color: {agentIdentity(m.author).color}" title={'@' + m.author + ' · ' + new Date(m.created_at).toLocaleString()}><span aria-hidden="true">{agentIdentity(m.author).icon}</span> {m.author}</span>
          </span>
          <span class="content-col">
            <!-- #695 row was "<author> > <team> ▸ <recipient> <body>" — the
                 team is the current view and the recipient already lives in
                 the @mention inside the body, so the breadcrumb was pure
                 redundancy. Render "<author> <body>" only; keep alert kind
                 labels, multi-recipient badge, routed-sub, and links. -->
            {#if kindLabel(m)}
              <span class="kind-label kind-{(m.kind || '').replace('alert:', '')}">{kindLabel(m)}</span>
            {/if}
            {#if m.recipients && m.recipients.length > 1}
              <span class="badge multi">multi ({m.recipients.length})</span>
            {/if}
          {#if m.routed_to_default && m.default_target}
            <span class="routed-sub" data-testid="routed-sub">↪ <span class="mention" style="color: {agentIdentity(m.default_target).color}">@{m.default_target}</span> <span class="muted">(lead)</span></span>
          {/if}
          {#if isLong(m.body) && !expanded[m.id]}
            <span class="body">
              {@html renderBody({ body: preview(m.body), team_github_url: m.team_github_url })}
              <button class="expand" onclick={() => expanded = { ...expanded, [m.id]: true }}>▾ more</button>
            </span>
          {:else}
            <span class="body">{@html renderBody(m)}</span>
            {#if isLong(m.body) && expanded[m.id]}
              <button class="expand" onclick={() => expanded = { ...expanded, [m.id]: false }}>▴ less</button>
            {/if}
          {/if}
          </span>
          <span class="ts" title={new Date(m.created_at).toLocaleString()}>{timeHM(m.created_at)}</span>
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
        placeholder={defaultTarget ? `Message @${defaultTarget} (team lead) — @-mention to address someone else · Enter sends` : 'Type a message — @-mention an agent or @everyone · Enter sends'}
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
          <span class="muted">→ @{defaultTarget}</span>
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
  .filter-chip {
    display: flex; align-items: center; gap: 0.5rem;
    padding: 0.35rem 1rem; background: rgba(135,206,235,0.10);
    border-bottom: 1px solid var(--border, #2a2a2a);
    color: var(--accent-2, #87ceeb); font-size: 0.8rem;
  }
  .filter-chip .x { background: transparent; border: 0; color: inherit; font-size: 1.05rem; cursor: pointer; padding: 0 0.3rem; }
  .filter-chip .x:hover { color: #fff; }
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
  .chip {
    background: var(--bg-elevated, #1a1a1a); border: 1px solid var(--border, #333);
    padding: 0.1rem 0.45rem; border-radius: 999px; font-size: 0.78rem; font-family: ui-monospace, monospace;
  }
  .chip.from { color: var(--accent-2, #87ceeb); border-color: var(--accent-2, #87ceeb); }
  .arrow { color: var(--fg-muted, #666); }
  .ts { color: var(--fg-muted, #666); font-size: 0.75rem; margin-left: auto; }
  .badge.multi { background: rgba(135,206,235,0.18); color: var(--accent-2, #87ceeb); font-size: 0.7rem; padding: 0.05rem 0.4rem; border-radius: 3px; }
  .routed-sub { font-size: 0.75rem; color: var(--fg-muted, #888); margin: 0.1rem 0 0.25rem 0.1rem; }
  .routed-sub .muted { color: var(--fg-muted, #666); }
  .routed-sub .mention { color: var(--accent-2, #87ceeb); font-weight: 600; }
  .body { padding: 0.35rem 0.6rem 0.35rem 0.8rem; background: var(--bg-elevated, #131313); border-left: 2px solid var(--border, #2a2a2a); border-radius: 0 4px 4px 0; white-space: pre-wrap; word-break: break-word; font-size: 0.88rem; }
  .body :global(.mention) { color: var(--accent-2, #87ceeb); font-weight: 600; }
  .body :global(.ticket) { color: #ffb86c; text-decoration: none; font-weight: 600; cursor: pointer; }
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
  /* #695 — compact one-line transcript rows */
  .msg.compact { display: grid; grid-template-columns: minmax(72px, max-content) minmax(0, 1fr) max-content; width: 100%; box-sizing: border-box; gap: 0 0.5rem; align-items: baseline; padding: 0.1rem 0.2rem; margin-bottom: 0.15rem; }
  /* #709.9 — grouped chips ghost until hover */
  .msg.compact .chip.from.ghost { visibility: hidden; }
  .msg.compact:hover .chip.from.ghost { visibility: visible; opacity: 0.6; }
  /* #709.10 — narrow panes STACK instead of squeezing content to ~0 */
  .messages { container-type: inline-size; }
  @container (max-width: 360px) {
    .msg.compact { grid-template-columns: minmax(0, 1fr) max-content; }
    .msg.compact .chip-col { grid-column: 1 / -1; }
    .msg.compact .chip.from.ghost { display: none; }
  }
  .msg.compact.grouped { padding-top: 0; }
  .msg.compact .chip-col { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .msg.compact .chip.from { font-weight: 600; font-size: 0.82rem; }
  .msg.compact .content-col { min-width: 0; overflow-wrap: anywhere; font-size: 0.85rem; }
  .msg.compact .body { display: inline; }
  .msg.compact .ts { color: var(--fg-muted, #666); font-size: 0.72rem; white-space: nowrap; }
  .msg.compact .routed-sub { display: inline; font-size: 0.78rem; color: var(--fg-muted, #999); margin-right: 0.3rem; }
  .member-chip { font-size: 0.74rem; margin-right: 0.4rem; white-space: nowrap; }
  .member-chip.muted { color: var(--fg-muted, #888); }
</style>

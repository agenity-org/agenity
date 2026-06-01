<!--
  TeamTranscript — unified messaging pane (#659 epic #654).
  Shows ALL messages in the selected team (agent↔agent, h↔a, multi, broadcast)
  with sender/recipient chips, threads inline, compose at bottom.
-->
<script>
  import { onMount } from 'svelte';

  let { team = 'default' } = $props();

  let transcript = $state({ channel: null, messages: [] });
  let composeBody = $state('');
  let composeSending = $state(false);
  let composeError = $state('');
  let lastFetchError = $state('');

  const API = '/api/v1';

  async function refresh() {
    try {
      const r = await fetch(`${API}/teams/${encodeURIComponent(team)}/messages`);
      const text = await r.text();
      if (!r.ok) {
        let msg = `HTTP ${r.status}`;
        try { msg = JSON.parse(text).error || msg; } catch { msg = text.slice(0, 80); }
        lastFetchError = msg;
        return;
      }
      transcript = JSON.parse(text);
      lastFetchError = '';
    } catch (e) {
      lastFetchError = e?.message || 'fetch failed';
    }
  }

  async function send() {
    const body = composeBody.trim();
    if (!body) return;
    composeError = '';
    composeSending = true;
    try {
      const r = await fetch(`${API}/teams/${encodeURIComponent(team)}/messages`, {
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
  const previewTokens = $derived(previewWillWake * 400); // rough estimate
  const previewCost = $derived((previewTokens * 0.000015).toFixed(4));

  // Group messages by day for the divider headers
  const groupedMessages = $derived.by(() => {
    const groups = {};
    for (const m of (transcript.messages || [])) {
      const day = new Date(m.created_at).toLocaleDateString();
      groups[day] = groups[day] || [];
      groups[day].push(m);
    }
    return Object.entries(groups);
  });

  function relTime(ts) {
    if (!ts) return '';
    const s = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s/60)}m`;
    if (s < 86400) return `${Math.floor(s/3600)}h`;
    return `${Math.floor(s/86400)}d`;
  }

  function highlightMentions(body) {
    return body.replace(/@([a-zA-Z][a-zA-Z0-9_-]*)/g, '<span class="mention">@$1</span>');
  }

  function recipientLabel(m) {
    if (!m.recipients || m.recipients.length === 0) return '(no recipients)';
    if (m.recipients.includes('everyone')) return '@everyone';
    if (m.recipients.length === 1) return '@' + m.recipients[0];
    return m.recipients.map(r => '@' + r).join(', ') + ` (${m.recipients.length})`;
  }

  onMount(() => {
    refresh();
    const id = setInterval(refresh, 5000);
    return () => clearInterval(id);
  });

  function handleKeydown(e) {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      send();
    }
  }
</script>

<section class="transcript" data-testid="team-transcript">
  <header class="header">
    <h2>Team: <span class="team-name">{team}</span></h2>
    <div class="members">
      members: {(transcript.channel?.members || []).map(m => '@' + m).join(' ') || '(none)'}
    </div>
  </header>

  <div class="messages">
    {#each groupedMessages as [day, msgs]}
      <div class="day-divider">── {day} ──</div>
      {#each msgs.slice().reverse() as m (m.id)}
        <article class="msg">
          <div class="row1">
            <span class="chip from">@{m.author}</span>
            <span class="arrow">→</span>
            <span class="chip to" title={(m.recipients || []).join(', ')}>{recipientLabel(m)}</span>
            <span class="ts">{relTime(m.created_at)}</span>
            {#if m.recipients && m.recipients.length > 1}
              <span class="badge multi">multi ({m.recipients.length})</span>
            {/if}
          </div>
          <div class="body">{@html highlightMentions(m.body)}</div>
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
      placeholder="Type a message — @-mention to ping specific agents (e.g., @tech-lead) or @everyone for broadcast. Cmd/Ctrl+Enter to send."
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
  .header { padding: 0.8rem 1rem; border-bottom: 1px solid var(--border, #2a2a2a); }
  .header h2 { margin: 0; font-size: 1rem; }
  .team-name { color: var(--accent-2, #87ceeb); }
  .members { color: var(--fg-muted, #888); font-size: 0.78rem; margin-top: 0.3rem; }
  .messages { flex: 1; overflow-y: auto; padding: 0.8rem 1rem; }
  .day-divider { color: var(--fg-muted, #666); font-size: 0.75rem; text-align: center; margin: 0.8rem 0 0.5rem; }
  .msg { margin-bottom: 0.85rem; }
  .row1 { display: flex; align-items: center; gap: 0.4rem; flex-wrap: wrap; margin-bottom: 0.25rem; }
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

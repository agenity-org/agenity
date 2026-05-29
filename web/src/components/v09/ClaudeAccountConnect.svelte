<!--
  ClaudeAccountConnect — seamless OAuth capture flow for an Anthropic
  Claude account. Ported from the v0.8 SpawnWizard (#136 R5 redo) when
  the v0.9 wizard split into Stage1-4 components.

  5-step dance (all 4 endpoints already exist on the backend):
    1. POST /claude-tokens/login-begin       → spawn ephemeral agent
    2. GET  /claude-tokens/login-url/{name}  → poll for OAuth URL once
                                              claude-code prints it
    3. operator clicks URL in their browser, authorises on claude.ai,
       gets redirected to a page showing the authorisation code
    4. operator pastes that code → POST /claude-tokens/login-submit/{name}
    5. server injects code into agent PTY, harvests credentials.json,
       upserts vault, terminates ephemeral agent, returns the new
       token's vault id.

  Same conversational shape as the git-PAT inline flow: button → status
  → URL → code paste → done. No browser alert(); inline error banner.

  Props:
    oncomplete(tokenID): fires once the new vault entry is saved; the
                        caller can preselect it.
    oncancel():          fires when the operator cancels mid-flow.
-->
<script>
  // autostart=true: begin the OAuth flow on mount so the parent's
  // "+ Connect Claude account" click goes straight to "Spawning
  // capture agent…" — no double-button.
  let { oncomplete, oncancel, autostart = false } = $props();

  let oauthMode = $state(false);
  let oauthAgentName = $state('');
  let oauthURL = $state('');
  let oauthCode = $state('');
  let oauthLabel = $state('');
  let oauthBusy = $state(false);
  let oauthError = $state('');
  let oauthStatus = $state('');
  let oauthPollHandle = null;

  const API = '/api-v08/v1';

  $effect(() => {
    if (autostart) beginOAuthLogin();
  });

  async function beginOAuthLogin() {
    oauthBusy = true; oauthError = ''; oauthURL = ''; oauthCode = '';
    oauthStatus = 'Spawning capture agent…';
    try {
      const r = await fetch(`${API}/claude-tokens/login-begin`, { method: 'POST' });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      const d = await r.json();
      oauthAgentName = d.name;
      oauthMode = true;
      oauthStatus = 'Waiting for Claude to print the login URL…';
      pollForOAuthURL();
    } catch (e) {
      oauthError = e.message || String(e);
      oauthBusy = false;
    }
  }

  function pollForOAuthURL() {
    let attempts = 0;
    const tick = async () => {
      if (!oauthMode || !oauthAgentName) return;
      attempts++;
      try {
        const r = await fetch(`${API}/claude-tokens/login-url/${oauthAgentName}`);
        if (r.status === 200) {
          const d = await r.json();
          oauthURL = d.url;
          oauthStatus = 'Click the link below to authorise, then paste the code that Claude shows you.';
          oauthBusy = false;
          return;
        }
      } catch {}
      if (attempts > 60) {
        oauthError = 'Capture agent never printed a Claude login URL. Cancel and retry.';
        oauthBusy = false;
        return;
      }
      oauthPollHandle = setTimeout(tick, 1000);
    };
    tick();
  }

  async function submitOAuthCode() {
    if (!oauthCode.trim()) {
      oauthError = 'Paste the code Claude showed you after authorising.';
      return;
    }
    oauthBusy = true; oauthError = '';
    oauthStatus = 'Submitting code to capture agent…';
    try {
      const r = await fetch(`${API}/claude-tokens/login-submit/${oauthAgentName}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code: oauthCode.trim(), label: oauthLabel.trim() }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      const d = await r.json();
      oncomplete?.(d.id);
      resetState();
    } catch (e) {
      oauthError = e.message || String(e);
      oauthBusy = false;
    }
  }

  async function cancelOAuthLogin() {
    if (oauthPollHandle) { clearTimeout(oauthPollHandle); oauthPollHandle = null; }
    if (oauthAgentName) {
      try { await fetch(`${API}/claude-tokens/login-cancel/${oauthAgentName}`, { method: 'POST' }); } catch {}
    }
    resetState();
    oncancel?.();
  }

  function resetState() {
    oauthMode = false;
    oauthAgentName = '';
    oauthURL = '';
    oauthCode = '';
    oauthLabel = '';
    oauthBusy = false;
    oauthError = '';
    oauthStatus = '';
    if (oauthPollHandle) { clearTimeout(oauthPollHandle); oauthPollHandle = null; }
  }
</script>

<div class="claude-connect">
  {#if !oauthMode && !autostart}
    <header>
      <h4>Connect a Claude account</h4>
    </header>
    <p class="helper">
      chepherd spawns a temporary capture agent that asks Claude for an OAuth URL.
      You authorise it in your browser, paste back the code Claude shows you, and
      chepherd stores the refresh-token in its vault. No tokens cross your clipboard manually.
    </p>
    {#if oauthError}
      <p class="connect-error" role="alert">⚠ {oauthError}</p>
    {/if}
    <button class="primary" onclick={beginOAuthLogin} disabled={oauthBusy}>
      {oauthBusy ? 'Spawning…' : '+ Connect via Anthropic OAuth'}
    </button>
  {:else if !oauthMode && oauthError}
    <!-- autostart errored before reaching the URL — show retry. -->
    <header>
      <h4>Connect a Claude account</h4>
    </header>
    <p class="connect-error" role="alert">⚠ {oauthError}</p>
    <button class="primary" onclick={beginOAuthLogin} disabled={oauthBusy}>
      {oauthBusy ? 'Spawning…' : 'Try again'}
    </button>
  {:else}
    <header>
      <h4>Connecting Claude account…</h4>
      <button type="button" class="link" onclick={cancelOAuthLogin}>Cancel</button>
    </header>
    {#if oauthStatus}
      <p class="status">{oauthStatus}</p>
    {/if}
    {#if oauthURL}
      <div class="url-block">
        <span class="label">Step 1 — Authorise:</span>
        <a href={oauthURL} target="_blank" rel="noopener">{oauthURL} ↗</a>
      </div>
      <label class="onprem-row">
        <span>Step 2 — Paste the code Claude showed you</span>
        <input type="text" bind:value={oauthCode} placeholder="e.g. abc123#xyz789" autocomplete="off" />
      </label>
      <label class="onprem-row">
        <span>Label <em class="optional">(optional)</em></span>
        <input type="text" bind:value={oauthLabel} placeholder="e.g. work / personal" />
      </label>
    {/if}
    {#if oauthError}
      <p class="connect-error" role="alert">⚠ {oauthError}</p>
    {/if}
    <div class="actions">
      {#if oauthURL}
        <button class="primary" onclick={submitOAuthCode} disabled={oauthBusy || !oauthCode.trim()}>
          {oauthBusy ? 'Submitting…' : 'Save + Connect'}
        </button>
      {/if}
    </div>
  {/if}
</div>

<style>
  .claude-connect {
    background: var(--bg-elevated, #1a1a1a);
    border: 1px solid var(--border, #2a2a2a);
    border-radius: 6px;
    padding: 0.8rem 1rem;
    margin: 0.6rem 0;
    display: flex; flex-direction: column; gap: 0.6rem;
  }
  header { display: flex; align-items: center; gap: 0.6rem; }
  h4 { margin: 0; flex: 1; font-size: 0.9rem; color: var(--fg, #f5f5f5); }
  .helper { margin: 0; font-size: 0.82rem; color: var(--fg-muted, #aaa); line-height: 1.5; }
  .status { margin: 0; font-size: 0.85rem; color: var(--accent-2, #87ceeb); font-style: italic; }
  .url-block {
    background: var(--bg, #0a0a0a); border: 1px solid var(--border, #2a2a2a);
    border-radius: 4px; padding: 0.5rem 0.7rem;
    display: flex; flex-direction: column; gap: 0.25rem;
  }
  .url-block .label { font-size: 0.76rem; color: var(--fg-muted, #aaa); }
  .url-block a {
    color: var(--accent-2, #87ceeb); text-decoration: none; word-break: break-all;
    font-size: 0.84rem;
  }
  .url-block a:hover { text-decoration: underline; }
  .onprem-row { display: flex; flex-direction: column; gap: 0.2rem; font-size: 0.78rem; color: var(--fg-muted, #888); }
  .onprem-row input { padding: 0.4rem 0.55rem; border-radius: 4px; border: 1px solid var(--border, #2a2a2a); background: var(--bg, #0a0a0a); color: var(--fg, #f5f5f5); font: inherit; }
  .optional { color: var(--fg-muted, #888); font-style: italic; font-size: 0.72rem; font-weight: 400; }
  .primary {
    background: var(--accent-2, #87ceeb); border: 0; color: #0a0a0a;
    padding: 0.45rem 0.95rem; border-radius: 4px; cursor: pointer; font-weight: 600; font: inherit;
  }
  .primary:disabled { opacity: 0.4; cursor: not-allowed; }
  .link {
    background: transparent; border: 0; color: var(--accent-2, #87ceeb);
    cursor: pointer; font: inherit; font-size: 0.78rem; padding: 0; text-decoration: underline;
  }
  .link:hover { color: var(--fg, #fff); }
  .connect-error {
    margin: 0; padding: 0.45rem 0.7rem;
    background: rgba(231,76,60,0.10); border: 1px solid rgba(231,76,60,0.35);
    border-radius: 4px; color: #e74c3c; font-size: 0.82rem;
  }
  .actions { display: flex; gap: 0.4rem; justify-content: flex-end; }
</style>

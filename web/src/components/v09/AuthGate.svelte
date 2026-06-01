<!--
  AuthGate (#239) — top-level auth gate for the chepherd dashboard.

  Wraps the Workspace shell so the chepherd login prompt fires at FIRST
  PAGE LOAD instead of mid-wizard. Operator-reported P0: prior shape
  let the dashboard render with stale-or-empty data + popped a login
  modal only after first write-API 401. That meant operator could walk
  the whole spawn wizard, click Launch, THEN hit auth → lost work +
  trust erosion.

  Behavior:
    1. On mount, check localStorage for chepherd-token
    2. If missing: render the login screen — no workspace mount, no
       silent /api fetches
    3. If present: probe /api/v1/sessions with the token
       - 200 → render the Workspace (children prop)
       - 401 → clear stale token, render login screen
    4. Listen for chepherd-logout event from Workspace's topbar logout
       button → clear token + re-render login screen
    5. Listen for chepherd-401 event from the global fetch wrapper
       (#157) → if it fires AFTER initial auth (mid-session token
       expiry), re-prompt login. Workspace's own modal fallback still
       handles in-place re-auth without losing wizard state.

  Props:
    version — passed through to <Workspace />; AuthGate is otherwise
              transparent to the child.

  Refs #157 #239.
-->
<script>
  import { onMount } from 'svelte';
  import Workspace from '../v08/Workspace.svelte';

  let { version = 'v0.8' } = $props();

  // #157 + #239 — install the fetch-auth wrapper at AuthGate's
  // module top, BEFORE the initial auth probe runs. Workspace.svelte
  // installs the same wrapper but it only runs when Workspace mounts;
  // AuthGate now mounts FIRST so without this duplicate install the
  // probe fetch goes out without the Authorization header.
  // window.__chepherdFetchPatched makes this idempotent across both
  // call sites; the function body is identical to Workspace's so
  // they're interchangeable.
  if (typeof window !== 'undefined' && !window.__chepherdFetchPatched) {
    window.__chepherdFetchPatched = true;
    const _origFetch = window.fetch.bind(window);
    window.fetch = (input, init) => {
      const url = typeof input === 'string' ? input : (input?.url || '');
      if (url.startsWith('/api/')) {
        let tok = '';
        try { tok = localStorage.getItem('chepherd-token') || ''; } catch {}
        init = init || {};
        init.headers = new Headers(init.headers || (typeof input !== 'string' ? input.headers : undefined));
        if (tok && !init.headers.has('Authorization')) {
          init.headers.set('Authorization', 'Bearer ' + tok);
        }
        return _origFetch(input, init).then(r => {
          if (r.status === 401) {
            try { window.dispatchEvent(new CustomEvent('chepherd-401')); } catch {}
          }
          return r;
        });
      }
      return _origFetch(input, init);
    };
  }

  // 'checking'    — initial mount probe in flight
  // 'login'       — render the login screen
  // 'ok'          — render the workspace
  let authStatus = $state('checking');
  let tokenInput = $state('');
  let loginError = $state('');
  let probing = $state(false);

  function readStoredToken() {
    if (typeof localStorage === 'undefined') return '';
    try { return localStorage.getItem('chepherd-token') || ''; } catch { return ''; }
  }
  function clearStoredToken() {
    try { localStorage.removeItem('chepherd-token'); } catch {}
  }
  function storeToken(t) {
    try { localStorage.setItem('chepherd-token', t); } catch {}
  }

  // Probe a gated endpoint to validate the token. /api/v1/sessions is
  // light (just returns the session list) + always returns 200 with
  // valid auth (even on empty state). The fetch-auth wrapper from
  // Workspace's module-top installs when Workspace is imported above,
  // so the Authorization header is auto-attached.
  async function probeAuth(tok) {
    if (!tok) return false;
    try {
      const r = await fetch('/api/v1/sessions');
      return r.status === 200;
    } catch {
      return false;
    }
  }

  async function attemptLogin() {
    const t = tokenInput.trim();
    if (!t) { loginError = 'paste the bootstrap token'; return; }
    probing = true; loginError = '';
    storeToken(t);
    const ok = await probeAuth(t);
    probing = false;
    if (ok) {
      authStatus = 'ok';
      tokenInput = '';
    } else {
      clearStoredToken();
      loginError = 'token rejected — paste a fresh one';
    }
  }

  function logout() {
    clearStoredToken();
    authStatus = 'login';
  }

  onMount(async () => {
    // #566 — propagate ?token= URL param to localStorage so the auth
    // probe below finds it via readStoredToken(). Strip from URL after.
    try {
      const urlTok = new URL(location.href).searchParams.get('token');
      if (urlTok) {
        storeToken(urlTok);
        const clean = new URL(location.href);
        clean.searchParams.delete('token');
        history.replaceState(null, '', clean.toString());
      }
    } catch {}

    const tok = readStoredToken();
    if (!tok) { authStatus = 'login'; return; }
    const ok = await probeAuth(tok);
    if (ok) {
      authStatus = 'ok';
    } else {
      clearStoredToken();
      authStatus = 'login';
    }
    // After mount, the global 'chepherd-401' event (dispatched by the
    // module-top fetch wrapper installed by Workspace) signals a
    // mid-session token rejection — could be expiry, server restart
    // with new auth.secret, etc. Bring up the login screen + clear
    // workspace.
    window.addEventListener('chepherd-401', () => {
      clearStoredToken();
      authStatus = 'login';
    });
    // Logout button in Workspace's topbar dispatches this.
    window.addEventListener('chepherd-logout', logout);
  });
</script>

{#if authStatus === 'checking'}
  <div class="loading-page">
    <p class="loading-msg">Loading workspace…</p>
  </div>
{:else if authStatus === 'login'}
  <div class="login-page" role="dialog" aria-labelledby="login-heading">
    <div class="login-card">
      <h2 id="login-heading">🔑 chepherd login</h2>
      <p class="prose">Paste the bootstrap token chepherd printed at startup.</p>
      <p class="prose tiny">Find it in chepherd's stdout (right after the “Bootstrap token (operator, 30d):” banner), or read it from <code>$STATE_DIR/auth.printed</code> where <code>$STATE_DIR</code> is the path you passed to <code>--state-dir</code> at boot.</p>
      <textarea
        bind:value={tokenInput}
        placeholder="eyJhbGc…"
        rows="4"
        aria-label="chepherd bootstrap token"
        autocomplete="off"
        spellcheck="false"
      ></textarea>
      {#if loginError}
        <div class="error" role="alert">{loginError}</div>
      {/if}
      <button class="primary" onclick={attemptLogin} disabled={probing}>
        {probing ? 'Verifying…' : 'Sign in'}
      </button>
    </div>
  </div>
{:else}
  <Workspace {version} />
{/if}

<style>
  .loading-page {
    position: fixed; inset: 0; display: flex; align-items: center; justify-content: center;
    background: var(--bg, #0a0a0a); color: var(--fg-muted, #888);
  }
  .loading-msg { font-size: 0.95rem; }

  /* Full-page login screen — replaces the prior in-Workspace modal
     overlay so the workspace doesn't render at all until auth resolves. */
  .login-page {
    position: fixed; inset: 0;
    display: flex; align-items: center; justify-content: center;
    background: var(--bg, #0a0a0a);
    padding: 1rem;
  }
  .login-card {
    background: var(--bg-elevated, #1a1a1a);
    border: 1px solid var(--border, #2a2a2a);
    border-radius: 8px;
    padding: 1.6rem 1.8rem;
    width: 100%; max-width: 30rem;
    display: flex; flex-direction: column; gap: 0.75rem;
    color: var(--fg, #f5f5f5);
  }
  h2 { font-size: 1.1rem; margin: 0; }
  .prose { color: var(--fg-muted, #aaa); margin: 0; font-size: 0.88rem; line-height: 1.5; }
  .prose.tiny { font-size: 0.78rem; }
  code {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    background: var(--bg, #0a0a0a); padding: 0.1rem 0.3rem; border-radius: 3px;
    font-size: 0.78rem; word-break: break-all;
  }
  textarea {
    width: 100%; box-sizing: border-box;
    padding: 0.5rem 0.6rem;
    background: var(--bg, #0a0a0a); color: var(--fg, #f5f5f5);
    border: 1px solid var(--border, #2a2a2a); border-radius: 4px;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.84rem; line-height: 1.4; resize: vertical;
  }
  .error {
    color: var(--danger, #e74c3c);
    font-size: 0.85rem;
    background: rgba(231, 76, 60, 0.08);
    padding: 0.4rem 0.6rem;
    border-left: 3px solid var(--danger, #e74c3c);
    border-radius: 2px;
  }
  .primary {
    background: var(--accent-2, #87ceeb);
    color: var(--bg, #0a0a0a);
    border: none; border-radius: 4px;
    padding: 0.55rem 1rem;
    font-weight: 600; font-size: 0.92rem;
    cursor: pointer;
  }
  .primary:disabled { opacity: 0.6; cursor: progress; }
  .primary:hover:not(:disabled) { filter: brightness(1.1); }
</style>

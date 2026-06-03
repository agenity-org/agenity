<!--
  WidgetTerminal — xterm.js viewer for an agent's PTY. Each pane carries
  its own per-pane agent selection (so two terminal panes side-by-side
  can show two different agents). Defaults to the workspace-wide
  selectedAgent so single-terminal use still works as before.

  Font tracks the workspace --ws-font CSS var: when operator hits A+/A-
  in the top bar, the terminal re-applies fontSize, re-fits the addon,
  and pushes a resize frame over the WebSocket so the underlying PTY
  re-wraps (Claude / shell honor the SIGWINCH-equivalent).
-->
<script>
  import { onMount, onDestroy } from 'svelte';
  let { selectedAgent, sessions, node } = $props();

  // Per-pane override: if node.config.agent is set, that wins; otherwise
  // fall back to the workspace-wide selectedAgent. If neither is set,
  // auto-pick the first non-shepherd live agent so the operator never
  // sees an empty "(no agent)" state at first open.
  let myAgent = $derived.by(() => {
    if (node?.config?.agent) return node.config.agent;
    if (selectedAgent) return selectedAgent;
    const candidate = (sessions || []).find(s => !s.exited && s.role !== 'shepherd')
                   || (sessions || []).find(s => !s.exited)
                   || null;
    if (candidate && node) {
      if (!node.config) node.config = {};
      node.config.agent = candidate.name;
      return candidate.name;
    }
    return '';
  });

  let term = null;
  let ws = null;
  let fitAddon = null;
  let resizeObs = null;
  let termContainer;
  let attached = null;
  let fontObs = null;
  let themeObs = null;
  let lastThemeSig = '';
  // Legacy OAuth banner removed (#136 R5 redo) — the spawn wizard's
  // dedicated stage 4 owns Claude login now. Showing a banner in the
  // terminal pane was confusing operators when auto-dismiss had already
  // taken care of the OAuth flow.

  function currentWsFont() {
    try {
      const v = getComputedStyle(document.documentElement).getPropertyValue('--ws-font').trim();
      const n = parseFloat(v);
      return n > 0 ? n : 13;
    } catch { return 13; }
  }

  function applyFontAndRefit() {
    if (!term || !fitAddon) return;
    const f = currentWsFont();
    if (term.options.fontSize !== f) {
      term.options.fontSize = f;
    }
    try { fitAddon.fit(); } catch {}
    sendResize();
  }

  function sendResize() {
    if (!ws || ws.readyState !== WebSocket.OPEN || !term) return;
    try { ws.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols })); } catch {}
  }

  async function attachTo(name) {
    if (attached === name) return;
    attached = name;
    if (ws) { ws.close(); ws = null; }
    if (resizeObs) { resizeObs.disconnect(); resizeObs = null; }
    if (term) {
      // #245 reopen v2 — remove the document-level escape-hatch listener
      // we attached in this term instance's setup. Otherwise it'd leak
      // across agent switches + duplicate-log on every keystroke.
      if (term._chepherdDocKeyHandler) {
        try { document.removeEventListener('keydown', term._chepherdDocKeyHandler, true); } catch {}
      }
      term.dispose();
      term = null;
    }
    if (!name) return;

    const { Terminal } = await import('@xterm/xterm');
    const { FitAddon } = await import('@xterm/addon-fit');
    const { WebLinksAddon } = await import('@xterm/addon-web-links');
    // #133 / 2026-05-27 operator review #2: "colors are still burnt".
    // The surgical 14-slot tuning (c1cc9ce) STILL shifted hue/saturation
    // away from what claude-code's TUI was designed for. Final answer:
    // DO NOT override any of the 16 ANSI slots. Set ONLY background /
    // foreground / cursor / selection — every other color falls back to
    // xterm.js's stock palette, which is the same one claude-code is
    // built against in iTerm/Alacritty/xterm/etc. The result is the
    // "original colors" the operator sees in a native terminal.
    const isLight = typeof document !== 'undefined' && document.documentElement.dataset.theme === 'light';
    const sel = isLight ? {
      background:          '#fafafa',
      foreground:          '#1a1a1a',
      cursor:              '#1a1a1a',
      cursorAccent:        '#fafafa',
      selectionBackground: '#cbd5e1',
    } : {
      background:          '#0a0a0a',
      foreground:          '#f5f5f5',
      cursor:              '#f5f5f5',
      cursorAccent:        '#0a0a0a',
      selectionBackground: '#2a3540',
    };
    // Mirror v0.7's known-good Terminal config exactly — operator
    // confirmed v0.6/v0.7/early-v0.8 rendered colors correctly with
    // these settings. Earlier "fixes" (convertEol:false, DejaVu font
    // stack, lineHeight/letterSpacing) drifted away from the working
    // baseline. Restored verbatim.
    term = new Terminal({ convertEol: true, fontFamily: 'ui-monospace, monospace', fontSize: currentWsFont(), theme: sel, cursorBlink: true, cols: 120, rows: 32 });
    fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(new WebLinksAddon());
    term.open(termContainer);
    const tryFit = () => {
      const r = termContainer?.getBoundingClientRect();
      if (!r || r.width < 10 || r.height < 10) return;
      try { fitAddon.fit(); } catch {}
    };
    tryFit(); requestAnimationFrame(tryFit); setTimeout(tryFit, 100); setTimeout(tryFit, 400);
    resizeObs = new ResizeObserver(tryFit);
    resizeObs.observe(termContainer);

    // #357 / #677: refuse to open the WS unless the session is live AND
    // present in the runtime list. The original guard only caught
    // live===false; a session that has dropped OUT of the list entirely
    // (a stale saved-layout pane pointing at a gone agent, e.g.
    // "code-reviewer") left `sess` undefined → the guard passed → an
    // infinite 5s 404-reconnect loop. Reset `attached` so a later
    // sessions update (Resume / spawn) re-fires attachTo and dials cleanly.
    const sess = (sessions || []).find(s => s.name === name);
    if (!sess || sess.live === false) {
      attached = null;
      return;
    }

    // #151 — auto-reconnect on transient WebSocket close. Exponential
    // backoff capped at 5s, gives up after 8 attempts (~30s).
    let wsReconnectAttempts = 0;
    let everOpened = false;
    const openWS = () => {
      let wsTok = '';
      try { wsTok = localStorage.getItem('chepherd-token') || ''; } catch {}
      const wsQ = wsTok ? ('?token=' + encodeURIComponent(wsTok)) : '';
      ws = new WebSocket(`${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/api-v08/v1/sessions/${name}/attach${wsQ}`);
      ws.binaryType = 'arraybuffer';
      ws.onmessage = (ev) => {
        if (!term) return;
        if (ev.data instanceof ArrayBuffer) {
          term.write(new Uint8Array(ev.data));
        } else {
          term.write(ev.data);
        }
      };
      ws.addEventListener('open', () => {
        everOpened = true;
        wsReconnectAttempts = 0;
        setTimeout(sendResize, 200);
      });
      ws.addEventListener('close', () => {
        if (attached !== name) return; // user switched agent — don't reconnect
        // #677 — a close WITHOUT the socket ever opening is a handshake
        // failure (e.g. HTTP 404 — the session no longer exists), which is
        // PERMANENT: never retry it (that was the 404 reconnect loop). Only
        // auto-reconnect a connection that had successfully opened (a real
        // transient drop) AND only while the session is still live-listed.
        if (!everOpened) {
          if (term) { try { term.write('\r\n\x1b[90m[session unavailable — it is no longer running]\x1b[0m\r\n'); } catch {} }
          return;
        }
        const stillLive = (sessions || []).some(s => s.name === name && s.live !== false);
        if (!stillLive) return;
        wsReconnectAttempts++;
        if (wsReconnectAttempts > 8) return;
        const delay = Math.min(5000, 250 * 2 ** wsReconnectAttempts);
        setTimeout(() => { if (attached === name) openWS(); }, delay);
      });
    };
    openWS();
    term.onResize(sendResize);
    term.onData((d) => { if (ws && ws.readyState === WebSocket.OPEN) ws.send(d); });
    // #142 — opt out of clipboard side-effects. Selecting text in the
    // terminal no longer silently writes to the host clipboard; user
    // must explicitly Ctrl-C/Cmd-C through the browser's native path.
    // Likewise OSC 52 ("application can write the clipboard") is now
    // ignored — claude-code or a malicious tool can no longer hijack
    // the operator's clipboard via an escape sequence.
    term.parser.registerOscHandler(52, () => true);

    // #245 — copy / paste keyboard bindings. xterm.js forwards every
    // keystroke to onData by default; Ctrl+C arrives at the PTY as
    // SIGINT regardless of selection state. Operator UX expects a
    // smart-Ctrl+C (Windows-Terminal / VSCode pattern):
    //
    //   Ctrl+C        copy-if-selection-else-SIGINT (architect Option 2)
    //   Ctrl+Shift+C  ALWAYS copy
    //   Ctrl+Insert   ALWAYS copy (legacy Linux shortcut)
    //   Ctrl+V        paste from clipboard
    //   Ctrl+Shift+V  paste from clipboard (terminal-emulator convention)
    //   Shift+Insert  paste from clipboard (legacy Linux shortcut)
    //
    // attachCustomKeyEventHandler returns false to swallow the event
    // (do NOT forward to PTY); returns true to let xterm.js handle
    // normally (so onData fires + bytes hit the PTY).
    term.attachCustomKeyEventHandler((ev) => {
      // #245 reopen v2 — VERBOSE handler-entry diagnostic.
      // Architect rejected walker's PASS as synthetic-event theater +
      // operator confirmed real Ctrl+C still doesn't copy. This logs
      // EVERY keydown that reaches the xterm handler so the operator's
      // next probe shows whether their Ctrl+C arrives at all (V3
      // event-order diagnosis) BEFORE we get to the copy decision.
      if (ev.type === 'keydown' && (ev.ctrlKey || ev.metaKey || ev.key === 'Insert')) {
        const sel = term.getSelection();
        console.log('[chepherd-term-handler] keydown key=' + ev.key + ' ctrl=' + ev.ctrlKey + ' meta=' + ev.metaKey + ' shift=' + ev.shiftKey + ' selectionLen=' + (sel ? sel.length : 0) + ' targetTag=' + (ev.target?.tagName || 'unknown'));
      }
      // Only intercept on KEY DOWN — keyup events shouldn't both copy
      // AND fire SIGINT.
      if (ev.type !== 'keydown') return true;
      const k = ev.key;
      const ctrl = ev.ctrlKey || ev.metaKey;
      const shift = ev.shiftKey;
      const sel = term.getSelection();
      const hasSel = !!sel && sel.length > 0;

      // Ctrl+Shift+C OR Ctrl+Insert → ALWAYS copy when there's a selection.
      // No selection = nothing to copy, fall through so the keystroke
      // reaches the PTY (harmless; no shell binding listens to these).
      if (ctrl && ((shift && (k === 'C' || k === 'c')) || k === 'Insert')) {
        console.log('[chepherd-term-handler] matched Ctrl+Shift+C / Ctrl+Insert — hasSel=' + hasSel);
        if (hasSel) { copySelectionToClipboard(sel); }
        return false;
      }
      // Ctrl+C — copy-if-selection-else-SIGINT (Option 2).
      if (ctrl && !shift && (k === 'c' || k === 'C')) {
        console.log('[chepherd-term-handler] matched Ctrl+C — hasSel=' + hasSel);
        if (hasSel) {
          copySelectionToClipboard(sel);
          // Clear selection so a subsequent Ctrl+C lands as SIGINT.
          try { term.clearSelection(); } catch {}
          return false;
        }
        // No selection — let xterm forward Ctrl+C to PTY as SIGINT.
        return true;
      }
      // Ctrl+V OR Ctrl+Shift+V OR Shift+Insert → paste.
      if ((ctrl && (k === 'v' || k === 'V')) || (shift && k === 'Insert')) {
        console.log('[chepherd-term-handler] matched Ctrl+V / Ctrl+Shift+V / Shift+Insert');
        pasteFromClipboard();
        return false;
      }
      return true;
    });

    // #245 reopen v2 — document-level KEYBOARD ESCAPE HATCH. If a
    // keystroke reaches `document` with `ev.target` NOT inside this
    // terminal pane but xterm STILL has a selection, the operator's
    // real-keyboard Ctrl+C doesn't fire the handler above (focus is
    // outside xterm). Catch it at document level and route to our
    // copy path when xterm has a non-empty selection AND target isn't
    // an input/textarea (where the operator is typing for real).
    // Diagnostic-first: log every Ctrl+C at document level so the
    // operator's probe shows whether the event chain even reaches
    // here when xterm's handler doesn't fire.
    const docKeyHandler = (ev) => {
      if (ev.type !== 'keydown') return;
      if (!ev.ctrlKey && !ev.metaKey) return;
      if (ev.key !== 'c' && ev.key !== 'C' && ev.key !== 'v' && ev.key !== 'V' && ev.key !== 'Insert') return;
      const tag = ev.target?.tagName || 'unknown';
      const xtermSel = term ? term.getSelection() : '';
      const xtermHasSel = !!xtermSel && xtermSel.length > 0;
      const containedInTerminal = termContainer?.contains(ev.target);
      console.log('[chepherd-term-doc] keydown key=' + ev.key + ' ctrl=' + ev.ctrlKey + ' shift=' + ev.shiftKey + ' targetTag=' + tag + ' inTerminal=' + containedInTerminal + ' xtermSelLen=' + xtermSel.length);
      // Escape hatch: xterm has selection but focus is elsewhere AND
      // target isn't an input/textarea — operator probably wants the
      // xterm selection copied (clicked away after mouse-selecting).
      if ((ev.key === 'c' || ev.key === 'C') && !ev.shiftKey && xtermHasSel && !containedInTerminal &&
          tag !== 'INPUT' && tag !== 'TEXTAREA' && !ev.target?.isContentEditable) {
        console.log('[chepherd-term-doc] ESCAPE-HATCH: copying xterm selection from document-level handler');
        copySelectionToClipboard(xtermSel);
        ev.preventDefault();
      }
    };
    document.addEventListener('keydown', docKeyHandler, true);
    // Stash the cleanup on the term instance so attachTo()'s next-call
    // teardown can remove it (`if (term) { term.dispose(); term = null }`
    // doesn't auto-clean document listeners).
    term._chepherdDocKeyHandler = docKeyHandler;
  }

  // #245 reopen — operator reports copy still doesn't work despite
  // PR #249 shipping the smart-Ctrl+C wiring. Architect-spec'd RCA
  // candidates: V1 selection-empty / V2 navigator.clipboard rejected /
  // V3 event-order / V4 browser-block. The bug was: every code path
  // SILENTLY swallowed errors (try{}catch{}), so V2/V4 cases never
  // surfaced to the operator — they just saw "nothing happened" and
  // assumed the wiring was broken. This rewrite:
  //
  //   1. Diagnostic console.log at every checkpoint (operator can
  //      open DevTools and see which path fired + where it died).
  //   2. Toast on every failure with the actual error message —
  //      operator gets immediate feedback instead of silence.
  //   3. execCommand fallback: focus the textarea FIRST + check the
  //      return value (it returns false when the browser blocks).
  //      Pre-fix, the textarea was append→select→exec without focus,
  //      so xterm's input proxy retained focus and the wrong textarea
  //      was the copy source.
  //   4. Paste failure surfaces (was silently no-op when WS not OPEN).
  //
  // Refs #245.
  let toast = $state({ msg: '', kind: '', t: 0 });
  function showToast(msg, kind) {
    toast = { msg, kind, t: Date.now() };
    const id = toast.t;
    setTimeout(() => { if (toast.t === id) toast = { msg: '', kind: '', t: 0 }; }, 3000);
  }
  async function copySelectionToClipboard(text) {
    console.log('[chepherd-term] copy attempt — selectionLen=' + (text ? text.length : 0));
    if (!text) {
      console.warn('[chepherd-term] copy aborted — empty selection (V1)');
      return;
    }
    // Path A — modern Clipboard API.
    if (navigator.clipboard && navigator.clipboard.writeText) {
      try {
        await navigator.clipboard.writeText(text);
        console.log('[chepherd-term] copy ✓ via navigator.clipboard.writeText');
        showToast('Copied ' + text.length + ' chars', 'ok');
        return;
      } catch (e) {
        console.warn('[chepherd-term] navigator.clipboard.writeText rejected (V2):', e?.message || e);
        // Fall through to execCommand path.
      }
    } else {
      console.warn('[chepherd-term] navigator.clipboard.writeText unavailable (V4) — using execCommand fallback');
    }
    // Path B — legacy execCommand fallback. Must focus the textarea
    // BEFORE select() or xterm's input proxy retains focus and the
    // copy command lands on the wrong selection.
    try {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed';
      ta.style.top = '0';
      ta.style.left = '0';
      ta.style.opacity = '0';
      ta.style.pointerEvents = 'none';
      ta.setAttribute('readonly', 'true');
      ta.setAttribute('tabindex', '-1');
      document.body.appendChild(ta);
      ta.focus();
      ta.select();
      ta.setSelectionRange(0, text.length);
      const ok = document.execCommand('copy');
      document.body.removeChild(ta);
      if (ok) {
        console.log('[chepherd-term] copy ✓ via document.execCommand');
        showToast('Copied ' + text.length + ' chars', 'ok');
      } else {
        console.error('[chepherd-term] document.execCommand("copy") returned false — browser blocked (V4)');
        showToast('Copy blocked by browser — check permissions', 'err');
      }
    } catch (e) {
      console.error('[chepherd-term] execCommand fallback threw:', e?.message || e);
      showToast('Copy failed: ' + (e?.message || 'unknown error'), 'err');
    }
  }
  async function pasteFromClipboard() {
    console.log('[chepherd-term] paste attempt — wsState=' + (ws ? ws.readyState : 'no-ws'));
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      console.warn('[chepherd-term] paste aborted — WS not open');
      showToast('Paste failed: terminal not connected', 'err');
      return;
    }
    if (!navigator.clipboard || !navigator.clipboard.readText) {
      console.warn('[chepherd-term] navigator.clipboard.readText unavailable (V4)');
      showToast('Paste blocked: clipboard API unavailable', 'err');
      return;
    }
    try {
      const txt = await navigator.clipboard.readText();
      if (!txt) {
        console.log('[chepherd-term] paste no-op — clipboard empty');
        return;
      }
      ws.send(txt);
      console.log('[chepherd-term] paste ✓ — sent ' + txt.length + ' chars to PTY');
      showToast('Pasted ' + txt.length + ' chars', 'ok');
    } catch (e) {
      console.error('[chepherd-term] navigator.clipboard.readText rejected (V2/V4):', e?.message || e);
      showToast('Paste failed: ' + (e?.message || 'permission denied'), 'err');
    }
  }

  // #245 — right-click context menu. Renders a tiny menu at the click
  // position; clicking outside dismisses. Items: Copy (enabled iff
  // term has a selection) + Paste (always enabled, will no-op when
  // clipboard is empty).
  let menuOpen = $state(false);
  let menuX = $state(0);
  let menuY = $state(0);
  let menuHasSelection = $state(false);
  function openCtxMenu(ev) {
    if (!term) return;
    ev.preventDefault();
    menuHasSelection = !!term.getSelection() && term.getSelection().length > 0;
    menuX = ev.clientX;
    menuY = ev.clientY;
    menuOpen = true;
  }
  function closeCtxMenu() { menuOpen = false; }
  async function menuCopy() {
    if (term) {
      const sel = term.getSelection();
      if (sel) await copySelectionToClipboard(sel);
    }
    menuOpen = false;
  }
  async function menuPaste() {
    await pasteFromClipboard();
    menuOpen = false;
  }

  $effect(() => { attachTo(myAgent); });

  onMount(() => {
    // Watch for --ws-font changes on documentElement style attribute.
    fontObs = new MutationObserver(() => applyFontAndRefit());
    fontObs.observe(document.documentElement, { attributes: true, attributeFilter: ['style'] });
    // R2 (#133): re-attach on theme toggle so the claude-code palette
    // switches between dark/light without a page reload.
    themeObs = new MutationObserver(() => {
      const sig = document.documentElement.dataset.theme || 'dark';
      if (sig !== lastThemeSig) {
        lastThemeSig = sig;
        attached = null; // trigger $effect to recreate Terminal with new palette
        attachTo(myAgent);
      }
    });
    themeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  });

  onDestroy(() => {
    if (ws) ws.close();
    if (resizeObs) resizeObs.disconnect();
    if (term) {
      if (term._chepherdDocKeyHandler) {
        try { document.removeEventListener('keydown', term._chepherdDocKeyHandler, true); } catch {}
      }
      term.dispose();
    }
    if (fontObs) fontObs.disconnect();
    if (themeObs) themeObs.disconnect();
  });

  let info = $derived(sessions?.find(s => s.name === myAgent));

  function pickAgent(ev) {
    const v = ev.target.value;
    if (!node) return;
    if (!node.config) node.config = {};
    node.config.agent = v;
    // Trigger re-attach
    attached = null;
  }
</script>

<!--
  No inner header — Pane.svelte's pane-header row now hosts the agent
  picker + Live/age/Ctx chips alongside the widget-pick + split/close.
  This widget renders only the xterm canvas, full height.
-->
<div class="term-pane">
  <div
    class="term-body"
    bind:this={termContainer}
    oncontextmenu={openCtxMenu}
  ></div>
  {#if menuOpen}
    <!-- #245 — full-screen invisible click-catcher dismisses the menu
         when the operator clicks anywhere outside it. -->
    <div class="ctx-backdrop" onclick={closeCtxMenu} role="presentation"></div>
    <div
      class="ctx-menu"
      style="left:{menuX}px; top:{menuY}px"
      role="menu"
      aria-label="Terminal context menu"
    >
      <button type="button" role="menuitem" onclick={menuCopy} disabled={!menuHasSelection}>
        Copy <span class="kbd">Ctrl+Shift+C</span>
      </button>
      <button type="button" role="menuitem" onclick={menuPaste}>
        Paste <span class="kbd">Ctrl+Shift+V</span>
      </button>
    </div>
  {/if}
  {#if toast.msg}
    <!-- #245 — clipboard feedback toast. Auto-dismisses after 3s.
         Operator-visible diagnostic for the silent-fail bug from PR #249. -->
    <div class="clipboard-toast" class:ok={toast.kind === 'ok'} class:err={toast.kind === 'err'} role="status" aria-live="polite">
      {toast.msg}
    </div>
  {/if}
</div>

<style>
  .term-pane { display: flex; flex-direction: column; height: 100%; background: var(--bg); position: relative; }
  .term-body { flex: 1; padding: 0.3rem 0.4rem; min-height: 0; overflow: hidden; }
  .term-body :global(.xterm) { height: 100%; }
  .term-body :global(.xterm-viewport) { height: 100% !important; }
  /* #245 — right-click context menu. position: fixed so menuX/menuY
     (clientX/Y from the pointer event) anchor correctly regardless of
     where the term-pane sits in the layout. */
  .ctx-backdrop {
    position: fixed; inset: 0;
    background: transparent;
    z-index: 50;
  }
  .ctx-menu {
    position: fixed;
    z-index: 51;
    min-width: 11rem;
    background: var(--bg-elevated, #1a1a1a);
    border: 1px solid var(--border, #2a2a2a);
    border-radius: 6px;
    padding: 0.25rem;
    box-shadow: 0 6px 20px rgba(0, 0, 0, 0.5);
    display: flex; flex-direction: column; gap: 0.1rem;
  }
  .ctx-menu button {
    display: flex; justify-content: space-between; align-items: center; gap: 0.7rem;
    padding: 0.45rem 0.6rem;
    background: transparent;
    border: 0;
    border-radius: 4px;
    color: var(--fg, #f5f5f5);
    font: inherit;
    font-size: 0.85rem;
    text-align: left;
    cursor: pointer;
  }
  .ctx-menu button:hover:not(:disabled) {
    background: rgba(135, 206, 235, 0.12);
  }
  .ctx-menu button:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  /* #245 reopen — clipboard feedback toast. Auto-dismisses after 3s
     and renders top-right of the terminal pane. Operator-visible
     diagnostic for the silent-fail bug from PR #249: every clipboard
     code path used to swallow errors silently. */
  .clipboard-toast {
    position: absolute;
    top: 0.5rem;
    right: 0.7rem;
    z-index: 60;
    padding: 0.4rem 0.7rem;
    border-radius: 5px;
    font-size: 0.78rem;
    font-weight: 500;
    border: 1px solid;
    background: var(--bg-elevated, #1a1a1a);
    pointer-events: none;
    box-shadow: 0 4px 14px rgba(0, 0, 0, 0.45);
  }
  .clipboard-toast.ok  { color: var(--success, #4ade80); border-color: var(--success, #4ade80); }
  .clipboard-toast.err { color: var(--danger, #e74c3c); border-color: var(--danger, #e74c3c); }
  .kbd {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.72rem;
    color: var(--fg-muted, #888);
    background: var(--bg, #0a0a0a);
    padding: 0.1rem 0.35rem;
    border-radius: 3px;
    border: 1px solid var(--border, #2a2a2a);
  }
</style>

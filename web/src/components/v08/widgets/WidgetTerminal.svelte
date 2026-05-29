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
    if (term) { term.dispose(); term = null; }
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

    // #151 — auto-reconnect on transient WebSocket close. Exponential
    // backoff capped at 5s, gives up after 8 attempts (~30s).
    let wsReconnectAttempts = 0;
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
        wsReconnectAttempts = 0;
        setTimeout(sendResize, 200);
      });
      ws.addEventListener('close', () => {
        if (attached !== name) return; // user switched agent — don't reconnect
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
      if (ctrl && ((shift && k === 'C') || k === 'Insert')) {
        if (hasSel) { copySelectionToClipboard(sel); }
        return false;
      }
      // Ctrl+C — copy-if-selection-else-SIGINT (Option 2).
      if (ctrl && !shift && (k === 'c' || k === 'C')) {
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
        pasteFromClipboard();
        return false;
      }
      return true;
    });
  }

  // #245 — clipboard helpers. Uses the modern Clipboard API; falls
  // back to the legacy document.execCommand('copy') path on browsers
  // that don't expose Clipboard API in this origin's permissions.
  async function copySelectionToClipboard(text) {
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(text);
        return;
      }
    } catch {}
    // Fallback: hidden textarea + execCommand('copy').
    try {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed'; ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      document.body.removeChild(ta);
    } catch {}
  }
  async function pasteFromClipboard() {
    try {
      if (navigator.clipboard && navigator.clipboard.readText) {
        const txt = await navigator.clipboard.readText();
        if (txt && ws && ws.readyState === WebSocket.OPEN) {
          ws.send(txt);
        }
      }
    } catch {}
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
    if (term) term.dispose();
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

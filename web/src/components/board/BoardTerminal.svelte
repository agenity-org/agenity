<!--
  BoardTerminal — live xterm.js PTY viewer for the "board" dashboard.

  Reuses the exact proven attach/resize/clipboard wiring from
  web/src/components/v08/widgets/WidgetTerminal.svelte (#245/#357/#677):
    - WebSocket /api-v08/v1/sessions/{name}/attach?token=… (binary + text)
    - {type:'resize', rows, cols} on FitAddon / font changes (SIGWINCH)
    - smart Ctrl+C copy, Ctrl+V paste, OSC 52 swallow
    - dark/light terminal theme that re-attaches on data-theme flip
    - --ws-font tracking

  Self-contained to web/src/components/board/ — no imports from v08.
-->
<script>
  import { onMount, onDestroy } from 'svelte';

  let { agent = '' } = $props();

  let term = null;
  let ws = null;
  let fitAddon = null;
  let resizeObs = null;
  let termContainer;
  let attached = null;
  let fontObs = null;
  let themeObs = null;
  let lastThemeSig = '';
  let toast = $state({ msg: '', kind: '', t: 0 });

  function currentWsFont() {
    try {
      const v = getComputedStyle(document.documentElement).getPropertyValue('--ws-font').trim();
      const n = parseFloat(v);
      return n > 0 ? n : 13;
    } catch { return 13; }
  }

  function sendResize() {
    if (!ws || ws.readyState !== WebSocket.OPEN || !term) return;
    try { ws.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols })); } catch {}
  }

  function applyFontAndRefit() {
    if (!term || !fitAddon) return;
    const f = currentWsFont();
    if (term.options.fontSize !== f) term.options.fontSize = f;
    try { fitAddon.fit(); } catch {}
    sendResize();
  }

  function showToast(msg, kind) {
    toast = { msg, kind, t: Date.now() };
    const id = toast.t;
    setTimeout(() => { if (toast.t === id) toast = { msg: '', kind: '', t: 0 }; }, 2600);
  }

  async function copySelectionToClipboard(text) {
    if (!text) return;
    if (navigator.clipboard && navigator.clipboard.writeText) {
      try {
        await navigator.clipboard.writeText(text);
        showToast('Copied ' + text.length + ' chars', 'ok');
        return;
      } catch {}
    }
    try {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.position = 'fixed'; ta.style.top = '0'; ta.style.left = '0';
      ta.style.opacity = '0'; ta.style.pointerEvents = 'none';
      ta.setAttribute('readonly', 'true'); ta.setAttribute('tabindex', '-1');
      document.body.appendChild(ta);
      ta.focus(); ta.select(); ta.setSelectionRange(0, text.length);
      const ok = document.execCommand('copy');
      document.body.removeChild(ta);
      showToast(ok ? 'Copied ' + text.length + ' chars' : 'Copy blocked by browser', ok ? 'ok' : 'err');
    } catch (e) {
      showToast('Copy failed', 'err');
    }
  }

  async function pasteFromClipboard() {
    if (!ws || ws.readyState !== WebSocket.OPEN) { showToast('Not connected', 'err'); return; }
    if (!navigator.clipboard || !navigator.clipboard.readText) { showToast('Clipboard unavailable', 'err'); return; }
    try {
      const txt = await navigator.clipboard.readText();
      if (!txt) return;
      ws.send(txt);
      showToast('Pasted ' + txt.length + ' chars', 'ok');
    } catch { showToast('Paste failed', 'err'); }
  }

  async function attachTo(name) {
    if (attached === name) return;
    attached = name;
    if (ws) { ws.close(); ws = null; }
    if (resizeObs) { resizeObs.disconnect(); resizeObs = null; }
    if (term) {
      if (term._boardDocKeyHandler) {
        try { document.removeEventListener('keydown', term._boardDocKeyHandler, true); } catch {}
      }
      term.dispose();
      term = null;
    }
    if (!name) return;

    const { Terminal } = await import('@xterm/xterm');
    const { FitAddon } = await import('@xterm/addon-fit');
    const { WebLinksAddon } = await import('@xterm/addon-web-links');

    const isLight = typeof document !== 'undefined' && document.documentElement.dataset.theme === 'light';
    const sel = isLight ? {
      background: '#ffffff', foreground: '#1a1a1a', cursor: '#1a1a1a',
      cursorAccent: '#ffffff', selectionBackground: '#cbd5e1',
    } : {
      background: '#0b0e14', foreground: '#f5f5f5', cursor: '#f5f5f5',
      cursorAccent: '#0b0e14', selectionBackground: '#2a3540',
    };
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
        if (ev.data instanceof ArrayBuffer) term.write(new Uint8Array(ev.data));
        else term.write(ev.data);
      };
      ws.addEventListener('open', () => { everOpened = true; wsReconnectAttempts = 0; setTimeout(sendResize, 200); });
      ws.addEventListener('close', () => {
        if (attached !== name) return;
        if (!everOpened) {
          if (term) { try { term.write('\r\n\x1b[90m[session unavailable — it is no longer running]\x1b[0m\r\n'); } catch {} }
          return;
        }
        wsReconnectAttempts++;
        if (wsReconnectAttempts > 8) return;
        const delay = Math.min(5000, 250 * 2 ** wsReconnectAttempts);
        setTimeout(() => { if (attached === name) openWS(); }, delay);
      });
    };
    openWS();
    term.onResize(sendResize);
    term.onData((d) => { if (ws && ws.readyState === WebSocket.OPEN) ws.send(d); });
    term.parser.registerOscHandler(52, () => true);

    term.attachCustomKeyEventHandler((ev) => {
      if (ev.type !== 'keydown') return true;
      const k = ev.key;
      const ctrl = ev.ctrlKey || ev.metaKey;
      const shift = ev.shiftKey;
      const selTxt = term.getSelection();
      const hasSel = !!selTxt && selTxt.length > 0;
      if (ctrl && ((shift && (k === 'C' || k === 'c')) || k === 'Insert')) {
        if (hasSel) copySelectionToClipboard(selTxt);
        return false;
      }
      if (ctrl && !shift && (k === 'c' || k === 'C')) {
        if (hasSel) { copySelectionToClipboard(selTxt); try { term.clearSelection(); } catch {} return false; }
        return true;
      }
      if ((ctrl && (k === 'v' || k === 'V')) || (shift && k === 'Insert')) {
        pasteFromClipboard();
        return false;
      }
      return true;
    });

    const docKeyHandler = (ev) => {
      if (ev.type !== 'keydown') return;
      if (!ev.ctrlKey && !ev.metaKey) return;
      if (ev.key !== 'c' && ev.key !== 'C') return;
      const tag = ev.target?.tagName || '';
      const xtermSel = term ? term.getSelection() : '';
      const xtermHasSel = !!xtermSel && xtermSel.length > 0;
      const containedInTerminal = termContainer?.contains(ev.target);
      if (!ev.shiftKey && xtermHasSel && !containedInTerminal &&
          tag !== 'INPUT' && tag !== 'TEXTAREA' && !ev.target?.isContentEditable) {
        copySelectionToClipboard(xtermSel);
        ev.preventDefault();
      }
    };
    document.addEventListener('keydown', docKeyHandler, true);
    term._boardDocKeyHandler = docKeyHandler;
  }

  $effect(() => { attachTo(agent); });

  onMount(() => {
    fontObs = new MutationObserver(() => applyFontAndRefit());
    fontObs.observe(document.documentElement, { attributes: true, attributeFilter: ['style'] });
    themeObs = new MutationObserver(() => {
      const sig = document.documentElement.dataset.theme || 'dark';
      if (sig !== lastThemeSig) {
        lastThemeSig = sig;
        attached = null;
        attachTo(agent);
      }
    });
    themeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  });

  onDestroy(() => {
    if (ws) ws.close();
    if (resizeObs) resizeObs.disconnect();
    if (term) {
      if (term._boardDocKeyHandler) {
        try { document.removeEventListener('keydown', term._boardDocKeyHandler, true); } catch {}
      }
      term.dispose();
    }
    if (fontObs) fontObs.disconnect();
    if (themeObs) themeObs.disconnect();
  });
</script>

<div class="bt-pane">
  {#if !agent}
    <div class="bt-empty">No agent bound to this pane. Click a card to focus one.</div>
  {/if}
  <div class="bt-body" bind:this={termContainer}></div>
  {#if toast.msg}
    <div class="bt-toast" class:ok={toast.kind === 'ok'} class:err={toast.kind === 'err'} role="status">{toast.msg}</div>
  {/if}
</div>

<style>
  .bt-pane { display: flex; flex-direction: column; height: 100%; background: var(--board-term-bg); position: relative; }
  .bt-body { flex: 1; padding: 0.35rem 0.45rem; min-height: 0; overflow: hidden; }
  .bt-body :global(.xterm) { height: 100%; }
  .bt-body :global(.xterm-viewport) { height: 100% !important; }
  .bt-empty {
    position: absolute; inset: 0; display: flex; align-items: center; justify-content: center;
    color: var(--board-fg-faint); font-size: 0.82rem; padding: 1rem; text-align: center; z-index: 2;
  }
  .bt-toast {
    position: absolute; top: 0.5rem; right: 0.7rem; z-index: 60;
    padding: 0.35rem 0.7rem; border-radius: 6px; font-size: 0.74rem; font-weight: 600;
    border: 1px solid var(--board-border-strong); background: var(--board-surface);
    pointer-events: none; box-shadow: 0 6px 18px rgba(0,0,0,0.35);
  }
  .bt-toast.ok { color: var(--board-ok); border-color: var(--board-ok); }
  .bt-toast.err { color: var(--board-danger); border-color: var(--board-danger); }
</style>

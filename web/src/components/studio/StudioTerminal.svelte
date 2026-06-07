<!--
  StudioTerminal — live xterm.js PTY viewer for the "studio" dashboard.

  Self-contained reimplementation of the terminal surface that reuses the
  EXACT data-layer wiring proven in v08/widgets/WidgetTerminal.svelte:
    - WebSocket attach at /api-v08/v1/sessions/{name}/attach?token=…
    - binary ArrayBuffer + text chunks → Terminal.write()
    - {type:'resize', rows, cols} frames on FitAddon size change / font change
    - smart Ctrl+C copy, Ctrl+V paste, OSC-52 swallowed
    - theme-reactive palette (background/foreground/cursor/selection only;
      stock xterm ANSI palette preserved so claude-code colors are native)
    - --ws-font CSS var drives fontSize + re-fit + SIGWINCH

  Per-pane agent: node.config.agent wins; else the workspace selectedAgent;
  else auto-pick the first live non-shepherd agent.
-->
<script>
  import { onMount, onDestroy } from 'svelte';

  let { selectedAgent, sessions = [], node } = $props();

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
  let toast = $state({ msg: '', kind: '', t: 0 });
  let connState = $state('idle'); // idle | connecting | live | gone

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
    if (term.options.fontSize !== f) term.options.fontSize = f;
    try { fitAddon.fit(); } catch {}
    sendResize();
  }

  function sendResize() {
    if (!ws || ws.readyState !== WebSocket.OPEN || !term) return;
    try { ws.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols })); } catch {}
  }

  function showToast(msg, kind) {
    toast = { msg, kind, t: Date.now() };
    const id = toast.t;
    setTimeout(() => { if (toast.t === id) toast = { msg: '', kind: '', t: 0 }; }, 2600);
  }

  async function copySelection(text) {
    if (!text) return;
    if (navigator.clipboard?.writeText) {
      try { await navigator.clipboard.writeText(text); showToast('Copied ' + text.length + ' chars', 'ok'); return; }
      catch {}
    }
    try {
      const ta = document.createElement('textarea');
      ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0'; ta.style.pointerEvents = 'none';
      ta.setAttribute('readonly', 'true'); document.body.appendChild(ta);
      ta.focus(); ta.select(); ta.setSelectionRange(0, text.length);
      const ok = document.execCommand('copy'); document.body.removeChild(ta);
      showToast(ok ? 'Copied ' + text.length + ' chars' : 'Copy blocked by browser', ok ? 'ok' : 'err');
    } catch (e) { showToast('Copy failed', 'err'); }
  }

  async function pasteClipboard() {
    if (!ws || ws.readyState !== WebSocket.OPEN) { showToast('Not connected', 'err'); return; }
    if (!navigator.clipboard?.readText) { showToast('Clipboard unavailable', 'err'); return; }
    try {
      const txt = await navigator.clipboard.readText();
      if (txt) { ws.send(txt); showToast('Pasted ' + txt.length + ' chars', 'ok'); }
    } catch { showToast('Paste denied', 'err'); }
  }

  async function attachTo(name) {
    if (attached === name) return;
    attached = name;
    if (ws) { ws.close(); ws = null; }
    if (resizeObs) { resizeObs.disconnect(); resizeObs = null; }
    if (term) {
      if (term._studioDocKey) {
        try { document.removeEventListener('keydown', term._studioDocKey, true); } catch {}
      }
      term.dispose(); term = null;
    }
    if (!name) { connState = 'idle'; return; }

    const { Terminal } = await import('@xterm/xterm');
    const { FitAddon } = await import('@xterm/addon-fit');
    const { WebLinksAddon } = await import('@xterm/addon-web-links');

    const isLight = typeof document !== 'undefined' && document.documentElement.dataset.theme === 'light';
    const sel = isLight ? {
      background: '#ffffff', foreground: '#1f2430', cursor: '#1f2430',
      cursorAccent: '#ffffff', selectionBackground: '#c7dcff',
    } : {
      background: '#0d1117', foreground: '#e6edf3', cursor: '#e6edf3',
      cursorAccent: '#0d1117', selectionBackground: '#264166',
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

    const sess = (sessions || []).find(s => s.name === name);
    if (!sess || sess.live === false) { attached = null; connState = 'gone'; return; }

    connState = 'connecting';
    let wsReconnect = 0;
    let everOpened = false;
    const openWS = () => {
      let tok = '';
      try { tok = localStorage.getItem('chepherd-token') || ''; } catch {}
      const q = tok ? ('?token=' + encodeURIComponent(tok)) : '';
      ws = new WebSocket(`${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/api-v08/v1/sessions/${name}/attach${q}`);
      ws.binaryType = 'arraybuffer';
      ws.onmessage = (ev) => {
        if (!term) return;
        if (ev.data instanceof ArrayBuffer) term.write(new Uint8Array(ev.data));
        else term.write(ev.data);
      };
      ws.addEventListener('open', () => {
        everOpened = true; wsReconnect = 0; connState = 'live';
        setTimeout(sendResize, 200);
      });
      ws.addEventListener('close', () => {
        if (attached !== name) return;
        if (!everOpened) {
          connState = 'gone';
          if (term) { try { term.write('\r\n\x1b[90m[session unavailable — it is no longer running]\x1b[0m\r\n'); } catch {} }
          return;
        }
        const stillLive = (sessions || []).some(s => s.name === name && s.live !== false);
        if (!stillLive) { connState = 'gone'; return; }
        wsReconnect++;
        if (wsReconnect > 8) { connState = 'gone'; return; }
        connState = 'connecting';
        const delay = Math.min(5000, 250 * 2 ** wsReconnect);
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
      const s = term.getSelection();
      const hasSel = !!s && s.length > 0;
      if (ctrl && ((shift && (k === 'C' || k === 'c')) || k === 'Insert')) {
        if (hasSel) copySelection(s);
        return false;
      }
      if (ctrl && !shift && (k === 'c' || k === 'C')) {
        if (hasSel) { copySelection(s); try { term.clearSelection(); } catch {} return false; }
        return true;
      }
      if ((ctrl && (k === 'v' || k === 'V')) || (shift && k === 'Insert')) {
        pasteClipboard();
        return false;
      }
      return true;
    });
    const docKey = (ev) => {
      if (ev.type !== 'keydown' || (!ev.ctrlKey && !ev.metaKey)) return;
      if (ev.key !== 'c' && ev.key !== 'C') return;
      const tag = ev.target?.tagName || '';
      const xsel = term ? term.getSelection() : '';
      if (!ev.shiftKey && xsel && !termContainer?.contains(ev.target) &&
          tag !== 'INPUT' && tag !== 'TEXTAREA' && !ev.target?.isContentEditable) {
        copySelection(xsel); ev.preventDefault();
      }
    };
    document.addEventListener('keydown', docKey, true);
    term._studioDocKey = docKey;
  }

  // right-click menu
  let menuOpen = $state(false);
  let menuX = $state(0);
  let menuY = $state(0);
  let menuHasSel = $state(false);
  function openMenu(ev) {
    if (!term) return;
    ev.preventDefault();
    menuHasSel = !!term.getSelection() && term.getSelection().length > 0;
    menuX = ev.clientX; menuY = ev.clientY; menuOpen = true;
  }

  $effect(() => { attachTo(myAgent); });

  onMount(() => {
    fontObs = new MutationObserver(() => applyFontAndRefit());
    fontObs.observe(document.documentElement, { attributes: true, attributeFilter: ['style'] });
    themeObs = new MutationObserver(() => {
      const sig = document.documentElement.dataset.theme || 'dark';
      if (sig !== lastThemeSig) { lastThemeSig = sig; attached = null; attachTo(myAgent); }
    });
    themeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
  });

  onDestroy(() => {
    if (ws) ws.close();
    if (resizeObs) resizeObs.disconnect();
    if (term) {
      if (term._studioDocKey) { try { document.removeEventListener('keydown', term._studioDocKey, true); } catch {} }
      term.dispose();
    }
    if (fontObs) fontObs.disconnect();
    if (themeObs) themeObs.disconnect();
  });
</script>

<div class="st-term">
  {#if !myAgent}
    <div class="st-term-empty">No live agent to attach. Spawn one or pick from the Explorer.</div>
  {/if}
  <div class="st-term-body" bind:this={termContainer} oncontextmenu={openMenu} role="presentation"></div>
  {#if menuOpen}
    <div class="st-ctx-backdrop" onclick={() => (menuOpen = false)} role="presentation"></div>
    <div class="st-ctx-menu" style="left:{menuX}px; top:{menuY}px" role="menu" aria-label="Terminal menu">
      <button type="button" role="menuitem" disabled={!menuHasSel}
              onclick={async () => { if (term) await copySelection(term.getSelection()); menuOpen = false; }}>
        Copy <span class="kbd">Ctrl+Shift+C</span>
      </button>
      <button type="button" role="menuitem" onclick={async () => { await pasteClipboard(); menuOpen = false; }}>
        Paste <span class="kbd">Ctrl+Shift+V</span>
      </button>
    </div>
  {/if}
  {#if toast.msg}
    <div class="st-toast" class:ok={toast.kind === 'ok'} class:err={toast.kind === 'err'} role="status">{toast.msg}</div>
  {/if}
</div>

<style>
  .st-term { display: flex; flex-direction: column; height: 100%; background: var(--st-term-bg); position: relative; min-height: 0; }
  .st-term-body { flex: 1; padding: 0.35rem 0.45rem; min-height: 0; overflow: hidden; }
  .st-term-body :global(.xterm) { height: 100%; }
  .st-term-body :global(.xterm-viewport) { height: 100% !important; }
  .st-term-body :global(.xterm-viewport)::-webkit-scrollbar { width: 10px; }
  .st-term-body :global(.xterm-viewport)::-webkit-scrollbar-thumb { background: var(--st-scroll-thumb); border-radius: 6px; }
  .st-term-empty {
    position: absolute; inset: 0; display: flex; align-items: center; justify-content: center;
    color: var(--st-fg-faint); font-size: 0.85rem; pointer-events: none; padding: 2rem; text-align: center;
  }
  .st-ctx-backdrop { position: fixed; inset: 0; z-index: 50; }
  .st-ctx-menu {
    position: fixed; z-index: 51; min-width: 12rem;
    background: var(--st-panel); border: 1px solid var(--st-border-strong);
    border-radius: 8px; padding: 0.3rem; box-shadow: var(--st-shadow);
    display: flex; flex-direction: column; gap: 0.1rem;
  }
  .st-ctx-menu button {
    display: flex; justify-content: space-between; align-items: center; gap: 0.7rem;
    padding: 0.45rem 0.6rem; background: transparent; border: 0; border-radius: 5px;
    color: var(--st-fg); font: inherit; font-size: 0.84rem; text-align: left; cursor: pointer;
  }
  .st-ctx-menu button:hover:not(:disabled) { background: var(--st-hover); }
  .st-ctx-menu button:disabled { opacity: 0.4; cursor: not-allowed; }
  .kbd {
    font-family: ui-monospace, monospace; font-size: 0.7rem; color: var(--st-fg-muted);
    background: var(--st-bg); padding: 0.1rem 0.35rem; border-radius: 4px; border: 1px solid var(--st-border);
  }
  .st-toast {
    position: absolute; top: 0.5rem; right: 0.7rem; z-index: 60;
    padding: 0.4rem 0.7rem; border-radius: 6px; font-size: 0.76rem; font-weight: 600;
    border: 1px solid; background: var(--st-panel); pointer-events: none; box-shadow: var(--st-shadow);
  }
  .st-toast.ok { color: var(--st-ok); border-color: var(--st-ok); }
  .st-toast.err { color: var(--st-danger); border-color: var(--st-danger); }
</style>

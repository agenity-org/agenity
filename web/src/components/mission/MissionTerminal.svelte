<!--
  MissionTerminal — live xterm.js PTY viewer for one agent, mission edition.

  Reuses the project's proven xterm wiring (WidgetTerminal.svelte):
    - WebSocket attach to /api/v1/sessions/{name}/attach?token=… (the
      production /api origin used by the v0.9.x data layer + the spec's
      terminalRendering contract). Binary chunks → Terminal.write().
    - FitAddon + ResizeObserver → {type:'resize',rows,cols} SIGWINCH frame.
    - Font tracks --ws-font; smart Ctrl+C copy / Ctrl+V paste; OSC52 off.
    - Re-themes on mission theme change (mode prop) without a reload.

  Each pane carries its OWN agent (the `agent` prop), so a tiled grid shows
  many different agents' terminals at once.
-->
<script>
  import { onMount, onDestroy } from 'svelte';
  import { xtermTheme } from './theme.js';

  let { agent = '', mode = 'dark' } = $props();

  let term = null;
  let ws = null;
  let fitAddon = null;
  let resizeObs = null;
  let termContainer;
  let attached = null;
  let status = $state('idle'); // idle | connecting | live | closed | unavailable

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

  async function copySelection(text) {
    if (!text) return;
    if (navigator.clipboard?.writeText) {
      try { await navigator.clipboard.writeText(text); return; } catch {}
    }
    try {
      const ta = document.createElement('textarea');
      ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0';
      ta.setAttribute('readonly', 'true'); document.body.appendChild(ta);
      ta.focus(); ta.select(); document.execCommand('copy'); document.body.removeChild(ta);
    } catch {}
  }
  async function pasteClipboard() {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    if (!navigator.clipboard?.readText) return;
    try { const txt = await navigator.clipboard.readText(); if (txt) ws.send(txt); } catch {}
  }

  async function attachTo(name) {
    if (attached === name) return;
    attached = name;
    if (ws) { ws.close(); ws = null; }
    if (resizeObs) { resizeObs.disconnect(); resizeObs = null; }
    if (term) { term.dispose(); term = null; }
    if (!name) { status = 'idle'; return; }
    status = 'connecting';

    const { Terminal } = await import('@xterm/xterm');
    const { FitAddon } = await import('@xterm/addon-fit');
    const { WebLinksAddon } = await import('@xterm/addon-web-links');

    term = new Terminal({
      convertEol: true,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      fontSize: currentWsFont(),
      theme: xtermTheme(mode),
      cursorBlink: true,
      cols: 120, rows: 32,
    });
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

    // smart copy/paste keybindings (return false = swallow; true = forward)
    term.attachCustomKeyEventHandler((ev) => {
      if (ev.type !== 'keydown') return true;
      const k = ev.key, ctrl = ev.ctrlKey || ev.metaKey, shift = ev.shiftKey;
      const sel = term.getSelection(); const hasSel = !!sel && sel.length > 0;
      if (ctrl && ((shift && (k === 'C' || k === 'c')) || k === 'Insert')) {
        if (hasSel) copySelection(sel); return false;
      }
      if (ctrl && !shift && (k === 'c' || k === 'C')) {
        if (hasSel) { copySelection(sel); try { term.clearSelection(); } catch {} return false; }
        return true;
      }
      if ((ctrl && (k === 'v' || k === 'V')) || (shift && k === 'Insert')) { pasteClipboard(); return false; }
      return true;
    });
    // OSC 52 clipboard hijack disabled.
    term.parser.registerOscHandler(52, () => true);

    let wsTok = '';
    try { wsTok = localStorage.getItem('chepherd-token') || ''; } catch {}
    const wsQ = wsTok ? ('?token=' + encodeURIComponent(wsTok)) : '';
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    let everOpened = false;
    ws = new WebSocket(`${proto}//${window.location.host}/api/v1/sessions/${name}/attach${wsQ}`);
    ws.binaryType = 'arraybuffer';
    ws.onmessage = (ev) => {
      if (!term) return;
      if (ev.data instanceof ArrayBuffer) term.write(new Uint8Array(ev.data));
      else term.write(ev.data);
    };
    ws.addEventListener('open', () => { everOpened = true; status = 'live'; setTimeout(sendResize, 200); });
    ws.addEventListener('close', () => {
      if (attached !== name) return;
      if (!everOpened) {
        status = 'unavailable';
        if (term) { try { term.write('\r\n\x1b[90m[session unavailable — it is no longer running]\x1b[0m\r\n'); } catch {} }
        return;
      }
      status = 'closed';
    });
    term.onResize(sendResize);
    term.onData((d) => { if (ws && ws.readyState === WebSocket.OPEN) ws.send(d); });
  }

  // React to agent change.
  $effect(() => { attachTo(agent); });

  // React to mission theme change — recreate terminal with the new palette.
  $effect(() => {
    mode; // track
    if (term && attached) { attached = null; attachTo(agent); }
  });

  // React to font change (--ws-font is mutated on documentElement.style).
  onMount(() => {
    const obs = new MutationObserver(() => {
      if (!term || !fitAddon) return;
      const f = currentWsFont();
      if (term.options.fontSize !== f) term.options.fontSize = f;
      try { fitAddon.fit(); } catch {}
      sendResize();
    });
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ['style'] });
    return () => obs.disconnect();
  });

  onDestroy(() => {
    if (ws) ws.close();
    if (resizeObs) resizeObs.disconnect();
    if (term) term.dispose();
  });
</script>

<div class="m-term" data-status={status}>
  <div class="m-term-body" bind:this={termContainer}></div>
  {#if status === 'idle'}
    <div class="m-term-overlay">No agent bound — pick one from the header ▾</div>
  {/if}
</div>

<style>
  .m-term { position: relative; height: 100%; width: 100%; background: var(--m-term-bg); overflow: hidden; }
  .m-term-body { position: absolute; inset: 0; padding: 0.3rem 0.45rem; }
  .m-term-body :global(.xterm) { height: 100%; }
  .m-term-body :global(.xterm-viewport) { height: 100% !important; }
  .m-term-body :global(.xterm-viewport)::-webkit-scrollbar { width: 9px; }
  .m-term-body :global(.xterm-viewport)::-webkit-scrollbar-thumb { background: var(--m-scroll); border-radius: 5px; }
  .m-term-overlay {
    position: absolute; inset: 0; display: flex; align-items: center; justify-content: center;
    color: var(--m-fg-faint); font-size: 0.82rem; pointer-events: none;
  }
</style>

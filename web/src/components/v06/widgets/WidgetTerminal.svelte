<!--
  WidgetTerminal — xterm.js viewer for the selected agent's PTY. WebSocket
  attach to /api/v1/sessions/{name}/attach. Resize handler wires SIGWINCH
  back through the WS via {type:"resize"} text frames.
-->
<script>
  import { onMount, onDestroy } from 'svelte';
  let { selectedAgent, sessions } = $props();

  let term = null;
  let ws = null;
  let fitAddon = null;
  let resizeObs = null;
  let termContainer;
  let attached = null;

  async function attachTo(name) {
    if (attached === name) return;
    attached = name;
    if (ws) { ws.close(); ws = null; }
    if (resizeObs) { resizeObs.disconnect(); resizeObs = null; }
    if (term) { term.dispose(); term = null; }
    if (!name) return;

    const { Terminal } = await import('@xterm/xterm');
    const { FitAddon } = await import('@xterm/addon-fit');
    const sel = (typeof document !== 'undefined' && document.documentElement.dataset.theme === 'light')
      ? { background: '#fafafa', foreground: '#1a1a1a', cursor: '#1a1a1a', selectionBackground: '#cbd5e1' }
      : { background: '#0a0a0a', foreground: '#f5f5f5', cursor: '#f5f5f5', selectionBackground: '#2a3540' };
    term = new Terminal({ convertEol: true, fontFamily: 'ui-monospace, monospace', fontSize: 14, theme: sel, cursorBlink: true, cols: 120, rows: 32 });
    fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.open(termContainer);
    const tryFit = () => {
      const r = termContainer?.getBoundingClientRect();
      if (!r || r.width < 10 || r.height < 10) return;
      try { fitAddon.fit(); } catch {}
    };
    tryFit(); requestAnimationFrame(tryFit); setTimeout(tryFit, 100); setTimeout(tryFit, 400);
    resizeObs = new ResizeObserver(tryFit);
    resizeObs.observe(termContainer);

    ws = new WebSocket(`${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/api-v06/v1/sessions/${name}/attach`);
    ws.binaryType = 'arraybuffer';
    ws.onmessage = (ev) => {
      if (!term) return;
      if (ev.data instanceof ArrayBuffer) term.write(new Uint8Array(ev.data));
      else term.write(ev.data);
    };
    const sendResize = () => {
      if (!ws || ws.readyState !== WebSocket.OPEN || !term) return;
      try { ws.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols })); } catch {}
    };
    term.onResize(sendResize);
    ws.addEventListener('open', () => setTimeout(sendResize, 200));
    term.onData((d) => { if (ws && ws.readyState === WebSocket.OPEN) ws.send(d); });
    term.onSelectionChange(() => {
      const s = term.getSelection();
      if (s && navigator.clipboard) navigator.clipboard.writeText(s).catch(()=>{});
    });
    term.parser.registerOscHandler(52, (data) => {
      const parts = data.split(';');
      if (parts.length < 2) return true;
      try {
        const text = atob(parts[1]);
        if (navigator.clipboard) navigator.clipboard.writeText(text).catch(()=>{});
      } catch {}
      return true;
    });
  }

  $effect(() => { attachTo(selectedAgent); });

  onDestroy(() => {
    if (ws) ws.close();
    if (resizeObs) resizeObs.disconnect();
    if (term) term.dispose();
  });

  let info = $derived(sessions?.find(s => s.name === selectedAgent));
</script>

<div class="term-pane">
  <div class="term-title">
    {#if selectedAgent}
      <span class="dot" class:shepherd={info?.role === 'shepherd'}>{info?.role === 'shepherd' ? '✻' : '●'}</span>
      <span class="name">{selectedAgent}</span>
      <span class="sub">— live attach</span>
    {:else}
      <span class="sub">Pick an agent ← (or use Spawn to create one)</span>
    {/if}
  </div>
  <div class="term-body" bind:this={termContainer}></div>
</div>

<style>
  .term-pane { display: flex; flex-direction: column; height: 100%; background: var(--bg); }
  .term-title { padding: 0.3rem 0.7rem; background: var(--bg-elev); border-bottom: 1px solid var(--border); font-family: ui-monospace, monospace; font-size: 0.82rem; }
  .term-title .dot { color: var(--accent-2); margin-right: 0.3rem; }
  .term-title .dot.shepherd { color: var(--accent); }
  .term-title .name { font-weight: 600; }
  .term-title .sub { color: var(--fg-muted); margin-left: 0.4rem; font-size: 0.78rem; }
  .term-body { flex: 1; padding: 0.3rem 0.4rem; min-height: 0; overflow: hidden; }
  .term-body :global(.xterm) { height: 100%; }
  .term-body :global(.xterm-viewport) { height: 100% !important; }
</style>

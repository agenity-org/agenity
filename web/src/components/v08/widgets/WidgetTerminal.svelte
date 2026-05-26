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
  let oauthUrl = $state('');  // detected OAuth URL shown as clickable banner
  let rawBuf = '';             // rolling buffer for URL extraction (not shown)

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
    const sel = (typeof document !== 'undefined' && document.documentElement.dataset.theme === 'light')
      ? { background: '#fafafa', foreground: '#1a1a1a', cursor: '#1a1a1a', selectionBackground: '#cbd5e1' }
      : { background: '#0a0a0a', foreground: '#f5f5f5', cursor: '#f5f5f5', selectionBackground: '#2a3540' };
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

    ws = new WebSocket(`${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/api-v08/v1/sessions/${name}/attach`);
    ws.binaryType = 'arraybuffer';
    ws.onmessage = (ev) => {
      if (!term) return;
      let text = '';
      if (ev.data instanceof ArrayBuffer) {
        term.write(new Uint8Array(ev.data));
        try { text = new TextDecoder().decode(ev.data); } catch {}
      } else {
        term.write(ev.data);
        text = ev.data;
      }
      // Rolling buffer for OAuth URL detection (keep last 4 KB to avoid unbounded growth)
      rawBuf = (rawBuf + text).slice(-4096);
      const m = rawBuf.match(/https:\/\/claude\.(?:ai|com)\/[^\s\x1b\x07\x0d\x0a"']+/);
      if (m && m[0] !== oauthUrl) oauthUrl = m[0];
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

  $effect(() => { attachTo(myAgent); });

  onMount(() => {
    // Watch for --ws-font changes on documentElement style attribute.
    fontObs = new MutationObserver(() => applyFontAndRefit());
    fontObs.observe(document.documentElement, { attributes: true, attributeFilter: ['style'] });
  });

  onDestroy(() => {
    if (ws) ws.close();
    if (resizeObs) resizeObs.disconnect();
    if (term) term.dispose();
    if (fontObs) fontObs.disconnect();
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
  {#if oauthUrl}
  <div class="oauth-banner">
    <span class="oauth-icon">🔑</span>
    <span>Claude login required —</span>
    <a href={oauthUrl} target="_blank" rel="noreferrer noopener">click to authenticate</a>
    <button class="oauth-dismiss" onclick={() => oauthUrl = ''}>✕</button>
  </div>
  {/if}
  <div class="term-body" bind:this={termContainer}></div>
</div>

<style>
  .term-pane { display: flex; flex-direction: column; height: 100%; background: var(--bg); }
  .oauth-banner {
    display: flex; align-items: center; gap: 0.4rem;
    padding: 0.35rem 0.7rem; font-size: 0.8rem;
    background: color-mix(in srgb, var(--accent) 18%, var(--bg));
    border-bottom: 1px solid color-mix(in srgb, var(--accent) 35%, transparent);
    color: var(--fg);
  }
  .oauth-banner a { color: var(--accent); font-weight: 600; text-decoration: underline; }
  .oauth-banner a:hover { opacity: 0.8; }
  .oauth-icon { font-size: 1rem; }
  .oauth-dismiss {
    margin-left: auto; background: none; border: none; cursor: pointer;
    color: var(--fg-dim, #888); font-size: 0.75rem; padding: 0 0.2rem;
  }
  .oauth-dismiss:hover { color: var(--fg); }
  .term-body { flex: 1; padding: 0.3rem 0.4rem; min-height: 0; overflow: hidden; }
  .term-body :global(.xterm) { height: 100%; }
  .term-body :global(.xterm-viewport) { height: 100% !important; }
</style>

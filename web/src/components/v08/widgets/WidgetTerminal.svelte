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
    // #133 R5 (2026-05-27 operator review): SURGICAL palette restore —
    // the original #133/bec5e28 palette gave claude-code's /usage its
    // light-blue tone + tuned cyan/blue/green/yellow throughout the TUI.
    // The wholesale revert in b1e211b nuked all 16 slots which broke
    // every tuned color (operator: "all the other colors are also ruined").
    //
    // Root cause of the welcome-banner robot breakage was specifically
    // magenta (`#d75faf` pink) — claude-code's robot ART uses ANSI
    // magenta and expects the standard xterm purple, not chepherd-pink.
    // Solution: restore all 14 "tuned" slots and leave ONLY magenta /
    // brightMagenta / red / brightRed at xterm.js defaults so the
    // claude-code mascot renders correctly.
    const isLight = typeof document !== 'undefined' && document.documentElement.dataset.theme === 'light';
    const sel = isLight ? {
      background:          '#fafafa',
      foreground:          '#1a1a1a',
      cursor:              '#1a1a1a',
      cursorAccent:        '#fafafa',
      selectionBackground: '#cbd5e1',
      // 14 tuned slots for light theme (magenta + red intentionally
      // omitted → xterm.js defaults preserve claude-code's robot).
      black:        '#1a1a1a',
      green:        '#1e7e34',
      yellow:       '#b8860b',
      blue:         '#0066cc',
      cyan:         '#008a8a',
      white:        '#dcdcdc',
      brightBlack:  '#666666',
      brightGreen:  '#28a745',
      brightYellow: '#daa520',
      brightBlue:   '#3498db',
      brightCyan:   '#17a2b8',
      brightWhite:  '#1a1a1a',
    } : {
      background:          '#0a0a0a',
      foreground:          '#f5f5f5',
      cursor:              '#f5f5f5',
      cursorAccent:        '#0a0a0a',
      selectionBackground: '#2a3540',
      // 14 tuned slots for dark theme — cyan #5fd7ff is the
      // light-blue operator expects to see in /usage output.
      black:        '#0a0a0a',
      green:        '#5fd75f',
      yellow:       '#ffa500', // chepherd accent
      blue:         '#5fafff',
      cyan:         '#5fd7ff',
      white:        '#dadada',
      brightBlack:  '#5a5a5a',
      brightGreen:  '#87ff87',
      brightYellow: '#ffd75f',
      brightBlue:   '#87afff',
      brightCyan:   '#afffff',
      brightWhite:  '#ffffff',
    };
    // #150 — convertEol: false. The native PTY already emits CRLF for
    // line endings on terminal-style output; setting convertEol: true
    // adds a CR before EVERY LF including ones in PTY-managed escape
    // sequences, which mangles claude-code's box-drawing output.
    // Font: prefer common terminal-emulator monospace fonts that render
    // half-block + box-drawing glyphs cleanly (claude-code's mascot uses
    // U+2580/2584 half-blocks). ui-monospace on linux falls back to a
    // font that renders these as solid blocks → the welcome-banner robot
    // collapses into a colored rectangle. Standard terminal fonts handle
    // them correctly.
    term = new Terminal({
      convertEol: false,
      fontFamily: '"DejaVu Sans Mono", "Liberation Mono", "Menlo", "Consolas", "Cascadia Code", "Fira Code", monospace',
      fontSize: currentWsFont(),
      lineHeight: 1.0,
      letterSpacing: 0,
      theme: sel,
      cursorBlink: true,
      cols: 120,
      rows: 32,
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
  <div class="term-body" bind:this={termContainer}></div>
</div>

<style>
  .term-pane { display: flex; flex-direction: column; height: 100%; background: var(--bg); }
  .term-body { flex: 1; padding: 0.3rem 0.4rem; min-height: 0; overflow: hidden; }
  .term-body :global(.xterm) { height: 100%; }
  .term-body :global(.xterm-viewport) { height: 100% !important; }
</style>

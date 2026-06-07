<!--
  MuxPane — the recursive tiling node for the "mux" dashboard.

  A node is one of:
    { kind:'pane', id, widget, config:{agent} }   — a leaf (one terminal/widget)
    { kind:'h', ratio, a, b }                      — left | right split, col-resize
    { kind:'v', ratio, a, b }                      — top / bottom split, row-resize

  Leaf panes carry a per-pane agent binding (config.agent) so two terminal
  panes side-by-side show two different agents — this is the "pane switching"
  + "multiple terminals visible" requirement. Splits have draggable dividers
  (the "pane resizing" requirement). The tree is arbitrary depth (the "layout
  flexibility" requirement) and persisted via saveLayout().

  All visuals come from CSS variables defined by the parent shell, so both
  themes are covered with zero hard-coded colors.
-->
<script>
  import Self from './MuxPane.svelte';
  import WidgetTerminal from '../v08/widgets/WidgetTerminal.svelte';
  import TeamTranscript from '../TeamTranscript.svelte';
  import MuxInspector from './MuxInspector.svelte';
  import MuxEvents from './MuxEvents.svelte';
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let {
    node,
    sessions = [],
    memberships = [],
    events = [],
    selectedAgent = null,
    focusedPaneID = '',
    selectAgent = () => {},
    focusPane = () => {},
    splitPane = () => {},
    closePane = () => {},
    setPaneWidget = () => {},
    setPaneAgent = () => {},
    setRatio = () => {},
  } = $props();

  // ── divider drag (pane resizing) ──────────────────────────────────
  let containerEl;
  let dragging = false;
  let startPos = 0;
  let startRatio = 0;

  function startDrag(e) {
    e.preventDefault();
    dragging = true;
    startPos = node.kind === 'h' ? e.clientX : e.clientY;
    startRatio = node.ratio ?? 0.5;
    window.addEventListener('mousemove', onDrag);
    window.addEventListener('mouseup', endDrag);
    document.body.style.cursor = node.kind === 'h' ? 'col-resize' : 'row-resize';
    document.body.style.userSelect = 'none';
  }
  function onDrag(e) {
    if (!dragging || !containerEl) return;
    const rect = containerEl.getBoundingClientRect();
    const total = node.kind === 'h' ? rect.width : rect.height;
    if (total <= 0) return;
    const delta = (node.kind === 'h' ? e.clientX : e.clientY) - startPos;
    let r = startRatio + delta / total;
    r = Math.max(0.12, Math.min(0.88, r));
    setRatio(node.id, r);
  }
  function endDrag() {
    dragging = false;
    window.removeEventListener('mousemove', onDrag);
    window.removeEventListener('mouseup', endDrag);
    document.body.style.cursor = '';
    document.body.style.userSelect = '';
  }

  // ── leaf-pane helpers ─────────────────────────────────────────────
  const WIDGETS = [
    { id: 'terminal', label: 'terminal', glyph: '▦', desc: 'live PTY for an agent' },
    { id: 'team-transcript', label: 'transcript', glyph: '💬', desc: 'team conversation' },
    { id: 'inspector', label: 'inspector', glyph: 'ⓘ', desc: 'focused agent — identity · scorecard' },
    { id: 'events', label: 'events', glyph: '⏱', desc: 'live runtime event stream' },
  ];

  // Per-pane picker overlay state (which screen the empty/menu pane shows).
  let picker = $state(null); // null | 'widget' | 'agent'

  let boundAgent = $derived(
    node.kind === 'pane' && node.widget === 'terminal'
      ? (node.config?.agent || selectedAgent || '')
      : ''
  );
  let boundSession = $derived((sessions || []).find((s) => s.name === boundAgent) || null);
  let ident = $derived(boundSession ? agentIdentity(boundSession) : null);

  function relAge(at) {
    if (!at) return '—';
    const s = Math.floor((Date.now() - new Date(at).getTime()) / 1000);
    if (s < 60) return `${s}s`;
    if (s < 3600) return `${Math.floor(s / 60)}m`;
    if (s < 86400) return `${Math.floor(s / 3600)}h`;
    return `${Math.floor(s / 86400)}d`;
  }

  function chooseWidget(w) {
    if (w === 'terminal') { setPaneWidget(node.id, 'terminal'); picker = 'agent'; return; }
    setPaneWidget(node.id, w);
    picker = null;
  }
  function chooseAgent(name) {
    setPaneAgent(node.id, name);
    picker = null;
  }

  // Title for the pane header.
  let title = $derived.by(() => {
    if (node.kind !== 'pane') return '';
    if (node.widget === 'terminal') return boundAgent ? boundAgent : 'pick agent';
    const w = WIDGETS.find((x) => x.id === node.widget);
    return w ? w.label : node.widget;
  });
  let titleGlyph = $derived.by(() => {
    if (node.kind !== 'pane') return '';
    if (node.widget === 'terminal') return ident ? ident.icon : '▦';
    const w = WIDGETS.find((x) => x.id === node.widget);
    return w ? w.glyph : '▦';
  });
</script>

{#if node.kind === 'h' || node.kind === 'v'}
  <div class="split {node.kind}" bind:this={containerEl}>
    <div class="cell" style={node.kind === 'h' ? `width:${(node.ratio ?? 0.5) * 100}%` : `height:${(node.ratio ?? 0.5) * 100}%`}>
      <Self node={node.a} {sessions} {memberships} {events} {selectedAgent} {focusedPaneID}
            {selectAgent} {focusPane} {splitPane} {closePane} {setPaneWidget} {setPaneAgent} {setRatio} />
    </div>
    <div
      class="divider {node.kind} {dragging ? 'dragging' : ''}"
      onmousedown={startDrag}
      role="separator"
      aria-orientation={node.kind === 'h' ? 'vertical' : 'horizontal'}
      title="drag to resize"
    ><span class="grip"></span></div>
    <div class="cell" style={node.kind === 'h' ? `width:${(1 - (node.ratio ?? 0.5)) * 100}%` : `height:${(1 - (node.ratio ?? 0.5)) * 100}%`}>
      <Self node={node.b} {sessions} {memberships} {events} {selectedAgent} {focusedPaneID}
            {selectAgent} {focusPane} {splitPane} {closePane} {setPaneWidget} {setPaneAgent} {setRatio} />
    </div>
  </div>
{:else}
  <!-- leaf pane -->
  <div
    class="pane {focusedPaneID === node.id ? 'focused' : ''}"
    data-pane-id={node.id}
    onmousedown={() => { focusPane(node.id); if (node.widget === 'terminal' && boundAgent) selectAgent(boundAgent, { fromPane: node.id }); }}
    role="group"
  >
    <header class="bar" style={ident && focusedPaneID === node.id ? `box-shadow: inset 0 -2px 0 ${ident.color}` : ''}>
      <span class="tag" style={ident ? `color:${ident.color}` : ''}>
        <span class="glyph">{titleGlyph}</span>
        <span class="name">{title}</span>
      </span>
      {#if node.widget === 'terminal' && boundSession}
        <span class="dot {!boundSession.exited && boundSession.live !== false ? 'live' : 'dead'}" title={boundSession.exited ? 'exited' : 'live'}></span>
        <span class="meta">{relAge(boundSession.created_at)}</span>
        {#if boundSession.role}<span class="meta role">{boundSession.role}</span>{/if}
      {/if}
      <span class="flex"></span>
      <button class="bb" title="switch this pane to another agent / widget" onclick={(e) => { e.stopPropagation(); picker = picker ? null : (node.widget === 'terminal' ? 'agent' : 'widget'); }} aria-label="switch pane">⇄</button>
      <button class="bb" title="split right (Prefix |)" onclick={(e) => { e.stopPropagation(); splitPane(node.id, 'h'); }} aria-label="split right">▥</button>
      <button class="bb" title="split down (Prefix -)" onclick={(e) => { e.stopPropagation(); splitPane(node.id, 'v'); }} aria-label="split down">▤</button>
      <button class="bb close" title="close pane (Prefix x)" onclick={(e) => { e.stopPropagation(); closePane(node.id); }} aria-label="close pane">✕</button>
    </header>

    <div class="body">
      {#if node.widget === 'terminal' && !boundAgent}
        <!-- terminal needs an agent: show the agent picker inline -->
        <div class="picker">
          <h4>Bind this pane to an agent</h4>
          <ul class="plist">
            {#each sessions as s (s.name)}
              {@const id = agentIdentity(s)}
              <li>
                <button class="prow" onclick={() => chooseAgent(s.name)}>
                  <span class="prow-ic" style="color:{id.color}">{id.icon}</span>
                  <span class="prow-nm">{s.name}</span>
                  <span class="prow-rl">{s.role || ''}</span>
                  <span class="prow-dot {s.exited ? 'dead' : 'live'}"></span>
                </button>
              </li>
            {/each}
            {#if !sessions.length}<li class="pempty">No agents running — spawn one with <kbd>+ spawn</kbd>.</li>{/if}
          </ul>
        </div>
      {:else if node.widget === 'terminal'}
        <WidgetTerminal {selectedAgent} {sessions} {node} />
      {:else if node.widget === 'team-transcript'}
        <TeamTranscript team={node.config?.team || 'default'} />
      {:else if node.widget === 'inspector'}
        <MuxInspector {sessions} {memberships} {selectedAgent} {selectAgent} />
      {:else if node.widget === 'events'}
        <MuxEvents {events} {sessions} />
      {:else}
        <div class="picker">
          <h4>Pick a widget for this pane</h4>
          <ul class="plist">
            {#each WIDGETS as w (w.id)}
              <li><button class="prow" onclick={() => chooseWidget(w.id)}>
                <span class="prow-ic">{w.glyph}</span>
                <span class="prow-nm">{w.label}</span>
                <span class="prow-rl">{w.desc}</span>
              </button></li>
            {/each}
          </ul>
        </div>
      {/if}

      {#if picker}
        <!-- overlay picker, opened from the ⇄ button -->
        <div class="overlay" onmousedown={(e) => { if (e.target === e.currentTarget) picker = null; }} role="presentation">
          <div class="card">
            <header class="card-head">
              {#if picker === 'agent'}
                <button class="back" onclick={() => (picker = 'widget')}>← widget</button>
                <h4>Bind agent</h4>
              {:else}
                <h4>Switch widget</h4>
              {/if}
              <button class="x" onclick={() => (picker = null)}>✕</button>
            </header>
            {#if picker === 'widget'}
              <ul class="plist">
                {#each WIDGETS as w (w.id)}
                  <li><button class="prow" onclick={() => chooseWidget(w.id)}>
                    <span class="prow-ic">{w.glyph}</span>
                    <span class="prow-nm">{w.label}</span>
                    <span class="prow-rl">{w.desc}</span>
                  </button></li>
                {/each}
              </ul>
            {:else}
              <ul class="plist">
                {#each sessions as s (s.name)}
                  {@const id = agentIdentity(s)}
                  <li><button class="prow" onclick={() => chooseAgent(s.name)}>
                    <span class="prow-ic" style="color:{id.color}">{id.icon}</span>
                    <span class="prow-nm">{s.name}</span>
                    <span class="prow-rl">{s.role || ''}</span>
                    <span class="prow-dot {s.exited ? 'dead' : 'live'}"></span>
                  </button></li>
                {/each}
                {#if !sessions.length}<li class="pempty">No agents running.</li>{/if}
              </ul>
            {/if}
          </div>
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .split { display: flex; width: 100%; height: 100%; overflow: hidden; }
  .split.h { flex-direction: row; }
  .split.v { flex-direction: column; }
  .cell { overflow: hidden; min-width: 0; min-height: 0; }

  /* tmux-style divider: thin line, fat hit-area, glowing on hover/drag */
  .divider { position: relative; flex: 0 0 auto; background: var(--mux-border); transition: background 0.12s; }
  .divider.h { width: 2px; cursor: col-resize; }
  .divider.v { height: 2px; cursor: row-resize; }
  .divider::after { content: ''; position: absolute; inset: 0; }
  .divider.h::after { left: -4px; right: -4px; }
  .divider.v::after { top: -4px; bottom: -4px; }
  .divider:hover, .divider.dragging { background: var(--mux-accent); }
  .grip { position: absolute; background: var(--mux-accent); opacity: 0; transition: opacity 0.12s; border-radius: 2px; }
  .divider.h .grip { width: 2px; height: 24px; top: 50%; left: 0; transform: translateY(-50%); }
  .divider.v .grip { height: 2px; width: 24px; left: 50%; top: 0; transform: translateX(-50%); }
  .divider:hover .grip, .divider.dragging .grip { opacity: 1; }

  /* leaf pane shell */
  .pane {
    display: flex; flex-direction: column; height: 100%; position: relative;
    background: var(--mux-bg); border: 1px solid var(--mux-border);
    overflow: hidden;
  }
  .pane.focused { border-color: var(--mux-accent); }

  .bar {
    display: flex; align-items: center; gap: 0.35rem;
    padding: 0.18rem 0.35rem 0.18rem 0.5rem;
    background: var(--mux-bar); border-bottom: 1px solid var(--mux-border);
    font-family: var(--mux-mono); font-size: 0.72rem;
    min-height: 1.55rem;
  }
  .tag { display: inline-flex; align-items: center; gap: 0.35rem; min-width: 0; color: var(--mux-fg-muted); font-weight: 600; }
  .tag .glyph { font-size: 0.82rem; line-height: 1; }
  .tag .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .pane.focused .tag { color: var(--mux-fg); }
  .dot { width: 7px; height: 7px; border-radius: 50%; flex: 0 0 auto; }
  .dot.live { background: var(--mux-ok); box-shadow: 0 0 5px var(--mux-ok); }
  .dot.dead { background: var(--mux-fg-faint); }
  .meta { color: var(--mux-fg-faint); font-size: 0.66rem; white-space: nowrap; }
  .meta.role { color: var(--mux-fg-muted); opacity: 0.8; }
  .flex { flex: 1; }
  .bb {
    background: transparent; border: none; color: var(--mux-fg-faint);
    cursor: pointer; padding: 0 0.25rem; font-size: 0.78rem; line-height: 1;
    border-radius: 3px; height: 1.25rem;
  }
  .bb:hover { color: var(--mux-accent); background: var(--mux-hover); }
  .bb.close:hover { color: var(--mux-danger); background: var(--mux-danger-soft); }

  .body { flex: 1; min-height: 0; overflow: hidden; position: relative; background: var(--mux-bg); }

  /* inline / overlay pickers */
  .picker { height: 100%; overflow-y: auto; padding: 1rem; }
  .picker h4 { margin: 0 0 0.7rem; font-size: 0.82rem; color: var(--mux-fg); font-weight: 600; text-align: center; }
  .overlay { position: absolute; inset: 0; background: var(--mux-scrim); display: flex; align-items: flex-start; justify-content: center; padding-top: 1.5rem; z-index: 30; }
  .card { width: min(92%, 22rem); background: var(--mux-bar); border: 1px solid var(--mux-border-strong); border-radius: 8px; padding: 0.7rem 0.8rem; box-shadow: var(--mux-shadow); }
  .card-head { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; }
  .card-head h4 { margin: 0; flex: 1; font-size: 0.82rem; color: var(--mux-fg); }
  .card-head .back { background: transparent; border: none; color: var(--mux-accent); cursor: pointer; font-size: 0.74rem; font-family: var(--mux-mono); padding: 0.1rem 0.3rem; border-radius: 4px; }
  .card-head .back:hover { background: var(--mux-hover); }
  .card-head .x { background: transparent; border: none; color: var(--mux-fg-faint); cursor: pointer; font-size: 0.85rem; }
  .card-head .x:hover { color: var(--mux-danger); }

  .plist { list-style: none; margin: 0; padding: 0; }
  .prow {
    display: flex; align-items: center; gap: 0.55rem; width: 100%;
    background: transparent; border: 1px solid transparent; border-radius: 6px;
    padding: 0.4rem 0.5rem; cursor: pointer; color: var(--mux-fg);
    font-family: var(--mux-mono); font-size: 0.8rem; text-align: left;
  }
  .prow:hover { background: var(--mux-hover); border-color: var(--mux-border); }
  .prow-ic { width: 1.2rem; text-align: center; flex: 0 0 auto; color: var(--mux-accent); }
  .prow-nm { font-weight: 600; white-space: nowrap; }
  .prow-rl { color: var(--mux-fg-faint); font-size: 0.7rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
  .prow-dot { width: 7px; height: 7px; border-radius: 50%; flex: 0 0 auto; }
  .prow-dot.live { background: var(--mux-ok); }
  .prow-dot.dead { background: var(--mux-fg-faint); }
  .pempty { color: var(--mux-fg-muted); font-size: 0.78rem; padding: 0.6rem 0.5rem; }
  kbd { font-family: var(--mux-mono); background: var(--mux-bg); border: 1px solid var(--mux-border); border-radius: 3px; padding: 0 0.3rem; font-size: 0.72rem; }
</style>

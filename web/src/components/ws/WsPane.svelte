<!--
  WsPane — recursive renderer for a workspace's split-tree.

  A `leaf` renders a WsLeaf (any of the 7 pane types). A `split` renders
  two child WsPanes with a draggable divider between them (#7 — every
  split, H and V, resizes via its own splitter). <svelte:self> recurses so
  arbitrarily deep arrangements compose. Each leaf receives a `collapse`
  descriptor derived from its parent split for the chevron affordance (#1).

  All live data (events/peers/tasks) + the focusedSession + the context-menu
  callback are threaded through the recursion unchanged.
-->
<script>
  import WsLeaf from './WsLeaf.svelte';
  import * as T from './layoutWs.js';

  let {
    node,
    sessions = [],
    teams = [],
    memberships = [],
    events = [],
    peers = [],
    tasks = [],
    focusedSession = null,
    selectedAgent = '',
    focusedLeafId = '',
    maximizedLeafId = '',
    parentSplit = null,
    parentSide = '',
    onfocusleaf = () => {},
    onsetratio = () => {},
    onsplit = () => {},
    onclose = () => {},
    onsetagent = () => {},
    onsetwidget = () => {},
    onmaximize = () => {},
    oncollapse = () => {},
    onleafctxmenu = () => {},
    onpickagent = () => {},
    onopenagentnew = () => {},
    onagentctxmenu = () => {},
    onteamctxmenu = () => {},
    canClose = true,
  } = $props();

  let container = $state(null);
  let dragging = $state(false);

  function startDrag(ev) {
    ev.preventDefault();
    dragging = true;
    const horiz = node.kind === 'h';
    const rect = container.getBoundingClientRect();
    function onMove(e) {
      const point = e.touches ? e.touches[0] : e;
      let ratio = horiz ? (point.clientX - rect.left) / rect.width : (point.clientY - rect.top) / rect.height;
      onsetratio(node.id, ratio);
    }
    function onUp() {
      dragging = false;
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
      window.removeEventListener('touchmove', onMove);
      window.removeEventListener('touchend', onUp);
    }
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    window.addEventListener('touchmove', onMove, { passive: false });
    window.addEventListener('touchend', onUp);
  }

  let aPct = $derived(node.kind !== 'leaf' ? (node.ratio ?? 0.5) * 100 : 0);
  let leafCollapse = $derived.by(() => {
    if (node.kind !== 'leaf' || !parentSplit) return null;
    const small = T.collapsedSide(parentSplit);
    return { axis: parentSplit.kind, collapsed: small === parentSide };
  });
</script>

{#if node.kind === 'leaf'}
  <WsLeaf
    {node}
    {sessions}
    {teams}
    {memberships}
    {events}
    {peers}
    {tasks}
    {focusedSession}
    {selectedAgent}
    focused={focusedLeafId === node.id}
    {canClose}
    maximized={maximizedLeafId === node.id}
    collapse={leafCollapse}
    onfocus={() => onfocusleaf(node.id)}
    onsplit={(dir) => onsplit(node.id, dir)}
    onclose={() => onclose(node.id)}
    onsetagent={(name) => onsetagent(node.id, name)}
    onsetwidget={(w) => onsetwidget(node.id, w)}
    onmaximize={() => onmaximize(node.id)}
    oncollapse={() => oncollapse(node.id)}
    onctxmenu={(x, y) => onleafctxmenu(node.id, x, y)}
    {onpickagent}
    {onopenagentnew}
    {onagentctxmenu}
    {onteamctxmenu}
  />
{:else}
  <div class="split {node.kind === 'h' ? 'split-h' : 'split-v'}" bind:this={container}>
    <div class="split-side" style={node.kind === 'h' ? `width:${aPct}%` : `height:${aPct}%`}>
      <svelte:self
        node={node.a}
        {sessions} {teams} {memberships} {events} {peers} {tasks} {focusedSession} {selectedAgent}
        {focusedLeafId} {maximizedLeafId}
        parentSplit={node} parentSide="a"
        {onfocusleaf} {onsetratio} {onsplit} {onclose} {onsetagent} {onsetwidget} {onmaximize} {oncollapse} {onleafctxmenu} {onpickagent} {onopenagentnew} {onagentctxmenu} {onteamctxmenu}
        canClose={true}
      />
    </div>
    <div class="divider {node.kind === 'h' ? 'divider-h' : 'divider-v'} {dragging ? 'is-dragging' : ''}" onmousedown={startDrag} ontouchstart={startDrag} role="separator" aria-orientation={node.kind === 'h' ? 'vertical' : 'horizontal'} aria-label="Resize panes" tabindex="-1">
      <span class="grip"></span>
    </div>
    <div class="split-side" style={node.kind === 'h' ? `width:${100 - aPct}%` : `height:${100 - aPct}%`}>
      <svelte:self
        node={node.b}
        {sessions} {teams} {memberships} {events} {peers} {tasks} {focusedSession} {selectedAgent}
        {focusedLeafId} {maximizedLeafId}
        parentSplit={node} parentSide="b"
        {onfocusleaf} {onsetratio} {onsplit} {onclose} {onsetagent} {onsetwidget} {onmaximize} {oncollapse} {onleafctxmenu} {onpickagent} {onopenagentnew} {onagentctxmenu} {onteamctxmenu}
        canClose={true}
      />
    </div>
  </div>
{/if}

<style>
  .split { display: flex; width: 100%; height: 100%; min-width: 0; min-height: 0; }
  .split-h { flex-direction: row; }
  .split-v { flex-direction: column; }
  .split-side { position: relative; min-width: 0; min-height: 0; overflow: hidden; }

  .divider { flex: 0 0 auto; position: relative; display: flex; align-items: center; justify-content: center; background: transparent; transition: background 0.15s ease; z-index: 4; }
  .divider-h { width: 10px; cursor: col-resize; }
  .divider-v { height: 10px; cursor: row-resize; }
  .divider:hover, .divider.is-dragging { background: color-mix(in srgb, var(--calm-accent) 22%, transparent); }

  .grip { background: var(--calm-border-strong); border-radius: 999px; transition: background 0.15s ease; }
  .divider-h .grip { width: 3px; height: 34px; }
  .divider-v .grip { width: 34px; height: 3px; }
  .divider:hover .grip, .divider.is-dragging .grip { background: var(--calm-accent); }
</style>

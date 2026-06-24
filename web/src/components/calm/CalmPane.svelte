<!--
  CalmPane — recursive renderer for the calm split-tree.

  A `leaf` renders the chosen widget inside a CalmLeaf frame (header +
  body). A `split` renders two child CalmPanes with a draggable divider
  between them; dragging adjusts the split ratio (HARD REQ #7: EVERY
  split is resizable — both H and V — via its own draggable splitter).

  Self-referential: <svelte:self> renders the children, so arbitrarily
  deep H/V splits compose. Each leaf also receives a `collapse` descriptor
  derived from its parent split so the leaf header can show an intuitive
  chevron-arrow collapse/expand affordance along the split axis (#6).
-->
<script>
  import CalmLeaf from './CalmLeaf.svelte';
  import * as T from './layoutTree.js';

  let {
    node,
    sessions = [],
    focusedLeafId = '',
    maximizedLeafId = '',
    // The parent split this node hangs under (null at the root) plus the
    // side ('a'|'b') this node occupies. Used to derive the per-leaf
    // collapse affordance + axis.
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
    canClose = true,
  } = $props();

  let container = $state(null);
  let dragging = $state(false);

  function startDrag(ev) {
    ev.preventDefault();
    dragging = true;
    const horiz = node.kind === 'h'; // 'h' = side-by-side, divider moves on X
    const rect = container.getBoundingClientRect();

    function onMove(e) {
      const point = e.touches ? e.touches[0] : e;
      let ratio;
      if (horiz) {
        ratio = (point.clientX - rect.left) / rect.width;
      } else {
        ratio = (point.clientY - rect.top) / rect.height;
      }
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

  // For a leaf: derive its collapse descriptor from the parent split.
  // axis = parent split kind ('h'|'v'); collapsed = whether THIS side is
  // the one currently driven small.
  let leafCollapse = $derived.by(() => {
    if (node.kind !== 'leaf' || !parentSplit) return null;
    const small = T.collapsedSide(parentSplit); // 'a' | 'b' | ''
    return { axis: parentSplit.kind, collapsed: small === parentSide };
  });
</script>

{#if node.kind === 'leaf'}
  <CalmLeaf
    {node}
    {sessions}
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
  />
{:else}
  <div
    class="split {node.kind === 'h' ? 'split-h' : 'split-v'}"
    bind:this={container}
  >
    <div class="split-side" style={node.kind === 'h' ? `width:${aPct}%` : `height:${aPct}%`}>
      <svelte:self
        node={node.a}
        {sessions}
        {focusedLeafId}
        {maximizedLeafId}
        parentSplit={node}
        parentSide="a"
        {onfocusleaf}
        {onsetratio}
        {onsplit}
        {onclose}
        {onsetagent}
        {onsetwidget}
        {onmaximize}
        {oncollapse}
        canClose={true}
      />
    </div>
    <div
      class="divider {node.kind === 'h' ? 'divider-h' : 'divider-v'} {dragging ? 'is-dragging' : ''}"
      onmousedown={startDrag}
      ontouchstart={startDrag}
      role="separator"
      aria-orientation={node.kind === 'h' ? 'vertical' : 'horizontal'}
      aria-label="Resize panes"
      tabindex="-1"
    >
      <span class="grip"></span>
    </div>
    <div class="split-side" style={node.kind === 'h' ? `width:${100 - aPct}%` : `height:${100 - aPct}%`}>
      <svelte:self
        node={node.b}
        {sessions}
        {focusedLeafId}
        {maximizedLeafId}
        parentSplit={node}
        parentSide="b"
        {onfocusleaf}
        {onsetratio}
        {onsplit}
        {onclose}
        {onsetagent}
        {onsetwidget}
        {onmaximize}
        {oncollapse}
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

  .divider {
    flex: 0 0 auto;
    position: relative;
    display: flex; align-items: center; justify-content: center;
    background: transparent;
    transition: background 0.15s ease;
    z-index: 4;
  }
  .divider-h { width: 10px; cursor: col-resize; }
  .divider-v { height: 10px; cursor: row-resize; }
  .divider:hover, .divider.is-dragging { background: color-mix(in srgb, var(--calm-accent) 22%, transparent); }

  .grip {
    background: var(--calm-border-strong);
    border-radius: 999px;
    transition: background 0.15s ease;
  }
  .divider-h .grip { width: 3px; height: 34px; }
  .divider-v .grip { width: 34px; height: 3px; }
  .divider:hover .grip, .divider.is-dragging .grip { background: var(--calm-accent); }
</style>

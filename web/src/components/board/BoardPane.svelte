<!--
  BoardPane — recursive renderer for the flexible terminal-pane tree.

  Tree node shapes:
    { kind:'pane',  id, agent }              leaf — a live terminal bound to `agent`
    { kind:'h', id, ratio, a, b }            horizontal split (left | right), draggable divider
    { kind:'v', id, ratio, a, b }            vertical split (top / bottom), draggable divider

  HARD REQUIREMENTS satisfied here:
    1. PANE SWITCHING  — each leaf has its own agent picker; pick rebinds.
    2. PANE RESIZING   — draggable dividers mutate node.ratio live.
    3. LAYOUT FLEX     — split ⬍/⬌ + close on every leaf builds arbitrary trees.

  Self-recursive (imports itself) per Svelte 5 component recursion.
-->
<script>
  import BoardPane from './BoardPane.svelte';
  import BoardTerminal from './BoardTerminal.svelte';
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let {
    node,
    sessions = [],
    focusedPaneId = '',
    onsplit = () => {},
    onclose = () => {},
    onpick = () => {},
    onfocuspane = () => {},
    closable = true,
  } = $props();

  let container;

  // ----- divider drag -----
  let dragging = $state(false);
  function startDrag(e) {
    e.preventDefault();
    dragging = true;
    const rect = container.getBoundingClientRect();
    const horiz = node.kind === 'h';
    const move = (ev) => {
      const p = ('touches' in ev && ev.touches[0]) ? ev.touches[0] : ev;
      let r = horiz
        ? (p.clientX - rect.left) / rect.width
        : (p.clientY - rect.top) / rect.height;
      r = Math.max(0.12, Math.min(0.88, r));
      node.ratio = r;
    };
    const up = () => {
      dragging = false;
      window.removeEventListener('mousemove', move);
      window.removeEventListener('mouseup', up);
      window.removeEventListener('touchmove', move);
      window.removeEventListener('touchend', up);
    };
    window.addEventListener('mousemove', move);
    window.addEventListener('mouseup', up);
    window.addEventListener('touchmove', move, { passive: false });
    window.addEventListener('touchend', up);
  }

  let id = $derived(node?.kind === 'pane' ? agentIdentity((sessions || []).find(s => s.name === node.agent) || node.agent || '') : null);
  let live = $derived(node?.kind === 'pane' ? (sessions || []).filter(s => !s.exited) : []);
</script>

{#if node.kind === 'pane'}
  <div
    class="leaf"
    class:focused={focusedPaneId === node.id}
    data-pane-id={node.id}
    onpointerdowncapture={() => onfocuspane(node.id)}
    role="group"
  >
    <header class="phead">
      <span class="pdot" style="background:{id?.color || 'var(--board-fg-faint)'}"></span>
      <select
        class="picker"
        value={node.agent || ''}
        onchange={(e) => onpick(node.id, e.currentTarget.value)}
        title="bind this pane to an agent"
      >
        <option value="">— pick agent —</option>
        {#each live as s}
          <option value={s.name}>{s.name}{s.role ? ' (' + s.role + ')' : ''}</option>
        {/each}
      </select>
      <span class="grow"></span>
      <button class="ptool" title="split right" onclick={() => onsplit(node.id, 'h')}>⬌</button>
      <button class="ptool" title="split down" onclick={() => onsplit(node.id, 'v')}>⬍</button>
      {#if closable}
        <button class="ptool close" title="close pane" onclick={() => onclose(node.id)}>✕</button>
      {/if}
    </header>
    <div class="pterm">
      <BoardTerminal agent={node.agent || ''} />
    </div>
  </div>
{:else}
  <div class="split {node.kind}" class:dragging bind:this={container}>
    <div class="slot" style={node.kind === 'h' ? `flex:${node.ratio}` : `flex:${node.ratio}`}>
      <BoardPane node={node.a} {sessions} {focusedPaneId} {onsplit} {onclose} {onpick} {onfocuspane} />
    </div>
    <div
      class="divider {node.kind}"
      role="separator"
      tabindex="-1"
      aria-orientation={node.kind === 'h' ? 'vertical' : 'horizontal'}
      onmousedown={startDrag}
      ontouchstart={startDrag}
    ><span class="grip"></span></div>
    <div class="slot" style={`flex:${1 - node.ratio}`}>
      <BoardPane node={node.b} {sessions} {focusedPaneId} {onsplit} {onclose} {onpick} {onfocuspane} />
    </div>
  </div>
{/if}

<style>
  .leaf {
    display: flex; flex-direction: column; height: 100%; min-height: 0; min-width: 0;
    background: var(--board-term-bg);
    border: 1px solid var(--board-border);
    border-radius: 10px; overflow: hidden;
    transition: border-color .12s ease, box-shadow .12s ease;
  }
  .leaf.focused { border-color: var(--board-accent); box-shadow: 0 0 0 1px var(--board-accent-soft); }

  .phead {
    display: flex; align-items: center; gap: 0.45rem;
    padding: 0.3rem 0.45rem; flex: 0 0 auto;
    background: var(--board-surface); border-bottom: 1px solid var(--board-border);
  }
  .pdot { width: 8px; height: 8px; border-radius: 50%; flex: 0 0 auto; }
  .picker {
    background: var(--board-input); color: var(--board-fg);
    border: 1px solid var(--board-border-strong); border-radius: 6px;
    font-size: 0.76rem; padding: 0.18rem 0.4rem; max-width: 16rem;
  }
  .picker:focus { outline: none; border-color: var(--board-accent); }
  .grow { flex: 1; }
  .ptool {
    background: transparent; border: 0; color: var(--board-fg-muted);
    cursor: pointer; font-size: 0.85rem; line-height: 1;
    width: 24px; height: 24px; border-radius: 5px;
    display: inline-flex; align-items: center; justify-content: center;
  }
  .ptool:hover { background: var(--board-hover); color: var(--board-fg); }
  .ptool.close:hover { color: var(--board-danger); }

  .pterm { flex: 1; min-height: 0; }

  .split { display: flex; height: 100%; width: 100%; min-height: 0; min-width: 0; gap: 0; }
  .split.h { flex-direction: row; }
  .split.v { flex-direction: column; }
  .slot { min-width: 0; min-height: 0; display: flex; }
  .slot > :global(*) { flex: 1; min-width: 0; min-height: 0; }

  .divider { flex: 0 0 auto; position: relative; background: transparent; }
  .divider.h { width: 9px; cursor: col-resize; }
  .divider.v { height: 9px; cursor: row-resize; }
  .divider .grip {
    position: absolute; inset: 0; margin: auto; background: var(--board-border-strong);
    border-radius: 999px; transition: background .12s ease;
  }
  .divider.h .grip { width: 3px; height: 36px; }
  .divider.v .grip { height: 3px; width: 36px; }
  .divider:hover .grip { background: var(--board-accent); }
  .split.dragging .grip { background: var(--board-accent); }
</style>

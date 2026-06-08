<!--
  WsStrip — the WORKSPACE STRIP in the header. Each chip is a whole saved
  DESKTOP (not a content tab): clicking switches to that workspace, the
  numeric badge hints its Ctrl+N shortcut (1..9). '+' adds a workspace;
  double-click OR right-click renames; drag a chip to reorder (#strip).
  Right-click opens a context menu: Rename / Duplicate / Move left / Move
  right / Delete (#6).

  The root owns the workspace array + all mutations; this component is a
  thin, controlled view that emits intents.
-->
<script>
  let {
    workspaces = [],
    activeId = '',
    renamingId = '',
    renameVal = $bindable(''),
    onselect = () => {},
    onadd = () => {},
    onstartrename = () => {},
    oncommitrename = () => {},
    oncancelrename = () => {},
    onreorder = () => {},      // (fromIndex, toIndex)
    onctxmenu = () => {},      // (workspace, clientX, clientY)
  } = $props();

  let dragId = $state('');

  function autofocus(node) { try { node.focus(); node.select?.(); } catch {} return {}; }

  function onDragStart(e, w) {
    dragId = w.id;
    try { e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', w.id); } catch {}
  }
  function onDragOver(e) { e.preventDefault(); try { e.dataTransfer.dropEffect = 'move'; } catch {} }
  function onDrop(e, target) {
    e.preventDefault();
    if (!dragId || dragId === target.id) { dragId = ''; return; }
    const from = workspaces.findIndex((w) => w.id === dragId);
    const to = workspaces.findIndex((w) => w.id === target.id);
    if (from >= 0 && to >= 0) onreorder(from, to);
    dragId = '';
  }
</script>

<div class="strip" role="tablist" aria-label="Workspaces">
  {#each workspaces as w, i (w.id)}
    <div
      class="chip {activeId === w.id ? 'active' : ''} {dragId === w.id ? 'dragging' : ''}"
      role="tab"
      aria-selected={activeId === w.id}
      tabindex="0"
      draggable={renamingId !== w.id}
      onclick={() => onselect(w.id)}
      onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onselect(w.id); } }}
      ondblclick={() => onstartrename(w.id)}
      oncontextmenu={(e) => { e.preventDefault(); onctxmenu(w, e.clientX, e.clientY); }}
      ondragstart={(e) => onDragStart(e, w)}
      ondragover={onDragOver}
      ondrop={(e) => onDrop(e, w)}
      ondragend={() => (dragId = '')}
      title={`${w.name} — ${i < 9 ? `Ctrl+${i + 1}, ` : ''}double-click to rename, right-click for menu, drag to reorder`}
    >
      {#if i < 9}<span class="chip-num">{i + 1}</span>{/if}
      {#if renamingId === w.id}
        <input
          class="chip-rename"
          bind:value={renameVal}
          onclick={(e) => e.stopPropagation()}
          onkeydown={(e) => { if (e.key === 'Enter') oncommitrename(); if (e.key === 'Escape') oncancelrename(); }}
          onblur={oncommitrename}
          use:autofocus
        />
      {:else}
        <span class="chip-name">{w.name}</span>
      {/if}
    </div>
  {/each}
  <button class="strip-add" title="Add a workspace" aria-label="Add workspace" onclick={(e) => { e.stopPropagation(); onadd(); }}>＋</button>
</div>

<style>
  .strip { display: flex; align-items: center; gap: 0.25rem; overflow-x: auto; overflow-y: hidden; min-width: 0; padding-bottom: 2px; }
  .strip::-webkit-scrollbar { height: 0; }

  .chip {
    display: inline-flex; align-items: center; gap: 0.35rem;
    padding: 0.34rem 0.6rem;
    background: var(--calm-chip); border: 1px solid var(--calm-border);
    border-radius: 8px; cursor: pointer; font-size: 0.78rem; color: var(--calm-fg-muted);
    white-space: nowrap; flex: 0 0 auto; max-width: 14rem;
    transition: background 0.13s ease, color 0.13s ease, border-color 0.13s ease;
  }
  .chip:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }
  .chip.active { color: var(--calm-fg); border-color: color-mix(in srgb, var(--calm-accent) 55%, var(--calm-border)); background: color-mix(in srgb, var(--calm-accent) 14%, var(--calm-surface)); font-weight: 600; }
  .chip.dragging { opacity: 0.5; }

  .chip-num { font-size: 0.6rem; font-weight: 700; color: var(--calm-fg-faint); background: var(--calm-surface-2); border: 1px solid var(--calm-border); border-radius: 5px; padding: 0.02rem 0.3rem; flex: 0 0 auto; }
  .chip.active .chip-num { color: var(--calm-accent); border-color: color-mix(in srgb, var(--calm-accent) 40%, var(--calm-border)); }
  .chip-name { overflow: hidden; text-overflow: ellipsis; }
  .chip-rename { width: 8rem; background: var(--calm-input); color: var(--calm-fg); border: 1px solid var(--calm-accent); border-radius: 5px; font: inherit; font-size: 0.76rem; padding: 0.05rem 0.3rem; }

  .strip-add { width: 28px; height: 28px; flex: 0 0 auto; display: inline-flex; align-items: center; justify-content: center; background: transparent; border: 1px dashed var(--calm-border-strong); color: var(--calm-fg-muted); border-radius: 8px; cursor: pointer; font-size: 0.9rem; }
  .strip-add:hover { color: var(--calm-accent); border-color: var(--calm-accent); }
</style>

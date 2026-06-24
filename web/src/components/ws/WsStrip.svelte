<!--
  WsStrip — the WORKSPACE STRIP in the header, styled as a CLEAR TAB BAR:
  obvious tabs with a sit-on-the-baseline active treatment, and EVERY tab
  the SAME FIXED WIDTH (uniform, not auto-sized to the label). Each tab is a
  whole saved DESKTOP: clicking switches to it, the numeric badge hints its
  Ctrl+N shortcut (1..9). '+' adds a workspace; double-click OR right-click
  renames; drag a tab to reorder. Right-click opens a context menu: Rename /
  Duplicate / Move left / Move right / Delete.

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
    onclose = () => {},        // (workspaceId) — delete that workspace
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
        {#if workspaces.length > 1}
          <button
            class="chip-close"
            title="Close this workspace"
            aria-label={`Close workspace ${w.name}`}
            onclick={(e) => { e.stopPropagation(); onclose(w.id); }}
            onkeydown={(e) => e.stopPropagation()}
            ondblclick={(e) => e.stopPropagation()}
            onmousedown={(e) => e.stopPropagation()}
            draggable="false"
          >✕</button>
        {/if}
      {/if}
    </div>
  {/each}
  <button class="strip-add" title="Add a workspace" aria-label="Add workspace" onclick={(e) => { e.stopPropagation(); onadd(); }}>＋</button>
</div>

<style>
  /* TAB BAR — tabs sit on a shared baseline so they read unmistakably as
     tabs; the active one connects to the body below. */
  .strip {
    display: flex; align-items: flex-end; gap: 0.2rem;
    overflow-x: auto; overflow-y: hidden; min-width: 0;
    border-bottom: 1px solid var(--calm-border);
  }
  .strip::-webkit-scrollbar { height: 0; }

  /* Uniform FIXED-WIDTH tabs — same width regardless of label length. */
  .chip {
    display: inline-flex; align-items: center; gap: 0.35rem;
    box-sizing: border-box;
    width: 9.5rem; flex: 0 0 9.5rem;
    height: 30px; padding: 0 0.55rem;
    background: var(--calm-surface-2); color: var(--calm-fg-muted);
    border: 1px solid var(--calm-border); border-bottom: 0;
    border-radius: 8px 8px 0 0; cursor: pointer; font-size: 0.78rem;
    white-space: nowrap; position: relative; top: 1px;
    transition: background 0.13s ease, color 0.13s ease, border-color 0.13s ease;
  }
  .chip:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }
  .chip.active {
    color: var(--calm-fg); font-weight: 600;
    background: var(--calm-surface);
    border-color: var(--calm-border-strong);
    box-shadow: inset 0 2px 0 0 var(--calm-accent);
  }
  /* Mask the bar's baseline under the active tab so it "connects". */
  .chip.active::after { content: ''; position: absolute; left: 0; right: 0; bottom: -1px; height: 1px; background: var(--calm-surface); }
  .chip.dragging { opacity: 0.5; }

  .chip-num { font-size: 0.6rem; font-weight: 700; color: var(--calm-fg-faint); background: var(--calm-bg); border: 1px solid var(--calm-border); border-radius: 5px; padding: 0.02rem 0.3rem; flex: 0 0 auto; }
  .chip.active .chip-num { color: var(--calm-accent); border-color: color-mix(in srgb, var(--calm-accent) 40%, var(--calm-border)); }
  .chip-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .chip-rename { flex: 1; min-width: 0; width: 100%; background: var(--calm-input); color: var(--calm-fg); border: 1px solid var(--calm-accent); border-radius: 5px; font: inherit; font-size: 0.76rem; padding: 0.05rem 0.3rem; }

  /* Browser-style close ✕: hidden until tab hover or active, then a small
     clickable target on the right. Never closes the last remaining tab
     (button isn't rendered when only one workspace exists). */
  .chip-close {
    flex: 0 0 auto; display: inline-flex; align-items: center; justify-content: center;
    width: 16px; height: 16px; margin-left: 0.1rem; padding: 0;
    background: transparent; border: 0; border-radius: 4px;
    color: var(--calm-fg-faint); cursor: pointer; font-size: 0.7rem; line-height: 1;
    opacity: 0; visibility: hidden;
    transition: opacity 0.12s ease, background 0.12s ease, color 0.12s ease;
  }
  .chip:hover .chip-close,
  .chip.active .chip-close,
  .chip-close:focus-visible { opacity: 1; visibility: visible; }
  .chip-close:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }

  .strip-add { width: 28px; height: 28px; flex: 0 0 auto; align-self: center; display: inline-flex; align-items: center; justify-content: center; background: transparent; border: 1px dashed var(--calm-border-strong); color: var(--calm-fg-muted); border-radius: 8px; cursor: pointer; font-size: 0.9rem; margin-left: 0.2rem; }
  .strip-add:hover { color: var(--calm-accent); border-color: var(--calm-accent); }
</style>

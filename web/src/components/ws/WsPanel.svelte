<!--
  WsPanel — the universal collapsible + maximizable FRAME for every
  persistent region of the WORKSPACES layout (the roster on the left, the
  inspector on the right). It is the single answer to HARD REQ #1
  (arrow-collapse EVERY region) and HARD REQ #2 (full-screen EVERY region):

    · A chevron arrow on the panel EDGE collapses / expands it. The glyph
      + edge are derived from `side` ('left' | 'right') so it always points
      the intuitive way (◀ ▶). NO hamburger / 3-dots.
    · A maximize / restore control fills the shell with this one region.
    · When COLLAPSED the frame renders a thin rail carrying the expand
      chevron + the (rotated) title, so the region never fully vanishes.

  The root owns the outer size + the single ESC keydown listener; this
  component only owns the chrome + collapse/maximize affordances.
-->
<script>
  let {
    title = '',
    glyph = '',
    side = 'left',           // left | right
    collapsed = false,
    maximized = false,
    badge = '',
    oncollapse = () => {},
    onmaximize = () => {},
    children,
    headerExtra,
  } = $props();

  let chevron = $derived.by(() => {
    if (side === 'left')  return collapsed ? '▶' : '◀';
    return collapsed ? '◀' : '▶'; // right mirrors
  });
</script>

{#if collapsed}
  <div class="rail-collapsed" data-side={side}>
    <button class="chev-btn" title={`Expand ${title}`} aria-label={`Expand ${title}`} onclick={oncollapse}>{chevron}</button>
    <span class="rail-glyph">{glyph}</span>
    <span class="rail-title">{title}</span>
  </div>
{:else}
  <section class="panel {maximized ? 'is-max' : ''}" data-side={side} aria-label={title}>
    <header class="panel-head">
      <div class="head-id">
        {#if glyph}<span class="head-glyph">{glyph}</span>{/if}
        <span class="head-title">{title}</span>
        {#if badge}<span class="head-badge">{badge}</span>{/if}
      </div>
      <div class="head-ctl">
        {#if headerExtra}{@render headerExtra()}{/if}
        <button class="ctl" title={maximized ? 'Restore' : 'Maximize panel'} aria-label={maximized ? 'Restore panel' : 'Maximize panel'} onclick={onmaximize}>{maximized ? '🗗' : '⛶'}</button>
        <button class="ctl" title={`Collapse ${title}`} aria-label={`Collapse ${title}`} onclick={oncollapse}>{chevron}</button>
      </div>
    </header>
    <div class="panel-body">
      {@render children?.()}
    </div>
  </section>
{/if}

<style>
  .panel { display: flex; flex-direction: column; width: 100%; height: 100%; min-width: 0; min-height: 0; background: var(--calm-surface); overflow: hidden; }
  .panel.is-max { border: 1px solid var(--calm-border-strong); border-radius: 8px; box-shadow: var(--calm-shadow-lg); }

  .panel-head { flex: 0 0 auto; display: flex; align-items: center; justify-content: space-between; gap: 0.5rem; padding: 0.35rem 0.35rem 0.35rem 0.6rem; border-bottom: 1px solid var(--calm-border); background: var(--calm-surface-2); min-width: 0; }
  .head-id { display: flex; align-items: center; gap: 0.4rem; min-width: 0; overflow: hidden; }
  .head-glyph { color: var(--calm-accent-2); font-size: 0.85rem; flex: 0 0 auto; }
  .head-title { font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--calm-fg-faint); font-weight: 700; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .head-badge { font-size: 0.6rem; font-weight: 700; color: var(--calm-fg-faint); background: var(--calm-chip); padding: 0.05rem 0.35rem; border-radius: 6px; flex: 0 0 auto; }
  .head-ctl { display: flex; align-items: center; gap: 0.1rem; flex: 0 0 auto; }

  .ctl { width: 26px; height: 26px; display: inline-flex; align-items: center; justify-content: center; background: transparent; border: 1px solid transparent; color: var(--calm-fg-muted); border-radius: 6px; cursor: pointer; font-size: 0.78rem; transition: background 0.14s ease, color 0.14s ease; }
  .ctl:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }

  .panel-body { flex: 1; min-height: 0; min-width: 0; overflow: hidden; display: flex; flex-direction: column; }

  .rail-collapsed { display: flex; flex-direction: column; align-items: center; gap: 0.6rem; width: 100%; height: 100%; padding: 0.4rem 0; background: var(--calm-surface-2); color: var(--calm-fg-faint); overflow: hidden; }
  .chev-btn { width: 26px; height: 26px; flex: 0 0 auto; display: inline-flex; align-items: center; justify-content: center; background: var(--calm-chip); border: 1px solid var(--calm-border); color: var(--calm-fg-muted); border-radius: 6px; cursor: pointer; font-size: 0.72rem; transition: background 0.14s ease, color 0.14s ease; }
  .chev-btn:hover { background: var(--calm-chip-hover); color: var(--calm-accent); }
  .rail-glyph { font-size: 0.9rem; color: var(--calm-accent-2); }
  .rail-title { font-size: 0.62rem; text-transform: uppercase; letter-spacing: 0.12em; font-weight: 700; white-space: nowrap; writing-mode: vertical-rl; transform: rotate(180deg); }
</style>

<!--
  IconPicker — anchored popover for the agent identity icon (#709.3,
  replacing the native prompt() shipped in #694 — a hard-ban worst
  practice: blocking, unstyled, no preview, hostile on touch).

  Curated grid (role glyphs + common emoji) + free input with live
  preview + "role default" reset. Emits onpick(icon) — empty string
  means "clear override". Esc / click-away = cancel (no call).

  Props:
    agentName: string        — shown in the header
    current:   string        — current override ('' = none)
    x, y:      number        — anchor position (viewport px)
    onpick(icon), oncancel()
-->
<script>
  let { agentName = '', current = '', x = 0, y = 0, onpick = () => {}, oncancel = () => {} } = $props();

  // Row 1: the role-default glyphs (so an operator can also pin one
  // explicitly); rows 2-3: common, visually-distinct emoji.
  const CURATED = [
    '♛', '△', '⚖', '⚒', '✻', '◎', '⇄', '●',
    '🦊', '🐙', '🦉', '🐝', '🚀', '🔧', '🧪', '📦',
    '🛰️', '🧭', '🌿', '⚡', '🎯', '🛡️', '🔬', '🗜️',
  ];

  let free = $state(current || '');
  const preview = $derived((free || '').trim().slice(0, 16));

  function pick(icon) { onpick(icon); }
  function onkeydown(e) {
    if (e.key === 'Escape') { e.stopPropagation(); oncancel(); }
    if (e.key === 'Enter' && preview) { e.stopPropagation(); pick(preview); }
  }
  // Clamp so the popover stays on-screen.
  const left = $derived(Math.min(x, (typeof window !== 'undefined' ? window.innerWidth : 1200) - 280));
  const top = $derived(Math.min(y, (typeof window !== 'undefined' ? window.innerHeight : 800) - 260));
</script>

<!-- svelte-ignore a11y_no_static_element_interactions a11y_click_events_have_key_events -->
<div class="ip-backdrop" onclick={oncancel} onkeydown={onkeydown}>
  <div class="ip-pop" style="left: {left}px; top: {top}px;" role="dialog" aria-label="Change icon for {agentName}" data-testid="icon-picker" onclick={(e) => e.stopPropagation()}>
    <header>icon for <strong>{agentName}</strong></header>
    <div class="ip-grid" role="listbox" aria-label="curated icons">
      {#each CURATED as ic}
        <button type="button" class="ip-cell" class:sel={ic === current} title={ic} onclick={() => pick(ic)}>
          <span aria-hidden="true">{ic}</span>
        </button>
      {/each}
    </div>
    <div class="ip-free">
      <input
        type="text"
        placeholder="any emoji…"
        bind:value={free}
        onkeydown={onkeydown}
        aria-label="custom icon"
        data-testid="icon-picker-input"
      />
      <span class="ip-preview" aria-hidden="true">{preview || '·'}</span>
      <button type="button" class="ip-apply" disabled={!preview} onclick={() => pick(preview)}>set</button>
    </div>
    <footer>
      <button type="button" class="ip-reset" onclick={() => pick('')} data-testid="icon-picker-reset">↺ role default</button>
      <button type="button" class="ip-cancel" onclick={oncancel}>cancel</button>
    </footer>
  </div>
</div>

<style>
  .ip-backdrop { position: fixed; inset: 0; z-index: 80; }
  .ip-pop { position: fixed; width: 264px; background: var(--bg-elevated, #1d1d1d); border: 1px solid var(--border, #2a2a2a); border-radius: 8px; box-shadow: 0 8px 28px rgba(0,0,0,0.5); padding: 0.55rem; }
  header { font-size: 0.78rem; color: var(--fg-muted, #999); margin-bottom: 0.45rem; }
  header strong { color: var(--fg, #ddd); }
  .ip-grid { display: grid; grid-template-columns: repeat(8, 1fr); gap: 2px; }
  .ip-cell { background: none; border: 1px solid transparent; border-radius: 5px; font-size: 1rem; line-height: 1.6; cursor: pointer; padding: 0.1rem 0; }
  .ip-cell:hover, .ip-cell:focus { background: var(--bg, #111); outline: none; border-color: var(--border, #2a2a2a); }
  .ip-cell.sel { border-color: var(--accent-2, #87ceeb); }
  .ip-free { display: flex; align-items: center; gap: 0.4rem; margin-top: 0.5rem; }
  .ip-free input { flex: 1; min-width: 0; background: var(--bg, #111); border: 1px solid var(--border, #2a2a2a); border-radius: 5px; color: var(--fg, #ddd); padding: 0.25rem 0.45rem; font-size: 0.85rem; }
  .ip-preview { width: 1.6rem; text-align: center; font-size: 1.1rem; }
  .ip-apply { background: var(--accent-2, #87ceeb); color: #06222e; border: none; border-radius: 5px; padding: 0.25rem 0.55rem; font-size: 0.8rem; cursor: pointer; }
  .ip-apply:disabled { opacity: 0.4; cursor: default; }
  footer { display: flex; justify-content: space-between; margin-top: 0.5rem; }
  footer button { background: none; border: none; color: var(--fg-muted, #999); cursor: pointer; font-size: 0.78rem; }
  footer button:hover { color: var(--fg, #ddd); }
</style>

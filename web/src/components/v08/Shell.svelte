<!--
  Shell — the #690/#709.S1.1 fixed workspace chrome.

  ┌──────────┬──────────────────────────┬──────────────────┐
  │ RAIL     │ CENTER (pane grid:       │ CONTEXT          │
  │ sessions │  terminals · transcript  │  agent details   │
  │ (fixed)  │  · kanban — the ONLY     │  (fixed)         │
  │          │  splittable region)      ├──────────────────┤
  │          │                          │ team transcript  │
  │          │                          │  (fixed)         │
  └──────────┴──────────────────────────┴──────────────────┘

  The rail, agent-details (Inspector) and team transcript are FIXED
  CHROME: collapsible (chevrons, persisted per browser) but never
  closable, never draggable, never splittable. The free-form pane grid
  is demoted to powering the center region only. This is the structural
  half of the #690 redesign that the first delivery shipped only as
  defaults ("you shipped the wireframe's vocabulary, not its
  structure" — operator, 2026-06-04).

  Slots (snippets): rail, center, context-top, context-bottom.
-->
<script>
  let { rail, center, contextTop, contextBottom } = $props();

  function persisted(key, dflt) {
    try { const v = localStorage.getItem(key); return v === null ? dflt : v === '1'; } catch { return dflt; }
  }
  let railOpen = $state(persisted('chepherd-shell-rail', true));
  let contextOpen = $state(persisted('chepherd-shell-context', true));
  function toggle(which) {
    if (which === 'rail') {
      railOpen = !railOpen;
      try { localStorage.setItem('chepherd-shell-rail', railOpen ? '1' : '0'); } catch {}
    } else {
      contextOpen = !contextOpen;
      try { localStorage.setItem('chepherd-shell-context', contextOpen ? '1' : '0'); } catch {}
    }
  }
</script>

<div class="shell" data-testid="shell">
  <aside class="shell-rail" class:collapsed={!railOpen} data-testid="shell-rail">
    {#if railOpen}
      <div class="region-body">{@render rail?.()}</div>
    {/if}
    <button class="collapse rail-toggle" onclick={() => toggle('rail')} title={railOpen ? 'collapse sessions' : 'expand sessions'} aria-label={railOpen ? 'collapse sessions rail' : 'expand sessions rail'}>{railOpen ? '⟨' : '⟩'}</button>
  </aside>

  <main class="shell-center" data-testid="shell-center">
    {@render center?.()}
  </main>

  <aside class="shell-context" class:collapsed={!contextOpen} data-testid="shell-context">
    <button class="collapse ctx-toggle" onclick={() => toggle('context')} title={contextOpen ? 'collapse context' : 'expand context'} aria-label={contextOpen ? 'collapse context column' : 'expand context column'}>{contextOpen ? '⟩' : '⟨'}</button>
    {#if contextOpen}
      <div class="region-body split">
        <section class="ctx-top" data-testid="shell-context-top" aria-label="agent details">{@render contextTop?.()}</section>
        <section class="ctx-bottom" data-testid="shell-context-bottom" aria-label="team transcript">{@render contextBottom?.()}</section>
      </div>
    {/if}
  </aside>
</div>

<style>
  .shell { display: flex; height: 100%; min-height: 0; }
  .shell-rail { position: relative; width: 230px; min-width: 230px; border-right: 1px solid var(--border, #2a2a2a); display: flex; flex-direction: column; }
  .shell-rail.collapsed { width: 18px; min-width: 18px; }
  .shell-center { flex: 1; min-width: 0; min-height: 0; }
  .shell-context { position: relative; width: 340px; min-width: 340px; border-left: 1px solid var(--border, #2a2a2a); display: flex; flex-direction: column; }
  .shell-context.collapsed { width: 18px; min-width: 18px; }
  .region-body { flex: 1; min-height: 0; overflow: hidden; display: flex; flex-direction: column; }
  .region-body.split { display: flex; flex-direction: column; }
  .ctx-top { flex: 0 0 46%; min-height: 0; overflow: hidden; border-bottom: 1px solid var(--border, #2a2a2a); display: flex; flex-direction: column; }
  .ctx-bottom { flex: 1; min-height: 0; overflow: hidden; display: flex; flex-direction: column; }
  .collapse { position: absolute; top: 50%; transform: translateY(-50%); z-index: 5; width: 16px; height: 44px; background: var(--bg-elevated, #1d1d1d); border: 1px solid var(--border, #2a2a2a); color: var(--fg-muted, #888); cursor: pointer; font-size: 0.7rem; padding: 0; }
  .collapse:hover { color: var(--fg, #ddd); }
  .rail-toggle { right: -1px; border-radius: 4px 0 0 4px; }
  .ctx-toggle { left: -1px; border-radius: 0 4px 4px 0; }
</style>

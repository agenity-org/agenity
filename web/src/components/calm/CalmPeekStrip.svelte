<!--
  CalmPeekStrip — the calm signature. A horizontal strip of every open
  terminal pane as a compact card (identity color + icon + status). The
  focused pane is highlighted; clicking any card brings that pane forward
  (HARD REQ #1 — smooth pane switching). The trailing "＋" adds a pane.

  Calm direction: the focus stage stays quiet and singular; the strip is
  the at-a-glance index of everything else you have open, so you switch
  without hunting through nested splits.
-->
<script>
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let {
    leaves = [],
    sessions = [],
    focusedLeafId = '',
    onpick = () => {},
    onadd = () => {},
  } = $props();

  function sessionFor(leaf) {
    return sessions.find((s) => s.name === leaf.config?.agent) || null;
  }
  function statusOf(s) {
    if (!s) return 'none';
    if (s.exited) return 'exited';
    if (s.paused) return 'paused';
    if (s.live === false) return 'offline';
    return 'live';
  }
</script>

<div class="peek" role="tablist" aria-label="Open panes">
  {#each leaves as leaf (leaf.id)}
    {@const s = sessionFor(leaf)}
    {@const id = agentIdentity(s || (leaf.config?.agent || '?'))}
    {@const st = statusOf(s)}
    <button
      class="peek-card {focusedLeafId === leaf.id ? 'is-focused' : ''}"
      role="tab"
      aria-selected={focusedLeafId === leaf.id}
      onclick={() => onpick(leaf.id)}
      title={leaf.config?.agent || 'unbound terminal'}
    >
      <span class="bar" style={`background:${id.color}`}></span>
      <span class="pc-ic" style={`color:${id.color}`}>{id.icon}</span>
      <span class="pc-name">{leaf.config?.agent || 'unbound'}</span>
      <span class="pc-dot {st}"></span>
    </button>
  {/each}
  <button class="peek-add" onclick={onadd} title="Add a terminal pane" aria-label="Add a terminal pane">＋</button>
</div>

<style>
  .peek {
    flex: 0 0 auto;
    display: flex; align-items: stretch; gap: 0.45rem;
    overflow-x: auto; overflow-y: hidden;
    padding: 0.15rem 0.1rem 0.3rem;
    min-height: 0;
  }
  .peek-card {
    display: inline-flex; align-items: center; gap: 0.45rem;
    position: relative;
    padding: 0.4rem 0.7rem 0.4rem 0.85rem;
    background: var(--calm-surface);
    border: 1px solid var(--calm-border);
    border-radius: 11px;
    color: var(--calm-fg);
    cursor: pointer; font-size: 0.78rem;
    white-space: nowrap; flex: 0 0 auto;
    transition: background 0.14s ease, border-color 0.14s ease, transform 0.12s ease;
  }
  .peek-card:hover { background: var(--calm-chip-hover); transform: translateY(-1px); }
  .peek-card.is-focused {
    border-color: color-mix(in srgb, var(--calm-accent) 55%, var(--calm-border));
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--calm-accent) 40%, transparent);
    background: color-mix(in srgb, var(--calm-accent) 9%, var(--calm-surface));
  }
  .bar { position: absolute; left: 0; top: 7px; bottom: 7px; width: 3px; border-radius: 999px; }
  .pc-ic { font-size: 0.85rem; }
  .pc-name { font-weight: 600; max-width: 11rem; overflow: hidden; text-overflow: ellipsis; }
  .pc-dot { width: 7px; height: 7px; border-radius: 50%; flex: 0 0 auto; }
  .pc-dot.live { background: var(--calm-ok); box-shadow: 0 0 0 2px color-mix(in srgb, var(--calm-ok) 25%, transparent); }
  .pc-dot.paused { background: var(--calm-warn); }
  .pc-dot.exited, .pc-dot.offline, .pc-dot.none { background: var(--calm-fg-faint); }

  .peek-add {
    flex: 0 0 auto;
    width: 38px;
    display: inline-flex; align-items: center; justify-content: center;
    background: var(--calm-surface);
    border: 1px dashed var(--calm-border-strong);
    border-radius: 11px; color: var(--calm-fg-muted);
    cursor: pointer; font-size: 1.1rem;
    transition: color 0.14s ease, border-color 0.14s ease;
  }
  .peek-add:hover { color: var(--calm-accent); border-color: var(--calm-accent); }
</style>

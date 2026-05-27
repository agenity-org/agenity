<!--
  Stage1Shape — v0.9 SpawnWizard Stage 1 (#177, architect pivot 2026-05-27).

  6-card 2×3 grid (visible builtins only). Per architect:
  Row 1: Solo · Pair · Trio
  Row 2: Scrum · Review · Custom

  Picking the Custom card jumps to an empty Stage 3 with "+ Add agent".
  All other cards auto-fill Stage 3 with the template's slot list.

  Props:
    selectedId:  currently-selected template id (two-way bound)
    onselect(id, template): called when operator picks a card; passes
                            the full template object so the parent can
                            cache it without a refetch
-->
<script>
  let { selectedId = $bindable(''), onselect } = $props();

  let templates = $state([]);
  let loading = $state(true);
  let error = $state('');

  // Inline SVG icon paths from lucide.dev — matches the 6 builtins'
  // Icon field. New icons (PlusCircle, Shield, KanbanSquare, Network)
  // mirror the Skill Library's curated set.
  const icons = {
    User:          '<circle cx="12" cy="8" r="4"/><path d="M4 22a8 8 0 1 1 16 0" fill="none" stroke="currentColor" stroke-width="2"/>',
    Users:         '<circle cx="9" cy="8" r="4"/><path d="M2 22a7 7 0 0 1 14 0" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="17" cy="9" r="3"/><path d="M22 21a5 5 0 0 0-7-4.5" fill="none" stroke="currentColor" stroke-width="2"/>',
    Network:       '<circle cx="12" cy="5" r="2.5"/><circle cx="5" cy="19" r="2.5"/><circle cx="19" cy="19" r="2.5"/><path d="M12 7.5v3.5M12 11l-6 6M12 11l6 6" fill="none" stroke="currentColor" stroke-width="2"/>',
    KanbanSquare:  '<rect x="3" y="3" width="18" height="18" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><line x1="8" y1="7" x2="8" y2="13" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"/><line x1="12" y1="7" x2="12" y2="17" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"/><line x1="16" y1="7" x2="16" y2="10" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"/>',
    Shield:        '<path d="M12 2L4 5v7c0 5 3.5 8.5 8 10 4.5-1.5 8-5 8-10V5l-8-3z"/>',
    PlusCircle:    '<circle cx="12" cy="12" r="10" fill="none" stroke="currentColor" stroke-width="2"/><line x1="12" y1="7" x2="12" y2="17" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"/><line x1="7" y1="12" x2="17" y2="12" stroke="currentColor" stroke-width="2.5" stroke-linecap="round"/>',
  };

  async function loadTemplates() {
    loading = true;
    try {
      const r = await fetch('/api-v08/v1/team-templates?visible=true');
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const j = await r.json();
      templates = j.templates || [];
      if (!selectedId && templates.length) {
        selectedId = templates[0].id;
        onselect?.(templates[0].id, templates[0]);
      }
    } catch (e) {
      error = String(e);
    } finally {
      loading = false;
    }
  }

  const selected = $derived(templates.find(t => t.id === selectedId));

  function pick(t) {
    selectedId = t.id;
    onselect?.(t.id, t);
  }

  $effect(() => { loadTemplates(); });
</script>

<div class="stage1">
  <h2>What kind of workspace?</h2>
  <p class="lead">Pick a team shape. The agent roster auto-fills — you can adjust on the next step.</p>

  {#if loading}
    <p class="hint">Loading templates…</p>
  {:else if error}
    <p class="err">Failed to load templates: {error}</p>
  {:else}
    <div class="grid" role="radiogroup" aria-label="template">
      {#each templates as t}
        <button
          type="button"
          class="card"
          class:selected={t.id === selectedId}
          aria-pressed={t.id === selectedId}
          onclick={() => pick(t)}
        >
          <span class="icon" aria-hidden="true">
            <svg viewBox="0 0 24 24" width="26" height="26" fill="currentColor">{@html icons[t.icon] || '<circle cx="12" cy="12" r="6"/>'}</svg>
          </span>
          <span class="name">{t.name}</span>
          <span class="count">{t.slots.length} {t.slots.length === 1 ? 'agent' : 'agents'}</span>
        </button>
      {/each}
    </div>

    {#if selected}
      <section class="explain" aria-label="selected template details">
        <h3>{selected.name}</h3>
        {#if selected.description}<p class="desc">{selected.description}</p>{/if}
        {#if selected.when_to_use}<p class="when"><strong>Best for:</strong> {selected.when_to_use}</p>{/if}
        {#if selected.slots.length > 0}
          <ul class="roster">
            {#each selected.slots as s}
              <li>
                <span class="m-label">{s.label}</span>
                <span class="m-role">· {s.primary_skill}</span>
              </li>
            {/each}
          </ul>
        {:else}
          <p class="hint">Empty template — you'll add agents on Stage 3 with the "+ Add agent" button.</p>
        {/if}
      </section>
    {/if}

    <a class="admin-link" href="/admin/templates">Manage templates →</a>
  {/if}
</div>

<style>
  .stage1 { padding: 1.25rem; }
  h2 { font-size: 1.15rem; margin: 0 0 0.35rem 0; }
  .lead { color: var(--fg-muted, #888); margin: 0 0 1.2rem 0; font-size: 0.9rem; }
  /* 3-column grid — operator-confirmed 2×3 for the canonical 6 cards.
     If admin promotes a hidden template to visible, the grid simply
     overflows into a 3rd row (still 3-col). */
  .grid {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 0.7rem;
    margin-bottom: 1rem;
  }
  .card {
    display: flex; flex-direction: column; align-items: center; gap: 0.4rem;
    padding: 1.05rem 0.55rem 0.95rem; border-radius: 8px;
    background: var(--bg-elevated, #1a1a1a);
    border: 1.5px solid var(--border, #2a2a2a);
    color: var(--fg, #f5f5f5);
    cursor: pointer;
    transition: border-color 80ms, background 80ms;
    font: inherit;
    text-align: center;
  }
  .card:hover { border-color: var(--accent-2, #87ceeb); background: rgba(135, 206, 235, 0.04); }
  .card.selected {
    border-color: var(--accent-2, #87ceeb);
    background: rgba(135, 206, 235, 0.08);
    box-shadow: 0 0 0 2px rgba(135, 206, 235, 0.18) inset;
  }
  .icon { color: var(--accent-2, #87ceeb); }
  .name { font-weight: 600; font-size: 0.92rem; line-height: 1.15; }
  .count { font-size: 0.74rem; color: var(--fg-muted, #888); }

  .explain {
    margin-top: 0.4rem; padding: 0.85rem 1rem;
    background: var(--bg-elevated, #1a1a1a);
    border: 1px solid var(--border, #2a2a2a);
    border-radius: 8px;
  }
  .explain h3 { margin: 0 0 0.4rem 0; font-size: 0.95rem; }
  .desc { margin: 0 0 0.4rem 0; font-size: 0.88rem; color: var(--fg, #f5f5f5); }
  .when { margin: 0 0 0.5rem 0; font-size: 0.85rem; color: var(--fg-muted, #aaa); }
  .roster { list-style: none; padding: 0; margin: 0.4rem 0 0 0; display: flex; flex-wrap: wrap; gap: 0.5rem; }
  .roster li {
    background: var(--bg, #0a0a0a); border: 1px solid var(--border, #2a2a2a);
    padding: 0.18rem 0.5rem; border-radius: 4px; font-size: 0.78rem;
  }
  .m-label { font-weight: 600; }
  .m-role { color: var(--fg-muted, #888); }

  .admin-link {
    display: inline-block; margin-top: 1rem;
    color: var(--accent-2, #87ceeb); text-decoration: none; font-size: 0.85rem;
  }
  .admin-link:hover { text-decoration: underline; }

  .hint { color: var(--fg-muted, #888); font-size: 0.85rem; }
  .err { color: var(--danger, #e74c3c); }
</style>

<!--
  Stage1Shape — v0.9.1 SpawnWizard Stage 1 (#177, architect 2026-05-28 FINAL+).

  6-card 2×3 grid with Fibonacci sizing badges:
  Row 1: Solo (1) · Pair (2) · Trio (3)
  Row 2: Scrum (5) · Squad (8) · Custom (0)

  Each card shows: icon · name · Fibonacci size badge · "N agents" line.

  Roster preview (in the "explain" panel) now renders ROLE + OWNED
  SKILLS instead of the legacy single "primary_skill" string —
  reflects the architect 2026-05-28 spec: agent identity = ONE role +
  N owned skills (with optional per-skill scope).

  Picking Custom jumps to an empty Stage 3 with "+ Add agent".
  All other cards auto-fill Stage 3 with the template's slot list.

  Props:
    selectedId:  currently-selected template id (two-way bound)
    onselect(id, template): called when operator picks a card; parent
                            caches the full template object
-->
<script>
  let { selectedId = $bindable(''), onselect } = $props();

  let templates = $state([]);
  let loading = $state(true);
  let error = $state('');

  // Inline SVG icon paths from lucide.dev — mirrors the 6 builtins'
  // Icon field. Layers3 added for Squad (replaces removed "review"
  // template); other icons unchanged.
  const icons = {
    User:          '<circle cx="12" cy="8" r="4"/><path d="M4 22a8 8 0 1 1 16 0" fill="none" stroke="currentColor" stroke-width="2"/>',
    Users:         '<circle cx="9" cy="8" r="4"/><path d="M2 22a7 7 0 0 1 14 0" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="17" cy="9" r="3"/><path d="M22 21a5 5 0 0 0-7-4.5" fill="none" stroke="currentColor" stroke-width="2"/>',
    Network:       '<circle cx="12" cy="5" r="2.5"/><circle cx="5" cy="19" r="2.5"/><circle cx="19" cy="19" r="2.5"/><path d="M12 7.5v3.5M12 11l-6 6M12 11l6 6" fill="none" stroke="currentColor" stroke-width="2"/>',
    KanbanSquare:  '<rect x="3" y="3" width="18" height="18" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><line x1="8" y1="7" x2="8" y2="13" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"/><line x1="12" y1="7" x2="12" y2="17" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"/><line x1="16" y1="7" x2="16" y2="10" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"/>',
    Layers3:       '<path d="M12 2L2 7l10 5 10-5-10-5z" fill="none" stroke="currentColor" stroke-width="2"/><path d="M2 12l10 5 10-5" fill="none" stroke="currentColor" stroke-width="2"/><path d="M2 17l10 5 10-5" fill="none" stroke="currentColor" stroke-width="2"/>',
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

  // Fibonacci size badge — prefer the explicit size_label from the
  // server (v0.9.1+ templates set this), fall back to slot count for
  // older catalog entries.
  function sizeBadge(t) {
    if (t.size_label !== undefined && t.size_label !== null && t.size_label !== '') {
      return t.size_label;
    }
    return String(t.slots?.length ?? 0);
  }

  function pick(t) {
    selectedId = t.id;
    onselect?.(t.id, t);
  }

  function slotRole(s) {
    return s.role_id || s.primary_skill || 'role';
  }

  // Per-role logo (Lucide-style line icons). Operator request 2026-05-29:
  // after picking a shape show members as a horizontal card row with a
  // logo per role — no skills (those live on Stage 3).
  const ROLE_LOGO = {
    'product-owner':        '<rect x="5" y="4" width="14" height="17" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><path d="M9 4v2h6V4" fill="none" stroke="currentColor" stroke-width="2"/><path d="M9 12h6M9 16h6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
    'architect':            '<path d="M3 21l9-18 9 18" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/><path d="M7 13h10" fill="none" stroke="currentColor" stroke-width="2"/>',
    'tech-lead':            '<path d="M13 2L4 14h7l-1 8 9-12h-7l1-8z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/>',
    'scrum-master':         '<path d="M21 12a9 9 0 1 1-3-6.7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/><path d="M21 4v5h-5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>',
    'generalist':           '<circle cx="12" cy="8" r="4" fill="none" stroke="currentColor" stroke-width="2"/><path d="M4 22a8 8 0 0 1 16 0" fill="none" stroke="currentColor" stroke-width="2"/>',
    'full-stack-developer': '<polyline points="8,18 2,12 8,6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/><polyline points="16,6 22,12 16,18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>',
    'frontend-developer':   '<rect x="2" y="4" width="20" height="14" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><path d="M8 22h8M12 18v4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
    'backend-developer':    '<rect x="3" y="4" width="18" height="6" rx="1" fill="none" stroke="currentColor" stroke-width="2"/><rect x="3" y="14" width="18" height="6" rx="1" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="7" cy="7" r="0.8" fill="currentColor"/><circle cx="7" cy="17" r="0.8" fill="currentColor"/>',
    'devops-sre':           '<circle cx="6" cy="6" r="2.5" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="6" cy="18" r="2.5" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="18" cy="12" r="2.5" fill="none" stroke="currentColor" stroke-width="2"/><path d="M8 6h8M8 18h8M6 8.5v7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
    'qa-engineer':          '<path d="M12 2L4 5v7c0 5 3.5 8.5 8 10 4.5-1.5 8-5 8-10V5l-8-3z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/><path d="M9 12l2 2 4-4" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/>',
    'security-engineer':    '<rect x="5" y="11" width="14" height="10" rx="2" fill="none" stroke="currentColor" stroke-width="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4" fill="none" stroke="currentColor" stroke-width="2"/>',
    'code-reviewer':        '<circle cx="11" cy="11" r="6" fill="none" stroke="currentColor" stroke-width="2"/><path d="M21 21l-5.5-5.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>',
  };
  function logoFor(s) {
    const r = slotRole(s);
    return ROLE_LOGO[r] || ROLE_LOGO['generalist'];
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
          <span class="size-row">
            <span class="size-badge" title="Fibonacci sizing">{sizeBadge(t)}</span>
            <span class="count">{t.slots.length} {t.slots.length === 1 ? 'agent' : 'agents'}</span>
          </span>
        </button>
      {/each}
    </div>

    {#if selected}
      <section class="explain" aria-label="selected template details">
        <h3>{selected.name}</h3>
        {#if selected.description}<p class="desc">{selected.description}</p>{/if}
        {#if selected.when_to_use}<p class="when"><strong>Best for:</strong> {selected.when_to_use}</p>{/if}
        {#if selected.slots.length > 0}
          <div
            class="roster-row"
            aria-label="team members"
            style="grid-template-columns: repeat({selected.slots.length}, minmax(0, 1fr));"
          >
            {#each selected.slots as s}
              <div class="member-card" title={slotRole(s)}>
                <span class="m-logo" aria-hidden="true">
                  <svg viewBox="0 0 24 24" width="22" height="22">{@html logoFor(s)}</svg>
                </span>
                <span class="m-label">{s.label}</span>
                <span class="m-role">{slotRole(s)}</span>
              </div>
            {/each}
          </div>
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
  .size-row { display: flex; align-items: center; gap: 0.4rem; }
  .size-badge {
    display: inline-block; min-width: 1.35rem;
    padding: 0.05rem 0.35rem; border-radius: 999px;
    background: var(--accent-2, #87ceeb); color: #0a0a0a;
    font-size: 0.72rem; font-weight: 700; line-height: 1.2;
  }
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
  /* Horizontal member-card row (operator request 2026-05-29 — no skills,
     just role + logo; skills live on Stage 3).
     Uniform width — grid columns = exact slot count, so Squad (8) fits
     in one line, Solo (1) takes the full row, Pair (2) splits 50/50, etc.
     Cards never wrap; min-content is suppressed via minmax(0, 1fr). */
  .roster-row {
    display: grid; gap: 0.45rem; margin-top: 0.4rem;
  }
  .member-card {
    display: flex; flex-direction: column; align-items: center;
    gap: 0.2rem;
    padding: 0.55rem 0.35rem; border-radius: 8px;
    background: var(--bg, #0a0a0a); border: 1px solid var(--border, #2a2a2a);
    min-width: 0;  /* allow grid track to shrink below content width */
    overflow: hidden;
  }
  .m-logo { color: var(--accent-2, #87ceeb); line-height: 0; }
  .m-label {
    font-weight: 600; font-size: 0.78rem; color: var(--fg, #f5f5f5);
    max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .m-role {
    font-size: 0.7rem; color: var(--fg-muted, #888);
    max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }

  .admin-link {
    display: inline-block; margin-top: 1rem;
    color: var(--accent-2, #87ceeb); text-decoration: none; font-size: 0.85rem;
  }
  .admin-link:hover { text-decoration: underline; }

  .hint { color: var(--fg-muted, #888); font-size: 0.85rem; }
  .err { color: var(--danger, #e74c3c); }
</style>

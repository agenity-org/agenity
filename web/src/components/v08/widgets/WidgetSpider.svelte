<script>
  import SpiderChart from '../../SpiderChart.svelte';
  let { selectedAgent, sessions } = $props();
  let info = $derived(sessions?.find(s => s.name === selectedAgent));
  function axesFor(sc) {
    if (!sc) return [];
    return [
      { label: 'Goal',       value: sc.G || 0 },
      { label: 'Velocity',   value: sc.V || 0 },
      { label: 'Focus',      value: sc.F || 0 },
      { label: 'End-state',  value: sc.E || 0 },
      { label: 'Discipline', value: sc.D || 0 },
    ];
  }
  function relTime(ts) {
    if (!ts) return '—';
    const s = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s/60)}m ago`;
    return `${Math.floor(s/3600)}h ago`;
  }
</script>

<div class="spider-card">
  <h4>Shepherd assessment</h4>
  {#if !info}
    <p class="hint">No agent selected.</p>
  {:else if info.scorecard}
    <SpiderChart axes={axesFor(info.scorecard)} />
    <div class="meta">
      <span class="k">scored</span><span class="v">{relTime(info.scorecard.at)}</span>
      {#if info.last_verdict}<span class="k">verdict</span><span class="v"><span class="verdict v-{info.last_verdict}">{info.last_verdict}</span></span>{/if}
      {#if info.intervention_count > 0}<span class="k">interventions</span><span class="v">{info.intervention_count}</span>{/if}
    </div>
    {#if info.scorecard.note}
      <p class="note">{info.scorecard.note}</p>
    {/if}
  {:else}
    <p class="hint">Shepherd assessing — first scorecard arrives within 60s.</p>
  {/if}
</div>

<style>
  .spider-card { padding: 0.75rem 0.85rem; height: 100%; overflow-y: auto; background: var(--bg); }
  h4 { font-size: 0.74rem; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.06em; margin: 0 0 0.55rem 0; font-weight: 600; }
  .hint { color: var(--fg-faint); font-size: 0.85rem; }
  .meta { display: grid; grid-template-columns: minmax(70px, auto) 1fr; column-gap: 0.6rem; row-gap: 0.25rem; margin-top: 0.7rem; }
  .meta .k { color: var(--fg-muted); font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.04em; }
  .meta .v { font-size: 0.85rem; }
  .verdict { display: inline-block; padding: 0.04rem 0.45rem; border-radius: 4px; font-size: 0.72rem; font-weight: 600; }
  .v-silent { background: rgba(150,150,150,0.18); color: var(--fg-muted); }
  .v-praise { background: rgba(52,211,153,0.18); color: #34d399; }
  .v-coach  { background: rgba(255,165,0,0.18); color: var(--accent); }
  .v-intervene { background: rgba(255,107,107,0.18); color: var(--danger); }
  .note { color: var(--fg-muted); font-size: 0.82rem; font-style: italic; margin: 0.6rem 0 0 0; padding-left: 0.4rem; border-left: 2px solid var(--border-strong); line-height: 1.35; }
</style>

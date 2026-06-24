<!--
  WidgetMCPLog — dedicated audit-log widget filtering the runtime event
  stream to MCP-related events only (scorecard, verdict, note,
  record_event, alert_human, prompt_poke, etc.). Operator can grep by
  actor / kind / body and see who-called-what.
-->
<script>
  let { events } = $props();
  let filter = $state('');

  const MCP_KINDS = new Set([
    'scorecard', 'verdict', 'note', 'alert_human', 'review_axis',
    'prompt_poke', 'shepherd_handoff', 'shepherd_kickoff_retry',
    'template_applied', 'pause', 'unpause',
  ]);

  let filtered = $derived.by(() => {
    const f = (filter || '').trim().toLowerCase();
    return (events || [])
      .filter(e => MCP_KINDS.has(e.kind) || (e.actor && e.actor !== 'runtime' && e.actor !== 'operator'))
      .filter(e => !f || [e.kind, e.actor, e.body].join(' ').toLowerCase().includes(f))
      .slice(-200).reverse();
  });
</script>

<div class="wrap">
  <header>
    <h4>MCP audit log</h4>
    <input bind:value={filter} placeholder="filter by kind / actor / body…" />
    <span class="count">{filtered.length}</span>
  </header>
  <ul>
    {#each filtered as e}
      <li>
        <span class="t">{(e.at || '').slice(11,19)}</span>
        <span class="k" class:c-verdict={e.kind==='verdict'} class:c-sc={e.kind==='scorecard'} class:c-alert={e.kind==='alert_human'} class:c-poke={e.kind==='prompt_poke'}>{e.kind}</span>
        <span class="a">{e.actor}</span>
        <span class="b">{e.body}</span>
      </li>
    {/each}
    {#if !filtered.length}<li class="empty">No MCP events match.</li>{/if}
  </ul>
</div>

<style>
  .wrap { display: flex; flex-direction: column; height: 100%; overflow: hidden; }
  header { display: flex; align-items: center; gap: 0.4rem; padding: 0.4rem 0.55rem; border-bottom: 1px solid var(--border); }
  h4 { margin: 0; color: var(--accent); font-size: 0.82rem; }
  input { flex: 1; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 4px; padding: 0.15rem 0.4rem; font-size: 0.74rem; }
  .count { color: var(--fg-muted); font-size: 0.72rem; }
  ul { flex: 1; list-style: none; margin: 0; padding: 0; overflow-y: auto; }
  li { display: grid; grid-template-columns: 56px 95px 90px 1fr; gap: 0.4rem; padding: 0.15rem 0.55rem; font-size: 0.72rem; font-family: ui-monospace, monospace; border-bottom: 1px solid var(--border); }
  li:hover { background: var(--bg-input); }
  .t { color: var(--fg-muted); }
  .k { color: var(--fg-muted); }
  .c-verdict { color: var(--accent-2); }
  .c-sc { color: var(--accent); }
  .c-alert { color: var(--danger); font-weight: 600; }
  .c-poke { color: #ffaa55; }
  .a { color: var(--fg); }
  .b { color: var(--fg-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .empty { color: var(--fg-faint); text-align: center; padding: 1rem; font-style: italic; }
</style>

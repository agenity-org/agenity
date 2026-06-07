<!--
  MuxEvents — terminal-log-style live event feed. Renders the global
  events array (polled + SSE-streamed by the shell) as a monospace log
  with agent-colored sources. Read-only.
-->
<script>
  import { agentIdentity } from '../../lib/agentIdentity.js';

  let { events = [], sessions = [] } = $props();

  let rows = $derived.by(() => {
    const list = (events || []).slice(-300);
    return list.map((e, i) => {
      const who = e.agent || e.session || e.from || e.actor || e.name || '';
      const kind = e.kind || e.type || e.event || 'event';
      const msg = e.body || e.message || e.text || e.detail || e.summary || '';
      const at = e.at || e.created_at || e.ts || e.time || '';
      return { i, who, kind, msg, at, raw: e };
    });
  });

  function hhmmss(at) {
    if (!at) return '';
    try { return new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }); }
    catch { return ''; }
  }
  function kindClass(k) {
    const s = String(k).toLowerCase();
    if (s.includes('fail') || s.includes('error') || s.includes('danger')) return 'k-fail';
    if (s.includes('stuck') || s.includes('warn')) return 'k-warn';
    if (s.includes('done') || s.includes('accomplish') || s.includes('pass') || s.includes('ok')) return 'k-ok';
    return 'k-info';
  }

  let scroller = $state(null);
  let stick = $state(true);
  function onScroll() {
    if (!scroller) return;
    stick = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 30;
  }
  $effect(() => {
    rows;
    if (stick && scroller) setTimeout(() => { if (scroller) scroller.scrollTop = scroller.scrollHeight; }, 20);
  });
</script>

<div class="evlog" bind:this={scroller} onscroll={onScroll}>
  {#each rows as r (r.i)}
    {@const id = r.who ? agentIdentity(r.who) : null}
    <div class="ev">
      <span class="ts">{hhmmss(r.at)}</span>
      <span class="kd {kindClass(r.kind)}">{r.kind}</span>
      {#if r.who}<span class="who" style="color:{id.color}">{id.icon} {r.who}</span>{/if}
      <span class="msg">{r.msg}</span>
    </div>
  {/each}
  {#if !rows.length}
    <div class="empty">Waiting for events…</div>
  {/if}
</div>

<style>
  .evlog { height: 100%; overflow-y: auto; padding: 0.4rem 0.6rem; font-family: var(--mux-mono); font-size: 0.74rem; background: var(--mux-bg); line-height: 1.55; }
  .ev { display: flex; gap: 0.5rem; align-items: baseline; padding: 0.05rem 0; border-bottom: 1px solid var(--mux-border-faint); }
  .ts { color: var(--mux-fg-faint); flex: 0 0 auto; }
  .kd { flex: 0 0 auto; font-weight: 700; font-size: 0.66rem; padding: 0 0.3rem; border-radius: 3px; text-transform: uppercase; }
  .k-info { color: var(--mux-accent-2); background: var(--mux-accent-2-soft); }
  .k-ok { color: var(--mux-ok); background: var(--mux-ok-soft); }
  .k-warn { color: var(--mux-warn); background: var(--mux-warn-soft); }
  .k-fail { color: var(--mux-danger); background: var(--mux-danger-soft); }
  .who { flex: 0 0 auto; font-weight: 600; }
  .msg { color: var(--mux-fg); overflow-wrap: anywhere; min-width: 0; }
  .empty { color: var(--mux-fg-muted); padding: 1rem 0.5rem; }
</style>

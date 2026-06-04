<!--
  WidgetInspector — the unified agent-context panel (#691, UX-1 of #690).

  ONE component replacing the five per-pane-bound context widgets
  (agent-identity, agent-runtime, agent-details, scorecard/spider,
  prompt). Input is the single global `selectedAgent` (the focus
  engine in Workspace.svelte) — so multiple inspectors can never
  disagree about which agent they show.

  Tabs:
    overview  — identity + runtime (merged AgentIdentity + AgentRuntime)
    scorecard — G/V/F/E spider (WidgetSpider)
    debug     — read-only system prompt + per-agent MCP/event log

  Pin (📌): freezes this inspector on the currently shown agent
  (persisted in node.config.pinnedAgent via saveLayout) so the operator
  can compare two agents — the unpinned inspector keeps following focus.
-->
<script>
  import { agentIdentity } from '../../../lib/agentIdentity.js';
  import WidgetAgentIdentity from './WidgetAgentIdentity.svelte';
  import WidgetAgentRuntime from './WidgetAgentRuntime.svelte';
  import WidgetSpider from './WidgetSpider.svelte';
  import WidgetAgentPrompt from './WidgetAgentPrompt.svelte';
  import WidgetMCPLog from './WidgetMCPLog.svelte';

  let { sessions = [], memberships = [], events = [], selectedAgent = null, node = null, saveLayout = () => {} } = $props();

  let tab = $state('overview');


  const pinned = $derived(node?.config?.pinnedAgent || null);
  const shownName = $derived(pinned || selectedAgent);
  const agent = $derived((sessions || []).find(s => s.name === shownName) || null);
  const myMems = $derived((memberships || []).filter(m => m.agent_name === shownName));
  // Per-agent slice of the event stream for the debug tab. Events carry
  // an `actor`; fall back to body-mention so knock/verdict lines about
  // the agent still show.
  const agentEvents = $derived((events || []).filter(e =>
    !shownName || e.actor === shownName || (e.body || '').includes(shownName)));

  // #692 — mesh detail for hub/external peers (subsumes the deleted
  // federation + multi-host panes). Fetched lazily, only while an
  // external agent is shown; refreshed with the peer-list cadence.
  let meshPeers = $state([]);
  let meshTimer = null;
  const isExternal = $derived(agent && (agent.agent === 'external-a2a' || agent.external));
  const meshPeer = $derived((meshPeers || []).find(p => (p.name || p.sid) === shownName) || null);
  async function fetchPeers() {
    try {
      const r = await fetch('/api/v1/peers');
      if (!r.ok) return;
      meshPeers = (await r.json()).peers || [];
    } catch {}
  }
  $effect(() => {
    if (isExternal) {
      fetchPeers();
      meshTimer = setInterval(fetchPeers, 10000);
      return () => { if (meshTimer) clearInterval(meshTimer); };
    }
  });
  function syncedAgo(p) {
    const t = p?.syncedAt; // /api/v1/peers wire shape (was WidgetFederation's relTime source)
    if (!t) return '—';
    const s = Math.max(0, Math.round((Date.now() - new Date(t).getTime()) / 1000));
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    return `${Math.floor(s / 3600)}h ago`;
  }

  function togglePin() {
    if (!node) return;
    node.config = { ...(node.config || {}), pinnedAgent: pinned ? null : shownName };
    // Mirror into the tab snapshot so the pin survives tab switches +
    // layout reloads (tabs[] is what saveLayout persists).
    if (Array.isArray(node.tabs) && typeof node.activeTab === 'number' && node.tabs[node.activeTab]) {
      node.tabs[node.activeTab] = { widget: 'inspector', config: node.config };
    }
    saveLayout();
  }
</script>

<div class="inspector" data-testid="inspector">
  <header class="insp-head">
    {#if agent}
      {@const ident = agentIdentity(agent)}
      <span class="ident-chip" style="color: {ident.color}" aria-hidden="true">{ident.icon}</span>
    {/if}
    <span class="who" data-testid="inspector-agent">{shownName || '—'}</span>
    {#if agent?.team}<span class="team">· {agent.team}</span>{/if}
    {#if pinned}<span class="pin-tag" data-testid="inspector-pinned">pinned</span>{/if}
    {#if node}
      <!-- #709.S1.1 — pin only exists on PANE-hosted instances (compare
           mode); the fixed-chrome instance always follows focus, so a
           silent no-op pin button would be dead chrome. -->
      <button class="pin" class:on={!!pinned} title={pinned ? 'unpin — follow focus again' : 'pin this agent (compare mode)'} onclick={togglePin}>📌</button>
    {/if}
  </header>
  <nav class="insp-tabs" data-testid="inspector-tabs">
    {#each ['overview', 'scorecard', 'debug'] as t}
      <button class:active={tab === t} onclick={() => (tab = t)}>{t}</button>
    {/each}
  </nav>

  <div class="insp-body">
    {#if !agent}
      <p class="hint">No agent focused — click a session row or a terminal tab.</p>
    {:else if tab === 'overview'}
      {#if isExternal}
        <!-- #692 — mesh detail (replaces the federation + multi-host panes) -->
        <div class="mesh" data-testid="inspector-mesh">
          <div class="mesh-row"><span class="k">peer</span><span class="v">⇄ {shownName}</span></div>
          <div class="mesh-row"><span class="k">reachability</span><span class="v">{meshPeer?.card?.url?.startsWith?.('hub://') ? 'via hub mesh (no inbound)' : (meshPeer?.card?.url || 'direct')}</span></div>
          <div class="mesh-row"><span class="k">synced</span><span class="v">{syncedAgo(meshPeer)}</span></div>
          {#if meshPeer?.card?.name}<div class="mesh-row"><span class="k">card</span><span class="v">{meshPeer.card.name}</span></div>{/if}
        </div>
      {/if}
      <WidgetAgentIdentity {agent} />
      <WidgetAgentRuntime {agent} />
      {#if myMems.length}
        <div class="mems">
          {#each myMems as m}
            <span class="mem">{m.team_name} · {m.role}</span>
          {/each}
        </div>
      {/if}
    {:else if tab === 'scorecard'}
      <WidgetSpider selectedAgent={shownName} {sessions} />
    {:else}
      <WidgetAgentPrompt {agent} />
      <WidgetMCPLog events={agentEvents} />
    {/if}
  </div>
</div>

<style>
  .inspector { display: flex; flex-direction: column; height: 100%; min-height: 0; }
  .insp-head { display: flex; align-items: center; gap: 0.45rem; padding: 0.45rem 0.7rem; border-bottom: 1px solid var(--border, #2a2a2a); }
  .who { font-weight: 700; font-size: 0.95rem; }
  .ident-chip { font-size: 0.95rem; }
  .team { color: var(--fg-muted, #888); font-size: 0.85rem; }
  .pin-tag { font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.05em; color: var(--accent-2, #87ceeb); border: 1px solid var(--accent-2, #87ceeb); border-radius: 3px; padding: 0 0.3rem; }
  .pin { margin-left: auto; background: none; border: none; cursor: pointer; opacity: 0.45; font-size: 0.9rem; }
  .pin.on, .pin:hover { opacity: 1; }
  .insp-tabs { display: flex; gap: 0.15rem; padding: 0.3rem 0.6rem 0; border-bottom: 1px solid var(--border, #2a2a2a); }
  .insp-tabs button { background: none; border: none; border-bottom: 2px solid transparent; color: var(--fg-muted, #999); padding: 0.25rem 0.55rem; cursor: pointer; font-size: 0.82rem; }
  .insp-tabs button.active { color: var(--fg, #ddd); border-bottom-color: var(--accent-2, #87ceeb); }
  .insp-body { flex: 1; overflow: auto; min-height: 0; }
  .hint { color: var(--fg-muted, #888); padding: 0.8rem; font-size: 0.85rem; }
  .mems { display: flex; flex-wrap: wrap; gap: 0.3rem; padding: 0.5rem 0.7rem; }
  .mesh { padding: 0.5rem 0.7rem; border-bottom: 1px solid var(--border, #2a2a2a); }
  .mesh-row { display: flex; gap: 0.6rem; font-size: 0.82rem; padding: 0.1rem 0; }
  .mesh-row .k { color: var(--fg-muted, #888); min-width: 6.5rem; }
  .mesh-row .v { color: var(--fg, #ddd); }
  .mem { font-size: 0.75rem; color: var(--fg-muted, #999); border: 1px solid var(--border, #2a2a2a); border-radius: 3px; padding: 0.05rem 0.35rem; }
</style>

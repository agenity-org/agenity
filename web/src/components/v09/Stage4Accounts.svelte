<!--
  Stage 4 — Accounts.

  Per-agent-type credential picker with ▶ advanced expand for per-agent
  overrides. Inline Connect-Claude flow lives here (was Stage 3 banner).

  UX (operator-locked 2026-05-29):
    Row 1: claude-code  → [▼ pick account]  [+ Connect Claude account]
    Row 2: codex-cli    → [▼ pick account]
    ...
    ▶ Advanced (per-agent override)  ← starts collapsed when type defaults
                                       resolve every agent

  Outputs bound to the wizard:
    typeAccounts[agentType]        = vault entry id (default for that type)
    agentAccountOverrides[label]  = vault entry id (per-agent override)

  Validation surfaced by the wizard:
    Stage 4 canAdvance = every agent has a resolvable account_id
    (override OR type default). The Next button to Stage 5 won't enable
    until that's true.

  Props:
    agents                       — read-only roster
    typeAccounts                 — $bindable map (agentType → vault id)
    agentAccountOverrides        — $bindable map (agent label → vault id)
-->
<script>
  import ClaudeAccountConnect from './ClaudeAccountConnect.svelte';

  let {
    agents = [],
    // #237 — per-agent agent_type override coming from Stage 4
    // Agent Types & Models. When an agent's type changes in Stage 4,
    // the per-agent dropdown here filters against the NEW type's
    // compatibility, not the template default. Optional; defaults to
    // empty so callers that haven't been wired don't break.
    agentTypeOverrides = {},
    typeAccounts = $bindable({}),
    agentAccountOverrides = $bindable({}),
  } = $props();

  // Resolves the effective agent type for the per-agent row, honoring
  // Stage 4 overrides first (#237) then falling back to the template
  // default. Used by `entriesForType` lookups in the per-agent UI.
  function effectiveTypeFor(a) {
    return agentTypeOverrides[a.label] || a.agent_type || 'claude-code';
  }

  const API = '/api/v1';

  // agentType → human-readable class label for the dropdown placeholder
  // ("— pick anthropic account —"). The actual eligibility filter uses
  // PROVIDER_COMPATIBILITY below, NOT this map. Centralised here so
  // adding a new agent type only needs one touch site.
  const TYPE_TO_CLASS = {
    'claude-code': 'anthropic',
    'codex-cli':   'openai',
    'gemini-cli':  'google',
  };

  // Source of truth for which vault providers can drive which agent
  // types (#232). Replaces the prior brittle exact-match filter
  // (`v.account_class === cls || v.provider === cls`) that excluded
  // `claude-oauth` vault entries from `claude-code` dropdowns because
  // their provider string isn't `anthropic`. Locks the compatibility
  // map at the source so the v0.9.3 Accounts-redesign (#225 section E)
  // inherits a single canonical table rather than re-inventing one.
  //
  // Reading direction: keys = vault provider IDs; values = the agent
  // types compatible with that provider. To add a new provider, add a
  // row here; to add a new agent type, add it to every compatible
  // provider's list. Providers absent from this map are treated as
  // compatible with NOTHING (safer default than the prior unknown-
  // type wildcard).
  const PROVIDER_COMPATIBILITY = {
    'claude-oauth':   ['claude-code', 'aider'],
    'anthropic-api':  ['claude-code', 'aider', 'opencode'],
    'openai-api':     ['codex-cli', 'aider'],
    'openrouter':     ['claude-code', 'codex-cli', 'aider', 'opencode'],
    'openova-newapi': ['claude-code', 'codex-cli', 'aider', 'opencode'],
    'dashscope':      ['qwen-code', 'aider'],
    'qwen-oauth':     ['qwen-code'],
    'google-ai':      ['gemini-cli', 'aider'],
    'vertex-ai':      ['gemini-cli'],
    'google-oauth':   ['gemini-cli'],
    'ollama':         ['aider', 'opencode'],
  };

  let vaultEntries = $state([]);    // [{ id, label, provider, account_class, ... }]
  let loadingVault = $state(false);
  let showConnect = $state(false);  // inline Connect Claude flow
  let advancedOpen = $state(false); // ▶ per-agent override panel
  let bootHydrated = $state(false); // guard against re-running auto-select

  async function loadVault() {
    loadingVault = true;
    try {
      const r = await fetch(`${API}/vault`);
      if (!r.ok) { vaultEntries = []; return; }
      const j = await r.json();
      vaultEntries = Array.isArray(j) ? j : (j.entries || []);
    } catch {
      vaultEntries = [];
    } finally {
      loadingVault = false;
    }
  }

  $effect(() => { loadVault(); });

  // Distinct agent types in this team (preserve first-seen order).
  const teamTypes = $derived.by(() => {
    const seen = new Set();
    const out = [];
    for (const a of agents) {
      const t = effectiveTypeFor(a);
      if (!seen.has(t)) { seen.add(t); out.push(t); }
    }
    return out;
  });

  // Whether any agents of a given type exist (for the "advanced"
  // multi-row UI).
  function agentsOfType(t) {
    return agents.filter(a => effectiveTypeFor(a) === t);
  }

  function classOf(t) { return TYPE_TO_CLASS[t] || ''; }

  // entriesForType returns vault entries whose provider is declared
  // compatible with agent type `t` per PROVIDER_COMPATIBILITY (#232).
  // Backward-compatible: if a vault entry carries an `account_class`
  // field that matches the type's class string, it's also included —
  // covers older vault entries from the v0.8/v0.9.0 schema before
  // provider was the canonical key.
  function entriesForType(t) {
    const legacyCls = classOf(t);
    return vaultEntries.filter(v => {
      const compat = PROVIDER_COMPATIBILITY[v.provider] || [];
      if (compat.includes(t)) return true;
      // Legacy fallback for pre-v0.9.2 entries that may set account_class
      // explicitly (e.g. 'anthropic', 'openai', 'google').
      if (legacyCls && v.account_class === legacyCls) return true;
      return false;
    });
  }

  // Auto-select the newest matching entry per agent-type once the
  // vault list arrives, IFF the operator hasn't already picked one
  // and there's exactly one obvious match. (UX nicety; operator can
  // override via the dropdown.)
  $effect(() => {
    if (bootHydrated) return;
    if (vaultEntries.length === 0) return;
    bootHydrated = true;
    const updates = {};
    let any = false;
    for (const t of teamTypes) {
      if (typeAccounts[t]) continue;
      const matches = entriesForType(t);
      if (matches.length === 1) {
        updates[t] = matches[0].id;
        any = true;
      }
    }
    if (any) typeAccounts = { ...typeAccounts, ...updates };
  });

  function pickType(t, id) {
    typeAccounts = { ...typeAccounts, [t]: id };
  }
  function pickAgent(label, id) {
    if (!id) {
      const { [label]: _, ...rest } = agentAccountOverrides;
      agentAccountOverrides = rest;
    } else {
      agentAccountOverrides = { ...agentAccountOverrides, [label]: id };
    }
  }

  function effectiveFor(a) {
    return agentAccountOverrides[a.label] || typeAccounts[effectiveTypeFor(a)] || '';
  }
  const allResolved = $derived.by(() =>
    agents.length > 0 && agents.every(a => !!effectiveFor(a))
  );
  const missingCount = $derived.by(() =>
    agents.filter(a => !effectiveFor(a)).length
  );

  function onClaudeConnected(newID) {
    showConnect = false;
    loadVault().then(() => {
      // Auto-pick the new entry for claude-code if no other was set.
      if (!typeAccounts['claude-code'] || teamTypes.includes('claude-code')) {
        typeAccounts = { ...typeAccounts, 'claude-code': newID };
      }
    });
  }
</script>

<div class="stage4">
  <h2>Which accounts do they use?</h2>
  <p class="lead">
    chepherd injects credentials per agent. Pick a default per type — open
    <strong>▶ Advanced</strong> if a specific agent needs a different one.
    <strong>Launch unlocks once every agent has an account.</strong>
  </p>

  {#if loadingVault}
    <p class="hint">Loading vault…</p>
  {:else}
    <div class="rows">
      {#each teamTypes as t}
        {@const matches = entriesForType(t)}
        <div class="row">
          <div class="type">
            <span class="type-name">{t}</span>
            <span class="type-count">×{agentsOfType(t).length}</span>
          </div>
          <select
            class="picker"
            value={typeAccounts[t] || ''}
            onchange={(e) => pickType(t, e.currentTarget.value)}
          >
            <option value="">— pick {classOf(t) || 'account'} —</option>
            {#each matches as v}
              <option value={v.id}>{v.label || v.id} {v.provider ? `(${v.provider})` : ''}</option>
            {/each}
          </select>
          {#if t === 'claude-code'}
            {#if !showConnect}
              <button type="button" class="link" onclick={() => showConnect = true}>+ Connect Claude account</button>
            {/if}
          {/if}
        </div>
      {/each}
    </div>

    {#if showConnect}
      <ClaudeAccountConnect
        autostart={true}
        oncomplete={onClaudeConnected}
        oncancel={() => showConnect = false}
      />
    {/if}

    {#if agents.length > 1}
      <button type="button" class="adv-toggle" onclick={() => advancedOpen = !advancedOpen}>
        {advancedOpen ? '▼' : '▶'} Advanced — per-agent override
      </button>
      {#if advancedOpen}
        <div class="adv">
          <p class="adv-help">
            Override the type default for a single agent. Leave on
            <em>(use default)</em> to inherit from the row above.
          </p>
          {#each agents as a}
            {@const at = effectiveTypeFor(a)}
            {@const cls = classOf(at)}
            {@const matches = entriesForType(at)}
            <div class="adv-row">
              <span class="adv-label">{a.label}</span>
              <span class="adv-type">{at}</span>
              <select
                class="picker"
                value={agentAccountOverrides[a.label] || ''}
                onchange={(e) => pickAgent(a.label, e.currentTarget.value)}
              >
                <option value="">— use default ({typeAccounts[at] || 'unset'}) —</option>
                {#each matches as v}
                  <option value={v.id}>{v.label || v.id}</option>
                {/each}
              </select>
            </div>
          {/each}
        </div>
      {/if}
    {/if}

    <div class="state" class:ok={allResolved} class:miss={!allResolved}>
      {#if allResolved}
        ✓ All {agents.length} agents have an account selected. Next →
      {:else}
        ⚠ {missingCount} of {agents.length} agents still need an account before Launch unlocks.
      {/if}
    </div>
  {/if}
</div>

<style>
  .stage4 { padding: 1.1rem 1.25rem; }
  h2 { font-size: 1.15rem; margin: 0 0 0.35rem 0; }
  .lead { color: var(--fg-muted, #888); margin: 0 0 1rem 0; font-size: 0.85rem; line-height: 1.5; }
  .lead strong { color: var(--accent-2, #87ceeb); }
  .hint { color: var(--fg-muted, #888); font-size: 0.85rem; }

  .rows { display: flex; flex-direction: column; gap: 0.5rem; margin-bottom: 0.75rem; }
  .row { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; }
  .type { display: flex; align-items: baseline; gap: 0.3rem; min-width: 7.5rem; }
  .type-name { color: var(--fg, #f5f5f5); font-weight: 600; font-size: 0.9rem; }
  .type-count { color: var(--fg-muted, #888); font-size: 0.78rem; }
  .picker {
    flex: 1; min-width: 14rem;
    padding: 0.4rem 0.55rem; border-radius: 4px;
    border: 1px solid var(--border, #2a2a2a);
    background: var(--bg, #0a0a0a); color: var(--fg, #f5f5f5);
    font: inherit; font-size: 0.86rem;
  }
  .link {
    background: transparent; border: 0; color: var(--accent-2, #87ceeb);
    cursor: pointer; font: inherit; font-size: 0.82rem; padding: 0.2rem 0.3rem;
    text-decoration: underline;
  }
  .link:hover { color: var(--fg, #fff); }

  .adv-toggle {
    background: transparent; border: 0; color: var(--accent-2, #87ceeb);
    cursor: pointer; font: inherit; font-size: 0.82rem;
    padding: 0.35rem 0; margin: 0.55rem 0 0.4rem 0;
  }
  .adv-toggle:hover { text-decoration: underline; }
  .adv {
    background: var(--bg, #0a0a0a); border: 1px solid var(--border, #2a2a2a);
    border-radius: 5px; padding: 0.65rem 0.85rem; margin-bottom: 0.85rem;
  }
  .adv-help { color: var(--fg-muted, #888); font-size: 0.78rem; margin: 0 0 0.55rem 0; line-height: 1.5; }
  .adv-help em { color: var(--fg-muted, #aaa); font-style: italic; }
  .adv-row {
    display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap;
    padding: 0.25rem 0;
    border-bottom: 1px solid rgba(255,255,255,0.04);
  }
  .adv-row:last-child { border-bottom: 0; }
  .adv-label { font-weight: 600; min-width: 6rem; font-size: 0.86rem; }
  .adv-type {
    font-size: 0.72rem; padding: 0.04rem 0.4rem; border-radius: 999px;
    background: rgba(135, 206, 235, 0.14); color: #87ceeb;
  }

  .state {
    margin-top: 0.65rem; padding: 0.5rem 0.75rem;
    border-radius: 5px; font-size: 0.85rem; line-height: 1.4;
  }
  .state.ok { background: rgba(46,213,115,0.08); border: 1px solid rgba(46,213,115,0.3); color: #2ed573; }
  .state.miss { background: rgba(255,193,7,0.08); border: 1px solid rgba(255,193,7,0.3); color: #f7b500; }
</style>

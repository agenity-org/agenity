<!--
  Stage 4 — Agent Types & Models (NEW per #237 / operator 2026-05-30).

  Per-agent override of:
    - agent type: claude-code / codex-cli / aider / qwen-code / gemini-cli / opencode
    - model: list filtered by the selected agent type

  Sits between Stage 3 (Skills) and the renumbered Stage 5 (Accounts).
  Defaults each row to the template's `agent_type_default` (already
  rendered into `agent.agent_type` by Stage 3's hydration) + the first
  model in MODELS_BY_TYPE for that type. Operator can override per
  agent.

  Output (bindable to SpawnWizardV9):
    agentTypeOverrides[agentLabel]  → agent type string
    agentModelOverrides[agentLabel] → model string

  Stage 5 Accounts reads agentTypeOverrides to filter the vault entries
  per agent (`entriesForType(agentTypeOverrides[label] || agent.agent_type)`).
  Stage 6 Launch reads both to populate the POST body:
    body.agent = agentTypeOverrides[label] || agent.agent_type
    body.stat_sheet.model_tier = agentModelOverrides[label]

  Hardcoded model lists for v0.9.2. v0.9.3 will federate per-provider
  via the provider's /v1/models endpoint — out of scope for #237.

  Refs #237 #232.
-->
<script>
  let {
    agents = [],
    agentTypeOverrides = $bindable({}),
    agentModelOverrides = $bindable({}),
  } = $props();

  // Source of truth for agent-type slugs the wizard exposes. Order
  // matches the dropdown UI; first entry is the fallback default when
  // an agent has no `agent_type` set.
  const AGENT_TYPES = [
    { slug: 'claude-code', label: 'claude-code' },
    { slug: 'codex-cli',   label: 'codex-cli' },
    { slug: 'aider',       label: 'aider' },
    { slug: 'qwen-code',   label: 'qwen-code' },
    { slug: 'gemini-cli',  label: 'gemini-cli' },
    { slug: 'opencode',    label: 'opencode' },
  ];

  // Hardcoded model list per agent type. Aligned with #237's source of
  // truth + matches what the per-agent CLI actually accepts via
  // `--model <name>` (claude-code) or the equivalent flag on each
  // flavor. opencode is chepherd-managed → no model picker; renders a
  // single fixed entry so the dropdown isn't empty.
  const MODELS_BY_TYPE = {
    'claude-code': ['claude-opus-4', 'claude-sonnet-4', 'claude-haiku-4'],
    'codex-cli':   ['gpt-5-codex', 'gpt-4-codex'],
    'aider':       ['claude-opus-4', 'claude-sonnet-4', 'gpt-5', 'gpt-4', 'gpt-4-turbo'],
    'qwen-code':   ['qwen3-coder-plus', 'qwen3-coder'],
    'gemini-cli':  ['gemini-2.5-pro', 'gemini-2.5-flash'],
    'opencode':    ['chepherd-managed'],
  };

  function typeOf(a) {
    return agentTypeOverrides[a.label] || a.agent_type || 'claude-code';
  }
  function modelOf(a) {
    const cur = agentModelOverrides[a.label];
    if (cur) return cur;
    const t = typeOf(a);
    return MODELS_BY_TYPE[t]?.[0] || '';
  }
  function modelsFor(a) {
    return MODELS_BY_TYPE[typeOf(a)] || [];
  }

  function pickType(a, slug) {
    agentTypeOverrides = { ...agentTypeOverrides, [a.label]: slug };
    // Clear the model override when type changes so the next render
    // picks the new type's first model as the default. Operator can
    // re-select if they want a non-default.
    const { [a.label]: _, ...rest } = agentModelOverrides;
    agentModelOverrides = rest;
  }
  function pickModel(a, model) {
    agentModelOverrides = { ...agentModelOverrides, [a.label]: model };
  }

  // Hydrate defaults — for each agent that has no model override, set
  // one to the first allowed for its current type. Lets the Launch POST
  // body always carry a concrete model_tier rather than empty.
  $effect(() => {
    const updates = {};
    let any = false;
    for (const a of agents) {
      if (agentModelOverrides[a.label]) continue;
      const t = typeOf(a);
      const models = MODELS_BY_TYPE[t] || [];
      if (models.length > 0) {
        updates[a.label] = models[0];
        any = true;
      }
    }
    if (any) agentModelOverrides = { ...agentModelOverrides, ...updates };
  });
</script>

<div class="stage4at">
  <header class="head">
    <div class="title">
      <h2>Which agents + which models?</h2>
      <p class="lead">Pick the CLI flavor for each agent and the model it runs on. Defaults inherit from the template; override per-agent here.</p>
    </div>
  </header>

  {#if agents.length === 0}
    <p class="hint">No agents in this team — add some on Stage 3.</p>
  {:else}
    <div class="rows" role="list">
      {#each agents as a (a.label)}
        <div class="row" role="listitem">
          <div class="agent-label">{a.label}</div>
          <label class="picker">
            <span class="picker-label">Type</span>
            <select
              value={typeOf(a)}
              onchange={(e) => pickType(a, e.currentTarget.value)}
              aria-label={`Agent type for ${a.label}`}
            >
              {#each AGENT_TYPES as t (t.slug)}
                <option value={t.slug}>{t.label}</option>
              {/each}
            </select>
          </label>
          <label class="picker">
            <span class="picker-label">
              Model
              {#if typeOf(a) !== 'claude-code' && typeOf(a) !== 'opencode'}
                <span class="picker-note" title="v0.9.2 only wires the model flag for claude-code; other flavors fall back to their CLI default until v0.9.3 federates per-flavor model env">(default only for this flavor)</span>
              {/if}
            </span>
            <select
              value={modelOf(a)}
              onchange={(e) => pickModel(a, e.currentTarget.value)}
              aria-label={`Model for ${a.label}`}
              disabled={modelsFor(a).length <= 1}
            >
              {#each modelsFor(a) as m (m)}
                <option value={m}>{m}</option>
              {/each}
            </select>
          </label>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .stage4at { padding: 1.1rem 1.25rem; }
  .head { margin-bottom: 0.85rem; }
  h2 { font-size: 1.15rem; margin: 0 0 0.35rem 0; }
  .lead { color: var(--fg-muted, #888); margin: 0; font-size: 0.84rem; line-height: 1.5; }

  .rows { display: flex; flex-direction: column; gap: 0.55rem; }
  .row {
    display: grid;
    grid-template-columns: minmax(8rem, 1fr) minmax(9rem, 1fr) minmax(11rem, 1.5fr);
    gap: 0.7rem;
    align-items: end;
    padding: 0.45rem 0.55rem;
    background: var(--bg-elevated, #1a1a1a);
    border: 1px solid var(--border, #2a2a2a);
    border-radius: 6px;
  }
  .agent-label {
    font-size: 0.9rem; color: var(--fg, #f5f5f5); font-weight: 500;
    align-self: center;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .picker {
    display: flex; flex-direction: column; gap: 0.18rem;
    font-size: 0.74rem; color: var(--fg-muted, #888);
    min-width: 0;
  }
  .picker-label { letter-spacing: 0.02em; display: flex; gap: 0.4rem; align-items: baseline; }
  .picker-note {
    color: var(--accent-warn, #d4a35a);
    font-size: 0.66rem;
    font-style: italic;
    text-transform: none;
    letter-spacing: 0;
  }
  .picker select {
    padding: 0.4rem 0.55rem;
    border: 1px solid var(--border, #2a2a2a);
    background: var(--bg, #0a0a0a);
    color: var(--fg, #f5f5f5);
    font: inherit;
    border-radius: 4px;
    cursor: pointer;
    appearance: none;
    -webkit-appearance: none;
    background-image:
      linear-gradient(45deg, transparent 50%, var(--fg-muted, #888) 50%),
      linear-gradient(135deg, var(--fg-muted, #888) 50%, transparent 50%);
    background-position: calc(100% - 14px) 50%, calc(100% - 9px) 50%;
    background-size: 5px 5px;
    background-repeat: no-repeat;
    padding-right: 1.6rem;
  }
  .picker select:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }
  .picker select:hover:not(:disabled) { border-color: var(--accent-2, #87ceeb); }
  .picker select:focus { outline: 2px solid var(--accent-2, #87ceeb); outline-offset: 1px; }

  .hint { color: var(--fg-muted, #888); font-size: 0.9rem; }

  @media (max-width: 640px) {
    .row {
      grid-template-columns: 1fr;
      gap: 0.35rem;
    }
    .agent-label {
      font-size: 0.95rem; padding-bottom: 0.15rem;
      border-bottom: 1px solid rgba(255,255,255,0.05);
    }
  }
</style>
